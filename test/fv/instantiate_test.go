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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/event-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var (
	// This Lua script matches all Services with label
	// sveltos: fv
	serviceLuaScript = `
function evaluate()
    hs = {}
	hs.matching = false
	hs.message = ""	
	if obj.metadata.labels ~= nil then
	  for key, value in pairs(obj.metadata.labels) do
        if key == "sveltos" then
		  if value == "fv" then
		    hs.matching = true
		  end
		end
      end
	end
	return hs
end`

	networkPolicy = `kind: NetworkPolicy
apiVersion: networking.k8s.io/v1
metadata:
  name: front-{{ .Resource.metadata.name }}
  namespace: {{ .Resource.metadata.namespace }}
spec:
  podSelector:
    matchLabels:
      {{ range $key, $value := .Resource.spec.selector }}
      {{ $key }}: {{ $value }}
      {{ end }}
  ingress:
    - from:
      - podSelector:
          matchLabels:
            app: wordpress
      ports:
        {{ range $port := .Resource.spec.ports }}
        - port: {{ $port.port }}
        {{ end }}`
)

var _ = Describe("Instantiate one ClusterProfile per resource", func() {
	const (
		namePrefix     = "events-"
		projectsveltos = "projectsveltos"
	)

	var clusterSummary *configv1alpha1.ClusterSummary

	It("Verifies ClusterProfiles is instantiated using eventreport values", Label("FV"), func() {
		serviceNamespace := randomString()

		Byf("Create a EventSource matching Services in namespace: %s", serviceNamespace)
		eventSource := libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				Group:            "",
				Version:          "v1",
				Kind:             "Service",
				Namespace:        serviceNamespace,
				Script:           serviceLuaScript,
				CollectResources: true,
			},
		}
		Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

		By("Creating a ConfigMap containing a calico policy")
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      randomString(),
				// Mark resource as template so instantiateReferencedPolicies
				// will generate a new one in projectsveltos namespace
				Annotations: map[string]string{
					libsveltosv1alpha1.PolicyTemplateAnnotation: "ok",
				},
			},
			Data: map[string]string{
				"calico": networkPolicy,
			},
		}
		Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

		policyRef := configv1alpha1.PolicyRef{
			Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			Namespace: cm.Namespace,
			Name:      cm.Name,
		}

		Byf("Create a EventBasedAddOn referencing EventSource %s", eventSource.Name)
		eventBasedAddOn := getEventBasedAddOn(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1alpha1.PolicyRef{policyRef})
		eventBasedAddOn.Spec.OneForEvent = true
		eventBasedAddOn.Spec.HelmCharts = []configv1alpha1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v2.6.0",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
				HelmChartAction:  configv1alpha1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Create(context.TODO(), eventBasedAddOn)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying EventSource %s is present in the managed cluster", eventSource.Name)
		Eventually(func() error {
			currentEventSource := &libsveltosv1alpha1.EventSource{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying EventReports %s is present in the managed cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1alpha1.EventReport{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying EventReports %s is present in the management cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1alpha1.EventReport{}
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
		var port int32 = 5467
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: serviceNamespace, // EventSource filters service in this namespace
				Name:      randomString(),
				Labels: map[string]string{
					"sveltos": "fv", // those labels are needed to match lua script
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

		Byf("Verifying EventReports %s is present in the managed cluster with matching resource", eventSource.Name)
		Eventually(func() bool {
			currentEventReport := &libsveltosv1alpha1.EventReport{}
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
			currentEventReport := &libsveltosv1alpha1.EventReport{}
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
				client.MatchingLabels(getInstantiatedObjectLabels(eventBasedAddOn.Name)),
			}
			clusterProfileList := &configv1alpha1.ClusterProfileList{}
			err = k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		listOptions := []client.ListOption{
			client.MatchingLabels(getInstantiatedObjectLabels(eventBasedAddOn.Name)),
		}
		clusterProfileList := &configv1alpha1.ClusterProfileList{}
		Expect(k8sClient.List(context.TODO(), clusterProfileList, listOptions...)).To(Succeed())
		clusterProfile := &clusterProfileList.Items[0]
		clusterSummary = verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureHelm)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureResources)

		Byf("Verifying NewtworkPolicy is created in the namespace %s", serviceNamespace)
		listOptions = []client.ListOption{
			client.InNamespace(serviceNamespace),
		}
		networkPolicies := &networkingv1.NetworkPolicyList{}
		Expect(workloadClient.List(context.TODO(), networkPolicies, listOptions...)).To(Succeed())
		Expect(len(networkPolicies.Items)).To(Equal(1))

		Byf("Deleting EventBasedAddOn %s", eventBasedAddOn.Name)
		currentEventBasedAddOn := &v1alpha1.EventBasedAddOn{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventBasedAddOn.Name},
			currentEventBasedAddOn)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventBasedAddOn)).To(Succeed())

		Byf("Verifying EventSource %s is removed from the managed cluster", eventSource.Name)
		Eventually(func() bool {
			currentEventSource := &libsveltosv1alpha1.EventSource{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying EventReports %s is removed from the managed cluster", eventSource.Name)
		Eventually(func() bool {
			currentEventReport := &libsveltosv1alpha1.EventReport{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying EventBasedAddOn %s is removed from the management cluster", eventBasedAddOn.Name)
		Eventually(func() bool {
			err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventBasedAddOn.Name},
				currentEventBasedAddOn)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting EventSource %s in the managed cluster", eventSource.Name)
		currentEventSource := &libsveltosv1alpha1.EventSource{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
			currentEventSource)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())

		Byf("Verifying EventReports %s is removed from the management cluster", getEventReportName(eventSource.Name))
		Eventually(func() bool {
			currentEventReport := &libsveltosv1alpha1.EventReport{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: getEventReportName(eventSource.Name)},
				currentEventReport)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		By("Verifying ClusterProfile has been removed")
		Eventually(func() bool {
			listOptions = []client.ListOption{
				client.MatchingLabels(getInstantiatedObjectLabels(eventBasedAddOn.Name)),
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

func getEventReportName(eventSourceName string) string {
	return fmt.Sprintf("capi--%s--clusterapi-workload", eventSourceName)
}

// getInstantiatedObjectLabels returns the labels to add to a ClusterProfile created
// by an EventBasedAddOn for a given cluster
func getInstantiatedObjectLabels(eventBasedAddOnName string) map[string]string {
	return map[string]string{
		"eventbasedaddon.lib.projectsveltos.io/eventbasedaddonname": eventBasedAddOnName,
	}
}
