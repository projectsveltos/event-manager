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

package fv_test

import (
	"context"

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

// Regression test for a bug where helmCharts fields were JSON-marshaled as a whole slice and
// templated as one blob: JSON-escaping a quoted string literal used as a function argument
// inside a template action (e.g. `replace "-svc" ""`) corrupted the quotes, and Go's template
// parser rejected the result with `unexpected "\\" in operand`. Fields are now instantiated one
// at a time, so a quoted string literal argument survives untouched.
var _ = Describe("Instantiate HelmChart fields containing a quoted string literal function argument", func() {
	const (
		namePrefix = "events-helm-replace"
	)

	It("Verifies a helmChart field using replace with quoted arguments instantiates correctly",
		Label("FV", "PULLMODE"), func() {
			serviceNamespace := randomString()

			eventSource := libsveltosv1beta1.EventSource{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: libsveltosv1beta1.EventSourceSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:     "",
							Version:   coreV1Version,
							Kind:      serviceKind,
							Namespace: serviceNamespace,
						},
					},
					CollectResources: true,
				},
			}
			Byf("Create a EventSource %s matching in namespace: %s",
				eventSource.Name, serviceNamespace)
			Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

			eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
				map[string]string{key: value}, []configv1beta1.PolicyRef{})
			eventTrigger.Spec.OneForEvent = true
			eventTrigger.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:  metallbRepositoryURL,
					RepositoryName: metallbRepositoryName,
					ChartName:      metallbChartName,
					ChartVersion:   metallbChartVersion,
					// This is the bug trigger: a quoted string literal ("-svc", "") passed as a
					// function argument inside a template action.
					ReleaseName:      `{{ .Resource.metadata.name | replace "-svc" "" }}`,
					ReleaseNamespace: resourceNamespaceTemplate,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Byf("Create a EventTrigger %s referencing EventSource %s",
				eventTrigger.Name, eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Create namespace %s in the managed cluster", serviceNamespace)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: serviceNamespace,
				},
			}
			Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

			serviceName := randomString() + "-svc"
			Byf("Creating a Service %s in namespace %s in the managed cluster", serviceName, serviceNamespace)
			var port int32 = 8080
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: serviceNamespace, // EventSource filters service in this namespace
					Name:      serviceName,
					Labels: map[string]string{
						randomString(): randomString(),
					},
				},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{
						randomString(): randomString(),
					},
					Ports: []corev1.ServicePort{
						{
							Name: randomString(),
							Port: port,
						},
					},
				},
			}
			Expect(workloadClient.Create(context.TODO(), service)).To(Succeed())

			By("Verifying ClusterProfile has been created (template instantiation succeeded)")
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

			Expect(len(clusterProfile.Spec.HelmCharts)).To(Equal(1))

			Byf("Verifying ReleaseName was instantiated correctly (replace with quoted arguments applied)")
			expectedReleaseName := serviceName[:len(serviceName)-len("-svc")]
			Expect(clusterProfile.Spec.HelmCharts[0].ReleaseName).To(Equal(expectedReleaseName))
			Expect(clusterProfile.Spec.HelmCharts[0].ReleaseNamespace).To(Equal(serviceNamespace))

			verifyClusterSummary(clusterProfile, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())

			Byf("Deleting EventTrigger %s", eventTrigger.Name)
			currentEventTrigger := &v1beta1.EventTrigger{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
				currentEventTrigger)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

			verifyClusterProfilesAreGone(eventTrigger.Name)

			Byf("Verifying EventTrigger %s is removed from the management cluster", eventTrigger.Name)
			Eventually(func() bool {
				err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
					currentEventTrigger)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Deleting EventSource %s in the managed cluster", eventSource.Name)
			currentEventSource := &libsveltosv1beta1.EventSource{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())
		})
})
