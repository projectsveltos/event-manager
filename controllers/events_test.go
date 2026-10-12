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

package controllers_test

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const eventBufferSize = 10

var _ = Describe("EventTrigger and EventReport events", func() {
	var logger logr.Logger
	var recorder *events.FakeRecorder

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
		recorder = events.NewFakeRecorder(eventBufferSize)
		controllers.SetEventRecorder(recorder)
	})

	AfterEach(func() {
		controllers.SetEventRecorder(nil)
	})

	Context("deploying an EventTrigger to a cluster", func() {
		var cluster *corev1.ObjectReference
		var eventTrigger *v1beta1.EventTrigger

		BeforeEach(func() {
			cluster = &corev1.ObjectReference{
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Namespace:  randomString(),
				Name:       randomString(),
			}
			eventTrigger = getEventTriggerInstance(randomString(), randomString())
		})

		// record calls what deployEventTrigger calls before it saves the ClusterInfo of the cluster: previous is the
		// failure message the EventTrigger recorded for the cluster before this attempt (nil if none).
		record := func(previous *string, status libsveltosv1beta1.SveltosFeatureStatus, failureMessage *string) {
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			clusterInfo := &libsveltosv1beta1.ClusterInfo{Cluster: *cluster, Status: status, FailureMessage: failureMessage}
			controllers.RecordClusterInfoFailure(context.TODO(), c, eventTrigger, previous, clusterInfo)
		}

		message := func(m string) *string { return &m }

		It("records EventTriggerFailed for a failure, once, and again when the failure message changes", func() {
			clusterNote := "Sveltos " + cluster.Namespace + "/" + cluster.Name

			// First failure: nothing was recorded before. It does not matter which step of the processing set it.
			record(nil, libsveltosv1beta1.SveltosStatusProvisioning, message("failed resetting EventReport status"))
			Expect(recorder.Events).To(Receive(Equal("Warning EventTriggerFailed Deploying to cluster " + clusterNote +
				" failed: failed resetting EventReport status")))

			// The same failure on the next attempts, as it is now recorded in the EventTrigger: not new
			record(message("failed resetting EventReport status"), libsveltosv1beta1.SveltosStatusFailed,
				message("failed resetting EventReport status"))
			Expect(recorder.Events).To(BeEmpty())

			// A different failure
			record(message("failed resetting EventReport status"), libsveltosv1beta1.SveltosStatusFailed,
				message("EventSource not found"))
			Expect(recorder.Events).To(Receive(Equal("Warning EventTriggerFailed Deploying to cluster " + clusterNote +
				" failed: EventSource not found")))
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records the failure of a cluster as Removing when the EventTrigger is being removed from it", func() {
			recorder.Verbose = true

			record(nil, libsveltosv1beta1.SveltosStatusRemoving, message("failed to remove EventSource"))
			Expect(recorder.Events).To(Receive(And(
				ContainSubstring("EventTriggerFailed Removing"),
				ContainSubstring("Removing from cluster Sveltos "+cluster.Namespace+"/"+cluster.Name+" failed: failed to remove EventSource"))))
		})

		It("records nothing for a cluster which is paused, not ready or down, whose agent is not healthy, "+
			"or while a cleanup is in progress", func() {

			for _, m := range []string{
				"cannot proceed deploying. Either cluster is paused or not ready.",
				"agent in managed cluster is not healthy.",
				"cleanup is in progress",
			} {
				record(nil, libsveltosv1beta1.SveltosStatusProvisioning, message(m))
			}
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records nothing when there is no failure", func() {
			record(nil, libsveltosv1beta1.SveltosStatusProvisioned, message(""))
			record(nil, libsveltosv1beta1.SveltosStatusProvisioned, nil)
			Expect(recorder.Events).To(BeEmpty())
		})

		It("does not fail when no recorder is set", func() {
			controllers.SetEventRecorder(nil)
			Expect(func() {
				record(nil, libsveltosv1beta1.SveltosStatusFailed, message("failed to deploy EventSource"))
			}).ToNot(Panic())
		})
	})

	Context("generating the ClusterProfile instances of an EventReport", func() {
		var eventReport *libsveltosv1beta1.EventReport

		BeforeEach(func() {
			eventReport = getEventReport(randomString(), randomString(), randomString())
		})

		// getEventReportFromClient returns a client with the EventReport and the EventReport read from it,
		// as the processing of the EventReports gets it
		getEventReportFromClient := func() (client.Client, *libsveltosv1beta1.EventReport) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(eventReport).
				WithObjects(eventReport).Build()

			current := &libsveltosv1beta1.EventReport{}
			Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: eventReport.Namespace, Name: eventReport.Name},
				current)).To(Succeed())

			return c, current
		}

		It("records EventTriggerFailed for a new failure and a different one, not for the same one or when cleared", func() {
			c, current := getEventReportFromClient()

			controllers.SetEventReportFailureMessage(context.TODO(), c, current, errors.New("failed to instantiate template"),
				false, logger)
			Expect(recorder.Events).To(Receive(Equal(
				"Warning EventTriggerFailed Generating ClusterProfile instances failed: failed to instantiate template")))

			// Same failure: not new
			controllers.SetEventReportFailureMessage(context.TODO(), c, current, errors.New("failed to instantiate template"),
				false, logger)
			Expect(recorder.Events).To(BeEmpty())

			// Different failure
			controllers.SetEventReportFailureMessage(context.TODO(), c, current, errors.New("failed to create ClusterProfile"),
				false, logger)
			Expect(recorder.Events).To(Receive(Equal(
				"Warning EventTriggerFailed Generating ClusterProfile instances failed: failed to create ClusterProfile")))

			// Cleared: nothing recorded
			controllers.SetEventReportFailureMessage(context.TODO(), c, current, nil, false, logger)
			Expect(recorder.Events).To(BeEmpty())

			// Failing again after it was cleared is a new failure
			controllers.SetEventReportFailureMessage(context.TODO(), c, current, errors.New("failed to create ClusterProfile"),
				false, logger)
			Expect(recorder.Events).To(Receive(ContainSubstring("failed to create ClusterProfile")))
		})

		It("records nothing when only the failure message of the agent changed", func() {
			c, current := getEventReportFromClient()

			agentFailureMessage := randomString()
			current.Status.AgentFailureMessage = &agentFailureMessage
			controllers.SetEventReportFailureMessage(context.TODO(), c, current, nil, true, logger)
			Expect(recorder.Events).To(BeEmpty())
		})
	})
})
