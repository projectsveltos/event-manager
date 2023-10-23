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

// EventBasedAddOnReconciler reconciles a EventBasedAddOn object
type EventBasedAddOnReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Deployer             deployer.DeployerInterface
	EventReportMode      ReportMode
	ShardKey             string

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex

	// key: Sveltos/CAPI Cluster: value: set of all EventBasedAddOn instances matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: EventBasedAddOn: value: set of Sveltos/CAPI Clusters matched
	ToClusterMap map[types.NamespacedName]*libsveltosset.Set

	// key: EventBasedAddOns; value EventBasedAddOn Selector
	EventBasedAddOns map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - EventBasedAddOn is created
	// - Cluster is created with labels matching EventBasedAddOn
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which EventBasedAddOn to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	// Reason for the two maps:
	// EventBasedAddOn, via ClusterSelector, matches Sveltos/CAPI Clusters based on Cluster labels.
	// When a Sveltos/CAPI Cluster labels change, one or more EventBasedAddOns need to be reconciled.
	// In order to achieve so, EventBasedAddOn reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/CAPI Cluster
	// label changes, find all the EventBasedAddOn currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/CAPI Cluster, return all the EventBasedAddOns matching it).
	// In the MapFunc, if the list EventBasedAddOns operation failed, we would be unable to retry or re-enqueue the rigth set of
	// EventBasedAddOns.
	// Instead the approach taken is following:
	// - when a EventBasedAddOn is reconciled, update the EventBasedAddOns and the ClusterMap;
	// - in the MapFunc, given the Sveltos/CAPI Cluster that changed:
	//		* use EventBasedAddOns to find all EventBasedAddOn now matching the Cluster and reconcile those;
	//      * use ClusterMap to reconcile all EventBasedAddOns previously matching the Cluster.
	//
	// The ToClusterMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. EventBasedAddOn A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ToClusterMap A => 1,2
	// 2. Cluster 2 label changes and now EventBasedAddOn matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile EventBasedAddOn we have its current version we don't have its previous version. So we know EventBasedAddOn A
	// now matches Sveltos/CAPI Cluster 1, but we don't know it used to match Sveltos/CAPI Cluster 2.
	// So we use ToClusterMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// EventBasedAddOn matches Sveltos/CAPI Cluster 1 only and looking at ToClusterMap we know it used to reference
	// Svetos/CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ToClusterMap (so new value will be A => 1)

	// Following two maps follow same logic for EventBasedAddOn and EventSource.

	// key: EventSource: value: set of all EventBasedAddOn referencing it
	EventSourceMap map[corev1.ObjectReference]*libsveltosset.Set

	// Key: EventBasedAddOn: value: set of EventSource referenced
	ToEventSourceMap map[types.NamespacedName]*libsveltosset.Set

	// key: Referenced object; value: set of all EventBasedAddOns referencing the resource
	ReferenceMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: EventBasedAddOns name; value: set of referenced resources
	EventBasedAddOnMap map[types.NamespacedName]*libsveltosset.Set
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventbasedaddons,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventbasedaddons/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventbasedaddons/finalizers,verbs=update
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

func (r *EventBasedAddOnReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the EventBasedAddOn instance
	eventBasedAddOn := &v1alpha1.EventBasedAddOn{}
	if err := r.Get(ctx, req.NamespacedName, eventBasedAddOn); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch EventBasedAddOn")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch EventBasedAddOn %s",
			req.NamespacedName,
		)
	}

	eventBasedAddOnScope, err := scope.NewEventBasedAddOnScope(scope.EventBasedAddOnScopeParams{
		Client:          r.Client,
		Logger:          logger,
		EventBasedAddOn: eventBasedAddOn,
		ControllerName:  "eventBasedAddOn",
	})
	if err != nil {
		logger.Error(err, "Failed to create eventBasedAddOnScope")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"unable to create eventBasedAddOn scope for %s",
			req.NamespacedName,
		)
	}

	// Always close the scope when exiting this function so we can persist any EventBasedAddOn
	// changes.
	defer func() {
		if err := eventBasedAddOnScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted eventBasedAddOn
	if !eventBasedAddOn.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, eventBasedAddOnScope), nil
	}

	// Handle non-deleted eventBasedAddOn
	return r.reconcileNormal(ctx, eventBasedAddOnScope)
}

func (r *EventBasedAddOnReconciler) reconcileDelete(
	ctx context.Context,
	eventBasedAddOnScope *scope.EventBasedAddOnScope,
) reconcile.Result {

	logger := eventBasedAddOnScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling EventBasedAddOn delete")

	eventBasedAddOnScope.SetMatchingClusterRefs(nil)

	r.cleanMaps(eventBasedAddOnScope)

	f := getHandlersForFeature(v1alpha1.FeatureEventBasedAddOn)
	err := r.undeployEventBasedAddOn(ctx, eventBasedAddOnScope, f, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to undeploy")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
	}

	if controllerutil.ContainsFinalizer(eventBasedAddOnScope.EventBasedAddOn, v1alpha1.EventBasedAddOnFinalizer) {
		controllerutil.RemoveFinalizer(eventBasedAddOnScope.EventBasedAddOn, v1alpha1.EventBasedAddOnFinalizer)
	}

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}
}

func (r *EventBasedAddOnReconciler) reconcileNormal(
	ctx context.Context,
	eventBasedAddOnScope *scope.EventBasedAddOnScope,
) (reconcile.Result, error) {

	logger := eventBasedAddOnScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling EventBasedAddOn")

	if !controllerutil.ContainsFinalizer(eventBasedAddOnScope.EventBasedAddOn, v1alpha1.EventBasedAddOnFinalizer) {
		if err := r.addFinalizer(ctx, eventBasedAddOnScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	parsedSelector, _ := labels.Parse(eventBasedAddOnScope.GetSelector())
	matchingCluster, err := clusterproxy.GetMatchingClusters(ctx, r.Client, parsedSelector, eventBasedAddOnScope.Logger)
	if err != nil {
		return reconcile.Result{}, err
	}

	eventBasedAddOnScope.SetMatchingClusterRefs(matchingCluster)

	err = r.updateClusterInfo(ctx, eventBasedAddOnScope)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to update clusterConditions")
		return reconcile.Result{}, err
	}

	r.updateMaps(eventBasedAddOnScope)

	f := getHandlersForFeature(v1alpha1.FeatureEventBasedAddOn)
	if err := r.deployEventBasedAddOn(ctx, eventBasedAddOnScope, f, logger); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to deploy")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EventBasedAddOnReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.EventBasedAddOn{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}
	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.SveltosCluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	/* TODO
	// When projectsveltos ClusterProfile changes, according to ClusterProfilePredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(&source.Kind{Type: &configv1alpha1.ClusterProfile{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForClusterProfile),
		ClusterProfilePredicates(mgr.GetLogger().WithValues("predicate", "clusterrofilepredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}
	*/

	// When projectsveltos EventReports changes, according to EventPredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.EventReport{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForEventReport),
		EventReportPredicates(mgr.GetLogger().WithValues("predicate", "eventreportpredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When projectsveltos EventSources changes, according to EventSourcePredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.EventSource{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForEventSource),
		EventSourcePredicates(mgr.GetLogger().WithValues("predicate", "eventsourcepredicate")),
	)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When ConfigMap changes, according to ConfigMapPredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.ConfigMap{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForReference),
		ConfigMapPredicates(mgr.GetLogger().WithValues("predicate", "configmappredicate")),
	)
	if err != nil {
		return nil, err
	}

	// When Secret changes, according to SecretPredicates,
	// one or more EventBasedAddOns need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.Secret{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForReference),
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

func (r *EventBasedAddOnReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more EventBasedAddOns need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}

	// When cluster-api machine changes, according to MachinePredicates,
	// one or more EventBasedAddOn need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Machine{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueEventBasedAddOnForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}

	return nil
}

func (r *EventBasedAddOnReconciler) addFinalizer(ctx context.Context, eventBasedAddOnScope *scope.EventBasedAddOnScope) error {
	controllerutil.AddFinalizer(eventBasedAddOnScope.EventBasedAddOn, v1alpha1.EventBasedAddOnFinalizer)
	// Register the finalizer immediately to avoid orphaning eventBasedAddOn resources on delete
	if err := eventBasedAddOnScope.PatchObject(ctx); err != nil {
		eventBasedAddOnScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			eventBasedAddOnScope.Name(),
		)
	}
	return nil
}

func (r *EventBasedAddOnReconciler) cleanMaps(eventBasedAddOnScope *scope.EventBasedAddOnScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	eventBasedAddOnInfo := getKeyFromObject(r.Scheme, eventBasedAddOnScope.EventBasedAddOn)

	for k, l := range r.ClusterMap {
		l.Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
		if l.Len() == 0 {
			delete(r.ClusterMap, k)
		}
	}

	delete(r.ToEventSourceMap, types.NamespacedName{Name: eventBasedAddOnScope.Name()})

	delete(r.ToClusterMap, types.NamespacedName{Name: eventBasedAddOnScope.Name()})

	for k, l := range r.EventSourceMap {
		l.Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
		if l.Len() == 0 {
			delete(r.EventSourceMap, k)
		}
	}

	delete(r.EventBasedAddOns, *eventBasedAddOnInfo)

	delete(r.EventBasedAddOnMap, types.NamespacedName{Name: eventBasedAddOnScope.Name()})

	for i := range r.ReferenceMap {
		eventBasedAddOnSet := r.ReferenceMap[i]
		eventBasedAddOnSet.Erase(eventBasedAddOnInfo)
	}
}

func (r *EventBasedAddOnReconciler) updateMaps(eventBasedAddOnScope *scope.EventBasedAddOnScope) {
	r.updateClusterMaps(eventBasedAddOnScope)

	r.updateEventSourceMaps(eventBasedAddOnScope)

	r.updateReferencedResourceMap(eventBasedAddOnScope)

	eventBasedAddOnInfo := getKeyFromObject(r.Scheme, eventBasedAddOnScope.EventBasedAddOn)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	r.EventBasedAddOns[*eventBasedAddOnInfo] = eventBasedAddOnScope.EventBasedAddOn.Spec.SourceClusterSelector
}

func (r *EventBasedAddOnReconciler) updateClusterMaps(eventBasedAddOnScope *scope.EventBasedAddOnScope) {
	currentClusters := &libsveltosset.Set{}
	for i := range eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs {
		cluster := eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{
			Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion,
		}
		currentClusters.Insert(clusterInfo)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	eventBasedAddOnInfo := getKeyFromObject(r.Scheme, eventBasedAddOnScope.EventBasedAddOn)

	// Get list of Clusters not matched anymore by EventBasedAddOn
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ToClusterMap[types.NamespacedName{Name: eventBasedAddOnScope.Name()}]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add EventBasedAddOn as consumer
	for i := range eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs {
		cluster := eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		r.getClusterMapForEntry(clusterInfo).Insert(eventBasedAddOnInfo)
	}

	// For each Cluster not matched anymore, remove EventBasedAddOn as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		r.getClusterMapForEntry(&clusterName).Erase(eventBasedAddOnInfo)
	}

	// Update list of Clusters currently referenced by EventBasedAddOn instance
	r.ToClusterMap[types.NamespacedName{Name: eventBasedAddOnScope.Name()}] = currentClusters
}

func (r *EventBasedAddOnReconciler) updateEventSourceMaps(eventBasedAddOnScope *scope.EventBasedAddOnScope) {
	// Get list of EventSource currently referenced
	currentReferences := &libsveltosset.Set{}
	currentReferences.Insert(&corev1.ObjectReference{
		APIVersion: libsveltosv1alpha1.GroupVersion.String(), // the only resources that can be referenced is EventSource
		Kind:       libsveltosv1alpha1.EventSourceKind,
		Name:       eventBasedAddOnScope.EventBasedAddOn.Spec.EventSourceName,
	})

	// Get list of References not referenced anymore by EventBasedAddOn
	var toBeRemoved []corev1.ObjectReference
	name := types.NamespacedName{Name: eventBasedAddOnScope.Name()}
	if v, ok := r.ToEventSourceMap[name]; ok {
		toBeRemoved = v.Difference(currentReferences)
	}

	// For each currently referenced instance, add EventBasedAddOn as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getEventSourceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
	}

	// For each resource not reference anymore, remove EventBasedAddOn as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getEventSourceMapForEntry(&referencedResource).Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
	}

	// Update list of EventSource instances currently referenced by EventBasedAddOn
	r.ToEventSourceMap[name] = currentReferences
}

func (r *EventBasedAddOnReconciler) updateReferencedResourceMap(eventBasedAddOnScope *scope.EventBasedAddOnScope) {
	// Get list of ConfigMap/Secret currently referenced
	currentReferences := r.getCurrentReferences(eventBasedAddOnScope)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of References not referenced anymore by EventBasedAddOn
	var toBeRemoved []corev1.ObjectReference
	eventBasedAddOnName := types.NamespacedName{Name: eventBasedAddOnScope.Name()}
	if v, ok := r.EventBasedAddOnMap[eventBasedAddOnName]; ok {
		toBeRemoved = v.Difference(currentReferences)
	}

	// For each currently referenced instance, add EventBasedAddOn as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getReferenceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
	}

	// For each resource not reference anymore, remove EventBasedAddOn as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getReferenceMapForEntry(&referencedResource).Erase(
			&corev1.ObjectReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       v1alpha1.EventBasedAddOnKind,
				Name:       eventBasedAddOnScope.Name(),
			},
		)
	}

	// Update list of ConfigMaps/Secrets currently referenced by EventBasedAddOn
	r.EventBasedAddOnMap[eventBasedAddOnName] = currentReferences
}

func (r *EventBasedAddOnReconciler) getEventSourceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.EventSourceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.EventSourceMap[*entry] = s
	}
	return s
}

func (r *EventBasedAddOnReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

func (r *EventBasedAddOnReconciler) getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ClusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ClusterMap[*entry] = s
	}
	return s
}

// updateClusterInfo updates EventBasedAddOn Status ClusterInfo by adding an entry for any
// new cluster matching EventBasedAddOn instance
func (r *EventBasedAddOnReconciler) updateClusterInfo(ctx context.Context,
	eventBasedAddOnScope *scope.EventBasedAddOnScope) error {

	chc := eventBasedAddOnScope.EventBasedAddOn

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

	eventBasedAddOnScope.SetClusterInfo(finalClusterInfo)
	return nil
}

func (r *EventBasedAddOnReconciler) getCurrentReferences(eventBasedAddOnScope *scope.EventBasedAddOnScope) *libsveltosset.Set {
	currentReferences := &libsveltosset.Set{}
	for i := range eventBasedAddOnScope.EventBasedAddOn.Spec.PolicyRefs {
		referencedNamespace := eventBasedAddOnScope.EventBasedAddOn.Spec.PolicyRefs[i].Namespace
		referencedName := eventBasedAddOnScope.EventBasedAddOn.Spec.PolicyRefs[i].Name

		// If referenced resource namespace is empty, at instantiation time the cluster namespace will be used.
		// Here to track referenced ConfigMaps/Resource, we use all current matching clusters
		for j := range eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs {
			clusterRef := eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs[j]
			namespace := getReferenceResourceNamespace(clusterRef.Namespace, referencedNamespace)

			currentReferences.Insert(&corev1.ObjectReference{
				APIVersion: corev1.SchemeGroupVersion.String(), // the only resources that can be referenced are Secret and ConfigMap
				Kind:       eventBasedAddOnScope.EventBasedAddOn.Spec.PolicyRefs[i].Kind,
				Namespace:  namespace,
				Name:       referencedName,
			})
		}
	}

	for i := range eventBasedAddOnScope.EventBasedAddOn.Spec.KustomizationRefs {
		referencedNamespace := eventBasedAddOnScope.EventBasedAddOn.Spec.KustomizationRefs[i].Namespace
		referencedName := eventBasedAddOnScope.EventBasedAddOn.Spec.KustomizationRefs[i].Name

		// If referenced resource namespace is empty, at instantiation time the cluster namespace will be used.
		// Here to track referenced ConfigMaps/Resource, we use all current matching clusters
		for j := range eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs {
			clusterRef := eventBasedAddOnScope.EventBasedAddOn.Status.MatchingClusterRefs[j]
			namespace := getReferenceResourceNamespace(clusterRef.Namespace, referencedNamespace)

			ref := &corev1.ObjectReference{
				Kind:      eventBasedAddOnScope.EventBasedAddOn.Spec.KustomizationRefs[i].Kind,
				Namespace: namespace,
				Name:      referencedName,
			}

			switch eventBasedAddOnScope.EventBasedAddOn.Spec.KustomizationRefs[i].Kind {
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
