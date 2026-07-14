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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// metricNamespace prefixes every custom metric in this file, purely to avoid name collisions with
	// unrelated tools scraped by the same Prometheus instance. Deliberately a fixed literal, not derived
	// from getSveltosNamespace() (the Kubernetes namespace this component happens to be deployed into,
	// which is user-configurable per install, and which is also set too late to be usable here: package-level
	// var initializers run before main(), and SetSveltosNamespace() is only called from inside main()).
	// A metric name prefix should stay stable across every install regardless.
	metricNamespace = "projectsveltos"

	metricClusterNameLabel      = "cluster_name"
	metricClusterNamespaceLabel = "cluster_namespace"
	metricClusterTypeLabel      = "cluster_type"
	metricStatusLabel           = "status"
	metricEventTriggerNameLabel = "event_trigger_name"

	statusSuccess = "success"
	statusFailure = "failure"
)

var (
	// reconcileDurationHistogram tracks how long it takes to process an EventTrigger for a cluster
	// (evaluate matching events, create/update the resulting ClusterProfile(s)). Labeled by cluster so
	// it can be filtered to a single cluster, aggregated to a namespace, or averaged fleet-wide.
	//
	// Not labeled by EventTrigger name: this is observed from programDuration, which is invoked as a
	// libsveltos/lib/deployer.MetricHandler callback — a type shared with several other components
	// (addon-controller, healthcheck-manager, classifier, etc.). That callback only carries cluster
	// identity and featureID, not the EventTrigger that requested the work, so adding that label here
	// would require changing the shared MetricHandler signature across all of them. reconcileOutcomeCounter
	// and reconcileLastSuccessTimestampGauge below get the EventTrigger name instead, since both are
	// recorded from event-manager-local code (deployEventTrigger) that already has it on hand.
	//
	// Replaces the previous programEventTriggerDurationHistogram/newEventTriggerHistogram pair, which had
	// the same per-cluster metric-name cardinality bug fixed in addon-controller: baking cluster identity
	// into the Prometheus metric Namespace (and thus its exported name) instead of using a label, producing
	// one distinct, never-unregistered metric per cluster.
	reconcileDurationHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_duration_seconds",
			Help:      "Duration distribution of processing an EventTrigger for a cluster",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30, 60, 120},
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel},
	)

	// reconcileOutcomeCounter tracks terminal reconcile outcomes (success/failure) per EventTrigger and
	// cluster. Incremented from deployEventTrigger, the one place that already knows whether processing
	// a cluster ended up Provisioned/Removed (success) or Failed/FailedNonRetriable (failure).
	reconcileOutcomeCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_outcome_total",
			Help:      "Total number of terminal reconcile outcomes for an EventTrigger on a cluster, by outcome",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricStatusLabel,
			metricEventTriggerNameLabel},
	)

	// matchingClustersGauge tracks how many clusters currently match a given EventTrigger's
	// sourceClusterSelector. Set from EventTriggerReconciler right after it computes the matching set,
	// independent of any per-cluster reconcile activity.
	matchingClustersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "matching_clusters",
			Help:      "Number of clusters currently matching an EventTrigger's sourceClusterSelector",
		},
		[]string{metricEventTriggerNameLabel},
	)

	// matchingResourcesGauge tracks how many resources the most recently processed EventReport matched
	// for a given EventTrigger on a given cluster (er.Spec.MatchingResources — not CloudEvents, a
	// separate matching mechanism on EventReport). Set unconditionally once an EventTrigger is confirmed
	// to apply to the cluster, independent of whether ClusterProfile creation subsequently succeeds:
	// this is a fact about the EventReport, not about what Sveltos did with it.
	matchingResourcesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "matching_resources",
			Help:      "Number of resources in the most recently processed EventReport matching an EventTrigger on a cluster",
		},
		[]string{metricEventTriggerNameLabel, metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel},
	)

	// reconcileLastSuccessTimestampGauge records when an EventTrigger last reached a successful terminal
	// state (Provisioned/Removed) for a cluster. Only ever moves forward on success and is left untouched
	// on failure, so it answers "how long has it been since this last worked" even for something that
	// fails intermittently rather than in a tight consecutive streak.
	//
	// No equivalent "consecutive failures" gauge: unlike addon-controller's ClusterSummary, which already
	// tracks FeatureSummary.ConsecutiveFailures, libsveltos's ClusterInfo (what EventTrigger.Status.ClusterInfo
	// is built from) has no running failure-streak field to mirror. Adding one would mean adding new state
	// to the CRD, not just exporting what's already computed — out of scope here.
	reconcileLastSuccessTimestampGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_last_success_timestamp_seconds",
			Help:      "Unix timestamp of the last successful (Provisioned/Removed) terminal outcome for an EventTrigger on a cluster",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricEventTriggerNameLabel},
	)
)

//nolint:gochecknoinits // forced pattern, can't workaround
func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(reconcileDurationHistogram, reconcileOutcomeCounter, matchingClustersGauge,
		reconcileLastSuccessTimestampGauge, matchingResourcesGauge)
}

func programDuration(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {

	if featureID != string(v1beta1.FeatureEventTrigger) {
		return
	}

	reconcileDurationHistogram.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
	}).Observe(elapsed.Seconds())

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("recorded duration for %s/%s %s: %s",
		clusterNamespace, clusterName, featureID, elapsed))
}

// trackReconcileOutcome records a terminal reconcile outcome for an EventTrigger on a cluster. success is
// true for SveltosStatusProvisioned/SveltosStatusRemoved, false for SveltosStatusFailed/SveltosStatusFailedNonRetriable.
// Non-terminal statuses (Provisioning, Removing) must not call this.
func trackReconcileOutcome(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	success bool, eventTriggerName string, logger logr.Logger) {

	status := statusFailure
	if success {
		status = statusSuccess
	}

	reconcileOutcomeCounter.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricStatusLabel:           status,
		metricEventTriggerNameLabel: eventTriggerName,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking reconcile outcome for %s %s/%s eventTrigger %s: %s",
		clusterType, clusterNamespace, clusterName, eventTriggerName, status))
}

// trackLastSuccess records the timestamp of the most recent successful (Provisioned/Removed) terminal
// outcome for an EventTrigger on a cluster. Only called from the success branch.
func trackLastSuccess(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	timestamp time.Time, eventTriggerName string, logger logr.Logger) {

	reconcileLastSuccessTimestampGauge.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricEventTriggerNameLabel: eventTriggerName,
	}).Set(float64(timestamp.Unix()))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking last success for %s %s/%s eventTrigger %s: %s",
		clusterType, clusterNamespace, clusterName, eventTriggerName, timestamp))
}

// trackMatchingClusters records how many clusters currently match an EventTrigger's sourceClusterSelector.
func trackMatchingClusters(eventTriggerName string, count int, logger logr.Logger) {
	matchingClustersGauge.With(prometheus.Labels{
		metricEventTriggerNameLabel: eventTriggerName,
	}).Set(float64(count))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking matching clusters for eventTrigger %s: %d",
		eventTriggerName, count))
}

// trackMatchingResources records how many resources the most recently processed EventReport matched
// for an EventTrigger on a cluster.
func trackMatchingResources(eventTriggerName, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, count int, logger logr.Logger) {

	matchingResourcesGauge.With(prometheus.Labels{
		metricEventTriggerNameLabel: eventTriggerName,
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
	}).Set(float64(count))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking matching resources for eventTrigger %s %s %s/%s: %d",
		eventTriggerName, clusterType, clusterNamespace, clusterName, count))
}
