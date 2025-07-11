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
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Instantiate one ClusterProfile per resource. Instantiate and deploy helm chart", func() {
	const (
		namePrefix     = "events-helm-chart"
		projectsveltos = "projectsveltos"
	)

	var clusterSummary *configv1beta1.ClusterSummary

	It("Verifies ClusterProfiles with helmChart and ValuesFrom is instantiated", Label("FV"), func() {
		serviceNamespace := randomString()

		Byf("Create a EventSource matching in namespace: %s", serviceNamespace)
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
						Kind:      "Service",
						Namespace: serviceNamespace,
					},
				},
				CollectResources: true,
			},
		}
		Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

		By("Creating a ConfigMap containing helm chart values")
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
				"values.yaml": `nameOverride: "{{ .Resource.metadata.name }}"`,
			},
		}
		Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

		Byf("Create a EventTrigger referencing EventSource %s", eventSource.Name)
		eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1beta1.PolicyRef{})
		eventTrigger.Spec.OneForEvent = true
		eventTrigger.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://metallb.github.io/metallb",
				RepositoryName:   "metallb",
				ChartName:        "metallb/metallb",
				ChartVersion:     "0.14.7",
				ReleaseName:      "{{ .Cluster.metadata.name }}",
				ReleaseNamespace: "{{ .Resource.metadata.namespace }}",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				ValuesFrom: []configv1beta1.ValueFrom{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: cm.Namespace,
						Name:      cm.Name,
					},
				},
			},
		}
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
			Consistently(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
					currentEventReport)
				return err != nil && meta.IsNoMatchError(err) // CRD never installed
			}, timeout/2, pollingInterval).Should(BeTrue())
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

		Byf("Verifying EventReports %s is present in the management cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Create namespace %s in the managed cluster", serviceNamespace)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: serviceNamespace,
			},
		}
		Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Creating a Service in namespace %s in the managed cluster", serviceNamespace)
		var port int32 = 8080
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: serviceNamespace, // EventSource filters service in this namespace
				Name:      randomString(),
				Labels: map[string]string{
					randomString(): randomString(),
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					randomString(): randomString(),
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

		if !isAgentLessMode() {
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
		}

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
		clusterSummary = verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Expect(len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts)).To(Equal(1))
		Expect(len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ValuesFrom)).To(Equal(1))
		configMapName := clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ValuesFrom[0].Name
		Byf("Verifying ConfigMap %s is instantiated in the projectsveltos namespace", configMapName)
		configMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "projectsveltos", Name: configMapName}, configMap)).To(Succeed())

		Byf("Deleting EventTrigger %s", eventTrigger.Name)
		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

		if !isAgentLessMode() {
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
		}

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

		Byf("Verifying EventReports %s is removed from the management cluster", getEventReportName(eventSource.Name))
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying ClusterProfile has been removed")
		Eventually(func() bool {
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

		Byf("Verifying ConfigMap %s in the projectsveltos namespace is removed", configMapName)
		err = k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "projectsveltos", Name: configMapName}, configMap)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})
