/*
Copyright 2023. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

type ReportMode int

const (
	// Default mode. In this mode, eventManager running
	// in the management cluster periodically collects
	// EventReports from Sveltos/CAPI clusters
	CollectFromManagementCluster ReportMode = iota

	// In this mode, sveltos agent sends EventReport
	// to management cluster.
	// SveltosAgent is provided with Kubeconfig to access
	// management cluster and can only update EventReport
	// (and ClassifierReport and HealthCheckReport).
	AgentSendReportsNoGateway
)

const (
	// deleteRequeueAfter is how long to wait before checking again to see if the cluster still has
	// children during deletion.
	deleteRequeueAfter = 20 * time.Second

	// normalRequeueAfter is how long to wait before checking again to see if the cluster can be moved
	// to ready after or workload features (for instance ingress or reporter) have failed
	normalRequeueAfter = 20 * time.Second
)

// EventTriggerReconciler reconciles a EventTrigger object
type EventTriggerReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Deployer             deployer.DeployerInterface
	EventReportMode      ReportMode
	ShardKey             string

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex

	// key: Sveltos/CAPI Cluster: value: set of all EventTrigger instances matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: EventTrigger: value: set of Sveltos/CAPI Clusters matched
	ToClusterMap map[types.NamespacedName]*libsveltosset.Set

	// key: EventTriggers; value EventTrigger Selector
	EventTriggers map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - EventTrigger is created
	// - Cluster is created with labels matching EventTrigger
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which EventTrigger to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	// Reason for the two maps:
	// EventTrigger, via ClusterSelector, matches Sveltos/CAPI Clusters based on Cluster labels.
	// When a Sveltos/CAPI Cluster labels change, one or more EventTriggers need to be reconciled.
	// In order to achieve so, EventTrigger reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/CAPI Cluster
	// label changes, find all the EventTrigger currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/CAPI Cluster, return all the EventTriggers matching it).
	// In the MapFunc, if the list EventTriggers operation failed, we would be unable to retry or re-enqueue the rigth set of
	// EventTriggers.
	// Instead the approach taken is following:
	// - when a EventTrigger is reconciled, update the EventTriggers and the ClusterMap;
	// - in the MapFunc, given the Sveltos/CAPI Cluster that changed:
	//		* use EventTriggers to find all EventTrigger now matching the Cluster and reconcile those;
	//      * use ClusterMap to reconcile all EventTriggers previously matching the Cluster.
	//
	// The ToClusterMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. EventTrigger A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ToClusterMap A => 1,2
	// 2. Cluster 2 label changes and now EventTrigger matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile EventTrigger we have its current version we don't have its previous version. So we know EventTrigger A
	// now matches Sveltos/CAPI Cluster 1, but we don't know it used to match Sveltos/CAPI Cluster 2.
	// So we use ToClusterMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// EventTrigger matches Sveltos/CAPI Cluster 1 only and looking at ToClusterMap we know it used to reference
	// Svetos/CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ToClusterMap (so new value will be A => 1)

	// Following two maps follow same logic for EventTrigger and EventSource.

	// key: EventSource: value: set of all EventTrigger referencing it
	EventSourceMap map[corev1.ObjectReference]*libsveltosset.Set

	// Key: EventTrigger: value: set of EventSource referenced
	ToEventSourceMap map[types.NamespacedName]*libsveltosset.Set

	// key: Referenced object; value: set of all EventTriggers referencing the resource
	ReferenceMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: EventTriggers name; value: set of referenced resources
	EventTriggerMap map[types.NamespacedName]*libsveltosset.Set
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventtriggers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventtriggers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventtriggers/finalizers,verbs=update
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventsources,verbs=get;list;watch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventreports,verbs=create;update;delete;get;watch;list
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles,verbs=get;list;update;create;delete;watch
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;watch;list;create;update;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;watch;list;create;update;delete

func (r *EventTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the EventTrigger instance
	eventTrigger := &v1alpha1.EventTrigger{}
	if err := r.Get(ctx, req.NamespacedName, eventTrigger); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch EventTrigger")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch EventTrigger %s",
			req.NamespacedName,
		)
	}

	eventTriggerScope, err := scope.NewEventTriggerScope(scope.EventTriggerScopeParams{
		Client:         r.Client,
		Logger:         logger,
		EventTrigger:   eventTrigger,
		ControllerName: "eventTrigger",
	})
	if err != nil {
		logger.Error(err, "Failed to create eventTriggerScope")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"unable to create eventTrigger scope for %s",
			req.NamespacedName,
		)
	}

	// Always close the scope when exiting this function so we can persist any EventTrigger
	// changes.
	defer func() {
		if err := eventTriggerScope.Close(ctx); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update: %v", err))
			reterr = err
		}
	}()

	// Handle deleted eventTrigger
	if !eventTrigger.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, eventTriggerScope), nil
	}

	// Handle non-deleted eventTrigger
	return r.reconcileNormal(ctx, eventTriggerScope)
}

func (r *EventTriggerReconciler) reconcileDelete(
	ctx context.Context,
	eventTriggerScope *scope.EventTriggerScope,
) reconcile.Result {

	logger := eventTriggerScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling EventTrigger delete")

	eventTriggerScope.SetMatchingClusterRefs(nil)

	r.cleanMaps(eventTriggerScope)

	f := getHandlersForFeature(v1alpha1.FeatureEventTrigger)
	err := r.undeployEventTrigger(ctx, eventTriggerScope, f, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to undeploy")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
	}

	if controllerutil.ContainsFinalizer(eventTriggerScope.EventTrigger, v1alpha1.EventTriggerFinalizer) {
		controllerutil.RemoveFinalizer(eventTriggerScope.EventTrigger, v1alpha1.EventTriggerFinalizer)
	}

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}
}

func (r *EventTriggerReconciler) reconcileNormal(
	ctx context.Context,
	eventTriggerScope *scope.EventTriggerScope,
) (reconcile.Result, error) {

	logger := eventTriggerScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling EventTrigger")

	if !controllerutil.ContainsFinalizer(eventTriggerScope.EventTrigger, v1alpha1.EventTriggerFinalizer) {
		if err := r.addFinalizer(ctx, eventTriggerScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	parsedSelector, err := labels.Parse(eventTriggerScope.GetSelector())
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse clusterSelector: %v", err))
		return reconcile.Result{}, err
	}
	matchingCluster, err := clusterproxy.GetMatchingClusters(ctx, r.Client, parsedSelector, "",
		eventTriggerScope.Logger)
	if err != nil {
		return reconcile.Result{}, err
	}

	eventTriggerScope.SetMatchingClusterRefs(matchingCluster)

	err = r.updateClusterInfo(ctx, eventTriggerScope)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to update clusterConditions")
		return reconcile.Result{}, err
	}

	r.updateMaps(eventTriggerScope)

	f := getHandlersForFeature(v1alpha1.FeatureEventTrigger)
	if err := r.deployEventTrigger(ctx, eventTriggerScope, f, logger); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to deploy")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EventTriggerReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.EventTrigger{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}
	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.SveltosCluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	/* TODO
	// When projectsveltos ClusterProfile changes, according to ClusterProfilePredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(&source.Kind{Type: &configv1alpha1.ClusterProfile{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForClusterProfile),
		ClusterProfilePredicates(mgr.GetLogger().WithValues("predicate", "clusterrofilepredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}
	*/

	// When projectsveltos EventReports changes, according to EventPredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.EventReport{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForEventReport),
		EventReportPredicates(mgr.GetLogger().WithValues("predicate", "eventreportpredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When projectsveltos EventSources changes, according to EventSourcePredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.EventSource{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForEventSource),
		EventSourcePredicates(mgr.GetLogger().WithValues("predicate", "eventsourcepredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When ConfigMap changes, according to ConfigMapPredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.ConfigMap{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForReference),
		ConfigMapPredicates(mgr.GetLogger().WithValues("predicate", "configmappredicate")),
	)
	if err != nil {
		return nil, err
	}

	// When Secret changes, according to SecretPredicates,
	// one or more EventTriggers need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.Secret{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForReference),
		SecretPredicates(mgr.GetLogger().WithValues("predicate", "secretpredicate")),
	)
	if err != nil {
		return nil, err
	}

	if r.EventReportMode == CollectFromManagementCluster {
		go collectEventReports(mgr.GetClient(), r.ShardKey, mgr.GetLogger())
	}

	return c, nil
}

func (r *EventTriggerReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more EventTriggers need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}

	// When cluster-api machine changes, according to MachinePredicates,
	// one or more EventTrigger need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Machine{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventTriggerForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}

	return nil
}

func (r *EventTriggerReconciler) addFinalizer(ctx context.Context, eventTriggerScope *scope.EventTriggerScope) error {
	controllerutil.AddFinalizer(eventTriggerScope.EventTrigger, v1alpha1.EventTriggerFinalizer)
	// Register the finalizer immediately to avoid orphaning eventTrigger resources on delete
	if err := eventTriggerScope.PatchObject(ctx); err != nil {
		eventTriggerScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			eventTriggerScope.Name(),
		)
	}
	return nil
}

func (r *EventTriggerReconciler) cleanMaps(eventTriggerScope *scope.EventTriggerScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	eventTriggerInfo := getKeyFromObject(r.Scheme, eventTriggerScope.EventTrigger)

	for k, l := range r.ClusterMap {
		l.Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
		if l.Len() == 0 {
			delete(r.ClusterMap, k)
		}
	}

	delete(r.ToEventSourceMap, types.NamespacedName{Name: eventTriggerScope.Name()})

	delete(r.ToClusterMap, types.NamespacedName{Name: eventTriggerScope.Name()})

	for k, l := range r.EventSourceMap {
		l.Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
		if l.Len() == 0 {
			delete(r.EventSourceMap, k)
		}
	}

	delete(r.EventTriggers, *eventTriggerInfo)

	delete(r.EventTriggerMap, types.NamespacedName{Name: eventTriggerScope.Name()})

	for i := range r.ReferenceMap {
		eventTriggerSet := r.ReferenceMap[i]
		eventTriggerSet.Erase(eventTriggerInfo)
	}
}

func (r *EventTriggerReconciler) updateMaps(eventTriggerScope *scope.EventTriggerScope) {
	r.updateClusterMaps(eventTriggerScope)

	r.updateEventSourceMaps(eventTriggerScope)

	r.updateReferencedResourceMap(eventTriggerScope)

	eventTriggerInfo := getKeyFromObject(r.Scheme, eventTriggerScope.EventTrigger)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	r.EventTriggers[*eventTriggerInfo] = eventTriggerScope.EventTrigger.Spec.SourceClusterSelector
}

func (r *EventTriggerReconciler) updateClusterMaps(eventTriggerScope *scope.EventTriggerScope) {
	currentClusters := &libsveltosset.Set{}
	for i := range eventTriggerScope.EventTrigger.Status.MatchingClusterRefs {
		cluster := eventTriggerScope.EventTrigger.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{
			Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion,
		}
		currentClusters.Insert(clusterInfo)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	eventTriggerInfo := getKeyFromObject(r.Scheme, eventTriggerScope.EventTrigger)

	// Get list of Clusters not matched anymore by EventTrigger
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ToClusterMap[types.NamespacedName{Name: eventTriggerScope.Name()}]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add EventTrigger as consumer
	for i := range eventTriggerScope.EventTrigger.Status.MatchingClusterRefs {
		cluster := eventTriggerScope.EventTrigger.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		r.getClusterMapForEntry(clusterInfo).Insert(eventTriggerInfo)
	}

	// For each Cluster not matched anymore, remove EventTrigger as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		r.getClusterMapForEntry(&clusterName).Erase(eventTriggerInfo)
	}

	// Update list of Clusters currently referenced by EventTrigger instance
	r.ToClusterMap[types.NamespacedName{Name: eventTriggerScope.Name()}] = currentClusters
}

func (r *EventTriggerReconciler) updateEventSourceMaps(eventTriggerScope *scope.EventTriggerScope) {
	// Get list of EventSource currently referenced
	currentReferences := &libsveltosset.Set{}
	currentReferences.Insert(&corev1.ObjectReference{
		APIVersion: libsveltosv1alpha1.GroupVersion.String(), // the only resources that can be referenced is EventSource
		Kind:       libsveltosv1alpha1.EventSourceKind,
		Name:       eventTriggerScope.EventTrigger.Spec.EventSourceName,
	})

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of References not referenced anymore by EventTrigger
	var toBeRemoved []corev1.ObjectReference
	name := types.NamespacedName{Name: eventTriggerScope.Name()}
	if v, ok := r.ToEventSourceMap[name]; ok {
		toBeRemoved = v.Difference(currentReferences)
	}

	// For each currently referenced instance, add EventTrigger as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getEventSourceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
	}

	// For each resource not reference anymore, remove EventTrigger as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getEventSourceMapForEntry(&referencedResource).Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
	}

	// Update list of EventSource instances currently referenced by EventTrigger
	r.ToEventSourceMap[name] = currentReferences
}

func (r *EventTriggerReconciler) updateReferencedResourceMap(eventTriggerScope *scope.EventTriggerScope) {
	// Get list of ConfigMap/Secret currently referenced
	currentReferences := r.getCurrentReferences(eventTriggerScope)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of References not referenced anymore by EventTrigger
	var toBeRemoved []corev1.ObjectReference
	eventTriggerName := types.NamespacedName{Name: eventTriggerScope.Name()}
	if v, ok := r.EventTriggerMap[eventTriggerName]; ok {
		toBeRemoved = v.Difference(currentReferences)
	}

	// For each currently referenced instance, add EventTrigger as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getReferenceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
	}

	// For each resource not reference anymore, remove EventTrigger as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getReferenceMapForEntry(&referencedResource).Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventTriggerKind,
				Name:       eventTriggerScope.Name(),
			},
		)
	}

	// Update list of ConfigMaps/Secrets currently referenced by EventTrigger
	r.EventTriggerMap[eventTriggerName] = currentReferences
}

func (r *EventTriggerReconciler) getEventSourceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.EventSourceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.EventSourceMap[*entry] = s
	}
	return s
}

func (r *EventTriggerReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

func (r *EventTriggerReconciler) getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ClusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ClusterMap[*entry] = s
	}
	return s
}

// updateClusterInfo updates EventTrigger Status ClusterInfo by adding an entry for any
// new cluster matching EventTrigger instance
func (r *EventTriggerReconciler) updateClusterInfo(ctx context.Context,
	eventTriggerScope *scope.EventTriggerScope) error {

	chc := eventTriggerScope.EventTrigger

	getClusterID := func(cluster corev1.ObjectReference) string {
		return fmt.Sprintf("%s:%s/%s", clusterproxy.GetClusterType(&cluster), cluster.Namespace, cluster.Name)
	}

	// Build Map for all Clusters with an entry in Classifier.Status.ClusterInfo
	clusterMap := make(map[string]bool)
	for i := range chc.Status.ClusterInfo {
		c := &chc.Status.ClusterInfo[i]
		clusterMap[getClusterID(c.Cluster)] = true
	}

	newClusterInfo := make([]libsveltosv1alpha1.ClusterInfo, 0)
	for i := range chc.Status.MatchingClusterRefs {
		c := chc.Status.MatchingClusterRefs[i]
		if _, ok := clusterMap[getClusterID(c)]; !ok {
			newClusterInfo = append(newClusterInfo,
				libsveltosv1alpha1.ClusterInfo{
					Cluster: c,
					Hash:    nil,
				})
		}
	}

	finalClusterInfo := chc.Status.ClusterInfo
	finalClusterInfo = append(finalClusterInfo, newClusterInfo...)

	eventTriggerScope.SetClusterInfo(finalClusterInfo)
	return nil
}

func (r *EventTriggerReconciler) getCurrentReferences(eventTriggerScope *scope.EventTriggerScope) *libsveltosset.Set {
	currentReferences := &libsveltosset.Set{}
	for i := range eventTriggerScope.EventTrigger.Spec.PolicyRefs {
		referencedNamespace := eventTriggerScope.EventTrigger.Spec.PolicyRefs[i].Namespace
		referencedName := eventTriggerScope.EventTrigger.Spec.PolicyRefs[i].Name

		// If referenced resource namespace is empty, at instantiation time the cluster namespace will be used.
		// Here to track referenced ConfigMaps/Resource, we use all current matching clusters
		for j := range eventTriggerScope.EventTrigger.Status.MatchingClusterRefs {
			clusterRef := eventTriggerScope.EventTrigger.Status.MatchingClusterRefs[j]
			namespace := getReferenceResourceNamespace(clusterRef.Namespace, referencedNamespace)

			currentReferences.Insert(&corev1.ObjectReference{
				APIVersion: corev1.SchemeGroupVersion.String(), // the only resources that can be referenced are Secret and ConfigMap
				Kind:       eventTriggerScope.EventTrigger.Spec.PolicyRefs[i].Kind,
				Namespace:  namespace,
				Name:       referencedName,
			})
		}
	}

	for i := range eventTriggerScope.EventTrigger.Spec.KustomizationRefs {
		referencedNamespace := eventTriggerScope.EventTrigger.Spec.KustomizationRefs[i].Namespace
		referencedName := eventTriggerScope.EventTrigger.Spec.KustomizationRefs[i].Name

		// If referenced resource namespace is empty, at instantiation time the cluster namespace will be used.
		// Here to track referenced ConfigMaps/Resource, we use all current matching clusters
		for j := range eventTriggerScope.EventTrigger.Status.MatchingClusterRefs {
			clusterRef := eventTriggerScope.EventTrigger.Status.MatchingClusterRefs[j]
			namespace := getReferenceResourceNamespace(clusterRef.Namespace, referencedNamespace)

			ref := &corev1.ObjectReference{
				Kind:      eventTriggerScope.EventTrigger.Spec.KustomizationRefs[i].Kind,
				Namespace: namespace,
				Name:      referencedName,
			}

			switch eventTriggerScope.EventTrigger.Spec.KustomizationRefs[i].Kind {
			case sourcev1.GitRepositoryKind:
				ref.APIVersion = sourcev1.GroupVersion.String()
			case sourcev1b2.OCIRepositoryKind:
				ref.APIVersion = sourcev1b2.GroupVersion.String()
			case sourcev1b2.BucketKind:
				ref.APIVersion = sourcev1b2.GroupVersion.String()
			}

			currentReferences.Insert(ref)
		}
	}
	return currentReferences
}
