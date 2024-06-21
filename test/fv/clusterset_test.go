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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Reference ClusterSet", func() {
	const (
		namePrefix     = "clusterset-"
		projectsveltos = "projectsveltos"
	)

	It("Verifies EventTrigger reacts to ClusterSet changes", Label("FV"), func() {
		Byf("Create a ClusterSet matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterSet := getClusterSet(namePrefix, map[string]string{key: value})
		clusterSet.Spec.MaxReplicas = 1
		Expect(k8sClient.Create(context.TODO(), clusterSet)).To(Succeed())
		verifyClusterSetMatches(clusterSet)

		By("Verify ClusterSet has selected the matching cluster")
		currentClusterSet := &libsveltosv1beta1.ClusterSet{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		Expect(currentClusterSet.Status.SelectedClusterRefs).ToNot(BeNil())
		Expect(len(currentClusterSet.Status.SelectedClusterRefs)).To(Equal(1))
		Expect(currentClusterSet.Status.SelectedClusterRefs).To(ContainElement(
			corev1.ObjectReference{
				Kind:       "Cluster",
				APIVersion: clusterv1.GroupVersion.String(),
				Namespace:  kindWorkloadCluster.Namespace,
				Name:       kindWorkloadCluster.Name,
			}))

		serviceNamespace := randomString()

		Byf("Create a EventSource matching Services in namespace: %s", serviceNamespace)
		eventSource := libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
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

		Byf("Create a EventTrigger referencing EventSource %s and referencing the ClusterSet %s", eventSource.Name, clusterSet.Name)
		eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1beta1.PolicyRef{})
		eventTrigger.Spec.OneForEvent = false
		eventTrigger.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://helm.nginx.com/stable/",
				RepositoryName:   "nginx-stable",
				ChartName:        "nginx-stable/nginx-ingress",
				ChartVersion:     "0.17.1",
				ReleaseName:      "nginx-latest",
				ReleaseNamespace: "nginx",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}
		eventTrigger.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					randomString(): randomString(),
				},
			},
		}

		eventTrigger.Spec.ClusterSetRefs = []string{clusterSet.Name}
		Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

		verifyEventTriggerMatches(eventTrigger)

		By("Update ClusterSet MaxReplicas to 0")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		currentClusterSet.Spec.MaxReplicas = 0
		Expect(k8sClient.Update(context.TODO(), currentClusterSet)).To(Succeed())

		By("Verify no cluster is selected anymore")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)
			if err != nil {
				return false
			}
			return len(currentClusterSet.Status.SelectedClusterRefs) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verify EventTrigger does not match any cluster anymore")
		Eventually(func() bool {
			currentEventTrigger := &v1beta1.EventTrigger{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: eventTrigger.Name}, currentEventTrigger)
			if err != nil {
				return false
			}
			return len(currentEventTrigger.Status.MatchingClusterRefs) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		By("Update ClusterSet MaxReplicas to 1")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		currentClusterSet.Spec.MaxReplicas = 2
		Expect(k8sClient.Update(context.TODO(), currentClusterSet)).To(Succeed())

		By("Verify matching cluster is selected again")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)
			if err != nil {
				return false
			}
			return len(currentClusterSet.Status.SelectedClusterRefs) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verify EventTrigger is using the cluster selected by ClusterSet")
		verifyEventTriggerMatches(eventTrigger)

		Byf("Deleting EventSource %s in the managed cluster", eventSource.Name)
		currentEventSource := &libsveltosv1beta1.EventSource{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
			currentEventSource)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

		Byf("Deleting EventTrigger %s", eventTrigger.Name)
		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

		Byf("Deleting ClusterSet %s in the managed cluster", clusterSet.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSet.Name},
			currentClusterSet)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentClusterSet)).To(Succeed())
	})
})
