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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	eventReportMalformedLabelError = "eventReport is malformed. Labels is empty"
	eventReportMissingLabelError   = "eventReport is malformed. Label missing"
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

// Periodically collects EventReports from each CAPI cluster.
func collectEventReports(c client.Client, shardKey string, logger logr.Logger) {
	interval := 10 * time.Second
	if shardKey != "" {
		// Make sharded controllers more aggressive in fetching
		interval = 5 * time.Second
	}

	ctx := context.TODO()
	for {
		logger.V(logs.LogDebug).Info("collecting EventReports")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		}

		for i := range clusterList {
			cluster := &clusterList[i]
			err = collectAndProcessEventReportsFromCluster(ctx, c, cluster, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect EventReports from cluster: %s/%s %v",
					cluster.Namespace, cluster.Name, err))
			}
		}

		time.Sleep(interval)
	}
}

func collectAndProcessEventReportsFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, logger logr.Logger) error {

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

	logger.V(logs.LogDebug).Info("collecting EventReports from cluster")
	eventReportList := libsveltosv1beta1.EventReportList{}
	err = remoteClient.List(ctx, &eventReportList)
	if err != nil {
		return err
	}

	currentEventReports := make(map[string]bool)
	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]
		l := logger.WithValues("eventReport", er.Name)
		// First update/delete eventReports in managemnent cluster
		if !er.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("deleting from management cluster")
			err = deleteEventReport(ctx, c, cluster, er, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to delete EventReport in management cluster. Err: %v", err))
			}
		} else {
			logger.V(logs.LogDebug).Info("updating in management cluster")
			err = updateEventReport(ctx, c, cluster, er, l)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update EventReport in management cluster. Err: %v", err))
			}
			// Name in the management cluster is different than name in the managed cluster
			eventSourceName := er.Labels[libsveltosv1beta1.EventSourceNameLabel]
			clusterType := clusterproxy.GetClusterType(cluster)
			eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, cluster.Name, &clusterType)
			currentEventReports[eventReportName] = true
		}

		logger.V(logs.LogDebug).Info("updating in managed cluster")
		// Update EventReport Status in managed cluster
		phase := libsveltosv1beta1.ReportProcessed
		er.Status.Phase = &phase
		err = remoteClient.Status().Update(ctx, er)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update EventReport in managed cluster. Err: %v", err))
		}
	}

	return removeEventReportsFromCluster(ctx, c, cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster),
		currentEventReports, logger)
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

	if eventReport.Spec.ClusterName != "" {
		// if ClusterName is set, this is coming from a
		// managed cluster. If management cluster is in turn
		// managed by another cluster, do not pull those.
		return nil
	}

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
