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
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// Namespace where reports will be generated
	ReportNamespace = "projectsveltos"
)

type getCurrentHash func(tx context.Context, c client.Client,
	chc *v1alpha1.EventBasedAddOn, cluster *corev1.ObjectReference, logger logr.Logger) ([]byte, error)

type feature struct {
	id          string
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
	undeploy    deployer.RequestHandler
}

// deployEventBasedAddon update necessary resources in managed clusters
func (r *EventBasedAddOnReconciler) deployEventBasedAddOn(ctx context.Context, eScope *scope.EventBasedAddOnScope,
	f feature, logger logr.Logger) error {

	resource := eScope.EventBasedAddOn

	logger = logger.WithValues("eventbasedaddon", resource.Name)
	logger.V(logs.LogDebug).Info("request to evaluate/deploy")

	var errorSeen error
	allProcessed := true

	for i := range resource.Status.ClusterInfo {
		c := resource.Status.ClusterInfo[i]

		clusterInfo, err := r.processEventBasedAddOn(ctx, eScope, &c.Cluster, f, logger)
		if err != nil {
			errorSeen = err
		}
		if clusterInfo != nil {
			resource.Status.ClusterInfo[i] = *clusterInfo
			if clusterInfo.Status != libsveltosv1alpha1.SveltosStatusProvisioned {
				allProcessed = false
			}
		}
	}

	logger.V(logs.LogDebug).Info("set clusterInfo")
	eScope.SetClusterInfo(resource.Status.ClusterInfo)

	if errorSeen != nil {
		return errorSeen
	}

	if !allProcessed {
		return fmt.Errorf("request to process EventBasedAddOn is still queued in one ore more clusters")
	}

	return nil
}

// undeployEventBasedAddon clean resources in managed clusters
func (r *EventBasedAddOnReconciler) undeployEventBasedAddOn(ctx context.Context, eScope *scope.EventBasedAddOnScope,
	f feature, logger logr.Logger) error {

	resource := eScope.EventBasedAddOn

	logger = logger.WithValues("eventbasedaddon", resource.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	var err error
	for i := range resource.Status.ClusterInfo {
		c := &resource.Status.ClusterInfo[i].Cluster

		_, tmpErr := r.removeEventBasedAddOn(ctx, eScope, c, f, logger)
		if tmpErr != nil {
			err = tmpErr
		}
	}

	return err
}

// processEventBasedAddOnForCluster deploys necessary resources in managed cluster.
func processEventBasedAddOnForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1alpha1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("eventbasedaddon", applicant)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))

	resource := &v1alpha1.EventBasedAddOn{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("eventBasedAddOn not found")
			return nil
		}
		return err
	}

	if !resource.DeletionTimestamp.IsZero() {
		logger.V(logs.LogDebug).Info("eventBasedAddOn marked for deletion")
		return nil
	}

	logger.V(logs.LogDebug).Info("Deploy eventBasedAddOn")

	err = deployEventSource(ctx, c, clusterNamespace, clusterName, clusterType, resource, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to deploy referenced EventSource")
		return err
	}

	err = removeStaleEventSources(ctx, c, clusterNamespace, clusterName, clusterType, resource, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to remove stale EventSources")
		return err
	}

	logger.V(logs.LogDebug).Info("Deployed eventBasedAddOn")
	return nil
}

// undeployEventBasedAddOnResourcesFromCluster cleans resources associtated with EventBasedAddOn instance from cluster
func undeployEventBasedAddOnResourcesFromCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1alpha1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("eventbasedaddon", applicant)

	resource := &v1alpha1.EventBasedAddOn{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("eventBasedAddOn not found")
			return nil
		}
		return err
	}

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("Undeploy eventBasedAddOn")

	err = removeStaleEventSources(ctx, c, clusterNamespace, clusterName, clusterType, resource, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove eventSources: %v", err))
		return err
	}

	logger.V(logs.LogDebug).Info("Undeployed eventBasedAddOn")
	return nil
}

// eventBasedAddOnHash returns the EventBasedAddOn hash
func eventBasedAddOnHash(ctx context.Context, c client.Client,
	e *v1alpha1.EventBasedAddOn, cluster *corev1.ObjectReference, logger logr.Logger) ([]byte, error) {

	resources, err := fetchReferencedResources(ctx, c, e, cluster, logger)
	if err != nil {
		return nil, err
	}

	config := render.AsCode(e.Spec)

	for i := range resources {
		switch r := resources[i].(type) {
		case *corev1.ConfigMap:
			config += render.AsCode(r.Data)
		case *corev1.Secret:
			config += render.AsCode(r.Data)
		case *libsveltosv1alpha1.EventSource:
			config += render.AsCode(r.Spec)
		case *libsveltosv1alpha1.EventReport:
			config += render.AsCode(r.Spec)
		default:
			panic(1)
		}
	}

	h := sha256.New()
	h.Write([]byte(config))
	return h.Sum(nil), nil
}

// processEventBasedAddOn detects whether it is needed to deploy EventBasedAddon resources in current passed cluster.
func (r *EventBasedAddOnReconciler) processEventBasedAddOn(ctx context.Context, eScope *scope.EventBasedAddOnScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger,
) (*libsveltosv1alpha1.ClusterInfo, error) {

	if !isClusterStillMatching(eScope, cluster) {
		return r.removeEventBasedAddOn(ctx, eScope, cluster, f, logger)
	}

	resource := eScope.EventBasedAddOn

	// Get EventBasedAddOn Spec hash (at this very precise moment)
	currentHash, err := eventBasedAddOnHash(ctx, r.Client, resource, cluster, logger)
	if err != nil {
		return nil, err
	}

	proceed, err := r.canProceed(ctx, eScope, cluster, logger)
	if err != nil {
		return nil, err
	} else if !proceed {
		return nil, nil
	}

	// If undeploying feature is in progress, wait for it to complete.
	// Otherwise, if we redeploy feature while same feature is still being cleaned up, if two workers process those request in
	// parallel some resources might end up missing.
	if r.Deployer.IsInProgress(cluster.Namespace, cluster.Name, resource.Name, f.id, clusterproxy.GetClusterType(cluster), true) {
		logger.V(logs.LogDebug).Info("cleanup is in progress")
		return nil, fmt.Errorf("cleanup of %s in cluster still in progress. Wait before redeploying", f.id)
	}

	// Get the EventBasedAddOn hash when EventBasedAddOn was last deployed/evaluated in this cluster (if ever)
	hash, currentStatus := r.getClusterHashAndStatus(resource, cluster)
	isConfigSame := reflect.DeepEqual(hash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("EventBasedAddOn has changed. Current hash %x. Previous hash %x",
			currentHash, hash))
	}

	var status *libsveltosv1alpha1.SveltosFeatureStatus
	var result deployer.Result

	if isConfigSame {
		logger.V(logs.LogInfo).Info("EventBasedAddOn has not changed")
		result = r.Deployer.GetResult(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id,
			clusterproxy.GetClusterType(cluster), false)
		status = r.convertResultStatus(result)
	}

	if status != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("result is available %q. updating status.", *status))
		var errorMessage string
		if result.Err != nil {
			errorMessage = result.Err.Error()
		}
		clusterInfo := &libsveltosv1alpha1.ClusterInfo{
			Cluster:        *cluster,
			Status:         *status,
			Hash:           currentHash,
			FailureMessage: &errorMessage,
		}

		if *status == libsveltosv1alpha1.SveltosStatusProvisioned {
			return clusterInfo, nil
		}
		if *status == libsveltosv1alpha1.SveltosStatusProvisioning {
			return clusterInfo, fmt.Errorf("EventBasedAddOn is still being provisioned")
		}
	} else if isConfigSame && currentStatus != nil && *currentStatus == libsveltosv1alpha1.SveltosStatusProvisioned {
		logger.V(logs.LogInfo).Info("already deployed")
		s := libsveltosv1alpha1.SveltosStatusProvisioned
		status = &s
	} else {
		logger.V(logs.LogInfo).Info("no result is available. queue job and mark status as provisioning")
		s := libsveltosv1alpha1.SveltosStatusProvisioning
		status = &s

		// Getting here means either EventBasedAddOn failed to be deployed or EventBasedAddOn has changed.
		// EventBasedAddOn must be (re)deployed.
		if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id, clusterproxy.GetClusterType(cluster),
			false, processEventBasedAddOnForCluster, programDuration, deployer.Options{}); err != nil {
			return nil, err
		}
	}

	clusterInfo := &libsveltosv1alpha1.ClusterInfo{
		Cluster:        *cluster,
		Status:         *status,
		Hash:           currentHash,
		FailureMessage: nil,
	}

	return clusterInfo, nil
}

func (r *EventBasedAddOnReconciler) removeEventBasedAddOn(ctx context.Context, eScope *scope.EventBasedAddOnScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger) (*libsveltosv1alpha1.ClusterInfo, error) {

	resource := eScope.EventBasedAddOn

	logger = logger.WithValues("eventbasedaddon", resource.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	// Remove any queued entry to deploy/evaluate
	r.Deployer.CleanupEntries(cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), false)

	// If deploying feature is in progress, wait for it to complete.
	// Otherwise, if we cleanup feature while same feature is still being provisioned, if two workers process those request in
	// parallel some resources might be left over.
	if r.Deployer.IsInProgress(cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), false) {

		logger.V(logs.LogDebug).Info("provisioning is in progress")
		return nil, fmt.Errorf("deploying %s still in progress. Wait before cleanup", f.id)
	}

	if r.isClusterEntryRemoved(resource, cluster) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil, nil
	}

	result := r.Deployer.GetResult(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), true)
	status := r.convertResultStatus(result)

	clusterInfo := &libsveltosv1alpha1.ClusterInfo{
		Cluster: *cluster,
		Status:  libsveltosv1alpha1.SveltosStatusRemoving,
		Hash:    nil,
	}

	if status != nil {
		if *status == libsveltosv1alpha1.SveltosStatusRemoving {
			return clusterInfo, fmt.Errorf("feature is still being removed")
		}

		if *status == libsveltosv1alpha1.SveltosStatusRemoved {
			if err := removeClusterInfoEntry(ctx, r.Client, cluster.Namespace, cluster.Name,
				clusterproxy.GetClusterType(cluster), resource, logger); err != nil {
				return nil, err
			}
			return clusterInfo, nil
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), true,
		undeployEventBasedAddOnResourcesFromCluster, programDuration, deployer.Options{}); err != nil {
		return nil, err
	}

	return clusterInfo, fmt.Errorf("cleanup request is queued")
}

// isClusterEntryRemoved returns true if feature is there is no entry for cluster in Status.ClusterInfo
func (r *EventBasedAddOnReconciler) isClusterEntryRemoved(resource *v1alpha1.EventBasedAddOn,
	cluster *corev1.ObjectReference) bool {

	for i := range resource.Status.ClusterInfo {
		cc := &resource.Status.ClusterInfo[i]
		if isClusterInfoForCluster(cc, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return false
		}
	}
	return true
}

func (r *EventBasedAddOnReconciler) convertResultStatus(result deployer.Result) *libsveltosv1alpha1.SveltosFeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := libsveltosv1alpha1.SveltosStatusProvisioned
		return &s
	case deployer.Failed:
		s := libsveltosv1alpha1.SveltosStatusFailed
		return &s
	case deployer.InProgress:
		s := libsveltosv1alpha1.SveltosStatusProvisioning
		return &s
	case deployer.Removed:
		s := libsveltosv1alpha1.SveltosStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}

// getClusterHashAndStatus returns the hash of the EventBasedAddOn that was deployed/evaluated in a given
// Cluster (if ever deployed/evaluated)
func (r *EventBasedAddOnReconciler) getClusterHashAndStatus(resource *v1alpha1.EventBasedAddOn,
	cluster *corev1.ObjectReference) ([]byte, *libsveltosv1alpha1.SveltosFeatureStatus) {

	for i := range resource.Status.ClusterInfo {
		clusterInfo := &resource.Status.ClusterInfo[i]
		if isClusterInfoForCluster(clusterInfo, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return clusterInfo.Hash, &clusterInfo.Status
		}
	}

	return nil, nil
}

// isPaused returns true if Sveltos/CAPI Cluster is paused or EventBasedAddOn has paused annotation.
func (r *EventBasedAddOnReconciler) isPaused(ctx context.Context, cluster *corev1.ObjectReference,
	resource *v1alpha1.EventBasedAddOn) (bool, error) {

	isClusterPaused, err := clusterproxy.IsClusterPaused(ctx, r.Client, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if isClusterPaused {
		return true, nil
	}

	return annotations.HasPaused(resource), nil
}

// canProceed returns true if cluster is ready to be programmed and it is not paused.
func (r *EventBasedAddOnReconciler) canProceed(ctx context.Context, eScope *scope.EventBasedAddOnScope,
	cluster *corev1.ObjectReference, logger logr.Logger) (bool, error) {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))

	paused, err := r.isPaused(ctx, cluster, eScope.EventBasedAddOn)
	if err != nil {
		return false, err
	}

	if paused {
		logger.V(logs.LogDebug).Info("Cluster/EventBasedAddOn is paused")
		return false, nil
	}

	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, r.Client, cluster, eScope.Logger)
	if err != nil {
		return false, err
	}

	if !ready {
		logger.V(logs.LogInfo).Info("Cluster is not ready yet")
		return false, nil
	}

	return true, nil
}

// isClusterStillMatching returns true if cluster is still matching by looking at EventBasedAddon
// Status MatchingClusterRefs
func isClusterStillMatching(eScope *scope.EventBasedAddOnScope, cluster *corev1.ObjectReference) bool {
	for i := range eScope.EventBasedAddOn.Status.MatchingClusterRefs {
		matchingCluster := &eScope.EventBasedAddOn.Status.MatchingClusterRefs[i]
		if reflect.DeepEqual(*matchingCluster, *cluster) {
			return true
		}
	}
	return false
}

// isClusterConditionForCluster returns true if the ClusterCondition is for the cluster clusterType, clusterNamespace,
// clusterName
func isClusterInfoForCluster(clusterInfo *libsveltosv1alpha1.ClusterInfo, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) bool {

	return clusterInfo.Cluster.Namespace == clusterNamespace &&
		clusterInfo.Cluster.Name == clusterName &&
		clusterproxy.GetClusterType(&clusterInfo.Cluster) == clusterType
}

func removeClusterInfoEntry(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	resource *v1alpha1.EventBasedAddOn, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentResource := &v1alpha1.EventBasedAddOn{}
		err := c.Get(ctx, types.NamespacedName{Name: resource.Name}, currentResource)
		if err != nil {
			return err
		}

		for i := range currentResource.Status.ClusterInfo {
			cc := &currentResource.Status.ClusterInfo[i]
			if isClusterInfoForCluster(cc, clusterNamespace, clusterName, clusterType) {
				currentResource.Status.ClusterInfo = remove(currentResource.Status.ClusterInfo, i)
				return c.Status().Update(context.TODO(), currentResource)
			}
		}

		return nil
	})

	return err
}

func remove(s []libsveltosv1alpha1.ClusterInfo, i int) []libsveltosv1alpha1.ClusterInfo {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

// deployEventSource deploys (creates or updates) referenced EventSource.
func deployEventSource(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	resource *v1alpha1.EventBasedAddOn, logger logr.Logger) error {

	currentReferenced, err := fetchEventSource(ctx, c, resource.Spec.EventSourceName)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect EventSource: %v", err))
		return err
	}
	if currentReferenced == nil {
		logger.V(logs.LogInfo).Info("EventSource not found")
		return nil
	}

	var remoteClient client.Client
	remoteClient, err = clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName, "",
		clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	// classifier installs sveltos-agent and CRDs it needs, including
	// EventSource and EventReport CRDs.

	err = createOrUpdateEventSource(ctx, remoteClient, resource, currentReferenced, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to create/update HealthCheck: %v", err))
		return err
	}

	return nil
}

func createOrUpdateEventSource(ctx context.Context, remoteClient client.Client, resource *v1alpha1.EventBasedAddOn,
	eventSource *libsveltosv1alpha1.EventSource, logger logr.Logger) error {

	logger = logger.WithValues("eventSource", eventSource.Name)

	currentEventSource := &libsveltosv1alpha1.EventSource{}
	err := remoteClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
	if err == nil {
		logger.V(logs.LogDebug).Info("updating eventSource")
		currentEventSource.Spec = eventSource.Spec
		deployer.AddOwnerReference(currentEventSource, resource)
		return remoteClient.Update(ctx, currentEventSource)
	}

	currentEventSource.Name = eventSource.Name
	currentEventSource.Spec = eventSource.Spec
	deployer.AddOwnerReference(currentEventSource, resource)

	logger.V(logs.LogDebug).Info("creating eventSource")
	return remoteClient.Create(ctx, currentEventSource)
}

// removeStaleEventSources removes stale EventSources.
// - If EventBasedAddOn is deleted, EventBasedAddOn will be removed as OwnerReference from any
// EventSource instance;
// - If EventBasedAddOn is still existing, EventBasedAddOn will be removed as OwnerReference from any
// EventSource instance it used to referenced and it is not referencing anymore.
// An EventSource with zero OwnerReference will be deleted from managed cluster.
func removeStaleEventSources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	resource *v1alpha1.EventBasedAddOn, logger logr.Logger) error {

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName, "",
		clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	eventSources := &libsveltosv1alpha1.EventSourceList{}
	err = remoteClient.List(ctx, eventSources)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get list eventSources: %v", err))
		return err
	}

	for i := range eventSources.Items {
		es := &eventSources.Items[i]
		l := logger.WithValues("eventsource", es.Name)

		if es.Name == resource.Spec.EventSourceName {
			// eventSource is still referenced
			continue
		}

		if !util.IsOwnedByObject(es, resource) {
			continue
		}

		l.V(logs.LogDebug).Info("removing OwnerReference")
		deployer.RemoveOwnerReference(es, resource)

		if len(es.GetOwnerReferences()) != 0 {
			l.V(logs.LogDebug).Info("updating")
			// Other EventBasedAddOn are still deploying this very same policy
			err = remoteClient.Update(ctx, es)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get update EventSource: %v", err))
				return err
			}
			continue
		}

		l.V(logs.LogDebug).Info("deleting")
		err = remoteClient.Delete(ctx, es)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get delete EventSource: %v", err))
			return err
		}
	}

	return nil
}
