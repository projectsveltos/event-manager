/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/eventreasons"
)

var (
	// eventRecorder records the events about EventTrigger and EventReport instances. It is set once, when the
	// manager starts, with SetEventRecorder: EventReport instances are processed by a loop which is not a
	// reconciler, so the recorder cannot be a field of the reconciler. No event is recorded when it is not set.
	eventRecorder events.EventRecorder
)

// SetEventRecorder sets the recorder used to record events.
func SetEventRecorder(recorder events.EventRecorder) {
	eventRecorder = recorder
}

// recordEventf records an event on regarding, about the related object (can be nil).
// It does nothing when no recorder is set.
// The note is capped, so an oversized message is not rejected by the API server.
func recordEventf(regarding, related runtime.Object, eventType, reason, action, format string, args ...any) {
	if eventRecorder == nil {
		return
	}

	note := eventreasons.TruncateNote(fmt.Sprintf(format, args...))
	eventRecorder.Eventf(regarding, related, eventType, reason, action, "%s", note)
}

// recordClusterInfoFailure records a Warning EventTriggerFailed event on the EventTrigger when deploying it to a
// cluster, or removing it from a cluster, failed. clusterInfo is what the EventTrigger is going to record for the
// cluster, previousMessage is the failure message it recorded before this attempt: nothing is recorded when this
// attempt fails with the same message, a retry which fails the same way is not news. A different message is
// recorded. It is called where the cluster info is saved, so every failure message is covered, whichever step of
// the processing set it.
// A cluster which is paused, not ready or down, and a cluster whose agent is not healthy, are not a failure to
// deploy: nothing is recorded for them.
// The cluster is the related object of the event: the events recorder merges events with the same reason,
// action and regarding object and keeps only the first note, so without it the failures in two clusters would
// be a single event.
func recordClusterInfoFailure(ctx context.Context, c client.Client, eventTrigger *v1beta1.EventTrigger,
	previousMessage *string, clusterInfo *libsveltosv1beta1.ClusterInfo) {

	if clusterInfo.FailureMessage == nil {
		return
	}

	message := *clusterInfo.FailureMessage
	if message == "" || isNotAFailure(message) {
		return
	}

	if previousMessage != nil && *previousMessage == message {
		return
	}

	cluster := &clusterInfo.Cluster
	clusterType := clusterproxy.GetClusterType(cluster)

	var related runtime.Object
	if clusterObject, err := clusterproxy.GetCluster(ctx, c, cluster.Namespace, cluster.Name, clusterType); err == nil {
		related = clusterObject
	}

	if clusterInfo.Status == libsveltosv1beta1.SveltosStatusRemoving {
		recordEventf(eventTrigger, related, corev1.EventTypeWarning, eventreasons.EventTriggerFailed,
			eventreasons.ActionRemoving, "Removing from cluster %s %s/%s failed: %s",
			clusterType, cluster.Namespace, cluster.Name, message)
		return
	}

	recordEventf(eventTrigger, related, corev1.EventTypeWarning, eventreasons.EventTriggerFailed,
		eventreasons.ActionDeploying, "Deploying to cluster %s %s/%s failed: %s",
		clusterType, cluster.Namespace, cluster.Name, message)
}

// isNotAFailure returns true for the failure messages which only say the cluster is not in a state where the
// EventTrigger can be deployed: they are not a failure of the deployment.
func isNotAFailure(message string) bool {
	return message == msgClusterPausedOrNotReady ||
		message == msgAgentNotHealthy ||
		message == msgCleanupInProgress
}

// recordEventReportFailure records a Warning EventTriggerFailed event on the EventReport when generating the
// ClusterProfile instances for it failed. The EventReport lives in the namespace of the cluster it comes from,
// so the event is recorded in that namespace, one for each cluster and EventSource.
func recordEventReportFailure(eventReport *libsveltosv1beta1.EventReport, message string) {
	recordEventf(eventReport, nil, corev1.EventTypeWarning, eventreasons.EventTriggerFailed,
		eventreasons.ActionReconciling, "Generating ClusterProfile instances failed: %s", message)
}
