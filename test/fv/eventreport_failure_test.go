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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("EventReport failure reporting", func() {
	const (
		namePrefix = "eventreport-failure-"
	)

	It("Records a FailureMessage on EventReport.Status when ClusterProfile generation fails, clears it once fixed",
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
							Evaluate:  serviceLuaScript,
						},
					},
					// Full resource data (not just the object reference) must be collected for
					// the ConfigMap template below to resolve .Resource.metadata.name.
					CollectResources: true,
				},
			}
			Byf("Create a EventSource %s matching Services in namespace: %s",
				eventSource.Name, serviceNamespace)
			Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

			// generatorConfigMapName is intentionally never created before the EventTrigger is:
			// ConfigMapGenerator with Optional unset (false) makes instantiateResourceFromGenerator
			// fail deterministically with "referenced resource ... does not exist yet"
			// (eventtrigger_deployer.go), exercising EventReport.Status.FailureMessage without
			// depending on Go template parsing/execution behavior.
			generatorConfigMapName := namePrefix + randomString()

			eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
				map[string]string{key: value}, nil)
			// OneForEvent selects the per-resource template context (.Resource); the default
			// (false) uses the plural .Resources instead, which the ConfigMap template below
			// does not use.
			eventTrigger.Spec.OneForEvent = true
			eventTrigger.Spec.ConfigMapGenerator = []v1beta1.GeneratorReference{
				{
					Namespace:                      defaultNamespace,
					Name:                           generatorConfigMapName,
					InstantiatedResourceNameFormat: "{{ .Cluster.metadata.name }}-generated",
				},
			}
			Byf("Create a EventTrigger %s referencing EventSource %s", eventTrigger.Name, eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			eventReportName := getEventReportName(eventSource.Name)

			Byf("Verifying EventReport %s is present in the management cluster", eventReportName)
			Eventually(func() error {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				return k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: eventReportName},
					currentEventReport)
			}, timeout, pollingInterval).Should(BeNil())

			createNamespaceAndService(workloadClient, serviceNamespace)

			Byf("Verifying EventReport %s Status.FailureMessage is set in the management cluster", eventReportName)
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: eventReportName},
					currentEventReport)
				if err != nil {
					return false
				}
				return currentEventReport.Status.FailureMessage != nil
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying no ClusterProfile has been created while generation keeps failing")
			listOptions := []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
			}
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			Expect(k8sClient.List(context.TODO(), clusterProfileList, listOptions...)).To(Succeed())
			Expect(len(clusterProfileList.Items)).To(BeZero())

			Byf("Creating the referenced ConfigMap %s/%s so generation can succeed",
				defaultNamespace, generatorConfigMapName)
			generatorConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: defaultNamespace,
					Name:      generatorConfigMapName,
					Annotations: map[string]string{
						instantiateAnnotation: instantiateOk,
					},
				},
				Data: map[string]string{
					"key": "{{ .Resource.metadata.name }}", //nolint: goconst // just a test
				},
			}
			Expect(k8sClient.Create(context.TODO(), generatorConfigMap)).To(Succeed())

			Byf("Verifying EventReport %s Status.FailureMessage is cleared in the management cluster", eventReportName)
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: eventReportName},
					currentEventReport)
				if err != nil {
					return false
				}
				return currentEventReport.Status.FailureMessage == nil
			}, timeout, pollingInterval).Should(BeTrue())

			By("Verifying ClusterProfile has been created now that generation succeeds")
			Eventually(func() bool {
				clusterProfileList := &configv1beta1.ClusterProfileList{}
				err = k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
				if err != nil {
					return false
				}
				return len(clusterProfileList.Items) == 1
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Deleting EventTrigger %s", eventTrigger.Name)
			currentEventTrigger := &v1beta1.EventTrigger{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
				currentEventTrigger)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

			verifyClusterProfilesAreGone(eventTrigger.Name)

			Byf("Deleting EventSource %s", eventSource.Name)
			currentEventSource := &libsveltosv1beta1.EventSource{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

			Byf("Deleting ConfigMap %s/%s", generatorConfigMap.Namespace, generatorConfigMap.Name)
			Expect(k8sClient.Delete(context.TODO(), generatorConfigMap)).To(Succeed())
		})
})
