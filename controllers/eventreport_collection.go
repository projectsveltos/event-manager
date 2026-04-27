/*
Copyright 2023-2026. projectsveltos.io. All rights reserved.

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
	"sync"
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

	// clustersPerWorker controls how many clusters each worker goroutine handles.
	clustersPerWorker = 50
	// maxCollectionWorkers caps the number of concurrent goroutines collecting EventReports.
	maxCollectionWorkers = 5
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
		logger.V(logs.LogInfo).Error(err, "failed to list EventReports")
		return err
	}

	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]
		err = c.Delete(ctx, er)
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
		logger.V(logs.LogInfo).Error(err, "failed to list EventReports.")
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
func buildEventTriggersForEventSourceMap(ctx context.Context, cluster *corev1.ObjectReference,
	eventTriggers *v1beta1.EventTriggerList) (map[string][]*v1beta1.EventTrigger, error) {

	clusterType := clusterproxy.GetClusterType(cluster)

	eventSourceMap := map[string][]*v1beta1.EventTrigger{}

	for i := range eventTriggers.Items {
		et := &eventTriggers.Items[i]

		eventSourceName, err := libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
			cluster.Namespace, cluster.Name, et.Spec.EventSourceName, clusterType)
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

// buildClustersWithEventTrigger returns the set of all clusters currently matched
// by at least one EventTrigger, derived from EventTrigger.Status.MatchingClusterRefs.
// This is used to skip clusters that have no EventTriggers and therefore no
// EventReports to collect.
func buildClustersWithEventTrigger(eventTriggers *v1beta1.EventTriggerList) map[corev1.ObjectReference]bool {
	clusters := make(map[corev1.ObjectReference]bool)
	for i := range eventTriggers.Items {
		for j := range eventTriggers.Items[i].Status.MatchingClusterRefs {
			clusters[eventTriggers.Items[i].Status.MatchingClusterRefs[j]] = true
		}
	}
	return clusters
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

// Periodically collects EventReports from each cluster (excluding pull mode clusters)
func collectEventReports(config *rest.Config, c client.Client, s *runtime.Scheme,
	shardKey, capiOnboardAnnotation, version string, logger logr.Logger) {

	interval := 10 * time.Second
	if shardKey != "" {
		// Make sharded controllers more aggressive in fetching
		interval = 5 * time.Second
	}
	firstCollection := true

	mgmtClusterSchema = s
	mgmtClusterConfig = config

	ctx := context.TODO()
	for {
		logger.V(logs.LogDebug).Info("collecting EventTriggers")
		// get all EventTriggers
		eventTriggers := &v1beta1.EventTriggerList{}
		err := c.List(ctx, eventTriggers)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get eventTriggers")
			time.Sleep(interval)
			continue
		}

		// build a map eventTrigger: matching clusters
		eventTriggerMap := buildEventTriggersForClusterMap(eventTriggers)

		logger.V(logs.LogDebug).Info("collecting managed clusters")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", capiOnboardAnnotation,
			shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get clusters")
			time.Sleep(interval)
			continue
		}

		// Restrict collection to clusters actually matched by an EventTrigger.
		// Clusters with no matching EventTrigger have no EventReports to collect,
		// so we avoid all per-cluster API calls for them. clustersWithET is built
		// from EventTrigger.Status.MatchingClusterRefs without shard awareness, so
		// we intersect with the shard-local clusterList.
		clustersWithET := buildClustersWithEventTrigger(eventTriggers)
		var clustersToCollect []corev1.ObjectReference
		for i := range clusterList {
			if clustersWithET[clusterList[i]] {
				clustersToCollect = append(clustersToCollect, clusterList[i])
			}
		}

		allSuccessfull := true

		// In agentless mode all EventReports live in the management cluster.
		// A single List call covers every cluster, avoiding N per-cluster API calls.
		// skipCollecting is also only invoked for clusters that actually have EventReports
		// needing attention.
		if getAgentInMgmtCluster() {
			if err := collectAndProcessAllEventReports(ctx, c, clustersToCollect, eventTriggers,
				eventTriggerMap, version, firstCollection, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to collect EventReports")
				allSuccessfull = false
			}
		} else {
			if err := processEventReportClusters(ctx, c, clustersToCollect, eventTriggers,
				eventTriggerMap, version, firstCollection, logger); err != nil {
				allSuccessfull = false
			}
		}

		if firstCollection && allSuccessfull {
			// Keep track of the first time we were able to successfully collect from all clusters
			firstCollection = false
		}

		time.Sleep(interval)
	}
}

// collectEventReportFromCluster instantiates the per-cluster EventSource map and
// delegates to collectAndProcessEventReportsFromCluster.
func collectEventReportFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference,
	eventTriggers *v1beta1.EventTriggerList,
	eventTriggerMap map[string]libsveltosset.Set,
	version string, firstCollection bool, logger logr.Logger) error {

	eventSourceMap, err := buildEventTriggersForEventSourceMap(ctx, cluster, eventTriggers)
	if err != nil {
		return err
	}

	l := logger.WithValues("cluster", fmt.Sprintf("%s %s/%s", cluster.Kind, cluster.Namespace, cluster.Name))
	return collectAndProcessEventReportsFromCluster(ctx, c, cluster, eventSourceMap, eventTriggerMap,
		version, firstCollection, l)
}

// processEventReportClusters collects and processes EventReports for the given clusters.
// It runs sequentially when there are fewer than clustersPerWorker clusters, and spawns
// up to maxCollectionWorkers goroutines otherwise.
func processEventReportClusters(ctx context.Context, c client.Client,
	clusters []corev1.ObjectReference, eventTriggers *v1beta1.EventTriggerList,
	eventTriggerMap map[string]libsveltosset.Set, version string, firstCollection bool,
	logger logr.Logger) error {

	logErr := func(cluster *corev1.ObjectReference, err error) {
		logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)).
			V(logs.LogInfo).Info(fmt.Sprintf("failed to collect EventReports: %v", err))
	}

	numWorkers := len(clusters) / clustersPerWorker
	if numWorkers > maxCollectionWorkers {
		numWorkers = maxCollectionWorkers
	}

	if numWorkers == 0 {
		// Sequential: fewer than clustersPerWorker clusters, goroutine overhead not justified.
		var retErr error
		for i := range clusters {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			cluster := &clusters[i]
			if err := collectEventReportFromCluster(ctx, c, cluster, eventTriggers, eventTriggerMap,
				version, firstCollection, logger); err != nil {
				logErr(cluster, err)
				retErr = err
			}
		}
		return retErr
	}

	// Parallel: one worker per clustersPerWorker clusters, capped at maxCollectionWorkers.
	work := make(chan corev1.ObjectReference, len(clusters))
	for _, cluster := range clusters {
		work <- cluster
	}
	close(work)

	var (
		mu     sync.Mutex
		retErr error
		wg     sync.WaitGroup
	)
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for clusterRef := range work {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ref := clusterRef
				if err := collectEventReportFromCluster(ctx, c, &ref, eventTriggers, eventTriggerMap,
					version, firstCollection, logger); err != nil {
					logErr(&ref, err)
					mu.Lock()
					retErr = err
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return retErr
	}
}

// collectAndProcessAllEventReports is used in agentless mode. It fetches all EventReports
// from the management cluster in a single List call, groups the ones needing attention by
// cluster, and processes only those clusters — avoiding N per-cluster List calls.
func collectAndProcessAllEventReports(ctx context.Context, c client.Client,
	clusterList []corev1.ObjectReference,
	eventTriggers *v1beta1.EventTriggerList,
	eventTriggerMap map[string]libsveltosset.Set,
	version string, firstCollection bool, logger logr.Logger) error {

	// Build a lookup of shard-local clusters keyed by namespace/name/clusterType to
	// correctly distinguish a SveltosCluster from a CAPI Cluster with the same name.
	type clusterKey struct{ ns, name, clusterType string }
	shardClusters := make(map[clusterKey]corev1.ObjectReference, len(clusterList))
	for i := range clusterList {
		ref := clusterList[i]
		ct := strings.ToLower(string(clusterproxy.GetClusterType(&ref)))
		shardClusters[clusterKey{ref.Namespace, ref.Name, ct}] = ref
	}

	// Single List of all EventReports from the management cluster.
	eventReportList := &libsveltosv1beta1.EventReportList{}
	if err := getManagementClusterClient().List(ctx, eventReportList); err != nil {
		return err
	}

	// Group EventReports needing attention by cluster. In steady state (not firstCollection)
	// we skip already-processed EventReports to avoid unnecessary work.
	type clusterData struct {
		ref corev1.ObjectReference
		ers []*libsveltosv1beta1.EventReport
	}
	byCluster := make(map[clusterKey]*clusterData)
	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]
		if er.Labels == nil {
			continue
		}
		if !firstCollection && er.DeletionTimestamp.IsZero() && !shouldReprocess(er) {
			continue
		}
		clusterName := er.Labels[libsveltosv1beta1.EventReportClusterNameLabel]
		clusterType := er.Labels[libsveltosv1beta1.EventReportClusterTypeLabel]
		clusterNs := er.Namespace
		if clusterName == "" || clusterType == "" {
			continue
		}
		key := clusterKey{clusterNs, clusterName, clusterType}
		if _, ok := shardClusters[key]; !ok {
			continue
		}
		cd, exists := byCluster[key]
		if !exists {
			ref := shardClusters[key]
			cd = &clusterData{ref: ref}
			byCluster[key] = cd
		}
		cd.ers = append(cd.ers, er)
	}

	var retErr error
	for _, cd := range byCluster {
		l := logger.WithValues("cluster", fmt.Sprintf("%s %s/%s", cd.ref.Kind, cd.ref.Namespace, cd.ref.Name))
		if err := processEventReportsForClusterInAgentlessMode(ctx, c, &cd.ref, cd.ers, eventTriggers, eventTriggerMap,
			version, firstCollection, l); err != nil {
			retErr = err
		}
	}

	return retErr
}

// processEventReportsForClusterInAgentlessMode handles all ready-to-process EventReports for a single
// cluster in agentless mode. It checks readiness, version compatibility, and delegates
// per-EventReport work to updateAllClusterProfiles / updateEventReportStatus.
func processEventReportsForClusterInAgentlessMode(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, ers []*libsveltosv1beta1.EventReport,
	eventTriggers *v1beta1.EventTriggerList, eventTriggerMap map[string]libsveltosset.Set,
	version string, firstCollection bool, logger logr.Logger) error {

	skip, err := skipCollecting(ctx, c, ref, logger)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	if !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, ref.Namespace, ref.Name,
		clusterproxy.GetClusterType(ref), true, logger) {

		logger.V(logs.LogDebug).Info("compatibility checks failed")
		return errors.New("compatibility checks failed")
	}

	// EventSourceName in EventTrigger.Spec can be a template, so the map must be
	// built per cluster using its namespace, name, and type.
	eventSourceMap, err := buildEventTriggersForEventSourceMap(ctx, ref, eventTriggers)
	if err != nil {
		return err
	}

	var retErr error
	mgmtClient := getManagementClusterClient()
	for _, er := range ers {
		reprocessing := firstCollection

		if !er.DeletionTimestamp.IsZero() {
			if err := deleteEventReport(ctx, c, ref, er, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to delete EventReport in management cluster")
				retErr = err
				continue
			}
			reprocessing = true
		} else if shouldReprocess(er) {
			// In agentless mode EventReports are generated directly in the management
			// cluster, so no copy from the managed cluster is needed.
			reprocessing = true
		}

		if !reprocessing {
			continue
		}

		logger.V(logs.LogDebug).Info("processing EventReport")
		if err := updateAllClusterProfiles(ctx, c, ref, er, eventSourceMap, eventTriggerMap, logger); err == nil {
			updateEventReportStatus(ctx, mgmtClient, er, logger)
		} else {
			retErr = err
		}
	}

	return retErr
}

func skipCollecting(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	logger logr.Logger) (bool, error) {

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return true, err
	}

	if !ready {
		return true, nil
	}

	paused, err := clusterproxy.IsClusterPaused(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to verify if cluster is paused")
		return true, err
	}

	if paused {
		return true, nil
	}

	return false, nil
}

func collectAndProcessEventReportsFromCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	eventSourceMap map[string][]*v1beta1.EventTrigger, eventTriggerMap map[string]libsveltosset.Set,
	version string, firstCollection bool, logger logr.Logger) error {

	skipCollecting, err := skipCollecting(ctx, c, cluster, logger)
	if err != nil {
		return err
	}
	if skipCollecting {
		return nil
	}

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		return err
	}
	if !isPullMode && !sveltos_upgrade.IsSveltosAgentVersionCompatible(ctx, c, version, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

		msg := "compatibility checks failed"
		logger.V(logs.LogDebug).Info(msg)
		return errors.New(msg)
	}

	logger.V(logs.LogDebug).Info("collecting EventReports from cluster")
	// EventReports location depends on sveltos-agent: management cluster if it's running there,
	// otherwise managed cluster.
	// For cluster in pull mode, the sveltos-applier copies the EventReports here
	clusterClient, err := getEventReportClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), isPullMode, logger)
	if err != nil {
		return err
	}

	var listOptions []client.ListOption
	if getAgentInMgmtCluster() || isPullMode {
		clusterType := clusterproxy.GetClusterType(cluster)
		// If agent is in the management cluster or in pull mode, EventReports for this
		// cluster are also in the management cluuster in the cluster namespace.
		listOptions = []client.ListOption{
			client.InNamespace(cluster.Namespace),
			client.MatchingLabels{
				libsveltosv1beta1.EventReportClusterNameLabel: cluster.Name,
				libsveltosv1beta1.EventReportClusterTypeLabel: strings.ToLower(string(clusterType)),
			},
		}
	}

	eventReportList := libsveltosv1beta1.EventReportList{}
	err = clusterClient.List(ctx, &eventReportList, listOptions...)
	if err != nil {
		return err
	}

	for i := range eventReportList.Items {
		er := &eventReportList.Items[i]

		if shouldIgnore(er, isPullMode) {
			continue
		}

		reprocessing := firstCollection
		l := logger.WithValues("eventReport", er.Name)
		// First update/delete eventReports in managemnent cluster
		var mgmtClusterEventReport *libsveltosv1beta1.EventReport
		if !er.DeletionTimestamp.IsZero() {
			l.V(logs.LogDebug).Info("deleting from management cluster")
			err = deleteEventReport(ctx, c, cluster, er, l)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to delete EventReport in management cluster")
				continue
			}
			reprocessing = true
		} else if shouldReprocess(er) {
			l.V(logs.LogDebug).Info("updating in management cluster")
			mgmtClusterEventReport, err = updateEventReport(ctx, c, cluster, er, isPullMode, l)
			if err != nil {
				l.V(logs.LogInfo).Error(err, "failed to update EventReport in management cluster")
				continue
			}
			reprocessing = true
		}

		if !reprocessing {
			continue
		}
		l.V(logs.LogDebug).Info("processing EventReport")
		if getAgentInMgmtCluster() {
			if mgmtClusterEventReport != nil {
				// After ClusterProfiles are updated, EventReport status is updated to Processed.
				// If in agentless mode, the Status of EventReport in the management cluster will be updated.
				// So set er to current version (update otherwise will fail with object has been modified)
				er = mgmtClusterEventReport
			}
		}

		err = updateAllClusterProfiles(ctx, c, cluster, er, eventSourceMap, eventTriggerMap, l)
		if err == nil {
			updateEventReportStatus(ctx, clusterClient, er, l)
		}
	}
	return nil
}

func updateEventReportStatus(ctx context.Context, clusterClient client.Client, er *libsveltosv1beta1.EventReport,
	logger logr.Logger) {

	logger.V(logs.LogDebug).Info("updating EventReport")
	// Update EventReport Status in managed cluster
	if er.Spec.CloudEvents != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("resetting CloudEvents (%d) in EventReport", len(er.Spec.CloudEvents)))
		// Once a cloudEvent is processed, forget about it. This means CloudEvent is never stored in the
		// management cluster and it is removed from the EventReport in the managed cluster. In other words
		// CloudEvents are stored in the EventReport instances in the managed clusters till those are successfully
		// process in the management cluster.
		er.Spec.CloudEvents = nil
		err := clusterClient.Update(ctx, er)
		if err != nil {
			// This can fail because for instance a new CloudEvent was received while we were processing it in the mgmt cluster.
			// That's OK though. CloudEvent source/subject are added as labels to profiles, so no new profile will be created
			// when reprocessing it
			logger.V(logs.LogInfo).Error(err, "failed to reset EventReport's CloudEvents in managed cluster")
			return
		}
	}
	phase := libsveltosv1beta1.ReportProcessed
	er.Status.Phase = &phase
	err := clusterClient.Status().Update(ctx, er)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update EventReport in managed cluster")
	}
}

// EventReports are collected from managed cluster to the management cluster.
// When an EventReport is collected from a managed cluster and created in the
// management cluster, the label eventreport.projectsveltos.io/cluster-name
// is added. All EventReports found in the management cluster with this
// labels should be ignored as collected from other managed clusters.
func shouldIgnore(er *libsveltosv1beta1.EventReport, isPullMode bool) bool {
	if isPullMode {
		// Sveltos-applier copies the EventReports to the management cluster
		// and add this label to the EventReport
		return false
	}

	if getAgentInMgmtCluster() {
		// If sveltos-agent is in the management cluster, EventReports
		// are directly generated by sveltos-agent here. So there is no
		// copy to ignore.
		return false
	}

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
	eventTriggerMap map[string]libsveltosset.Set, logger logr.Logger) error {

	clusterType := clusterproxy.GetClusterType(cluster)

	// Get all EventSource from EventReport
	eventSourceName := er.Labels[libsveltosv1beta1.EventSourceNameLabel]
	logger.V(logs.LogDebug).Info(fmt.Sprintf("eventSource is %s", eventSourceName))

	// Get all EventTriggers referencing this EventSource
	eventTriggers := eventSourceMap[eventSourceName]

	// For each EventTrigger
	for i := range eventTriggers {
		l := logger.WithValues("eventTrigger", eventTriggers[i].Name)

		exist, err := isEventTriggerExist(ctx, mgmtClient, eventTriggers[i].Name)
		if err != nil {
			l.V(logs.LogInfo).Error(err, "failed to verify if EventTrigger exist")
			return err
		}
		if !exist {
			continue
		}

		// If EventTrigger is currently not matching this cluster, ignore this EventReports
		if !isEventTriggerMatchingTheCluster(eventTriggers[i], cluster, eventTriggerMap) {
			l.V(logs.LogDebug).Info("cluster is not a match anymore. Ignore.")
			continue
		}

		l.V(logs.LogDebug).Info("updating ClusterProfile")
		err = updateClusterProfiles(ctx, mgmtClient, cluster.Namespace, cluster.Name, clusterType,
			eventTriggers[i], er, l)
		if err != nil {
			l.V(logs.LogInfo).Error(err, fmt.Sprintf("failed to update ClusterProfile for EventTrigger %s",
				eventTriggers[i].GetName()))
			return err
		}
	}

	return nil
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
	eventReport *libsveltosv1beta1.EventReport, isPullMode bool, logger logr.Logger,
) (*libsveltosv1beta1.EventReport, error) {

	if eventReport.Labels == nil {
		logger.V(logs.LogInfo).Info(eventReportMalformedLabelError)
		return eventReport, errors.New(eventReportMalformedLabelError)
	}

	eventSourceName, ok := eventReport.Labels[libsveltosv1beta1.EventSourceNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(eventReportMissingLabelError)
		return eventReport, errors.New(eventReportMissingLabelError)
	}

	// Verify EventSource still exists
	currentEventSource := libsveltosv1beta1.EventSource{}
	err := c.Get(ctx, types.NamespacedName{Name: eventSourceName}, &currentEventSource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return eventReport, nil
		}
		return nil, err
	}
	if !currentEventSource.DeletionTimestamp.IsZero() {
		return eventReport, nil
	}

	// In agentless mode, eventReports are directly created in the management cluster.
	if getAgentInMgmtCluster() {
		return eventReport, nil
	}

	// When Sveltos operates in pull mode, event reports are managed as follows:
	// The sveltos-applier, running within the managed cluster, is responsible for copying EventReports
	// to the management cluster. Consequently, the event-manager, located in the management cluster,
	// retrieves these EventReports directly from the management cluster, anticipating that the sveltos-applier
	// has already placed them there.
	// Following logic copies the fetched EventReport to the management cluster. So it is not needed
	// in pull mode.
	if isPullMode {
		return eventReport, nil
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
			currentEventReport.Spec.CloudEvents = nil // cloudEvents do not need to be stored in the mgmt cluster
			currentEventReport.Spec.ClusterNamespace = cluster.Namespace
			currentEventReport.Spec.ClusterName = cluster.Name
			currentEventReport.Spec.ClusterType = clusterType
			err = c.Create(ctx, currentEventReport)
			if err == nil {
				// We reset the Spec.CloudEvents because those are never stored in the management
				// cluster. The currentEventReport we return back is what is used to update ClusterProfiles
				// so before returning it back, set Spec.CloudEvents to the original values the fetched
				// EventReport had
				currentEventReport.Spec.CloudEvents = eventReport.Spec.CloudEvents
			}
			return currentEventReport, err
		}
		return eventReport, err
	}

	logger.V(logs.LogDebug).Info("update EventReport in management cluster")
	currentEventReport.Spec = eventReport.Spec
	currentEventReport.Spec.CloudEvents = nil // cloudEvents do not need to be stored in the mgmt cluster
	currentEventReport.Spec.ClusterNamespace = cluster.Namespace
	currentEventReport.Spec.ClusterName = cluster.Name
	currentEventReport.Spec.ClusterType = clusterType
	currentEventReport.Labels = libsveltosv1beta1.GetEventReportLabels(
		eventSourceName, cluster.Name, &clusterType)
	err = c.Update(ctx, currentEventReport)
	// We reset the Spec.CloudEvents because those are never stored in the management
	// cluster. The currentEventReport we return back is what is used to update ClusterProfiles
	// so before returning it back, set Spec.CloudEvents to the original values the fetched
	// EventReport had
	if err == nil {
		currentEventReport.Spec.CloudEvents = eventReport.Spec.CloudEvents
	}
	return currentEventReport, err
}

func isEventTriggerExist(ctx context.Context, c client.Client, eventTriggerName string,
) (bool, error) {

	eventTrigger := &v1beta1.EventTrigger{}
	if err := c.Get(ctx, types.NamespacedName{Name: eventTriggerName}, eventTrigger); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return eventTrigger.DeletionTimestamp.IsZero(), nil
}
