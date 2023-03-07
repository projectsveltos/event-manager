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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("EventSource Deployer", func() {
	var eventSource *libsveltosv1alpha1.EventSource

	BeforeEach(func() {
		eventSource = getEventSourceInstance(randomString())
	})

	It("deleteEventReport ", func() {
		eventSourceName := randomString()
		clusterName := randomString()
		clusterNamespace := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
			},
		}

		eventReportName := libsveltosv1alpha1.GetEventReportName(eventSourceName, clusterName, &clusterType)
		eventReport := getEventReport(eventReportName, clusterNamespace, clusterName)

		initObjects := []client.Object{
			eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		currentEventReport := &libsveltosv1alpha1.EventReport{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: eventReportName},
			currentEventReport)).To(Succeed())

		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		now := metav1.NewTime(time.Now())
		eventReport.DeletionTimestamp = &now
		eventReport.Labels = libsveltosv1alpha1.GetEventReportLabels(eventSourceName, clusterName, &clusterType)
		eventReport.Name = eventSourceName // in the managed cluster the name is the same of the EventSource

		// DeleteEventReport will find the corresponding EventReport in the managed cluster and delete it
		Expect(controllers.DeleteEventReport(context.TODO(), c, getClusterRef(cluster), eventReport, klogr.New())).To(Succeed())

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

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.RemoveEventReports(context.TODO(), c, eventSource, klogr.New())).To(Succeed())

		eventReportList := &libsveltosv1alpha1.EventReportList{}
		Expect(c.List(context.TODO(), eventReportList)).To(Succeed())
		Expect(len(eventReportList.Items)).To(BeZero())
	})

	It("removeEventReportsFromCluster deletes all EventReport for a given cluster instance", func() {
		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		clusterNamespace := randomString()
		clusterName := randomString()

		// Create a eventReport from clusterNamespace/clusterName for a random EventSource (eventSourceName)
		eventSourceName := randomString()
		eventReport1 := getEventReport(eventSourceName, clusterNamespace, clusterName)
		eventReport1.Labels = libsveltosv1alpha1.GetEventReportLabels(
			eventSource.Name, clusterName, &clusterType)

		// Create a eventReport from clusterNamespace/clusterName for a random EventSource (eventSourceName)
		eventSourceName = randomString()
		eventReport2 := getEventReport(eventSourceName, clusterNamespace, clusterName)
		eventReport2.Labels = libsveltosv1alpha1.GetEventReportLabels(
			eventSource.Name, clusterName, &clusterType)

		initObjects := []client.Object{
			eventReport1,
			eventReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.RemoveEventReportsFromCluster(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, klogr.New())).To(Succeed())

		eventReportList := &libsveltosv1alpha1.EventReportList{}
		Expect(c.List(context.TODO(), eventReportList)).To(Succeed())
		Expect(len(eventReportList.Items)).To(BeZero())
	})

	It("collectEventReports collects EventReports from clusters", func() {
		cluster := prepareCluster()

		// In managed cluster this is the namespace where EventReports
		// are created
		const eventReportNamespace = "projectsveltos"
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

		eventReport := getEventReport(eventSourceName, cluster.Namespace, cluster.Name)
		eventReport.Namespace = eventReportNamespace
		Expect(testEnv.Create(context.TODO(), eventReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport)).To(Succeed())

		Expect(controllers.CollectAndProcessEventReportsFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			klogr.New())).To(Succeed())

		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		validateEventReports(eventSourceName, cluster, &clusterType)

		// Update EventReports and validate again
		Expect(controllers.CollectAndProcessEventReportsFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			klogr.New())).To(Succeed())

		validateEventReports(eventSourceName, cluster, &clusterType)
	})
})

func validateEventReports(eventSourceName string, cluster *clusterv1.Cluster, clusterType *libsveltosv1alpha1.ClusterType) {
	// Verify EventReport is created
	// Eventual loop so testEnv Cache is synced
	Eventually(func() bool {
		eventReportName := libsveltosv1alpha1.GetEventReportName(eventSourceName, cluster.Name, clusterType)
		currentEventReport := &libsveltosv1alpha1.EventReport{}
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
		v, ok := currentEventReport.Labels[libsveltosv1alpha1.EventSourceLabelName]
		return ok && v == eventSourceName
	}, timeout, pollingInterval).Should(BeTrue())
}
