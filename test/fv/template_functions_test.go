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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var (
	//nolint: gosec // just a test with no real data
	configMapWithFunctions = `{{ $configMap := (getResource .Resource ) }}
{{ $configMap := (chainSetField $configMap "metadata.name" "copied-version") }}
{{ $configMap := (chainRemoveField $configMap "data" ) }}
{{ toYaml $configMap }}`
)

var _ = Describe("Template functions", func() {
	const (
		namePrefix     = "function-name-"
		projectsveltos = "projectsveltos"
	)

	It("Verifies use of extra template functions", Label("FV", "PULLMODE"),
		func() {
			cmNamespace := randomString()
			cmName := randomString()

			Byf("Create a EventSource matching ConfigMap in namespace: %s", cmNamespace)
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
							Group:     "",
							Version:   "v1",
							Kind:      "ConfigMap",
							Namespace: cmNamespace,
							Name:      cmName,
						},
					},
					CollectResources: true,
				},
			}
			Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

			By("Creating a ConfigMap with instantiate annotation and using template functions")
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
					"copy": configMapWithFunctions,
				},
			}
			Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

			policyRef := configv1beta1.PolicyRef{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: cm.Namespace,
				Name:      cm.Name,
			}

			Byf("Create a EventTrigger referencing EventSource %s", eventSource.Name)
			eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
				map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
			eventTrigger.Spec.OneForEvent = true
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

			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: cmNamespace,
				},
			}
			Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

			originalConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: cmNamespace,
				},
				Data: map[string]string{
					randomString(): randomString(),
					randomString(): randomString(),
				},
			}
			Expect(workloadClient.Create(context.TODO(), originalConfigMap)).To(Succeed())

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

			By("Verify ClusterProfile is created")
			Eventually(func() bool {
				listOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
				}
				clusterProfiles := &configv1beta1.ClusterProfileList{}
				Expect(k8sClient.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())

				return len(clusterProfiles.Items) == 1
			}, timeout, pollingInterval).Should(BeTrue())

			// A new ConfigMap is created on the managed cluster. It is in the same namespace
			// but with name copied-version and no data
			Eventually(func() bool {
				currentConfigMap := &corev1.ConfigMap{}
				err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: cmNamespace, Name: "copied-version"},
					currentConfigMap)
				if err != nil {
					return false
				}
				return len(currentConfigMap.Data) == 0
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

			By("Verifying ClusterProfile has been removed")
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

			By("Verifying ConfigMap copy has been removed")
			Eventually(func() bool {
				currentConfigMap := &corev1.ConfigMap{}
				err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: cmNamespace, Name: "copied-version"},
					currentConfigMap)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
