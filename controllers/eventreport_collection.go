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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	eventReportMalformedLabelError = "eventReport is malformed. Labels is empty"
	eventReportMissingLabelError   = "eventReport is malformed. Label missing"
)

var (
	mgmtClusterSchema *runtime.Scheme
	mgmtClusterConfig *rest.Config
)

// removeEventReports deletes all EventReport corresponding to EventSource instance
func removeEventReports(ctx context.Context, c client.Client, eventSourceName string,
	logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
		},
	}

	eventReportList := &libsveltosv1beta1.EventReportList{}
	err := c.List(ctx, eventReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list EventReports. Err: %v", err))
		return err
	}

	for i := range eventReportList.Items {
		cr := &eventReportList.Items[i]
		err = c.Delete(ctx, cr)
		if err != nil {
			return err
		}
	}

	return nil
}

// removeEventReportsFromCluster deletes all EventReport corresponding to Cluster instance
func removeEventReportsFromCluster(ctx context.Context, c client.Client, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, currentEventReports map[string]bool, logger logr.Logger) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			libsveltosv1beta1.EventReportClusterNameLabel: clusterName,
			libsveltosv1beta1.EventReportClusterTypeLabel: strings.ToLower(string(clusterType)),
		},
	}

	eventReportList := &libsveltosv1beta1.EventReportList{}
	err := c.List(ctx, eventReportList, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list EventReports. Err: %v", err))
		return err
	}

	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]
		if _, ok := currentEventReports[er.Name]; !ok {
			err = c.Delete(ctx, er)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// buildEventTriggersForEventSourceMap builds a map:
// key => eventSource name;
// values => slice of the EventTriggers referencing it
// This map is built one per cluster as EventSource can be expressed as a template and instantiated using
// cluster namespace, name and type
func buildEventTriggersForEventSourceMap(cluster *corev1.ObjectReference, eventTriggers *v1beta1.EventTriggerList,
) (map[string][]*v1beta1.EventTrigger, error) {

	clusterType := clusterproxy.GetClusterType(cluster)

	eventSourceMap := map[string][]*v1beta1.EventTrigger{}

	for i := range eventTriggers.Items {
		et := &eventTriggers.Items[i]

		eventSourceName, err := libsveltostemplate.GetReferenceResourceName(cluster.Namespace, cluster.Name,
			string(clusterType), et.Spec.EventSourceName)
		if err != nil {
			return nil, err
		}

		s := eventSourceMap[eventSourceName]
		if s == nil {
			s = make([]*v1beta1.EventTrigger, 0)
			eventSourceMap[eventSourceName] = s
		}

		s = append(s, et)
		eventSourceMap[eventSourceName] = s
	}

	return eventSourceMap, nil
}

func addEventTriggerMatchingCluster(et *v1beta1.EventTrigger,
	eventTriggerMap map[string]libsveltosset.Set) map[string]libsveltosset.Set {

	matchingClusters := libsveltosset.Set{}

	for i := range et.Status.MatchingClusterRefs {
		cluster := &et.Status.MatchingClusterRefs[i]
		matchingClusters.Insert(cluster)
	}

	eventTriggerMap[et.Name] = matchingClusters

	return eventTriggerMap
}

// buildClusterForEventTriggerMap builds a map:
// key => eventTrigger name
// values => slice of currently matching clusters
func buildEventTriggersForClusterMap(eventTriggers *v1beta1.EventTriggerList,
) map[string]libsveltosset.Set {

	eventTriggerMap := map[string]libsveltosset.Set{}

	for i := range eventTriggers.Items {
		et := &eventTriggers.Items[i]

		eventTriggerMap = addEventTriggerMatchingCluster(et, eventTriggerMap)
	}

	return eventTriggerMap
}

// Periodically collects EventReports from each CAPI cluster.
func collectEventReports(config *rest.Config, c client.Client, s *runtime.Scheme,
	shardKey, version string, logger logr.Logger) {

	interval := 10 * time.Second
	if shardKey != "" {
		// Make sharded controllers more aggressive in fetching
		interval = 5 * time.Second
	}

	mgmtClusterSchema = s
	mgmtClusterConfig = config

	ctx := context.TODO()
	for {
		logger.V(logs.LogDebug).Info("collecting EventTriggers")
		// get all EventTriggers
		eventTriggers := &v1beta1.EventTriggerList{}
		err := c.List(ctx, eventTriggers)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get eventTriggers: %v", err))
			time.Sleep(interval)
			continue
		}

		// build a map eventTrigger: matching clusters
		eventTriggerMap := buildEventTriggersForClusterMap(eventTriggers)

		logger.V(logs.LogDebug).Info("collecting managed clusters")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
			time.Sleep(interval)
			continue
		}

		for i := range clusterList {
			cluster := &clusterList[i]

			// Build a map of EventTrigger consuming an EventSource. This is built once per cluster
			// as EventSourceName in EventTrigger.Spec can be expressed as a template and instantiated
			// using cluster namespace, name and type.
			eventSourceMap, err := buildEventTriggersForEventSourceMap(cluster, eventTriggers)
			if err != nil {
				time.Sleep(interval)
				continue
			}

			err = collectAndProcessEventReportsFromCluster(ctx, c, cluster, eventSourceMap, eventTriggerMap,
				version, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect EventReports from cluster: %s/%s %v",
					cluster.Namespace, cluster.Name, err))
			}
		}

		time.Sleep(interval)
	}
}

func collectAndProcessEventReportsFromCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	eventSourceMap map[string][]*v1beta1.EventTrigger, eventTriggerMap map[string]libsveltosset.Set,
	version string, logger logr.Logger) error {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return err
	}

	if !ready {
		return nil
	}

	var remoteClient client.Client
	remoteClient, err = clusterproxy.GetKubernetesClient(ctx, c, cluster.Namespace, cluster.Name,
		"", "", clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	if !sveltos_upgrade.IsVersionCompatible(ctx, remoteClient, version) {
		msg := "compatibility checks failed"
		logger.V(logs.LogDebug).Info(msg)
		return errors.New(msg)
	}

	logger.V(logs.LogDebug).Info("collecting EventReports from cluster")

	eventReportList := libsveltosv1beta1.EventReportList{}
	err = remoteClient.List(ctx, &eventReportList)
	if err != nil {
		return err
	}

	currentEventReports := make(map[string]bool)
	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]

		if shouldIgnore(er) {
			continue
		}

		l := logger.WithValues("eventReport", er.Name)
		// First update/delete eventReports in managemnent cluster
		if !er.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("deleting from management cluster")
			err = deleteEventReport(ctx, c, cluster, er, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to delete EventReport in management cluster. Err: %v", err))
				continue
			}
		} else if shouldReprocess(er) {
			logger.V(logs.LogDebug).Info("updating in management cluster")
			err = updateEventReport(ctx, c, cluster, er, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update EventReport in management cluster. Err: %v", err))
				continue
			}
			// Name in the management cluster is different than name in the managed cluster
			eventSourceName := er.Labels[libsveltosv1beta1.EventSourceNameLabel]
			clusterType := clusterproxy.GetClusterType(cluster)
			eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, cluster.Name, &clusterType)
			currentEventReports[eventReportName] = true
		}

		updateAllClusterProfiles(ctx, c, cluster, er, eventSourceMap, eventTriggerMap, logger)

		logger.V(logs.LogDebug).Info("updating EventReport in the managed cluster")
		// Update EventReport Status in managed cluster
		phase := libsveltosv1beta1.ReportProcessed
		er.Status.Phase = &phase
		err = remoteClient.Status().Update(ctx, er)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update EventReport in managed cluster. Err: %v", err))
		}
	}

	return nil
}

// EventReports are collected from managed cluster to the management cluster.
// When an EventReport is collected from a managed cluster and created in the
// management cluster, the label eventreport.projectsveltos.io/cluster-name
// is added. All EventReports found in the management cluster with this
// labels should be ignored as collected from other managed clusters.
func shouldIgnore(er *libsveltosv1beta1.EventReport) bool {
	if er.Labels == nil {
		return false
	}

	_, ok := er.Labels[libsveltosv1beta1.EventReportClusterNameLabel]
	return ok
}

// If the EventReport in the managed cluster is marked as Processed, ignore it.
func shouldReprocess(er *libsveltosv1beta1.EventReport) bool {
	if er.Status.Phase == nil {
		return true
	}

	return *er.Status.Phase != libsveltosv1beta1.ReportProcessed
}

// isEventTriggerMatchingTheCluster returns true if EventTrigger is currently matching
// cluster
func isEventTriggerMatchingTheCluster(et *v1beta1.EventTrigger, cluster *corev1.ObjectReference,
	eventTriggerMap map[string]libsveltosset.Set) bool {

	matchingClusters := eventTriggerMap[et.Name]
	return matchingClusters.Has(cluster)
}

func updateAllClusterProfiles(ctx context.Context, mgmtClient client.Client, cluster *corev1.ObjectReference,
	er *libsveltosv1beta1.EventReport, eventSourceMap map[string][]*v1beta1.EventTrigger,
	eventTriggerMap map[string]libsveltosset.Set, logger logr.Logger) {

	clusterType := clusterproxy.GetClusterType(cluster)

	// Get all EventSource from EventReport
	eventSourceName := er.Labels[libsveltosv1beta1.EventSourceNameLabel]
	logger.V(logs.LogDebug).Info(fmt.Sprintf("eventSource is %s", eventSourceName))

	// Get all EventTriggers referencing this EventSource
	eventTriggers := eventSourceMap[eventSourceName]

	// For each EventTrigger
	for i := range eventTriggers {
		l := logger.WithValues("eventTrigger", eventTriggers[i].Name)

		// If EventTrigger is currently not matching this cluster, ignore this EventReports
		if !isEventTriggerMatchingTheCluster(eventTriggers[i], cluster, eventTriggerMap) {
			l.V(logs.LogDebug).Info("cluster is not a match anymore. Ignore.")
			continue
		}

		l.V(logs.LogDebug).Info("updating ClusterProfile")
		err := updateClusterProfiles(ctx, mgmtClient, cluster.Namespace, cluster.Name, clusterType,
			eventTriggers[i], er, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update ClusterProfile for EventTrigger %s: %v",
				eventTriggers[i].GetName(), err))
		}
	}
}

func deleteEventReport(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) error {

	if eventReport.Labels == nil {
		logger.V(logs.LogInfo).Info(eventReportMalformedLabelError)
		return errors.New(eventReportMalformedLabelError)
	}

	eventSourceName, ok := eventReport.Labels[libsveltosv1beta1.EventSourceNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(eventReportMissingLabelError)
		return errors.New(eventReportMissingLabelError)
	}

	clusterType := clusterproxy.GetClusterType(cluster)
	eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, cluster.Name, &clusterType)

	currentEventReport := &libsveltosv1beta1.EventReport{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: cluster.Namespace, Name: eventReportName},
		currentEventReport)
	if err == nil {
		return c.Delete(ctx, currentEventReport)
	}

	return nil
}

func updateEventReport(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	eventReport *libsveltosv1beta1.EventReport, logger logr.Logger) error {

	if eventReport.Labels == nil {
		logger.V(logs.LogInfo).Info(eventReportMalformedLabelError)
		return errors.New(eventReportMalformedLabelError)
	}

	eventSourceName, ok := eventReport.Labels[libsveltosv1beta1.EventSourceNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(eventReportMissingLabelError)
		return errors.New(eventReportMissingLabelError)
	}

	// Verify EventSource still exists
	currentEventSource := libsveltosv1beta1.EventSource{}
	err := c.Get(ctx, types.NamespacedName{Name: eventSourceName}, &currentEventSource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
	}
	if !currentEventSource.DeletionTimestamp.IsZero() {
		return nil
	}

	clusterType := clusterproxy.GetClusterType(cluster)
	eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, cluster.Name, &clusterType)

	currentEventReport := &libsveltosv1beta1.EventReport{}
	err = c.Get(ctx,
		types.NamespacedName{Namespace: cluster.Namespace, Name: eventReportName},
		currentEventReport)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("create EventReport in management cluster")
			currentEventReport.Namespace = cluster.Namespace
			currentEventReport.Name = eventReportName
			currentEventReport.Labels = libsveltosv1beta1.GetEventReportLabels(
				eventSourceName, cluster.Name, &clusterType)
			currentEventReport.Spec = eventReport.Spec
			currentEventReport.Spec.ClusterNamespace = cluster.Namespace
			currentEventReport.Spec.ClusterName = cluster.Name
			currentEventReport.Spec.ClusterType = clusterType
			return c.Create(ctx, currentEventReport)
		}
		return err
	}

	logger.V(logs.LogDebug).Info("update EventReport in management cluster")
	currentEventReport.Spec = eventReport.Spec
	currentEventReport.Spec.ClusterNamespace = cluster.Namespace
	currentEventReport.Spec.ClusterName = cluster.Name
	currentEventReport.Spec.ClusterType = clusterType
	currentEventReport.Labels = libsveltosv1beta1.GetEventReportLabels(
		eventSourceName, cluster.Name, &clusterType)
	return c.Update(ctx, currentEventReport)
}
