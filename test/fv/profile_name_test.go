/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var (
	serviceAccount = `kind: ServiceAccount
apiVersion: v1
metadata:
  name: front-{{ .Resource.metadata.name }}
  namespace: {{ .Resource.metadata.name }}`
)

var _ = Describe("Instantiate ClusterProfile with predictable names", func() {
	const (
		namePrefix     = "profile-name-"
		projectsveltos = "projectsveltos"
	)

	It("Verifies ClusterProfiles are instantiated with names based on InstantiatedProfileNameFormat", Label("FV", "PULLMODE"),
		func() {
			eventSource := libsveltosv1beta1.EventSource{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
					Annotations: map[string]string{
						randomString(): randomString(),
					},
				},
				Spec: libsveltosv1beta1.EventSourceSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:   "",
							Version: "v1",
							Kind:    "Namespace",
						},
					},
					CollectResources: true,
				},
			}
			Byf("Create a EventSource %s matching namespaces", eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      randomString(),
					// Mark resource as template so instantiateReferencedPolicies
					// will generate a new one in projectsveltos namespace
					Annotations: map[string]string{
						"projectsveltos.io/instantiate": "ok",
					},
				},
				Data: map[string]string{
					"sa": serviceAccount,
				},
			}
			Byf("Creating a ConfigMap %s/%s containing a ServiceAccount", cm.Namespace, cm.Name)
			Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

			policyRef := configv1beta1.PolicyRef{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: cm.Namespace,
				Name:      cm.Name,
			}

			eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
				map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
			eventTrigger.Spec.OneForEvent = true
			eventTrigger.Spec.InstantiatedProfileNameFormat = "{{ .Cluster.metadata.name }}-{{ .Resource.metadata.name }}-test"
			Byf("Create a EventTrigger %s referencing EventSource %s",
				eventTrigger.Name, eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			if isAgentLessMode() {
				Byf("Verifying EventSource %s is NOT present in the managed cluster", eventSource.Name)
				Consistently(func() bool {
					currentEventSource := &libsveltosv1beta1.EventSource{}
					err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
						currentEventSource)
					return err != nil && meta.IsNoMatchError(err) // CRD never installed
				}, timeout/2, pollingInterval).Should(BeTrue())

				Byf("Verifying EventReports %s is NOT present in the managed cluster", eventSource.Name)
				Eventually(func() bool {
					currentEventReport := &libsveltosv1beta1.EventReport{}
					err = workloadClient.Get(context.TODO(),
						types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
						currentEventReport)
					return err != nil && meta.IsNoMatchError(err) // CRD never installed
				}, timeout, pollingInterval).Should(BeTrue())
			} else {
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
			}

			Byf("Verifying EventReports %s is present in the management cluster", getEventReportName(eventSource.Name))
			Eventually(func() error {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				return k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
					currentEventReport)
			}, timeout, pollingInterval).Should(BeNil())

			currentEventReport := &libsveltosv1beta1.EventReport{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
				currentEventReport)).To(Succeed())

			By("Verify all ClusterProfile names")
			Eventually(func() bool {
				listOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
				}
				clusterProfiles := &configv1beta1.ClusterProfileList{}
				Expect(k8sClient.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())

				profileMap := map[string]*configv1beta1.ClusterProfile{}
				for i := range clusterProfiles.Items {
					profileMap[clusterProfiles.Items[i].Name] = &clusterProfiles.Items[i]
				}

				for i := range currentEventReport.Spec.MatchingResources {
					expectedName := fmt.Sprintf("%s-%s-test",
						kindWorkloadCluster.GetName(), currentEventReport.Spec.MatchingResources[i].Name)
					_, ok := profileMap[expectedName]
					if !ok {
						By(fmt.Sprintf("ClusterProfile %s missing", expectedName))
						return false
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Deleting EventTrigger %s", eventTrigger.Name)
			currentEventTrigger := &v1beta1.EventTrigger{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
				currentEventTrigger)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

			Byf("Deleting EventSource %s in the managed cluster", eventSource.Name)
			currentEventSource := &libsveltosv1beta1.EventSource{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

			By("Verifying ClusterProfiles have been removed")
			Eventually(func() bool {
				clusterProfiles := &configv1beta1.ClusterProfileList{}
				listOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
				}
				err := k8sClient.List(context.TODO(), clusterProfiles, listOptions...)
				if err != nil {
					return false
				}
				for i := range clusterProfiles.Items {
					cp := clusterProfiles.Items[i]
					if cp.DeletionTimestamp.IsZero() {
						return false
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
