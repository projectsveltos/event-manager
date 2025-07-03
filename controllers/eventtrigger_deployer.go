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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"text/template"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/sharding"
)

const (
	// Namespace where reports will be generated
	ReportNamespace = "projectsveltos"

	// TODO: remove this (it is replaced by eventSourceNameLabel)
	eventReportNameLabel = "eventtrigger.lib.projectsveltos.io/eventreportname"

	eventSourceNameLabel             = "eventtrigger.lib.projectsveltos.io/eventsourcename"
	eventTriggerNameLabel            = "eventtrigger.lib.projectsveltos.io/eventtriggername"
	clusterNamespaceLabel            = "eventtrigger.lib.projectsveltos.io/clusterNamespace"
	clusterNameLabel                 = "eventtrigger.lib.projectsveltos.io/clustername"
	clusterTypeLabel                 = "eventtrigger.lib.projectsveltos.io/clustertype"
	cloudEventSourceLabel            = "eventtrigger.lib.projectsveltos.io/cesource"
	cloudEventSubjectLabel           = "eventtrigger.lib.projectsveltos.io/cesubject"
	generatorLabel                   = "eventtrigger.lib.projectsveltos.io/fromgenerator"
	referencedResourceNamespaceLabel = "eventtrigger.lib.projectsveltos.io/refnamespace"
	referencedResourceNameLabel      = "eventtrigger.lib.projectsveltos.io/refname"
)

type getCurrentHash func(tx context.Context, c client.Client,
	chc *v1beta1.EventTrigger, cluster *corev1.ObjectReference, logger logr.Logger) ([]byte, error)

type feature struct {
	id          string
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
	undeploy    deployer.RequestHandler
}

func (r *EventTriggerReconciler) isClusterAShardMatch(ctx context.Context,
	clusterInfo *libsveltosv1beta1.ClusterInfo) (bool, error) {

	clusterType := clusterproxy.GetClusterType(&clusterInfo.Cluster)
	cluster, err := clusterproxy.GetCluster(ctx, r.Client, clusterInfo.Cluster.Namespace,
		clusterInfo.Cluster.Name, clusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	return sharding.IsShardAMatch(r.ShardKey, cluster), nil
}

// deployEventBasedAddon update necessary resources (eventSource) in the managed clusters
func (r *EventTriggerReconciler) deployEventTrigger(ctx context.Context, eScope *scope.EventTriggerScope,
	f feature, logger logr.Logger) error {

	resource := eScope.EventTrigger

	logger = logger.WithValues("eventTrigger", resource.Name)
	logger.V(logs.LogDebug).Info("request to evaluate/deploy")

	var errorSeen error
	allProcessed := true

	for i := range resource.Status.ClusterInfo {
		c := &resource.Status.ClusterInfo[i]

		shardMatch, err := r.isClusterAShardMatch(ctx, c)
		if err != nil {
			return err
		}

		var clusterInfo *libsveltosv1beta1.ClusterInfo
		if !shardMatch {
			l := logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s",
				c.Cluster.Kind, c.Cluster.Namespace, c.Cluster.Name))
			l.V(logs.LogDebug).Info("cluster is not a shard match")
			// Since cluster is not a shard match, another deployment will deploy and update
			// this specific clusterInfo status. Here we simply return current status.
			clusterInfo = c
			if clusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
				allProcessed = false
			}
			// This is a required parameter. It is set by the deployment matching the
			// cluster shard. if not set yet, set it to empty
			if clusterInfo.Hash == nil {
				str := base64.StdEncoding.EncodeToString([]byte("empty"))
				clusterInfo.Hash = []byte(str)
			}
		} else {
			clusterInfo, err = r.processEventTrigger(ctx, eScope, &c.Cluster, f, logger)
			if err != nil {
				errorSeen = err
			}
			if clusterInfo != nil {
				resource.Status.ClusterInfo[i] = *clusterInfo
				if clusterInfo.Status != libsveltosv1beta1.SveltosStatusProvisioned {
					allProcessed = false
				}
			}
		}
	}

	logger.V(logs.LogDebug).Info("set clusterInfo")
	eScope.SetClusterInfo(resource.Status.ClusterInfo)

	if errorSeen != nil {
		return errorSeen
	}

	if !allProcessed {
		return fmt.Errorf("request to process EventTrigger is still queued in one ore more clusters")
	}

	return nil
}

// undeployEventBasedAddon clean resources in managed clusters
func (r *EventTriggerReconciler) undeployEventTrigger(ctx context.Context, eScope *scope.EventTriggerScope,
	clusterInfo []libsveltosv1beta1.ClusterInfo, logger logr.Logger) error {

	f := getHandlersForFeature(v1beta1.FeatureEventTrigger)

	resource := eScope.EventTrigger

	logger = logger.WithValues("eventTrigger", resource.Name)
	logger.V(logs.LogDebug).Info("request to undeploy")

	var err error
	for i := range clusterInfo {
		shardMatch, tmpErr := r.isClusterAShardMatch(ctx, &clusterInfo[i])
		if tmpErr != nil {
			err = tmpErr
			continue
		}

		if !shardMatch && clusterInfo[i].Status != libsveltosv1beta1.SveltosStatusRemoved {
			// If shard is not a match, wait for other controller to remove
			err = fmt.Errorf("remove pending")
			continue
		}

		c := &clusterInfo[i].Cluster

		logger.V(logs.LogDebug).Info(fmt.Sprintf("undeploy EventTrigger from cluster %s:%s/%s",
			c.Kind, c.Namespace, c.Name))
		_, tmpErr = r.removeEventTrigger(ctx, eScope, c, f, logger)
		if tmpErr != nil {
			err = tmpErr
			continue
		}

		clusterInfo[i].Status = libsveltosv1beta1.SveltosStatusRemoved
	}

	return err
}

// processEventTriggerForCluster deploys necessary resources in managed cluster.
func processEventTriggerForCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1beta1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("eventTrigger", applicant)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))

	resource := &v1beta1.EventTrigger{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("eventTrigger not found")
			return nil
		}
		return err
	}

	if !resource.DeletionTimestamp.IsZero() {
		logger.V(logs.LogDebug).Info("eventTrigger marked for deletion")
		return nil
	}

	logger.V(logs.LogDebug).Info("Deploy eventTrigger")

	err = deployEventSource(ctx, c, clusterNamespace, clusterName, clusterType, resource, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to deploy referenced EventSource")
		return err
	}

	err = removeStaleEventSources(ctx, c, clusterNamespace, clusterName, clusterType, resource, false, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("failed to remove stale EventSources")
		return err
	}

	logger.V(logs.LogDebug).Info("Deployed eventTrigger")
	return nil
}

// undeployEventTriggerResourcesFromCluster cleans resources associtated with EventTrigger instance:
// - resources (EventSource) from managed cluster
// - resources (EventReports) from the management cluster (those were pulled from the managed cluster)
// - resources instantiated in the management cluster (ConfigMap/Secrets expressed as templated referenced
// in PolicyRefs/ValuesFrom sections)
func undeployEventTriggerResourcesFromCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1beta1.ClusterType, options deployer.Options, logger logr.Logger) error {

	logger = logger.WithValues("eventTrigger", applicant)

	resource := &v1beta1.EventTrigger{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, resource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("eventTrigger not found")
			return nil
		}
		return err
	}

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("Undeploy eventTrigger")

	err = removeStaleEventSources(ctx, c, clusterNamespace, clusterName, clusterType, resource, true, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove eventSources: %v", err))
		return err
	}

	logger.V(logs.LogDebug).Info("Undeployed eventTrigger.")

	logger.V(logs.LogDebug).Info("Clearing instantiated ClusterProfile/ConfigMap/Secret instances")
	return removeInstantiatedResources(ctx, c, clusterNamespace, clusterName, clusterType, resource,
		nil, nil, nil, logger)
}

// eventTriggerHash returns the EventTrigger hash
func eventTriggerHash(ctx context.Context, c client.Client,
	e *v1beta1.EventTrigger, cluster *corev1.ObjectReference, logger logr.Logger) ([]byte, error) {

	resources, err := fetchReferencedResources(ctx, c, e, cluster, logger)
	if err != nil {
		return nil, err
	}

	config := getVersion()
	config += render.AsCode(e.Spec)
	config += render.AsCode(e.Labels)

	for i := range resources {
		switch r := resources[i].(type) {
		case *corev1.ConfigMap:
			config += render.AsCode(r.Data)
		case *corev1.Secret:
			config += render.AsCode(r.Data)
		case *sourcev1b2.Bucket:
			config += render.AsCode(r.Status.Artifact)
		case *sourcev1b2.OCIRepository:
			config += render.AsCode(r.Status.Artifact)
		case *sourcev1.GitRepository:
			config += render.AsCode(r.Status.Artifact)
		case *libsveltosv1beta1.EventSource:
			config += render.AsCode(r.Spec)
		case *libsveltosv1beta1.EventReport:
			config += render.AsCode(r.Spec)
		default:
			panic(1)
		}
	}

	// When in agentless mode, EventSources instances are not copued to managed cluster anymore.
	// This addition ensures the EvenTrigger is redeployed due to the change in deployment location.
	if getAgentInMgmtCluster() {
		config += ("agentless")
	}

	h := sha256.New()
	h.Write([]byte(config))
	return h.Sum(nil), nil
}

// processEventTrigger detects whether it is needed to deploy EventBasedAddon resources in current passed cluster.
func (r *EventTriggerReconciler) processEventTrigger(ctx context.Context, eScope *scope.EventTriggerScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger,
) (*libsveltosv1beta1.ClusterInfo, error) {

	if !isClusterStillMatching(eScope, cluster) {
		return r.removeEventTrigger(ctx, eScope, cluster, f, logger)
	}

	resource := eScope.EventTrigger

	// Get EventTrigger Spec hash (at this very precise moment)
	currentHash, err := eventTriggerHash(ctx, r.Client, resource, cluster, logger)
	if err != nil {
		return nil, err
	}

	proceed, err := r.canProceed(ctx, eScope, cluster, logger)
	if err != nil {
		return nil, err
	} else if !proceed {
		return nil, nil
	}

	// Remove any queued entry to cleanup
	r.Deployer.CleanupEntries(cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), true)

	// If undeploying feature is in progress, wait for it to complete.
	// Otherwise, if we redeploy feature while same feature is still being cleaned up, if two workers process those request in
	// parallel some resources might end up missing.
	if r.Deployer.IsInProgress(cluster.Namespace, cluster.Name, resource.Name, f.id, clusterproxy.GetClusterType(cluster), true) {
		logger.V(logs.LogDebug).Info("cleanup is in progress")
		return nil, fmt.Errorf("cleanup of %s in cluster still in progress. Wait before redeploying", f.id)
	}

	// Get the EventTrigger hash when EventTrigger was last deployed/evaluated in this cluster (if ever)
	hash, currentStatus := r.getClusterHashAndStatus(resource, cluster)
	isConfigSame := reflect.DeepEqual(hash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("EventTrigger has changed. Current hash %x. Previous hash %x",
			currentHash, hash))
	}

	var status *libsveltosv1beta1.SveltosFeatureStatus
	var result deployer.Result

	if isConfigSame {
		logger.V(logs.LogInfo).Info("EventTrigger has not changed")
		result = r.Deployer.GetResult(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id,
			clusterproxy.GetClusterType(cluster), false)
		status = r.convertResultStatus(result)
	}

	clusterInfo := &libsveltosv1beta1.ClusterInfo{
		Cluster:        *cluster,
		Hash:           currentHash,
		FailureMessage: nil,
	}

	previousError := eScope.GetFailureMessage(cluster)
	clusterInfo.FailureMessage = previousError
	if status != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("result is available %q. updating status.", *status))
		if result.Err != nil {
			errorMessage := result.Err.Error()
			clusterInfo.FailureMessage = &errorMessage
		}

		if *status == libsveltosv1beta1.SveltosStatusProvisioned {
			clusterInfo.Status = *status
			clusterInfo.FailureMessage = nil
			return clusterInfo, nil
		}

		if *status == libsveltosv1beta1.SveltosStatusProvisioning {
			clusterInfo.Status = *status
			return clusterInfo, fmt.Errorf("EventTrigger is still being provisioned")
		}
	} else if isConfigSame && currentStatus != nil && *currentStatus == libsveltosv1beta1.SveltosStatusProvisioned {
		logger.V(logs.LogInfo).Info("already deployed")
		s := libsveltosv1beta1.SveltosStatusProvisioned
		clusterInfo.Status = s
		clusterInfo.FailureMessage = nil
	} else {
		logger.V(logs.LogInfo).Info("no result is available. queue job and mark status as provisioning")
		s := libsveltosv1beta1.SveltosStatusProvisioning
		clusterInfo.Status = s

		// Getting here means either EventTrigger failed to be deployed or EventTrigger has changed.
		// EventTrigger must be (re)deployed.
		if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id, clusterproxy.GetClusterType(cluster),
			false, f.deploy, programDuration, deployer.Options{}); err != nil {
			return nil, err
		}
	}

	return clusterInfo, nil
}

func (r *EventTriggerReconciler) removeEventTrigger(ctx context.Context, eScope *scope.EventTriggerScope,
	cluster *corev1.ObjectReference, f feature, logger logr.Logger) (*libsveltosv1beta1.ClusterInfo, error) {

	resource := eScope.EventTrigger

	logger = logger.WithValues("eventTrigger", resource.Name)
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

	clusterInfo := &libsveltosv1beta1.ClusterInfo{
		Cluster: *cluster,
		Status:  libsveltosv1beta1.SveltosStatusRemoving,
		Hash:    nil,
	}

	if status != nil {
		if *status == libsveltosv1beta1.SveltosStatusRemoving {
			return clusterInfo, fmt.Errorf("feature is still being removed")
		}

		if *status == libsveltosv1beta1.SveltosStatusRemoved {
			logger.V(logs.LogDebug).Info("status is removed")
			if err := removeClusterInfoEntry(ctx, r.Client, cluster.Namespace, cluster.Name,
				clusterproxy.GetClusterType(cluster), resource, logger); err != nil {
				return nil, err
			}
			clusterInfo.Status = libsveltosv1beta1.SveltosStatusRemoved
			return clusterInfo, nil
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, cluster.Namespace, cluster.Name, resource.Name, f.id,
		clusterproxy.GetClusterType(cluster), true,
		f.undeploy, programDuration, deployer.Options{}); err != nil {
		return nil, err
	}

	return clusterInfo, fmt.Errorf("cleanup request is queued")
}

// isClusterEntryRemoved returns true if feature is there is no entry for cluster in Status.ClusterInfo
func (r *EventTriggerReconciler) isClusterEntryRemoved(resource *v1beta1.EventTrigger,
	cluster *corev1.ObjectReference) bool {

	for i := range resource.Status.ClusterInfo {
		cc := &resource.Status.ClusterInfo[i]
		if isClusterInfoForCluster(cc, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return false
		}
	}
	return true
}

func (r *EventTriggerReconciler) convertResultStatus(result deployer.Result) *libsveltosv1beta1.SveltosFeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := libsveltosv1beta1.SveltosStatusProvisioned
		return &s
	case deployer.Failed:
		s := libsveltosv1beta1.SveltosStatusFailed
		return &s
	case deployer.InProgress:
		s := libsveltosv1beta1.SveltosStatusProvisioning
		return &s
	case deployer.Removed:
		s := libsveltosv1beta1.SveltosStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}

// getClusterHashAndStatus returns the hash of the EventTrigger that was deployed/evaluated in a given
// Cluster (if ever deployed/evaluated)
func (r *EventTriggerReconciler) getClusterHashAndStatus(resource *v1beta1.EventTrigger,
	cluster *corev1.ObjectReference) ([]byte, *libsveltosv1beta1.SveltosFeatureStatus) {

	for i := range resource.Status.ClusterInfo {
		clusterInfo := &resource.Status.ClusterInfo[i]
		if isClusterInfoForCluster(clusterInfo, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster)) {
			return clusterInfo.Hash, &clusterInfo.Status
		}
	}

	return nil, nil
}

// isPaused returns true if Sveltos/CAPI Cluster is paused or EventTrigger has paused annotation.
func (r *EventTriggerReconciler) isPaused(ctx context.Context, cluster *corev1.ObjectReference,
	resource *v1beta1.EventTrigger) (bool, error) {

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
func (r *EventTriggerReconciler) canProceed(ctx context.Context, eScope *scope.EventTriggerScope,
	cluster *corev1.ObjectReference, logger logr.Logger) (bool, error) {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))

	paused, err := r.isPaused(ctx, cluster, eScope.EventTrigger)
	if err != nil {
		return false, err
	}

	if paused {
		logger.V(logs.LogDebug).Info("Cluster/EventTrigger is paused")
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
func isClusterStillMatching(eScope *scope.EventTriggerScope, cluster *corev1.ObjectReference) bool {
	for i := range eScope.EventTrigger.Status.MatchingClusterRefs {
		matchingCluster := &eScope.EventTrigger.Status.MatchingClusterRefs[i]
		if reflect.DeepEqual(*matchingCluster, *cluster) {
			return true
		}
	}
	return false
}

// isClusterConditionForCluster returns true if the ClusterCondition is for the cluster clusterType, clusterNamespace,
// clusterName
func isClusterInfoForCluster(clusterInfo *libsveltosv1beta1.ClusterInfo, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) bool {

	return clusterInfo.Cluster.Namespace == clusterNamespace &&
		clusterInfo.Cluster.Name == clusterName &&
		clusterproxy.GetClusterType(&clusterInfo.Cluster) == clusterType
}

func removeClusterInfoEntry(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	resource *v1beta1.EventTrigger, logger logr.Logger) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentResource := &v1beta1.EventTrigger{}
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

func remove(s []libsveltosv1beta1.ClusterInfo, i int) []libsveltosv1beta1.ClusterInfo {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

// deployEventSource deploys (creates or updates) referenced EventSource.
func deployEventSource(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	eventTrigger *v1beta1.EventTrigger, logger logr.Logger) error {

	currentEventSource, err := fetchEventSource(ctx, c, clusterNamespace, clusterName,
		eventTrigger.Spec.EventSourceName, clusterType, logger)
	if err != nil {
		return err
	}
	if currentEventSource == nil {
		return nil
	}

	if getAgentInMgmtCluster() {
		return addEventSourceToConfigMap(ctx, c, clusterNamespace, clusterName, clusterType,
			eventTrigger, currentEventSource, logger)
	}

	// If sveltos-agent is deployed to the managed cluster, deply EventSource there
	var remoteClient client.Client
	remoteClient, err = clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	// classifier installs sveltos-agent and CRDs it needs, including
	// EventSource and EventReport CRDs.

	err = createOrUpdateEventSource(ctx, remoteClient, eventTrigger, currentEventSource, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to create/update EventSource: %v", err))
		return err
	}

	return nil
}

func createOrUpdateEventSource(ctx context.Context, remoteClient client.Client, resource *v1beta1.EventTrigger,
	eventSource *libsveltosv1beta1.EventSource, logger logr.Logger) error {

	logger = logger.WithValues("eventSource", eventSource.Name)

	currentEventSource := &libsveltosv1beta1.EventSource{}
	err := remoteClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
	if err == nil {
		logger.V(logs.LogDebug).Info("updating eventSource")
		currentEventSource.Spec = eventSource.Spec
		// Copy labels. If admin-label is set, sveltos-agent will impersonate
		// ServiceAccount representing the tenant admin when fetching resources
		currentEventSource.Labels = eventSource.Labels
		currentEventSource.Annotations = map[string]string{
			libsveltosv1beta1.DeployedBySveltosAnnotation: "true",
		}
		k8s_utils.AddOwnerReference(currentEventSource, resource)
		return remoteClient.Update(ctx, currentEventSource)
	}

	currentEventSource.Name = eventSource.Name
	currentEventSource.Spec = eventSource.Spec
	// Copy labels. If admin-label is set, sveltos-agent will impersonate
	// ServiceAccount representing the tenant admin when fetching resources
	currentEventSource.Labels = eventSource.Labels
	currentEventSource.Annotations = map[string]string{
		libsveltosv1beta1.DeployedBySveltosAnnotation: "true",
	}
	k8s_utils.AddOwnerReference(currentEventSource, resource)

	logger.V(logs.LogDebug).Info("creating eventSource")
	return remoteClient.Create(ctx, currentEventSource)
}

// removeStaleEventReports removes stale EventReports from the management cluster.
func removeStaleEventReports(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, eventSourceName string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			libsveltosv1beta1.EventReportClusterNameLabel: clusterName,
			libsveltosv1beta1.EventReportClusterTypeLabel: strings.ToLower(string(clusterType)),
		},
	}

	if eventSourceName != "" {
		listOptions = append(listOptions,
			client.MatchingLabels{libsveltosv1beta1.EventSourceNameLabel: eventSourceName},
		)
	}

	eventReportList := &libsveltosv1beta1.EventReportList{}
	err := c.List(ctx, eventReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list EventReports. Err: %v", err))
		return err
	}

	for i := range eventReportList.Items {
		err = c.Delete(ctx, &eventReportList.Items[i])
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to delete EventReport. Err: %v", err))
			return err
		}
	}

	return nil
}

// removeStaleEventSources removes stale EventSources.
// - If EventTrigger is deleted, EventTrigger will be removed as OwnerReference from any
// EventSource instance;
// - If EventTrigger is still existing, EventTrigger will be removed as OwnerReference from any
// EventSource instance it used to referenced and it is not referencing anymore.
// An EventSource with zero OwnerReference will be deleted from managed cluster.
func removeStaleEventSources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	eventTrigger *v1beta1.EventTrigger, removeAll bool, logger logr.Logger) error {

	// If the cluster does not exist anymore, return (cluster has been deleted
	// there is nothing to clear)
	_, err := clusterproxy.GetCluster(ctx, c, clusterNamespace,
		clusterName, clusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Remove all EventReports pulled from this managed cluster because of this EventSource
			err = removeStaleEventReports(ctx, c, clusterNamespace, clusterName, "", clusterType, logger)
			if err != nil {
				return nil
			}
		}
		return err
	}

	if getAgentInMgmtCluster() {
		leaveEntry := ""
		if !removeAll && eventTrigger.DeletionTimestamp.IsZero() {
			// If removeAll is false and eventTrigger still exists, remove all entries but the one pointing
			// to current referenced EventSource
			leaveEntry = eventTrigger.Spec.EventSourceName
		}

		return removeEventSourceFromConfigMap(ctx, c, clusterNamespace, clusterName, clusterType, eventTrigger,
			leaveEntry, logger)
	}

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		"", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get managed cluster client: %v", err))
		return err
	}

	eventSources := &libsveltosv1beta1.EventSourceList{}
	err = remoteClient.List(ctx, eventSources)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get list eventSources: %v", err))
		return err
	}

	for i := range eventSources.Items {
		es := &eventSources.Items[i]
		l := logger.WithValues("eventsource", es.Name)

		// removeAll indicates all EventSources deployed by this EventTrigger on this cluster
		// need to be removed (cluster is no longer a match)
		if !removeAll && eventTrigger.DeletionTimestamp.IsZero() &&
			es.Name == eventTrigger.Spec.EventSourceName {
			// eventTrigger still exists and eventSource is still referenced
			continue
		}

		if !util.IsOwnedByObject(es, eventTrigger) {
			continue
		}

		l.V(logs.LogDebug).Info("removing OwnerReference")
		k8s_utils.RemoveOwnerReference(es, eventTrigger)

		if len(es.GetOwnerReferences()) != 0 {
			l.V(logs.LogDebug).Info("updating")
			// Other EventTrigger are still deploying this very same policy
			err = remoteClient.Update(ctx, es)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get update EventSource: %v", err))
				return err
			}
			continue
		}

		// Since EventSource is about to be removed from the managed cluster, removes all
		// EventReports pulled from this managed cluster because of this EventSource
		err = removeStaleEventReports(ctx, c, clusterNamespace, clusterName, es.Name, clusterType, logger)
		if err != nil {
			return nil
		}

		l.V(logs.LogDebug).Info("deleting EventSource from managed cluster")
		err = remoteClient.Delete(ctx, es)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get delete EventSource: %v", err))
			return err
		}
	}

	return nil
}

// When instantiating one ClusterProfile for all resources those values are available.
// Resources contains matching Kubernetes resources (this one only
// if EventSource.Spec.CollectResource is set to true).
// MatchingResources is always available if Kubernetes resources were a match.
// CloudEvents represent matching CloudEvents.
type currentObjects struct {
	MatchingResources []corev1.ObjectReference
	Resources         []map[string]interface{}
	CloudEvents       []map[string]interface{}
	Cluster           map[string]interface{}
}

// When instantiating one ClusterProfile per resource those values are available.
// Resource contains matching Kubernetes resource (this one only
// if EventSource.Spec.CollectResource is set to true).
// MatchingResource is always available if Kubernetes resources were a match.
// CloudEvent represent a match CloudEvent.
// For every object, either MatchingResource/Resource is available or CloudEvent
type currentObject struct {
	MatchingResource corev1.ObjectReference
	Resource         map[string]interface{}
	CloudEvent       map[string]interface{}
	Cluster          map[string]interface{}
}

// updateClusterProfiles creates/updates ClusterProfile(s).
// One or more clusterProfiles will be created/updated depending on eventTrigger.Spec.OneForEvent flag.
// ClusterProfile(s) content will have:
// - ClusterRef set to reference passed in cluster;
// - HelmCharts instantiated from EventTrigger.Spec.HelmCharts using values from resources, collected
// from the managed cluster, matching the EventSource referenced by EventTrigger
func updateClusterProfiles(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	logger logr.Logger) error {

	var err error
	// If no resource is currently matching, clear all
	if !er.DeletionTimestamp.IsZero() || !hasMatchingResources(er) {
		// ClusterProfiles created because of CloudEvents are removed when CloudEventAction is set to Delete.
		// Fetch all ClusterProfiles created because of CloudEvents by this eventTrigger and append to list
		// of ClusterProfiles that are not stale
		clusterProfiles := []*configv1beta1.ClusterProfile{}
		clusterProfiles, err = appendCloudEventClusterProfiles(ctx, c, clusterNamespace, clusterName, eventTrigger.Name,
			clusterType, er, clusterProfiles)
		if err != nil {
			return err
		}
		return removeInstantiatedResources(ctx, c, clusterNamespace, clusterName, clusterType,
			eventTrigger, er, clusterProfiles, nil, logger)
	}

	var clusterProfiles []*configv1beta1.ClusterProfile
	var fromGenerators []libsveltosv1beta1.PolicyRef

	// Resources (ClusterProfiles, ConfigMaps and Secrets) created because of CloudEvent contains the
	// cloudEventSubjectLabel and cloudEventSourceLabel. This means a CloudEvent is uniquely identified
	// by Sveltos by looking at just Subject and Source.

	if eventTrigger.Spec.OneForEvent {
		clusterProfiles, err = instantiateOneClusterProfilePerResource(ctx, c, clusterNamespace, clusterName,
			clusterType, eventTrigger, er, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to create one clusterProfile instance per matching resource: %v", err))
			return err
		}
		// Instantiate ConfigMap/Secrets from ConfigMapGenerator/SecretGenerator
		fromGenerators, err = instantiateFromGeneratorsPerResource(ctx, c, eventTrigger, er, clusterNamespace,
			clusterName, clusterType, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate from generators: %v", err))
			return err
		}
	} else {
		clusterProfiles, err = instantiateOneClusterProfilePerAllResource(ctx, c, clusterNamespace, clusterName,
			clusterType, eventTrigger, er, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to create one clusterProfile instance per matching resource: %v", err))
			return err
		}
		fromGenerators, err = instantiateFromGeneratorsPerAllResource(ctx, c, eventTrigger, er, clusterNamespace,
			clusterName, clusterType, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate from generators: %v", err))
			return err
		}
	}

	// ClusterProfiles created because of CloudEvents are removed when CloudEventAction is set to Delete.
	// Fetch all ClusterProfiles created because of CloudEvents by this eventTrigger and append to list
	// of ClusterProfiles that are not stale
	clusterProfiles, err = appendCloudEventClusterProfiles(ctx, c, clusterNamespace, clusterName, eventTrigger.Name,
		clusterType, er, clusterProfiles)
	if err != nil {
		return err
	}

	// Remove stale ClusterProfiles/ConfigMaps/Secrets, i.e, resources previously created by this EventTrigger
	// instance for this cluster but currently not needed anymore
	return removeInstantiatedResources(ctx, c, clusterNamespace, clusterName, clusterType, eventTrigger, er,
		clusterProfiles, fromGenerators, logger)
}

// instantiateOneClusterProfilePerResource instantiate a ClusterProfile for each resource/cloudEvent currently matching
// the referenced EventSource (result is taken from EventReport).
// When instantiating:
// - "MatchingResource" references a corev1.ObjectReference representing the resource (always available)
// - "Resource" references an unstructured.Unstructured referencing the resource (available only if EventSource.Spec.CollectResources
// is set to true)
// - "CloudEvent" references a cloudEvent
func instantiateOneClusterProfilePerResource(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger,
	eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) ([]*configv1beta1.ClusterProfile, error) {

	clusterProfiles := make([]*configv1beta1.ClusterProfile, 0)
	objects, err := prepareCurrentObjectList(ctx, c, clusterNamespace, clusterName, clusterType, eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to prepare currentObject list %v", err))
		return nil, err
	}

	for i := range objects {
		var clusterProfile *configv1beta1.ClusterProfile

		clusterProfile, err = instantiateClusterProfileForResource(ctx, c, clusterNamespace, clusterName,
			clusterType, eventTrigger, eventReport, &objects[i], logger)
		if err != nil {
			return nil, err
		}
		if clusterProfile != nil {
			clusterProfiles = append(clusterProfiles, clusterProfile)
		}
	}

	return clusterProfiles, nil
}

// instantiateClusterProfileForResource creates one ClusterProfile by:
// - setting Spec.ClusterRef reference passed in cluster clusterNamespace/clusterName/ClusterType
// - instantiating eventTrigger.Spec.HelmCharts with passed in resource (one of the resource matching referenced EventSource)
// and copying this value to ClusterProfile.Spec.HelmCharts
// - instantiating eventTrigger.Spec.PolicyRefs with passed in resource (one of the resource matching referenced EventSource)
// in new ConfigMaps/Secrets and have ClusterProfile.Spec.PolicyRefs reference those;
// - labels are added to ClusterProfile to easily fetch all ClusterProfiles created by a given EventTrigger
func instantiateClusterProfileForResource(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	object *currentObject, logger logr.Logger) (*configv1beta1.ClusterProfile, error) {

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)
	if object.CloudEvent != nil {
		labels = appendInstantiatedObjectLabelsForCloudEvent(labels, getCESource(object.CloudEvent),
			getCESubject(object.CloudEvent))
	} else {
		labels = appendInstantiatedObjectLabelsForResource(labels,
			object.MatchingResource.Namespace, object.MatchingResource.Name)
	}
	labels = appendServiceAccountLabels(eventTrigger, labels)

	clusterProfileName, err := getClusterProfileName(ctx, c, labels)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile name: %v", err))
		return nil, err
	}

	// It is important to add this label here (after we searched if a ClusterProfile already exists)
	if er != nil && er.Labels != nil {
		// Given eventReportNameLabel is now misleading (it contains the EventSource name not the
		// EventReport one) introducing eventSourceNameLabel. In few releases after v0.52.2 we can
		// remove eventReportNameLabel
		labels[eventSourceNameLabel] = er.Labels[libsveltosv1beta1.EventSourceNameLabel]
	}

	clusterProfile := getNonInstantiatedClusterProfile(eventTrigger, clusterProfileName, labels)
	if object.CloudEvent != nil {
		instantiatedCloudEventAction, err := instantiateCloudEventAction(clusterNamespace, clusterName, eventTrigger,
			object, logger)
		if err != nil {
			return nil, err
		}

		if *instantiatedCloudEventAction == v1beta1.CloudEventActionDelete {
			// Resources created because of a cloudEvent are ONLY removed when same (same subject/source) cloudEvent
			// is received and EventTrigger.Spec.CloudEventAction is set to delete.
			return nil, deleteClusterProfile(ctx, c, clusterProfile, logger)
		}
	}

	clusterProfileSpec, err := instantiateClusterProfileSpecForResource(ctx, c, clusterNamespace, clusterName,
		clusterType, eventTrigger, labels, object, logger)
	if err != nil {
		return nil, err
	}
	clusterProfile.Spec = *clusterProfileSpec

	addTypeInformationToObject(mgmtClusterSchema, clusterProfile)

	dr, err := k8s_utils.GetDynamicResourceInterface(mgmtClusterConfig,
		clusterProfile.GetObjectKind().GroupVersionKind(), clusterProfile.GetNamespace())
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return nil, err
	}

	return clusterProfile, updateResource(ctx, dr, clusterProfile, logger)
}

// instantiateClusterProfileSpecForResource creates one ClusterProfile.Spec per event
func instantiateClusterProfileSpecForResource(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, labels map[string]string,
	object *currentObject, logger logr.Logger) (*configv1beta1.Spec, error) {

	clusterProfileSpec := *getClusterProfileSpec(eventTrigger)

	templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)
	err := setTemplateResourceRefs(&clusterProfileSpec, templateName, object.Cluster, object,
		eventTrigger, logger)
	if err != nil {
		return nil, err
	}

	err = setClusterSelector(&clusterProfileSpec, clusterNamespace, clusterName, clusterType, eventTrigger,
		object, logger)
	if err != nil {
		return nil, err
	}

	instantiateHelmChartsWithResource, err := instantiateHelmChartsWithResource(ctx, c, eventTrigger,
		clusterNamespace, templateName, eventTrigger.Spec.HelmCharts, object, labels, logger)
	if err != nil {
		return nil, err
	}
	clusterProfileSpec.HelmCharts = instantiateHelmChartsWithResource

	instantiateKustomizeRefsWithResource, err := instantiateKustomizationRefsWithResource(ctx, c, eventTrigger,
		clusterNamespace, templateName, eventTrigger.Spec.KustomizationRefs, object, labels, logger)
	if err != nil {
		return nil, err
	}
	clusterProfileSpec.KustomizationRefs = instantiateKustomizeRefsWithResource

	err = setPolicyRefs(ctx, c, &clusterProfileSpec, templateName, clusterNamespace, clusterName, clusterType,
		object, eventTrigger, labels, logger)
	if err != nil {
		return nil, err
	}

	return &clusterProfileSpec, nil
}

// instantiateOneClusterProfilePerAllResource creates one ClusterProfile by:
// - setting Spec.ClusterRef reference passed in cluster clusterNamespace/clusterName/ClusterType
// - instantiating eventTrigger.Spec.HelmCharts with passed in resource (one of the resource matching referenced EventSource)
// and copying this value to ClusterProfile.Spec.HelmCharts
// - instantiating eventTrigger.Spec.PolicyRefs with passed in resource (one of the resource matching referenced EventSource)
// in new ConfigMaps/Secrets and have ClusterProfile.Spec.PolicyRefs reference those;
// - labels are added to ClusterProfile to easily fetch all ClusterProfiles created by a given EvnteTrigger
func instantiateOneClusterProfilePerAllResource(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger,
	eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) ([]*configv1beta1.ClusterProfile, error) {

	objects, err := prepareCurrentObjects(ctx, c, clusterNamespace, clusterName, clusterType,
		eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to prepare currentObjects %v", err))
		return nil, err
	}

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		eventReport, clusterType)
	labels = appendServiceAccountLabels(eventTrigger, labels)

	clusterProfileName, err := getClusterProfileName(ctx, c, labels)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile name: %v", err))
		return nil, err
	}

	// It is important to add this label here (after we searched if a ClusterProfile already exists)
	if eventReport != nil && eventReport.Labels != nil {
		// Given eventReportNameLabel is now misleading (it contains the EventSource name not the
		// EventReport one) introducing eventSourceNameLabel. In few releases after v0.52.2 we can
		// remove eventReportNameLabel
		labels[eventSourceNameLabel] = eventReport.Labels[libsveltosv1beta1.EventSourceNameLabel]
	}

	clusterProfile := getNonInstantiatedClusterProfile(eventTrigger, clusterProfileName, labels)

	clusterProfileSpec, err := instantiateClusterProfileSpecPerAllResource(ctx, c, clusterNamespace,
		clusterName, clusterType, eventTrigger, labels, objects, logger)
	if err != nil {
		return nil, err
	}
	clusterProfile.Spec = *clusterProfileSpec

	addTypeInformationToObject(mgmtClusterSchema, clusterProfile)

	dr, err := k8s_utils.GetDynamicResourceInterface(mgmtClusterConfig,
		clusterProfile.GetObjectKind().GroupVersionKind(), clusterProfile.GetNamespace())
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return nil, err
	}

	return []*configv1beta1.ClusterProfile{clusterProfile}, updateResource(ctx, dr, clusterProfile, logger)
}

func instantiateClusterProfileSpecPerAllResource(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, labels map[string]string,
	objects *currentObjects, logger logr.Logger) (*configv1beta1.Spec, error) {

	clusterProfileSpec := getClusterProfileSpec(eventTrigger)

	templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)
	err := setTemplateResourceRefs(clusterProfileSpec, templateName, objects.Cluster, objects,
		eventTrigger, logger)
	if err != nil {
		return nil, err
	}

	err = setClusterSelector(clusterProfileSpec, clusterNamespace, clusterName, clusterType, eventTrigger,
		objects, logger)
	if err != nil {
		return nil, err
	}

	instantiateHelmChartsWithResources, err := instantiateHelmChartsWithAllResources(ctx, c, eventTrigger,
		clusterNamespace, templateName, eventTrigger.Spec.HelmCharts, objects, labels, logger)
	if err != nil {
		return nil, err
	}
	clusterProfileSpec.HelmCharts = instantiateHelmChartsWithResources

	instantiateKustomizeRefsWithResource, err := instantiateKustomizationRefsWithAllResources(ctx, c, eventTrigger,
		clusterNamespace, templateName, eventTrigger.Spec.KustomizationRefs, objects, labels, logger)
	if err != nil {
		return nil, err
	}
	clusterProfileSpec.KustomizationRefs = instantiateKustomizeRefsWithResource

	clusterProfileSpec.DriftExclusions = eventTrigger.Spec.DriftExclusions

	err = setPolicyRefs(ctx, c, clusterProfileSpec, templateName, clusterNamespace, clusterName, clusterType,
		objects, eventTrigger, labels, logger)
	if err != nil {
		return nil, err
	}

	return clusterProfileSpec, nil
}

func setTemplateResourceRefs(clusterProfileSpec *configv1beta1.Spec,
	templateName string, clusterContent map[string]interface{}, data any, eventTrigger *v1beta1.EventTrigger,
	logger logr.Logger) error {

	templateResourceRefs, err := instantiateTemplateResourceRefs(templateName, clusterContent, data,
		eventTrigger.Spec.TemplateResourceRefs, funcmap.HasTextTemplateAnnotation(eventTrigger.Annotations))
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate TemplateResourceRefs: %v", err))
		return err
	}
	clusterProfileSpec.TemplateResourceRefs = templateResourceRefs
	return nil
}

func setPolicyRefs(ctx context.Context, c client.Client, clusterProfileSpec *configv1beta1.Spec,
	templateName, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	data any, eventTrigger *v1beta1.EventTrigger, labels map[string]string, logger logr.Logger) error {

	clusterRef := getClusterRef(clusterNamespace, clusterName, clusterType)
	localPolicyRef, remotePolicyRef, err := instantiateReferencedPolicyRefs(ctx, c, eventTrigger,
		templateName, eventTrigger, clusterRef, data, labels, logger)
	if err != nil {
		return err
	}
	clusterProfileSpec.PolicyRefs = getClusterProfilePolicyRefs(localPolicyRef, remotePolicyRef)
	return nil
}

func setClusterSelector(clusterProfileSpec *configv1beta1.Spec, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, data any,
	logger logr.Logger) error {

	useSameCluster := true
	if !reflect.DeepEqual(eventTrigger.Spec.DestinationClusterSelector, libsveltosv1beta1.Selector{}) {
		useSameCluster = false
		clusterProfileSpec.ClusterRefs = nil
		clusterProfileSpec.ClusterSelector = eventTrigger.Spec.DestinationClusterSelector
	}

	if eventTrigger.Spec.DestinationCluster != nil {
		useSameCluster = false
		templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)

		raw, err := json.Marshal(*eventTrigger.Spec.DestinationCluster)
		if err != nil {
			return err
		}

		instantiated, err := instantiateSection(templateName, raw, data, false, logger)
		if err != nil {
			return err
		}

		var destinationCluster corev1.ObjectReference
		if err := json.Unmarshal(instantiated, &destinationCluster); err != nil {
			return err
		}

		clusterProfileSpec.ClusterRefs = []corev1.ObjectReference{destinationCluster}
	}

	if useSameCluster {
		clusterProfileSpec.ClusterRefs = []corev1.ObjectReference{*getClusterRef(clusterNamespace, clusterName, clusterType)}
		clusterProfileSpec.ClusterSelector = libsveltosv1beta1.Selector{}
	}

	return nil
}

func getClusterProfilePolicyRefs(localPolicyRef, remotePolicyRef []configv1beta1.PolicyRef) []configv1beta1.PolicyRef {
	result := make([]configv1beta1.PolicyRef, len(localPolicyRef)+len(remotePolicyRef))

	// Add local policyRef
	for i := range localPolicyRef {
		result[i] = configv1beta1.PolicyRef{
			DeploymentType: configv1beta1.DeploymentTypeLocal,
			Namespace:      localPolicyRef[i].Namespace,
			Name:           localPolicyRef[i].Name,
			Kind:           localPolicyRef[i].Kind,
			Path:           localPolicyRef[i].Path,
		}
	}

	numOfPolicyItems := len(localPolicyRef)
	// Add remote policyRef
	for i := range remotePolicyRef {
		result[numOfPolicyItems+i] = configv1beta1.PolicyRef{
			DeploymentType: configv1beta1.DeploymentTypeRemote,
			Namespace:      remotePolicyRef[i].Namespace,
			Name:           remotePolicyRef[i].Name,
			Kind:           remotePolicyRef[i].Kind,
			Path:           remotePolicyRef[i].Path,
		}
	}

	return result
}

// getResources returns a slice of unstructured.Unstructured by processing eventReport.Spec.Resources field
func getResources(eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) ([]unstructured.Unstructured, error) {
	elements := strings.Split(string(eventReport.Spec.Resources), "---")
	result := make([]unstructured.Unstructured, 0)
	for i := range elements {
		if elements[i] == "" {
			continue
		}

		var err error
		var policy *unstructured.Unstructured
		policy, err = k8s_utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
			return nil, err
		}

		result = append(result, *policy)
	}

	return result, nil
}

// getCloudEvents returns a slice of map[string]interface{}
func getCloudEvents(eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) ([]map[string]interface{}, error) {
	result := make([]map[string]interface{}, 0)
	for i := range eventReport.Spec.CloudEvents {
		var data map[string]interface{}
		err := json.Unmarshal(eventReport.Spec.CloudEvents[i], &data)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse cloudEvent %v", err))
			return nil, err
		}

		result = append(result, data)
	}

	return result, nil
}

func instantiateSection(templateName string, toBeInstantiated []byte, data any,
	useTxtFuncMap bool, logger logr.Logger) ([]byte, error) {

	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(
		funcmap.SveltosFuncMap(useTxtFuncMap)).Parse(string(toBeInstantiated))
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse template: %v", err))
		return nil, err
	}

	var buffer bytes.Buffer
	if err = tmpl.Execute(&buffer, data); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to execute template: %v", err))
		return nil, err
	}

	return buffer.Bytes(), nil
}

func instantiateHelmCharts(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, helmCharts []configv1beta1.HelmChart, data any,
	labels map[string]string, logger logr.Logger) ([]configv1beta1.HelmChart, error) {

	helmChartJson, err := json.Marshal(helmCharts)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshel helmCharts: %v", err))
		return nil, err
	}

	instantiatedData, err := instantiateSection(templateName, helmChartJson, data,
		funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to execute template: %v", err))
		return nil, err
	}

	instantiatedHelmCharts := make([]configv1beta1.HelmChart, 0)
	err = json.Unmarshal(instantiatedData, &instantiatedHelmCharts)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to unmarshal helmCharts: %v", err))
		return nil, err
	}

	for i := range instantiatedHelmCharts {
		err = instantiateValuesFrom(ctx, c, e, instantiatedHelmCharts[i].ValuesFrom,
			clusterNamespace, templateName, data, labels, logger)
		if err != nil {
			return nil, err
		}
	}

	return instantiatedHelmCharts, nil
}

func instantiateKustomizationRefs(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, kustomizationRefs []configv1beta1.KustomizationRef,
	data any, labels map[string]string, logger logr.Logger) ([]configv1beta1.KustomizationRef, error) {

	kustomizationRefsJson, err := json.Marshal(kustomizationRefs)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshel kustomizationRefs: %v", err))
		return nil, err
	}

	instantiatedData, err := instantiateSection(templateName, kustomizationRefsJson, data,
		funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to execute template: %v", err))
		return nil, err
	}

	instantiatedKustomizationRefs := make([]configv1beta1.KustomizationRef, 0)
	err = json.Unmarshal(instantiatedData, &instantiatedKustomizationRefs)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to unmarshal kustomizationRefs: %v", err))
		return nil, err
	}

	for i := range instantiatedKustomizationRefs {
		err = instantiateValuesFrom(ctx, c, e, instantiatedKustomizationRefs[i].ValuesFrom,
			clusterNamespace, templateName, data, labels, logger)
		if err != nil {
			return nil, err
		}
	}

	return instantiatedKustomizationRefs, nil
}

func instantiateValuesFrom(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	valuesFrom []configv1beta1.ValueFrom, clusterNamespace, templateName string, data any, labels map[string]string,
	logger logr.Logger) error {

	for i := range valuesFrom {
		ref := &valuesFrom[i]

		var namespace string
		if ref.Namespace == "" {
			namespace = clusterNamespace
		} else {
			instantiantedNamespace, err := instantiateSection(templateName, []byte(ref.Namespace), data,
				funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name: %v", err))
				return err
			}
			namespace = string(instantiantedNamespace)
		}

		name, err := instantiateSection(templateName, []byte(ref.Name), data,
			funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name: %v", err))
			return err
		}

		var resource client.Object
		if ref.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			resource, err = getConfigMap(ctx, c,
				types.NamespacedName{Namespace: namespace, Name: string(name)})
		} else {
			resource, err = getSecret(ctx, c,
				types.NamespacedName{Namespace: namespace, Name: string(name)})
		}

		if err != nil {
			if apierrors.IsNotFound(err) {
				// referenced ConfigMap/Secret does not exist. Assume is not intended to be a template.
				// So there is no need to instantiate a new one. Generated ClusterProfile can directly
				// reference this one
				continue
			}
			return err
		}

		var info *types.NamespacedName
		if _, ok := resource.GetAnnotations()[v1beta1.InstantiateAnnotation]; !ok {
			// referenced ConfigMap/Secret is not a template. So there is no
			// need to instantiate a new one. Generated ClusterProfile can directly
			// reference this one
			info = &types.NamespacedName{Namespace: resource.GetNamespace(), Name: resource.GetName()}
		} else {
			name, err := getResourceName(ctx, c, resource, labels)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get %s name: %v", resource.GetObjectKind(), err))
				return err
			}
			// If referenced resource is a template, assume it needs to be instantiated using
			// information from the resources in the managed cluster that generated the event.
			// Generate then a new ConfigMap/Secret. The autocreated ClusterProfile will reference
			// this new resource.
			info, err = instantiateReferencedPolicy(ctx, e, resource, templateName, name, data, labels, logger)
			if err != nil {
				msg := fmt.Sprintf("failed to instantiate content for ValuesFrom: %s/%s",
					resource.GetNamespace(), resource.GetName())
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s. Error: %v", msg, err))
				return errors.Wrapf(err, "%s", msg)
			}
		}

		ref.Name = info.Name
		ref.Namespace = info.Namespace
	}

	return nil
}

func instantiateDataSection(templateName string, content map[string]string, data any,
	useTxtFuncMap bool, logger logr.Logger) (map[string]string, error) {

	contentJson, err := json.Marshal(content)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshal content: %v", err))
		return nil, err
	}

	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(
		funcmap.SveltosFuncMap(useTxtFuncMap)).Parse(string(contentJson))
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse content: %v", err))
		return nil, err
	}

	var buffer bytes.Buffer
	if err = tmpl.Execute(&buffer, data); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to execute content: %v", err))
		return nil, err
	}

	instantiatedContent := make(map[string]string)
	err = json.Unmarshal(buffer.Bytes(), &instantiatedContent)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to unmarshal content: %v", err))
		return nil, err
	}

	return instantiatedContent, nil
}

func instantiateTemplateResourceRefs(templateName string,
	clusterContent map[string]interface{}, data any, templateResourceRefs []configv1beta1.TemplateResourceRef,
	useTxtFuncMap bool) ([]configv1beta1.TemplateResourceRef, error) {

	var uCluster unstructured.Unstructured
	uCluster.SetUnstructuredContent(clusterContent)

	instantiated := make([]configv1beta1.TemplateResourceRef, len(templateResourceRefs))
	for i := range templateResourceRefs {
		tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(
			funcmap.SveltosFuncMap(useTxtFuncMap)).Parse(templateResourceRefs[i].Resource.Name)
		if err != nil {
			return nil, err
		}

		var nameBuffer bytes.Buffer
		err = tmpl.Execute(&nameBuffer, data)
		if err != nil {
			return nil, err
		}

		tmpl, err = template.New(templateName).Option("missingkey=error").Funcs(
			funcmap.SveltosFuncMap(useTxtFuncMap)).Parse(templateResourceRefs[i].Resource.Namespace)
		if err != nil {
			return nil, err
		}

		var namespaceBuffer bytes.Buffer
		err = tmpl.Execute(&namespaceBuffer, data)
		if err != nil {
			return nil, err
		}

		instantiated[i] = templateResourceRefs[i]
		instantiated[i].Resource.Namespace = namespaceBuffer.String()
		instantiated[i].Resource.Name = nameBuffer.String()
	}

	return instantiated, nil
}

// instantiateHelmChartsWithResource instantiate eventTrigger.Spec.HelmCharts using information from passed in object
// which represents one of the resource matching referenced EventSource in the managed cluster.
func instantiateHelmChartsWithResource(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, helmCharts []configv1beta1.HelmChart, data any,
	labels map[string]string, logger logr.Logger) ([]configv1beta1.HelmChart, error) {

	return instantiateHelmCharts(ctx, c, e, clusterNamespace, templateName, helmCharts, data, labels, logger)
}

// instantiateHelmChartsWithAllResources instantiate eventTrigger.Spec.HelmCharts using information from passed in objects
// which represent all of the resources matching referenced EventSource in the managed cluster.
func instantiateHelmChartsWithAllResources(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, helmCharts []configv1beta1.HelmChart, data any,
	labels map[string]string, logger logr.Logger) ([]configv1beta1.HelmChart, error) {

	return instantiateHelmCharts(ctx, c, e, clusterNamespace, templateName, helmCharts, data, labels, logger)
}

// instantiateKustomizationRefsWithResource instantiate eventTrigger.Spec.KustomizationRefs using information from passed
// in object which represents one of the resource matching referenced EventSource in the managed cluster.
func instantiateKustomizationRefsWithResource(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, kustomizationRefs []configv1beta1.KustomizationRef, data any,
	labels map[string]string, logger logr.Logger,
) ([]configv1beta1.KustomizationRef, error) {

	return instantiateKustomizationRefs(ctx, c, e, clusterNamespace, templateName, kustomizationRefs, data, labels, logger)
}

// instantiateKustomizationRefsWithAllResources instantiate eventTrigger.Spec.KustomizationRefs using information from passed
// in objects which represent all of the resources matching referenced EventSource in the managed cluster.
func instantiateKustomizationRefsWithAllResources(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	clusterNamespace, templateName string, kustomizationRefs []configv1beta1.KustomizationRef, data any,
	labels map[string]string, logger logr.Logger) ([]configv1beta1.KustomizationRef, error) {

	return instantiateKustomizationRefs(ctx, c, e, clusterNamespace, templateName, kustomizationRefs, data, labels, logger)
}

// instantiateReferencedPolicies instantiate eventTrigger.Spec.PolicyRefs using information from passed in objects
// which represent all of the resources matching referenced EventSource in the managed cluster.
func instantiateReferencedPolicyRefs(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	templateName string, eventTrigger *v1beta1.EventTrigger, cluster *corev1.ObjectReference, objects any,
	labels map[string]string, logger logr.Logger) (localSet, remoteSet []configv1beta1.PolicyRef, err error) {

	// fetches all referenced ConfigMaps/Secrets
	var local map[configv1beta1.PolicyRef]client.Object
	var remote map[configv1beta1.PolicyRef]client.Object
	local, remote, err = fetchPolicyRefs(ctx, c, eventTrigger, cluster, objects, templateName, logger)
	if err != nil {
		return nil, nil, err
	}

	localSet, err = instantiateResources(ctx, c, e, templateName, local, objects, labels, logger)
	if err != nil {
		return nil, nil, err
	}
	remoteSet, err = instantiateResources(ctx, c, e, templateName, remote, objects, labels, logger)
	if err != nil {
		return nil, nil, err
	}

	return
}

func instantiateResources(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	templateName string, resources map[configv1beta1.PolicyRef]client.Object, objects any, labels map[string]string,
	logger logr.Logger) ([]configv1beta1.PolicyRef, error) {

	result := make([]configv1beta1.PolicyRef, 0)

	for k := range resources {
		ref := resources[k]
		_, kind := ref.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

		l := logger.WithValues("referencedResource", fmt.Sprintf("%s:%s/%s",
			ref.GetObjectKind().GroupVersionKind().Kind, ref.GetNamespace(), ref.GetName()))
		l.V(logs.LogDebug).Info("process referenced resource")
		var info *types.NamespacedName

		if _, ok := ref.GetAnnotations()[v1beta1.InstantiateAnnotation]; !ok {
			// referenced ConfigMap/Secret is not a template. So there is no
			// need to instantiate a new one. Generated ClusterProfile can directly
			// reference this one
			info = &types.NamespacedName{Namespace: ref.GetNamespace(), Name: ref.GetName()}
		} else {
			name, err := getResourceName(ctx, c, ref, labels)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get %s name: %v", ref.GetObjectKind(), err))
				return nil, err
			}
			// If referenced resource is a template, assume it needs to be instantiated using
			// information from the resources in the managed cluster that generated the event.
			// Generate then a new ConfigMap/Secret. The autocreated ClusterProfile will reference
			// this new resource.
			info, err = instantiateReferencedPolicy(ctx, e, ref, templateName, name, objects, labels, logger)
			if err != nil {
				return nil, err
			}
		}

		result = append(result, configv1beta1.PolicyRef{
			Namespace: info.Namespace, Name: info.Name,
			Kind: kind, Path: k.Path, Optional: k.Optional, DeploymentType: k.DeploymentType,
		})
	}

	return result, nil
}

func instantiateReferencedPolicy(ctx context.Context, e *v1beta1.EventTrigger, ref client.Object,
	templateName, name string, objects any, labels map[string]string, logger logr.Logger,
) (*types.NamespacedName, error) {

	l := logger.WithValues("referencedResource",
		fmt.Sprintf("%s:%s/%s", ref.GetObjectKind(), ref.GetNamespace(), ref.GetName()))

	content := getDataSection(ref)

	instantiatedContent, err := instantiateDataSection(templateName, content, objects,
		funcmap.HasTextTemplateAnnotation(e.Annotations), l)
	if err != nil {
		l.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiated referenced resource content: %v", err))
		return nil, err
	}
	content = instantiatedContent

	// Referenced resource labels are to be added. Make a copy
	tmpLabels := make(map[string]string)
	for k := range labels {
		tmpLabels[k] = labels[k]
	}

	// Resource name must depend on reference resource name as well. So add those labels.
	// If an EventTrigger is referencing N configMaps/Secrets, N equivalent referenced
	// resources must be created
	tmpLabels[referencedResourceNamespaceLabel] = ref.GetNamespace()
	tmpLabels[referencedResourceNameLabel] = ref.GetName()

	var instantiatedObject client.Object
	if ref.GetObjectKind().GroupVersionKind().Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
		instantiatedObject = generateConfigMap(ref, name, tmpLabels, content)
	} else {
		instantiatedObject = generateSecret(ref, name, tmpLabels, content)
	}
	addTypeInformationToObject(mgmtClusterSchema, instantiatedObject)

	dr, err := k8s_utils.GetDynamicResourceInterface(mgmtClusterConfig,
		instantiatedObject.GetObjectKind().GroupVersionKind(), instantiatedObject.GetNamespace())
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return nil, err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("patch resource %s %s:%s",
		ref.GetObjectKind().GroupVersionKind().Kind, ref.GetNamespace(), ref.GetName()))

	err = updateResource(ctx, dr, instantiatedObject, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to patch resource %s %s:%s: %v",
			ref.GetObjectKind().GroupVersionKind().Kind, ref.GetNamespace(), ref.GetName(), err))
		return nil, err
	}

	// Keep track of all Secret/ConfigMap instances with "projectsveltos.io/instantiate" annotation
	// referenced by an EventTrigger. When those changes, EventTrigger will be reconciled.
	resourceTracker := getTrackerInstance()
	resourceTracker.trackResourceForConsumer(
		&corev1.ObjectReference{Kind: ref.GetObjectKind().GroupVersionKind().Kind, Namespace: ref.GetNamespace(),
			Name: ref.GetName(), APIVersion: "v1"},
		&corev1.ObjectReference{Kind: v1beta1.EventTriggerKind, Name: e.GetName(), APIVersion: v1beta1.GroupVersion.String()},
	)

	return &types.NamespacedName{Namespace: ReportNamespace, Name: name}, nil
}

func generateConfigMap(ref client.Object, name string, labels, content map[string]string) client.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ReportNamespace,
			Labels:      labels,
			Annotations: ref.GetAnnotations(), //  libsveltosv1beta1.PolicyTemplateAnnotation might be set
		},
		Data: content,
	}
}

func generateSecret(ref client.Object, name string, labels, content map[string]string) client.Object {
	data := make(map[string][]byte)
	for key, value := range content {
		data[key] = []byte(value)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ReportNamespace,
			Labels:      labels,
			Annotations: ref.GetAnnotations(), //  libsveltosv1beta1.PolicyTemplateAnnotation might be set
		},
		Data: data,
		Type: libsveltosv1beta1.ClusterProfileSecretType,
	}
}

func getDataSection(ref client.Object) map[string]string {
	switch v := ref.(type) {
	case *corev1.ConfigMap:
		return v.Data
	case *corev1.Secret:
		data := make(map[string]string)
		for key, value := range v.Data {
			data[key] = string(value)
		}
		return data
	default:
		panic(1) // only referenced resources are ConfigMap/Secret
	}
}

func getTemplateName(clusterNamespace, clusterName, requestorName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterNamespace, clusterName, requestorName)
}

func getClusterRef(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) *corev1.ObjectReference {

	ref := &corev1.ObjectReference{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	if clusterType == libsveltosv1beta1.ClusterTypeSveltos {
		ref.APIVersion = libsveltosv1beta1.GroupVersion.String()
		ref.Kind = libsveltosv1beta1.SveltosClusterKind
	} else {
		ref.APIVersion = clusterv1.GroupVersion.String()
		ref.Kind = "Cluster"
	}

	return ref
}

// getClusterProfileName returns the name for a given ClusterProfile given the labels such ClusterProfile
// should have. It also returns whether the ClusterProfile must be created (if create a false, ClusterProfile
// should be simply updated). And an error if any occurs.
func getClusterProfileName(ctx context.Context, c client.Client, labels map[string]string,
) (name string, err error) {

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
	}

	clusterProfileList := &configv1beta1.ClusterProfileList{}
	err = c.List(ctx, clusterProfileList, listOptions...)
	if err != nil {
		return
	}

	objects := make([]client.Object, len(clusterProfileList.Items))
	for i := range clusterProfileList.Items {
		objects[i] = &clusterProfileList.Items[i]
	}

	return getInstantiatedObjectName(ctx, c, objects)
}

func getResourceName(ctx context.Context, c client.Client, ref client.Object,
	labels map[string]string) (name string, err error) {

	// Always append the labels identifying the referenced resource
	labels[referencedResourceNamespaceLabel] = ref.GetNamespace()
	labels[referencedResourceNameLabel] = ref.GetName()

	switch ref.(type) {
	case *corev1.ConfigMap:
		name, err = getConfigMapName(ctx, c, labels)
	case *corev1.Secret:
		name, err = getSecretName(ctx, c, labels)
	default:
		panic(1)
	}
	return
}

// getConfigMapName returns the name for a given ConfigMap given the labels such ConfigMap
// should have. And an error if any occurs.
func getConfigMapName(ctx context.Context, c client.Client, labels map[string]string,
) (name string, err error) {

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ReportNamespace), // all instantianted ConfigMaps are in this namespace
	}

	configMapList := &corev1.ConfigMapList{}
	err = c.List(ctx, configMapList, listOptions...)
	if err != nil {
		return
	}

	objects := make([]client.Object, 0)
	for i := range configMapList.Items {
		objects = append(objects, &configMapList.Items[i])
	}

	return getInstantiatedObjectName(ctx, c, objects)
}

// getSecretName returns the name for a given Secret given the labels such Secret
// should have. And an error if any occurs.
func getSecretName(ctx context.Context, c client.Client, labels map[string]string,
) (name string, err error) {

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ReportNamespace), // all instantianted Secrets are in this namespace
	}

	secretList := &corev1.SecretList{}
	err = c.List(ctx, secretList, listOptions...)
	if err != nil {
		return
	}

	objects := make([]client.Object, 0)
	for i := range secretList.Items {
		objects = append(objects, &secretList.Items[i])
	}

	return getInstantiatedObjectName(ctx, c, objects)
}

func getInstantiatedObjectName(ctx context.Context, c client.Client, objects []client.Object,
) (name string, err error) {

	prefix := "sveltos-"
	switch len(objects) {
	case 0:
		// no cluster exist yet. Return random name.
		// If one clusterProfile with this name already exists,
		// a conflict will happen. On retry different name will
		// be picked
		const nameLength = 20
		name = prefix + util.RandomString(nameLength)
		err = nil
	case 1:
		name = objects[0].GetName()
		err = nil
	default:
		err = fmt.Errorf("more than one resource of gvk %s found",
			objects[0].GetObjectKind().GroupVersionKind().String())

		// Leave first object, remove all others
		for i := range objects[1:] {
			// Ignore eventual error, since we are returning an error anyway
			_ = c.Delete(ctx, objects[i])
		}

		return name, err
	}
	return name, err
}

// getInstantiatedObjectLabels returns the labels to add to a ClusterProfile created
// by an EventTrigger for a given cluster
func getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName string,
	er *libsveltosv1beta1.EventReport, clusterType libsveltosv1beta1.ClusterType) map[string]string {

	labels := map[string]string{
		eventTriggerNameLabel: eventTriggerName,
		clusterNamespaceLabel: clusterNamespace,
		clusterNameLabel:      clusterName,
		clusterTypeLabel:      string(clusterType),
	}

	// When deleting all resources created by an EventTrigger, er will be nil
	if er != nil {
		// In release v0.52.2 when running in agentless mode, the EventReport name.
		// While previously the EventReport name used to match the EventSource, it is different
		// name (getEventReportNameInManagementCluster). This caused on upgrade, same ClusterProfile
		// to be instantiated twice because of that.
		// EventReport has projectsveltos.io/eventsource-name label. So use that instead to avoid
		// this problem
		labels[eventReportNameLabel] = er.Labels[libsveltosv1beta1.EventSourceNameLabel]
	}

	return labels
}

// appendInstantiatedObjectLabelsForResource appends labels specific to a specific resource
func appendInstantiatedObjectLabelsForResource(labels map[string]string, resourceNamespace, resourceName string) map[string]string {
	labels["eventtrigger.lib.projectsveltos.io/resourcename"] = resourceName

	if resourceNamespace != "" {
		labels["eventtrigger.lib.projectsveltos.io/resourcenamespace"] = resourceNamespace
	}

	return labels
}

// appendInstantiatedObjectLabelsForCloudEvent appends labels specific to a specific cloudEvent
func appendInstantiatedObjectLabelsForCloudEvent(labels map[string]string, csSource, ceSubject string) map[string]string {
	labels[cloudEventSourceLabel] = strings.ReplaceAll(csSource, "/", "-")
	labels[cloudEventSubjectLabel] = strings.ReplaceAll(ceSubject, "/", "-")

	return labels
}

func isGeneratedFromCloudEvent(resource client.Object) bool {
	lbls := resource.GetLabels()
	if lbls == nil {
		return false
	}
	if _, ok := lbls[cloudEventSourceLabel]; ok {
		return true
	}
	return false
}

func isFromGenerators(resource client.Object) bool {
	lbls := resource.GetLabels()
	if lbls == nil {
		return false
	}
	if _, ok := lbls[generatorLabel]; ok {
		return true
	}
	return false
}

func isResourceExist(resource client.Object) bool {
	if reflect.ValueOf(resource).IsNil() {
		return false
	}

	return resource.GetDeletionTimestamp().IsZero()
}

func getCESource(cloudEvent map[string]interface{}) string {
	v, ok := cloudEvent["source"]
	if !ok {
		return ""
	}

	s, ok := v.(string)
	if !ok {
		return ""
	}

	return s
}

func getCESubject(cloudEvent map[string]interface{}) string {
	v, ok := cloudEvent["subject"]
	if !ok {
		return ""
	}

	s, ok := v.(string)
	if !ok {
		return ""
	}

	return s
}

func removeInstantiatedResources(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	clusterProfiles []*configv1beta1.ClusterProfile, fromGenerators []libsveltosv1beta1.PolicyRef,
	logger logr.Logger) error {

	if err := removeClusterProfiles(ctx, c, clusterNamespace, clusterName, clusterType, eventTrigger, er,
		clusterProfiles, logger); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove stale clusterProfiles: %v", err))
		return err
	}

	policyRefs := make(map[libsveltosv1beta1.PolicyRef]bool) // ignore deploymentType
	for i := range clusterProfiles {
		cp := clusterProfiles[i]
		for j := range cp.Spec.PolicyRefs {
			policyRefs[libsveltosv1beta1.PolicyRef{
				Namespace: cp.Spec.PolicyRefs[j].Namespace,
				Name:      cp.Spec.PolicyRefs[j].Name,
				Kind:      cp.Spec.PolicyRefs[j].Kind,
			}] = true
		}
		policyRefs = appendHelmChartValuesFrom(policyRefs, cp.Spec.HelmCharts)
		policyRefs = appendKustomizationRefValuesFrom(policyRefs, cp.Spec.KustomizationRefs)
	}

	// Add all ConfigMap/Secret instances created started from Generators (ConfigMapGenerator/SecretGenerator)
	for i := range fromGenerators {
		policyRefs[fromGenerators[i]] = true
	}

	if err := removeConfigMaps(ctx, c, clusterNamespace, clusterName, clusterType, eventTrigger,
		er, policyRefs, logger); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove stale configMaps: %v", err))
		return err
	}

	if err := removeSecrets(ctx, c, clusterNamespace, clusterName, clusterType, eventTrigger,
		er, policyRefs, logger); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove stale secrets: %v", err))
		return err
	}

	return nil
}

func appendHelmChartValuesFrom(policyRefs map[libsveltosv1beta1.PolicyRef]bool, helmCharts []configv1beta1.HelmChart,
) map[libsveltosv1beta1.PolicyRef]bool {

	for i := range helmCharts {
		for j := range helmCharts[i].ValuesFrom {
			policyRefs[libsveltosv1beta1.PolicyRef{
				Namespace: helmCharts[i].ValuesFrom[j].Namespace,
				Name:      helmCharts[i].ValuesFrom[j].Name,
				Kind:      helmCharts[i].ValuesFrom[j].Kind,
			}] = true
		}
	}

	return policyRefs
}

func appendKustomizationRefValuesFrom(policyRefs map[libsveltosv1beta1.PolicyRef]bool,
	kustomizationRefs []configv1beta1.KustomizationRef) map[libsveltosv1beta1.PolicyRef]bool {

	for i := range kustomizationRefs {
		for j := range kustomizationRefs[i].ValuesFrom {
			policyRefs[libsveltosv1beta1.PolicyRef{
				Namespace: kustomizationRefs[i].ValuesFrom[j].Namespace,
				Name:      kustomizationRefs[i].ValuesFrom[j].Name,
				Kind:      kustomizationRefs[i].ValuesFrom[j].Kind,
			}] = true
		}
	}

	return policyRefs
}

// removeConfigMaps fetches all ConfigMaps created by EventTrigger instance for a given cluster.
// It deletes all stale ConfigMaps (all ConfigMap instances currently present and not in the policyRefs
// list).
// policyRefs arg represents all the ConfigMap the EventTrigger instance is currently managing for the
// given cluster
func removeConfigMaps(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	policyRefs map[libsveltosv1beta1.PolicyRef]bool, logger logr.Logger) error {

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ReportNamespace),
	}

	configMaps := &corev1.ConfigMapList{}
	err := c.List(ctx, configMaps, listOptions...)
	if err != nil {
		return err
	}

	for i := range configMaps.Items {
		cm := &configMaps.Items[i]
		// For existing EventTrigger and EventReport
		if isResourceExist(eventTrigger) && isResourceExist(er) &&
			isGeneratedFromCloudEvent(cm) && isFromGenerators(cm) {
			// ConfigMaps generated for CloudEvents out of Generators are removed
			// when CloudEventAction is set to Delete
			continue
		}

		if _, ok := policyRefs[*getPolicyRef(cm)]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting configMap %s", cm.Name))
			err = c.Delete(ctx, cm)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// removeSecrets fetches all Secrets created by EventTrigger instance for a given cluster.
// It deletes all stale Secrets (all Secret instances currently present and not in the policyRefs
// list).
// policyRefs arg represents all the ConfigMap the EventTrigger instance is currently managing for the
// given cluster
func removeSecrets(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	policyRefs map[libsveltosv1beta1.PolicyRef]bool, logger logr.Logger) error {

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ReportNamespace),
	}

	secrets := &corev1.SecretList{}
	err := c.List(ctx, secrets, listOptions...)
	if err != nil {
		return err
	}

	for i := range secrets.Items {
		secret := &secrets.Items[i]
		// For existing EventTrigger and EventReport
		if isResourceExist(eventTrigger) && isResourceExist(er) &&
			isGeneratedFromCloudEvent(secret) && isFromGenerators(secret) {
			// ConfigMaps generated for CloudEvents out of Generators are removed
			// when CloudEventAction is set to Delete
			continue
		}
		if _, ok := policyRefs[*getPolicyRef(secret)]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting secret %s", secret.Name))
			err = c.Delete(ctx, secret)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// removeClusterProfiles fetches all ClusterProfiles created by EventTrigger instance for a given cluster.
// It deletes all stale ClusterProfiles (all ClusterProfile instances currently present and not in the clusterProfiles
// list).
// clusterProfiles arg represents all the ClusterProfiles the EventTrigger instance is currently managing for the
// given cluster
func removeClusterProfiles(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	clusterProfiles []*configv1beta1.ClusterProfile, logger logr.Logger) error {

	// Build a map of current ClusterProfiles for faster indexing
	// Those are the clusterProfiles current eventTrigger instance is programming
	// for this cluster and need to not be removed
	currentClusterProfiles := make(map[string]bool)
	for i := range clusterProfiles {
		currentClusterProfiles[clusterProfiles[i].Name] = true
	}

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
	}

	clusterProfileList := &configv1beta1.ClusterProfileList{}
	err := c.List(ctx, clusterProfileList, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterProfileList.Items {
		cp := &clusterProfileList.Items[i]
		if _, ok := currentClusterProfiles[cp.Name]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting clusterProfile %s", cp.Name))
			err = c.Delete(ctx, cp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func unstructuredToTyped(config *rest.Config, u *unstructured.Unstructured) (runtime.Object, error) {
	obj, err := scheme.Scheme.New(u.GroupVersionKind())
	if err != nil {
		return nil, err
	}

	unstructuredContent := u.UnstructuredContent()
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredContent, &obj)
	if err != nil {
		return nil, err
	}

	return obj, nil
}

// fecthClusterObjects fetches resources representing a cluster.
// All fetched objects are in the management cluster.
// Currently limited to Cluster and Infrastructure Provider
func fecthClusterObjects(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) (map[string]interface{}, error) {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Fetch cluster %s: %s/%s",
		clusterType, clusterNamespace, clusterName))

	genericCluster, err := clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch cluster %v", err))
		return nil, err
	}
	return runtime.DefaultUnstructuredConverter.ToUnstructured(genericCluster)
}

func getNonInstantiatedClusterProfile(eventTrigger *v1beta1.EventTrigger,
	clusterProfileName string, labels map[string]string) *configv1beta1.ClusterProfile {

	return &configv1beta1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterProfileName,
			Labels: labels,
		},
		Spec: *getClusterProfileSpec(eventTrigger),
	}
}

func prepareCurrentObjects(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventReport *libsveltosv1beta1.EventReport,
	logger logr.Logger) (*currentObjects, error) {

	resources, err := getResources(eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get matching resources %v", err))
		return nil, err
	}

	cloudEvents, err := getCloudEvents(eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get matching cloudEvents %v", err))
		return nil, err
	}

	resourceValues := make([]map[string]interface{}, len(resources)+len(cloudEvents))
	for i := range resources {
		resourceValues[i] = resources[i].UnstructuredContent()
	}
	cluster, err := fecthClusterObjects(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get cluster %v", err))
		return nil, err
	}

	return &currentObjects{
		MatchingResources: eventReport.Spec.MatchingResources,
		Resources:         resourceValues,
		CloudEvents:       cloudEvents,
		Cluster:           cluster,
	}, nil
}

func prepareCurrentObjectList(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventReport *libsveltosv1beta1.EventReport, logger logr.Logger,
) ([]currentObject, error) {

	cluster, err := fecthClusterObjects(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get cluster %v", err))
		return nil, err
	}

	resources, err := getResources(eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get matching resources %v", err))
		return nil, err
	}

	objects := make([]currentObject, 0)
	if len(resources) != 0 {
		for i := range resources {
			matchingResource := corev1.ObjectReference{
				APIVersion: resources[i].GetAPIVersion(),
				Kind:       resources[i].GetKind(),
				Namespace:  resources[i].GetNamespace(),
				Name:       resources[i].GetName(),
			}
			objects = append(objects, currentObject{
				MatchingResource: matchingResource,
				Resource:         resources[i].UnstructuredContent(),
				Cluster:          cluster,
			})
		}
	} else {
		for i := range eventReport.Spec.MatchingResources {
			objects = append(objects, currentObject{
				MatchingResource: eventReport.Spec.MatchingResources[i],
				Resource:         nil,
				Cluster:          cluster,
			})
		}
	}

	cloudEvents, err := getCloudEvents(eventReport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get matching cloudEvents %v", err))
		return nil, err
	}
	for i := range cloudEvents {
		objects = append(objects, currentObject{
			CloudEvent: cloudEvents[i],
			Cluster:    cluster,
		})
	}

	return objects, nil
}

// updateResource creates or updates a resource in a Cluster.
// No action in DryRun mode.
func updateResource(ctx context.Context, dr dynamic.ResourceInterface, object client.Object,
	logger logr.Logger) error {

	l := logger.WithValues("resourceNamespace", object.GetNamespace(), "resourceName", object.GetName(),
		"resourceGVK", object.GetObjectKind().GroupVersionKind())
	l.V(logs.LogDebug).Info("updating resource")

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}

	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, object)
	if err != nil {
		return err
	}

	_, err = dr.Patch(ctx, object.GetName(), types.ApplyPatchType, data, options)
	if err != nil {
		return err
	}

	return nil
}

func appendServiceAccountLabels(eventTrigger *v1beta1.EventTrigger, labels map[string]string) map[string]string {
	// If EventTrigger was created by tenant admin, copy label over to created ClusterProfile
	if eventTrigger.Labels != nil {
		if serviceAccountName, ok := eventTrigger.Labels[libsveltosv1beta1.ServiceAccountNameLabel]; ok {
			labels[libsveltosv1beta1.ServiceAccountNameLabel] = serviceAccountName
		}
		if serviceAccountNamespace, ok := eventTrigger.Labels[libsveltosv1beta1.ServiceAccountNamespaceLabel]; ok {
			labels[libsveltosv1beta1.ServiceAccountNamespaceLabel] = serviceAccountNamespace
		}
	}

	return labels
}

func appendGeneratorLabel(labels map[string]string) map[string]string {
	labels[generatorLabel] = "ok"
	return labels
}

// instantiateFromGenerators instantiates ConfigMaps from ConfigMapGenerator and Secrets from SecretGenerator
func instantiateFromGeneratorsPerResource(ctx context.Context, c client.Client, eventTrigger *v1beta1.EventTrigger,
	er *libsveltosv1beta1.EventReport, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) ([]libsveltosv1beta1.PolicyRef, error) {

	templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)

	objects, err := prepareCurrentObjectList(ctx, c, clusterNamespace, clusterName, clusterType, er, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to prepare currentObject list %v", err))
		return nil, err
	}

	var result []libsveltosv1beta1.PolicyRef
	for i := range objects {
		labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
			er, clusterType)
		if objects[i].CloudEvent != nil {
			labels = appendInstantiatedObjectLabelsForCloudEvent(labels, getCESource(objects[i].CloudEvent),
				getCESubject(objects[i].CloudEvent))
		}
		labels = appendGeneratorLabel(labels)
		labels = appendServiceAccountLabels(eventTrigger, labels)

		if objects[i].CloudEvent != nil {
			instantiatedCloudEventAction, err := instantiateCloudEventAction(clusterNamespace, clusterName,
				eventTrigger, objects[i], logger)
			if err != nil {
				return nil, err
			}

			if *instantiatedCloudEventAction == v1beta1.CloudEventActionDelete {
				// Resources created because of a cloudEvent are ONLY removed when same (same subject/source)
				// cloudEvent is received and EventTrigger.Spec.CloudEventAction is set to delete.
				return nil, deleteInstantiatedFromGenerators(ctx, c, clusterNamespace, clusterName, clusterType,
					eventTrigger, er, objects[i].CloudEvent, logger)
			}
		}

		secretInfo, err := instantiateSecrets(ctx, c, eventTrigger, objects[i], clusterNamespace,
			templateName, labels, logger)
		if err != nil {
			return nil, err
		}
		result = append(result, secretInfo...)

		configMapInfo, err := instantiateConfigMaps(ctx, c, eventTrigger, objects[i], clusterNamespace,
			templateName, labels, logger)
		if err != nil {
			return nil, err
		}
		result = append(result, configMapInfo...)
	}

	return result, nil
}

// instantiateFromGeneratorsPerAllResource instantiates ConfigMaps from ConfigMapGenerator and Secrets from SecretGenerator
func instantiateFromGeneratorsPerAllResource(ctx context.Context, c client.Client, eventTrigger *v1beta1.EventTrigger,
	er *libsveltosv1beta1.EventReport, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) ([]libsveltosv1beta1.PolicyRef, error) {

	templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)

	objects, err := prepareCurrentObjects(ctx, c, clusterNamespace, clusterName, clusterType, er, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to prepare currentObjects %v", err))
		return nil, err
	}

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)
	labels = appendGeneratorLabel(labels)
	labels = appendServiceAccountLabels(eventTrigger, labels)

	result, err := instantiateSecrets(ctx, c, eventTrigger, objects, clusterNamespace,
		templateName, labels, logger)
	if err != nil {
		return nil, err
	}

	configMapInfo, err := instantiateConfigMaps(ctx, c, eventTrigger, objects, clusterNamespace,
		templateName, labels, logger)
	if err != nil {
		return nil, err
	}

	result = append(result, configMapInfo...)

	return result, nil
}

// instantiateSecrets instantiates Secrets in the management cluster from eventTrigger.Spec.SecretGenerator
func instantiateSecrets(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	data any, clusterNamespace, templateName string, labels map[string]string, logger logr.Logger,
) ([]libsveltosv1beta1.PolicyRef, error) {

	resources := make([]libsveltosv1beta1.PolicyRef, 0)

	for i := range e.Spec.SecretGenerator {
		generator := &e.Spec.SecretGenerator[i]
		info, err := instantiateResourceFromGenerator(ctx, c, generator, e, clusterNamespace, templateName,
			string(libsveltosv1beta1.SecretReferencedResourceKind), data, labels, logger)
		if err != nil {
			return nil, err
		}
		if info != nil {
			resources = append(resources, libsveltosv1beta1.PolicyRef{
				Namespace: info.Namespace,
				Name:      info.Name,
				Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
			})
		}
	}

	return resources, nil
}

// instantiateConfigMaps instantiates ConfigMaps in the management cluster from eventTrigger.Spec.ConfigMapGenerator
func instantiateConfigMaps(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	data any, clusterNamespace, templateName string, labels map[string]string, logger logr.Logger,
) ([]libsveltosv1beta1.PolicyRef, error) {

	resources := make([]libsveltosv1beta1.PolicyRef, 0)

	for i := range e.Spec.ConfigMapGenerator {
		generator := &e.Spec.ConfigMapGenerator[i]
		info, err := instantiateResourceFromGenerator(ctx, c, generator, e, clusterNamespace, templateName,
			string(libsveltosv1beta1.ConfigMapReferencedResourceKind), data, labels, logger)
		if err != nil {
			return nil, err
		}
		if info != nil {
			resources = append(resources, libsveltosv1beta1.PolicyRef{
				Namespace: info.Namespace,
				Name:      info.Name,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			})
		}
	}

	return resources, nil
}

// instantiateResourceFromGenerator creates a resource from generator and patch it to the management cluster
func instantiateResourceFromGenerator(ctx context.Context, c client.Client, generator *v1beta1.GeneratorReference,
	e *v1beta1.EventTrigger, clusterNamespace, templateName, kind string, data any, labels map[string]string,
	logger logr.Logger) (*types.NamespacedName, error) {

	var namespace string
	// The name of the referenced resource can be expressed as a template
	if generator.Namespace == "" {
		namespace = clusterNamespace
	} else {
		referencedNamespace, err := instantiateSection(templateName, []byte(generator.Namespace), data,
			funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
		if err != nil {
			return nil, err
		}
		namespace = string(referencedNamespace)
	}

	// The name of the referenced resource can be expressed as a template
	referencedName, err := instantiateSection(templateName, []byte(generator.Name), data,
		funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
	if err != nil {
		return nil, err
	}

	var referencedResource client.Object
	if kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
		referencedResource, err = getConfigMap(ctx, c,
			types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
	} else {
		referencedResource, err = getSecret(ctx, c,
			types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
				kind, namespace, string(referencedName)))
			return nil, fmt.Errorf("referenced resource %s %s/%s does not exist yet",
				kind, namespace, string(referencedName))
		}
		return nil, err
	}

	// Keep track of all referenced Generators. When those changes, EventTrigger will be reconciled.
	resourceTracker := getTrackerInstance()
	resourceTracker.trackResourceForConsumer(
		&corev1.ObjectReference{Kind: referencedResource.GetObjectKind().GroupVersionKind().Kind,
			Namespace: referencedResource.GetNamespace(), Name: referencedResource.GetName(), APIVersion: "v1"},
		&corev1.ObjectReference{Kind: v1beta1.EventTriggerKind, Name: e.GetName(), APIVersion: v1beta1.GroupVersion.String()},
	)

	instantiatedName, err := instantiateSection(templateName, []byte(generator.InstantiatedResourceNameFormat), data,
		funcmap.HasTextTemplateAnnotation(e.Annotations), logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate %q: %v", generator.InstantiatedResourceNameFormat, err))
		return nil, err
	}

	info, err := instantiateReferencedPolicy(ctx, e, referencedResource, templateName, string(instantiatedName), data, labels, logger)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func getValuesFrom(ctx context.Context, c client.Client, valuesFrom []configv1beta1.ValueFrom,
	templateName string, cluster *corev1.ObjectReference, data any, useTxtFuncMap bool, logger logr.Logger) []client.Object {

	result := make([]client.Object, 0, len(valuesFrom))
	for i := range valuesFrom {
		ref := &valuesFrom[i]

		var namespace string
		if ref.Namespace == "" {
			namespace = cluster.Namespace
		} else {
			instantiatedNamespace, err := instantiateSection(templateName, []byte(ref.Namespace), data, useTxtFuncMap, logger)
			if err != nil {
				continue
			}
			namespace = string(instantiatedNamespace)
		}

		name, err := instantiateSection(templateName, []byte(ref.Name), data, useTxtFuncMap, logger)
		if err != nil {
			continue
		}

		var resource client.Object
		if ref.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			resource, err = getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(name)})
		} else {
			resource, err = getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(name)})
		}
		if err != nil {
			continue
		}

		result = append(result, resource)
	}
	return result
}

func hasMatchingResources(er *libsveltosv1beta1.EventReport) bool {
	return len(er.Spec.CloudEvents) != 0 || len(er.Spec.MatchingResources) != 0
}

func appendCloudEventClusterProfiles(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, eventTriggerName string, clusterType libsveltosv1beta1.ClusterType,
	eventReport *libsveltosv1beta1.EventReport, clusterProfiles []*configv1beta1.ClusterProfile,
) ([]*configv1beta1.ClusterProfile, error) {

	currentClusterProfiles := &configv1beta1.ClusterProfileList{}
	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
		eventReport, clusterType)
	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
	}
	err := c.List(ctx, currentClusterProfiles, listOptions...)
	if err != nil {
		return nil, err
	}

	// Include only the one generated because of cloudEvents
	for i := range currentClusterProfiles.Items {
		cp := &currentClusterProfiles.Items[i]
		if isGeneratedFromCloudEvent(cp) {
			clusterProfiles = append(clusterProfiles, cp)
		}
	}

	return clusterProfiles, nil
}

func deleteClusterProfile(ctx context.Context, c client.Client, clusterProfile *configv1beta1.ClusterProfile,
	logger logr.Logger) error {

	currentClusterProfile := &configv1beta1.ClusterProfile{}
	err := c.Get(ctx, types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("delete ClusterProfile %s", clusterProfile.Name))
	return c.Delete(ctx, currentClusterProfile)
}

func deleteInstantiatedFromGenerators(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, eventTrigger *v1beta1.EventTrigger, er *libsveltosv1beta1.EventReport,
	cloudEvent map[string]interface{}, logger logr.Logger) error {

	labels := getInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
		er, clusterType)
	labels = appendGeneratorLabel(labels)
	labels = appendInstantiatedObjectLabelsForCloudEvent(labels, getCESource(cloudEvent),
		getCESubject(cloudEvent))

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(ReportNamespace),
	}

	configMaps := &corev1.ConfigMapList{}
	err := c.List(ctx, configMaps, listOptions...)
	if err != nil {
		return err
	}

	for i := range configMaps.Items {
		cm := &configMaps.Items[i]
		// For existing EventTrigger and EventReport
		logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting configMap %s/%s", cm.Namespace, cm.Name))
		err = c.Delete(ctx, cm)
		if err != nil {
			return err
		}
	}

	secrets := &corev1.SecretList{}
	err = c.List(ctx, secrets, listOptions...)
	if err != nil {
		return err
	}

	for i := range secrets.Items {
		secret := &secrets.Items[i]
		// For existing EventTrigger and EventReport
		logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting secret %s/%s", secret.Namespace, secret.Name))
		err = c.Delete(ctx, secret)
		if err != nil {
			return err
		}
	}

	return nil
}

func instantiateCloudEventAction(clusterNamespace, clusterName string, eventTrigger *v1beta1.EventTrigger,
	data any, logger logr.Logger) (*v1beta1.CloudEventAction, error) {

	templateName := getTemplateName(clusterNamespace, clusterName, eventTrigger.Name)

	instantiatedData, err := instantiateSection(templateName, []byte(eventTrigger.Spec.CloudEventAction), data,
		funcmap.HasTextTemplateAnnotation(eventTrigger.Annotations), logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate CloudEventAction template: %v", err))
		return nil, err
	}

	instantiatedCloudEventAction := v1beta1.CloudEventAction(string(instantiatedData))

	if instantiatedCloudEventAction != v1beta1.CloudEventActionCreate &&
		instantiatedCloudEventAction != v1beta1.CloudEventActionDelete {

		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to unmarshal CloudEventAction. Possible actions %s %s",
			v1beta1.CloudEventActionCreate, v1beta1.CloudEventActionDelete))
		return nil, err
	}

	return &instantiatedCloudEventAction, nil
}

func getClusterProfileSpec(eventTrigger *v1beta1.EventTrigger) *configv1beta1.Spec {
	return &configv1beta1.Spec{
		StopMatchingBehavior: eventTrigger.Spec.StopMatchingBehavior,
		SyncMode:             eventTrigger.Spec.SyncMode,
		Tier:                 eventTrigger.Spec.Tier,
		ContinueOnConflict:   eventTrigger.Spec.ContinueOnConflict,
		Reloader:             eventTrigger.Spec.Reloader,
		MaxUpdate:            eventTrigger.Spec.MaxUpdate,
		TemplateResourceRefs: nil, // this needs to be instantiated
		ValidateHealths:      eventTrigger.Spec.ValidateHealths,
		Patches:              eventTrigger.Spec.Patches,
		ExtraLabels:          eventTrigger.Spec.ExtraLabels,
		ExtraAnnotations:     eventTrigger.Spec.ExtraAnnotations,
		DriftExclusions:      eventTrigger.Spec.DriftExclusions,
		DependsOn:            eventTrigger.Spec.DependsOn,
	}
}
