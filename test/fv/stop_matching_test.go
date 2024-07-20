/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

package fv_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var (
	configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  namespace: default
  name: %s`
)

var _ = Describe("Deletes ClusterProfile when cluster is not a match anymore", func() {
	const (
		namePrefix     = "stop-matching"
		projectsveltos = "projectsveltos"
	)

	It("Verifies ClusterProfiles is deleted when cluster stops matching EventTrigger", Label("FV"), func() {
		nsKey := randomString()
		nsValue := randomString()

		Byf("Create a EventSource matching a namespace with label %s=%s", nsKey, nsValue)
		eventSource := libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.EventSourceSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Group:   "",
						Version: "v1",
						Kind:    "Namespace",
						LabelFilters: []libsveltosv1beta1.LabelFilter{
							{Key: nsKey, Operation: libsveltosv1beta1.OperationEqual, Value: nsValue},
						},
					},
				},
				CollectResources: true,
			},
		}
		Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

		cmName := randomString()
		By("Creating a ConfigMap containing a ConfigMap")
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      randomString(),
			},
			Data: map[string]string{
				"configmap": fmt.Sprintf(configMap, cmName),
			},
		}
		Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

		policyRef := configv1beta1.PolicyRef{
			Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace:      cm.Namespace,
			Name:           cm.Name,
			DeploymentType: configv1beta1.DeploymentTypeLocal,
		}

		Byf("Create a EventTrigger referencing EventSource %s", eventSource.Name)
		eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
		eventTrigger.Spec.OneForEvent = false
		eventTrigger.Spec.HelmCharts = nil
		Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying EventSource %s is present in the managed cluster", eventSource.Name)
		Eventually(func() error {
			currentEventSource := &libsveltosv1beta1.EventSource{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying EventReports %s is present in the managed cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying EventReports %s is present in the management cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
		}, timeout, pollingInterval).Should(BeNil())

		namespace := randomString()
		Byf("Create namespace %s in the managed cluster", namespace)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Labels: map[string]string{
					nsKey: nsValue,
				},
			},
		}
		Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Verifying EventReports %s is present in the managed cluster with matching resource", eventSource.Name)
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)
			if err != nil {
				return false
			}
			return len(currentEventReport.Spec.Resources) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying EventReports %s is present in the management cluster with matching resource", getEventReportName(eventSource.Name))
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
			if err != nil {
				return false
			}

			return len(currentEventReport.Spec.Resources) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying ClusterProfile has been created")
		Eventually(func() bool {
			listOptions := []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
			}
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err = k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		listOptions := []client.ListOption{
			client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
		}
		clusterProfileList := &configv1beta1.ClusterProfileList{}
		Expect(k8sClient.List(context.TODO(), clusterProfileList, listOptions...)).To(Succeed())
		clusterProfile := &clusterProfileList.Items[0]
		Expect(len(clusterProfile.Spec.PolicyRefs)).To(Equal(1))
		Expect(clusterProfile.Spec.PolicyRefs[0].DeploymentType).To(Equal(configv1beta1.DeploymentTypeLocal))
		_ = verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Change EventTrigger %s SourceClusterSelector", eventTrigger.Name)
		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())
		currentEventTrigger.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					randomString(): randomString(),
				},
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentEventTrigger)).To(Succeed())

		Byf("Verifying EventSource %s is removed from the managed cluster", eventSource.Name)
		Eventually(func() bool {
			currentEventSource := &libsveltosv1beta1.EventSource{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying EventReports %s is removed from the managed cluster", eventSource.Name)
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying ClusterProfile is deleted")
		Eventually(func() bool {
			listOptions := []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
			}
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err = k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting EventTrigger %s", eventTrigger.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

		Byf("Deleting EventSource %s in the managed cluster", eventSource.Name)
		currentEventSource := &libsveltosv1beta1.EventSource{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
			currentEventSource)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

		Byf("Verifying EventReports %s is removed from the management cluster", getEventReportName(eventSource.Name))
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
