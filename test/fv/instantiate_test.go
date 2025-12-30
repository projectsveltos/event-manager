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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
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
  labels:
    cluster: {{ .Cluster.metadata.name }}
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

	var clusterSummary *configv1beta1.ClusterSummary

	It("Verifies ClusterProfiles is instantiated using eventreport values", Label("FV", "PULLMODE"), func() {
		serviceNamespace := randomString()

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
						Evaluate:  serviceLuaScript,
					},
				},
				CollectResources: true,
			},
		}
		Byf("Create a EventSource %s matching Services in namespace: %s",
			eventSource.Name, serviceNamespace)
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
				"calico": networkPolicy,
			},
		}
		Byf("Creating a ConfigMap %s/%s containing a calico policy", cm.Namespace, cm.Name)
		Expect(k8sClient.Create(context.TODO(), cm)).To(Succeed())

		policyRef := configv1beta1.PolicyRef{
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace: cm.Namespace,
			Name:      cm.Name,
		}

		eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
		eventTrigger.Spec.OneForEvent = true
		eventTrigger.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v2.6.0",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
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

		Byf("Verifying EventReports %s is present in the management cluster", eventSource.Name)
		Eventually(func() error {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
				currentEventReport)
		}, timeout, pollingInterval).Should(BeNil())

		createNamespaceAndService(workloadClient, serviceNamespace)

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
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
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
		clusterSummary = verifyClusterSummary(clusterProfile, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

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
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
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
	})
})

func getEventReportName(eventSourceName string) string {
	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		return fmt.Sprintf("sveltos--%s--clusterapi-workload", eventSourceName)
	}
	return fmt.Sprintf("capi--%s--clusterapi-workload", eventSourceName)
}

// getInstantiatedObjectLabels returns the labels to add to a ClusterProfile created
// by an EventTrigger for a given cluster
func getInstantiatedObjectLabels(eventTriggerName string) map[string]string {
	return map[string]string{
		"eventtrigger.lib.projectsveltos.io/eventtriggername": eventTriggerName,
	}
}
