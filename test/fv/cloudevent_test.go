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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var (
	cm = `apiVersion: v1
kind: ConfigMap
metadata:
  name: namespace
  namespace: default
  annotations:
    projectsveltos.io/instantiate: ok
data:
  namespace.yaml: |
    kind: Namespace
    apiVersion: v1
    metadata:
      name: {{ .CloudEvent.subject }}`
)

var _ = Describe("CloudEvents", func() {
	const (
		namePrefix     = "ce-"
		projectsveltos = "projectsveltos"
	)

	It("EventTrigger using matching CloudEvents", Label("FV", "PULLMODE"), func() {
		Byf("Create a EventSource matching CloudEvents")
		eventSource := libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
				Annotations: map[string]string{
					randomString(): randomString(),
				},
			},
			Spec: libsveltosv1beta1.EventSourceSpec{
				MessagingMatchCriteria: []libsveltosv1beta1.MessagingMatchCriteria{
					{
						Subject: randomString(), // it does not really matter. Test will update EventReports
					},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), &eventSource)).To(Succeed())

		u, err := k8s_utils.GetUnstructured([]byte(cm))
		Expect(err).To(BeNil())

		Byf("Creating a ConfigMap %s/%s to be referenced in PolicyRefs section", u.GetNamespace(), u.GetName())
		err = k8sClient.Create(context.TODO(), u)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}

		policyRef := configv1beta1.PolicyRef{
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace: u.GetNamespace(),
			Name:      u.GetName(),
		}

		Byf("Create a EventTrigger referencing EventSource %s", eventSource.Name)
		eventTrigger := getEventTrigger(namePrefix, eventSource.Name,
			map[string]string{key: value}, []configv1beta1.PolicyRef{policyRef})
		eventTrigger.Spec.OneForEvent = true
		Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

		u, err = k8s_utils.GetUnstructured([]byte(tokenConfigMap))
		Expect(err).To(BeNil())
		err = k8sClient.Create(context.TODO(), u)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}

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

		subject := randomString()
		//nolint: lll // line with cloudEvent
		jsonString := fmt.Sprintf(`{"specversion":"1.0","id":"10001","source":"my.source","type":"my.type","subject":%q,"datacontenttype":"application/json","data":{"message":"hello"}}`, subject)
		jsonData := []byte(jsonString)

		currentEventReport := &libsveltosv1beta1.EventReport{}
		if isAgentLessMode() {
			Byf("Updating EventReports in the management cluster adding a matching CloudEvent")
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
				currentEventReport)).To(Succeed())
			currentEventReport.Spec.CloudEvents = [][]byte{jsonData}
			Expect(k8sClient.Update(context.TODO(), currentEventReport)).To(Succeed())
			waitingForDelivery := libsveltosv1beta1.ReportWaitingForDelivery
			currentEventReport.Status.Phase = &waitingForDelivery
			Expect(k8sClient.Status().Update(context.TODO(), currentEventReport)).To(Succeed())
		} else {
			Byf("Updating EventReports in the managed cluster adding a matching CloudEvent")
			Expect(workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
				currentEventReport)).To(Succeed())
			currentEventReport.Spec.CloudEvents = [][]byte{jsonData}
			Expect(workloadClient.Update(context.TODO(), currentEventReport)).To(Succeed())
			waitingForDelivery := libsveltosv1beta1.ReportWaitingForDelivery
			currentEventReport.Status.Phase = &waitingForDelivery
			Expect(workloadClient.Status().Update(context.TODO(), currentEventReport)).To(Succeed())
		}

		if isAgentLessMode() {
			Byf("Verifying EventReports %s has CloudEvents reset in the management cluster", eventSource.Name)
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSource.Name)},
					currentEventReport)
				if err != nil {
					return false
				}
				return len(currentEventReport.Spec.CloudEvents) == 0
			}, timeout, pollingInterval).Should(BeTrue())
		} else {
			Byf("Verifying EventReports %s has CloudEvents reset in the managed cluster", eventSource.Name)
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: projectsveltos, Name: eventSource.Name},
					currentEventReport)
				if err != nil {
					return false
				}
				return len(currentEventReport.Spec.CloudEvents) == 0
			}, timeout, pollingInterval).Should(BeTrue())
		}

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
		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		// Namespace with Subject name should be present in the managed cluster as response
		Byf("Verifying Namespace %s is created in the managed cluster", subject)
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: subject},
				currentNamespace)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

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

		Byf("Deleting EventSource %s in the management cluster", eventSource.Name)
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
