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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("ClusterProfile deletion precedes referenced resource cleanup", func() {
	const (
		namePrefix    = "profile-before-cm-"
		testFinalizer = "test.projectsveltos.io/do-not-delete"
	)

	// Verifies that when an EventTrigger update makes a ClusterProfile stale, the instantiated
	// ConfigMap it references is not deleted until the ClusterProfile itself is fully gone.
	//
	// The test pins the stale ClusterProfile in terminating state (via a test finalizer), then
	// asserts the instantiated ConfigMap survives until the finalizer is removed and the profile
	// is fully deleted — directly exercising the ordering fix in removeInstantiatedResources.
	It("Verifies instantiated ConfigMaps are not deleted before their referencing ClusterProfile is gone",
		Label("FV", "PULLMODE"), func() {

			nsKey := randomString()
			nsValue := randomString()

			// EventSource: watches namespaces that carry a specific label in the workload cluster.
			eventSource := libsveltosv1beta1.EventSource{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: libsveltosv1beta1.EventSourceSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:   "",
							Version: coreV1Version,
							Kind:    namespaceKind,
							LabelFilters: []libsveltosv1beta1.LabelFilter{
								{Key: nsKey, Operation: libsveltosv1beta1.OperationEqual, Value: nsValue},
							},
						},
					},
					CollectResources: true,
				},
			}
			Byf("Create EventSource %s matching namespaces with label %s=%s",
				eventSource.Name, nsKey, nsValue)
			Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

			// Template ConfigMap: the projectsveltos.io/instantiate annotation causes event-manager
			// to create an instantiated copy in the projectsveltos namespace (with event-trigger labels)
			// that the generated ClusterProfile will reference.
			templateCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: defaultNamespace,
					Name:      randomString(),
					Annotations: map[string]string{
						instantiateAnnotation: instantiateOk,
					},
				},
				Data: map[string]string{
					"namespace": "{{ .Resource.metadata.name }}",
				},
			}
			Byf("Create template ConfigMap %s/%s annotated for instantiation",
				templateCM.Namespace, templateCM.Name)
			Expect(k8sClient.Create(context.TODO(), templateCM)).To(Succeed())

			policyRef := configv1beta1.PolicyRef{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: templateCM.Namespace,
				Name:      templateCM.Name,
			}

			eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
				map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
			eventTrigger.Spec.OneForEvent = true
			eventTrigger.Spec.HelmCharts = nil
			Byf("Create EventTrigger %s referencing EventSource %s", eventTrigger.Name, eventSource.Name)
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
					return err != nil && meta.IsNoMatchError(err) // CRD never installed in agentless mode
				}, timeout/2, pollingInterval).Should(BeTrue())
			} else {
				Byf("Verifying EventSource %s is present in the managed cluster", eventSource.Name)
				Eventually(func() error {
					currentEventSource := &libsveltosv1beta1.EventSource{}
					return workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
						currentEventSource)
				}, timeout, pollingInterval).Should(BeNil())

				Byf("Verifying EventReport %s is present in the managed cluster", eventSource.Name)
				Eventually(func() error {
					currentEventReport := &libsveltosv1beta1.EventReport{}
					return workloadClient.Get(context.TODO(),
						types.NamespacedName{Namespace: sveltosNamespace, Name: eventSource.Name},
						currentEventReport)
				}, timeout, pollingInterval).Should(BeNil())
			}

			Byf("Verifying EventReport %s is present in the management cluster",
				getEventReportName(eventSource.Name))
			Eventually(func() error {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				return k8sClient.Get(context.TODO(),
					types.NamespacedName{
						Namespace: kindWorkloadCluster.GetNamespace(),
						Name:      getEventReportName(eventSource.Name),
					},
					currentEventReport)
			}, timeout, pollingInterval).Should(BeNil())

			// Trigger the event: create a matching namespace in the workload cluster.
			namespace := randomString()
			Byf("Create namespace %s in the workload cluster with label %s=%s", namespace, nsKey, nsValue)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   namespace,
					Labels: map[string]string{nsKey: nsValue},
				},
			}
			Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

			if !isAgentLessMode() {
				Byf("Verifying EventReport in managed cluster has matching resources")
				Eventually(func() bool {
					currentEventReport := &libsveltosv1beta1.EventReport{}
					err = workloadClient.Get(context.TODO(),
						types.NamespacedName{Namespace: sveltosNamespace, Name: eventSource.Name},
						currentEventReport)
					if err != nil {
						return false
					}
					return len(currentEventReport.Spec.Resources) != 0
				}, timeout, pollingInterval).Should(BeTrue())
			}

			Byf("Verifying EventReport %s in management cluster has matching resources",
				getEventReportName(eventSource.Name))
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{
						Namespace: kindWorkloadCluster.GetNamespace(),
						Name:      getEventReportName(eventSource.Name),
					},
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
				return err == nil && len(clusterProfileList.Items) == 1
			}, timeout, pollingInterval).Should(BeTrue())

			profileListOptions := []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
			}
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			Expect(k8sClient.List(context.TODO(), clusterProfileList, profileListOptions...)).To(Succeed())
			clusterProfile := &clusterProfileList.Items[0]

			By("Verifying instantiated ConfigMap is created in the projectsveltos namespace")
			Eventually(func() bool {
				cmListOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
					client.InNamespace(sveltosNamespace),
				}
				cmList := &corev1.ConfigMapList{}
				err = k8sClient.List(context.TODO(), cmList, cmListOptions...)
				return err == nil && len(cmList.Items) >= 1
			}, timeout, pollingInterval).Should(BeTrue())

			// Pin the ClusterProfile in terminating state so that we can observe the
			// instantiated ConfigMap surviving while the ClusterProfile is being deleted.
			Byf("Adding test finalizer %q to ClusterProfile %s", testFinalizer, clusterProfile.Name)
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				currentCP := &configv1beta1.ClusterProfile{}
				if err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentCP); err != nil {
					return err
				}
				currentCP.Finalizers = append(currentCP.Finalizers, testFinalizer)
				return k8sClient.Update(context.TODO(), currentCP)
			})).To(Succeed())

			// Make the existing ClusterProfile stale: update the EventTrigger so the workload
			// cluster no longer matches its sourceClusterSelector.
			Byf("Changing EventTrigger %s source cluster selector so the workload cluster no longer matches",
				eventTrigger.Name)
			currentEventTrigger := &v1beta1.EventTrigger{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: eventTrigger.Name}, currentEventTrigger)).To(Succeed())
			currentEventTrigger.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentEventTrigger)).To(Succeed())

			// event-manager detects the stale ClusterProfile, marks it for deletion, and requeues.
			// The test finalizer keeps it alive so we can inspect the intermediate state.
			Byf("Verifying stale ClusterProfile %s has DeletionTimestamp set", clusterProfile.Name)
			Eventually(func() bool {
				currentCP := &configv1beta1.ClusterProfile{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentCP)
				return err == nil && !currentCP.DeletionTimestamp.IsZero()
			}, timeout, pollingInterval).Should(BeTrue())

			// KEY ASSERTION: while the ClusterProfile is terminating, the instantiated ConfigMap it
			// references must not be deleted. This is the invariant enforced by the fix.
			By("Verifying instantiated ConfigMap is preserved while the stale ClusterProfile is terminating")
			Consistently(func() bool {
				cmListOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
					client.InNamespace(sveltosNamespace),
				}
				cmList := &corev1.ConfigMapList{}
				err = k8sClient.List(context.TODO(), cmList, cmListOptions...)
				return err == nil && len(cmList.Items) >= 1
			}, timeout/4, pollingInterval).Should(BeTrue())

			// Release the ClusterProfile so deletion can complete.
			Byf("Removing test finalizer %q from ClusterProfile %s", testFinalizer, clusterProfile.Name)
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				currentCP := &configv1beta1.ClusterProfile{}
				if err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentCP); err != nil {
					return err
				}
				newFinalizers := make([]string, 0, len(currentCP.Finalizers))
				for _, f := range currentCP.Finalizers {
					if f != testFinalizer {
						newFinalizers = append(newFinalizers, f)
					}
				}
				currentCP.Finalizers = newFinalizers
				return k8sClient.Update(context.TODO(), currentCP)
			})).To(Succeed())

			Byf("Verifying stale ClusterProfile %s is fully deleted", clusterProfile.Name)
			Eventually(func() bool {
				currentCP := &configv1beta1.ClusterProfile{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentCP)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			// Once the ClusterProfile is fully gone, event-manager must proceed to delete the
			// instantiated ConfigMap on the next reconciliation.
			By("Verifying instantiated ConfigMap is deleted after ClusterProfile is fully gone")
			Eventually(func() bool {
				cmListOptions := []client.ListOption{
					client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
					client.InNamespace(sveltosNamespace),
				}
				cmList := &corev1.ConfigMapList{}
				err = k8sClient.List(context.TODO(), cmList, cmListOptions...)
				return err == nil && len(cmList.Items) == 0
			}, timeout, pollingInterval).Should(BeTrue())

			// Cleanup
			Byf("Deleting EventTrigger %s", eventTrigger.Name)
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: eventTrigger.Name}, currentEventTrigger)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

			Byf("Deleting EventSource %s", eventSource.Name)
			currentEventSource := &libsveltosv1beta1.EventSource{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: eventSource.Name}, currentEventSource)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

			Byf("Verifying EventReport %s is removed from the management cluster",
				getEventReportName(eventSource.Name))
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{
						Namespace: kindWorkloadCluster.GetNamespace(),
						Name:      getEventReportName(eventSource.Name),
					},
					currentEventReport)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
