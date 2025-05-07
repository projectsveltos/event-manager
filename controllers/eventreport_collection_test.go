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

package controllers_test

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("EventSource Deployer", func() {
	var eventSource *libsveltosv1beta1.EventSource
	var logger logr.Logger
	var version string

	BeforeEach(func() {
		version = randomString()
		eventSource = getEventSourceInstance(randomString())
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	It("deleteEventReport ", func() {
		eventSourceName := randomString()
		clusterName := randomString()
		clusterNamespace := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
			},
		}

		eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, clusterName, &clusterType)
		eventReport := getEventReport(eventReportName, clusterNamespace, clusterName)

		initObjects := []client.Object{
			eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		currentEventReport := &libsveltosv1beta1.EventReport{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: eventReportName},
			currentEventReport)).To(Succeed())

		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		now := metav1.NewTime(time.Now())
		eventReport.DeletionTimestamp = &now
		eventReport.Labels = libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType)
		eventReport.Name = eventSourceName // in the managed cluster the name is the same of the EventSource

		// DeleteEventReport will find the corresponding EventReport in the managed cluster and delete it
		Expect(controllers.DeleteEventReport(context.TODO(), c, getClusterRef(cluster), eventReport, logger)).To(Succeed())

		err := c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: eventReportName}, currentEventReport)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("removeEventReports deletes all EventReport for a given EventSource instance", func() {
		eventReport1 := getEventReport(eventSource.Name, randomString(), randomString())
		eventReport2 := getEventReport(eventSource.Name, randomString(), randomString())
		initObjects := []client.Object{
			eventReport1,
			eventReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(controllers.RemoveEventReports(context.TODO(), c, eventSource.Name, logger)).To(Succeed())

		eventReportList := &libsveltosv1beta1.EventReportList{}
		Expect(c.List(context.TODO(), eventReportList)).To(Succeed())
		Expect(len(eventReportList.Items)).To(BeZero())
	})

	It("removeEventReportsFromCluster deletes all EventReport for a given cluster instance", func() {
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		clusterNamespace := randomString()
		clusterName := randomString()

		// Create a eventReport from clusterNamespace/clusterName for a random EventSource (eventSourceName)
		eventSourceName := randomString()
		eventReport1 := getEventReport(eventSourceName, clusterNamespace, clusterName)
		eventReport1.Labels = libsveltosv1beta1.GetEventReportLabels(
			eventSource.Name, clusterName, &clusterType)

		// Create a eventReport from clusterNamespace/clusterName for a random EventSource (eventSourceName)
		eventSourceName = randomString()
		eventReport2 := getEventReport(eventSourceName, clusterNamespace, clusterName)
		eventReport2.Labels = libsveltosv1beta1.GetEventReportLabels(
			eventSource.Name, clusterName, &clusterType)

		initObjects := []client.Object{
			eventReport1,
			eventReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(controllers.RemoveEventReportsFromCluster(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, map[string]bool{}, logger)).To(Succeed())

		eventReportList := &libsveltosv1beta1.EventReportList{}
		Expect(c.List(context.TODO(), eventReportList)).To(Succeed())
		Expect(len(eventReportList.Items)).To(BeZero())
	})

	It("collectEventReports collects EventReports from clusters", func() {
		cluster := prepareCluster(version)

		// In managed cluster this is the namespace where EventReports
		// are created
		const eventReportNamespace = controllers.ReportNamespace
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventReportNamespace,
			},
		}
		err := testEnv.Create(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		eventSourceName := randomString()
		eventSource := getEventSourceInstance(eventSourceName)
		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		eventReport := getEventReport(eventSourceName, "", "")
		eventReport.Namespace = eventReportNamespace
		Expect(testEnv.Create(context.TODO(), eventReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport)).To(Succeed())

		eventSourceMap := map[string][]*v1beta1.EventTrigger{}
		eventTriggerMap := map[string]libsveltosset.Set{}

		Expect(controllers.CollectAndProcessEventReportsFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			eventSourceMap, eventTriggerMap, version, logger)).To(Succeed())

		clusterType := libsveltosv1beta1.ClusterTypeCapi

		validateEventReports(eventSourceName, cluster, &clusterType)

		// Update EventReports and validate again
		Expect(controllers.CollectAndProcessEventReportsFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			eventSourceMap, eventTriggerMap, version, logger)).To(Succeed())

		validateEventReports(eventSourceName, cluster, &clusterType)
	})

	It("buildEventTriggersForEventSourceMap builds a map of EventTriggers referencing an EventSource", func() {
		eventSourceName1 := randomString()
		et1 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName1,
			},
		}

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		clusterRef := &corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}
		prefix := "test-template"
		eventSourceName2 := fmt.Sprintf("%s-{{ .Cluster.metadata.name }}", prefix)
		et2 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName2,
			},
		}

		Expect(testEnv.Create(context.TODO(), et1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, et1)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), et2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, et2)).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		eventTriggers := &v1beta1.EventTriggerList{}
		Expect(testEnv.List(context.TODO(), eventTriggers)).To(Succeed())

		eventSourceMap, err := controllers.BuildEventTriggersForEventSourceMap(context.TODO(), clusterRef,
			eventTriggers)
		Expect(err).To(BeNil())
		Expect(eventSourceMap).ToNot(BeNil())

		v, ok := eventSourceMap[eventSourceName1]
		Expect(ok).To(BeTrue())
		Expect(len(v)).To(Equal(1))
		Expect(v[0].Name).To(Equal(et1.Name))

		v, ok = eventSourceMap[fmt.Sprintf("%s-%s", prefix, cluster.Name)]
		Expect(ok).To(BeTrue())
		Expect(len(v)).To(Equal(1))
		Expect(v[0].Name).To(Equal(et2.Name))
	})

	It("buildEventTriggersForClusterMap builds a map of clusters matching an eventTrigger", func() {
		cluster1 := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		cluster2 := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		et1 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					*cluster1, *cluster2,
				},
			},
		}

		et2 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					*cluster1,
				},
			},
		}

		initObjects := []client.Object{
			et1, et2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		eventTriggers := &v1beta1.EventTriggerList{}
		Expect(c.List(context.TODO(), eventTriggers)).To(Succeed())

		eventTriggerMap := controllers.BuildEventTriggersForClusterMap(eventTriggers)

		v, ok := eventTriggerMap[et1.Name]
		Expect(ok).To(BeTrue())
		Expect(v.Len()).To(Equal(2))
		Expect(v.Has(cluster1)).To(BeTrue())
		Expect(v.Has(cluster2)).To(BeTrue())

		v, ok = eventTriggerMap[et2.Name]
		Expect(ok).To(BeTrue())
		Expect(v.Len()).To(Equal(1))
		Expect(v.Has(cluster1)).To(BeTrue())
		Expect(v.Has(cluster2)).To(BeFalse())
	})

	It("shouldIgnore ignore eventReports collected from managed clusters to the management cluster", func() {
		eventReport := libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		Expect(controllers.ShouldIgnore(&eventReport)).To(BeFalse())

		eventReport.Labels = map[string]string{
			libsveltosv1beta1.EventReportClusterNameLabel: randomString(),
		}

		Expect(controllers.ShouldIgnore(&eventReport)).To(BeTrue())
	})

	It("shouldReprocess returns true for EventReport with Status set to processed", func() {
		eventReport := libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventReportClusterNameLabel: randomString(),
				},
			},
		}

		Expect(controllers.ShouldReprocess(&eventReport)).To(BeTrue())

		phase := libsveltosv1beta1.ReportWaitingForDelivery
		eventReport.Status.Phase = &phase
		Expect(controllers.ShouldReprocess(&eventReport)).To(BeTrue())

		phase = libsveltosv1beta1.ReportProcessed
		eventReport.Status.Phase = &phase
		Expect(controllers.ShouldReprocess(&eventReport)).To(BeFalse())
	})

	It("isEventTriggerMatchingTheCluster returns true if a cluster is a match for an EventTrigger", func() {
		cluster1 := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		cluster2 := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		cluster3 := &corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}

		et1 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					*cluster1, *cluster2,
				},
			},
		}

		et2 := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
			},
			Status: v1beta1.EventTriggerStatus{},
		}

		initObjects := []client.Object{
			et1, et2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		eventTriggers := &v1beta1.EventTriggerList{}
		Expect(c.List(context.TODO(), eventTriggers)).To(Succeed())

		eventTriggerMap := controllers.BuildEventTriggersForClusterMap(eventTriggers)

		Expect(controllers.IsEventTriggerMatchingTheCluster(et1, cluster1, eventTriggerMap)).To(BeTrue())
		Expect(controllers.IsEventTriggerMatchingTheCluster(et1, cluster2, eventTriggerMap)).To(BeTrue())
		Expect(controllers.IsEventTriggerMatchingTheCluster(et1, cluster3, eventTriggerMap)).To(BeFalse())

		Expect(controllers.IsEventTriggerMatchingTheCluster(et2, cluster1, eventTriggerMap)).To(BeFalse())
		Expect(controllers.IsEventTriggerMatchingTheCluster(et2, cluster2, eventTriggerMap)).To(BeFalse())
		Expect(controllers.IsEventTriggerMatchingTheCluster(et2, cluster3, eventTriggerMap)).To(BeFalse())
	})
})

func validateEventReports(eventSourceName string, cluster *clusterv1.Cluster, clusterType *libsveltosv1beta1.ClusterType) {
	// Verify EventReport is created
	// Eventual loop so testEnv Cache is synced
	Eventually(func() bool {
		eventReportName := libsveltosv1beta1.GetEventReportName(eventSourceName, cluster.Name, clusterType)
		currentEventReport := &libsveltosv1beta1.EventReport{}
		err := testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: eventReportName}, currentEventReport)
		if err != nil {
			By("Not found")
			return false
		}
		if currentEventReport.Labels == nil {
			By("Missing labels")
			return false
		}
		if currentEventReport.Spec.ClusterNamespace != cluster.Namespace ||
			currentEventReport.Spec.ClusterName != cluster.Name {

			By("Spec ClusterNamespace and ClusterName not set")
			return false
		}
		v, ok := currentEventReport.Labels[libsveltosv1beta1.EventSourceNameLabel]
		return ok && v == eventSourceName
	}, timeout, pollingInterval).Should(BeTrue())
}
