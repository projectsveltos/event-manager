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
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Conversion", func() {
	const (
		namePrefix     = "conversion-"
		projectsveltos = "projectsveltos"
	)

	It("Post a EventTrigger.v1alpha1 and verify all is deployed", Label("FV"), func() {
		serviceNamespace := randomString()

		Byf("Create a EventSource matching Services in namespace: %s", serviceNamespace)
		eventSource := libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				ResourceSelectors: []libsveltosv1alpha1.ResourceSelector{
					{
						Group:     "",
						Version:   "v1",
						Kind:      "Service",
						Namespace: serviceNamespace,
						Evaluate:  serviceLuaScript,
					},
				},
				CollectResources: true,
			},
		}
		Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

		By("Creating a ConfigMap containing a ConfigMap")
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      randomString(),
				Annotations: map[string]string{
					"projectsveltos.io/instantiate": "ok",
				},
			},
			Data: map[string]string{
				"policy": networkPolicy,
			},
		}
		Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

		policyRef := configv1alpha1.PolicyRef{
			Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			Namespace: cm.Namespace,
			Name:      cm.Name,
		}

		Byf("Create a EventTrigger referencing EventSource %s", eventSource.Name)
		eventTrigger := &v1alpha1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: v1alpha1.EventTriggerSpec{
				SourceClusterSelector: libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s", key, value)),
				EventSourceName:       eventSource.Name,
				PolicyRefs:            []configv1alpha1.PolicyRef{policyRef},
			},
		}
		eventTrigger.Spec.OneForEvent = true
		eventTrigger.Spec.HelmCharts = []configv1alpha1.HelmChart{
			{
				RepositoryURL:    "https://charts.bitnami.com/bitnami",
				RepositoryName:   "bitnami",
				ChartName:        "bitnami/wildfly",
				ChartVersion:     "17.2.1",
				ReleaseName:      "wildfly",
				ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
				HelmChartAction:  configv1alpha1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		createNamespaceAndService(workloadClient, serviceNamespace)

		Byf("Verifying EventSource %s is present in the managed cluster", eventSource.Name)
		Eventually(func() error {
			currentEventSource := &libsveltosv1beta1.EventSource{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)
		}, timeout, pollingInterval).Should(BeNil())

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
		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureResources)

		Byf("Verifying NewtworkPolicy is created in the namespace %s", serviceNamespace)
		listOptions = []client.ListOption{
			client.InNamespace(serviceNamespace),
		}
		networkPolicies := &networkingv1.NetworkPolicyList{}
		Expect(workloadClient.List(context.TODO(), networkPolicies, listOptions...)).To(Succeed())
		Expect(len(networkPolicies.Items)).To(Equal(1))

		Byf("Deleting EventTrigger %s", eventTrigger.Name)
		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

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

		By("Verifying ClusterProfile has been removed")
		Eventually(func() bool {
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			listOptions = []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventTrigger.Name)),
			}
			err = k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			for i := range clusterProfileList.Items {
				cp := clusterProfileList.Items[i]
				if cp.DeletionTimestamp.IsZero() {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
