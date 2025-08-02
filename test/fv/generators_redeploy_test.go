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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

const (
	namespaceLabel = "imagepullsecret"
)

var _ = Describe("Redeploy when Generators change", func() {
	const (
		projectsveltos = "projectsveltos"

		eventTriggerName = "deploy-imagepullsecret"

		namePrefix = "redeploy-generator-"

		configMapGeneratorName = "namespaces-requiring-imagepullsecret"

		//nolint: gosec, lll // just a test
		regcredSecret = `apiVersion: v1
data:
  .dockerconfigjson: ewogICAgImF1dGhzIjogewogICAgICAgICJodHRwczovL2luZGV4LmRvY2tlci5pby92MS8iOiB7CiAgICAgICAgICAgICJhdXRoIjogImMzUkUyIgogICAgICAgIH0KICAgIH0KfQo=
kind: Secret
metadata:
  name: regcred
  namespace: default
type: kubernetes.io/dockerconfigjson`

		// This config instructs Sveltos to watch for namespace with label imagepullsecret:required in the managed cluster
		// When such a namespace is detected, the regcred Secret gets copied.
		sveltosConfig = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  namespaces: |
    {{- range $v := .MatchingResources }}
       {{ $v.Name }}: "ok"
    {{- end }}
---
apiVersion: lib.projectsveltos.io/v1beta1
kind: EventSource
metadata:
  name: new-namespace
spec:
  collectResources: false
  resourceSelectors:
  - group: ""
    version: "v1"
    kind: "Namespace"
    labelFilters:
    - key: %s
      operation: Equal
      value: required
---
apiVersion: lib.projectsveltos.io/v1beta1
kind: EventTrigger
metadata:
  name: %s
spec:
  sourceClusterSelector:
    matchLabels:
      %s: %s
  eventSourceName: new-namespace
  configMapGenerator:
  - name: %s
    namespace: default
    nameFormat: "{{ .Cluster.metadata.name }}-imagepullsecret"
  oneForEvent: false
  templateResourceRefs:
  - resource: # This refers to the resource that Sveltos dynamically generates using ConfigMapGenerator.
      apiVersion: v1
      kind: ConfigMap
      name: "{{ .Cluster.metadata.name }}-imagepullsecret"
      namespace: projectsveltos
    identifier: Namespaces
  - resource: # This is the ConfigMap containing the credentials to authenticate with private registry
      apiVersion: v1
      kind: Secret
      name: regcred
      namespace: default
    identifier: ImagePullSecret
  policyRefs:
  - name: deploy-imagepullsecret
    namespace: default
    kind: ConfigMap
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: deploy-imagepullsecret
  namespace: default
  annotations:
    projectsveltos.io/template: "ok"
data:
  content: |
    {{ $namespaces := ( ( index (getResource "Namespaces").data "namespaces" ) | fromYaml ) }}
    {{- range $key, $value := $namespaces }}
        apiVersion: v1
        kind: Secret
        metadata:
          namespace: {{ $key }}
          name: {{ (getResource "ImagePullSecret").metadata.name }}
        type: kubernetes.io/dockerconfigjson
        data:
          {{- range $secretKey, $secretValue := (getResource "ImagePullSecret").data }}
            {{ $secretKey }} : {{ $secretValue }}
          {{- end }}
    ---
    {{- end }}`

		newConfigMapString = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: default
data:
  namespaces: |
    {{- range $v := .MatchingResources }}
       {{ $v.Name }}: "ok"
       default: "ok"
    {{- end }}`
	)

	It("Configures Generators and have ClusterProfile consume those and redeploy on changes", Label("FV", "PULLMODE"), func() {
		// Prepare the test by removing label imagepullsecret from all namespaces in the workload cluster
		// This is neeed just if we re-run test on same workload cluster
		cleanLabelsFromNamespaces()

		// This is the Secret that gets copied over to managed cluster
		By("Create regcred Secret")
		regcred, err := k8s_utils.GetUnstructured([]byte(regcredSecret))
		Expect(err).To(BeNil())
		err = k8sClient.Create(context.TODO(), regcred)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}

		// This configuration will instruct Sveltos to:
		// 1. watch for namespace with label imagepullsecret: required
		// 2. copy the regcred Secret created above to this namespace
		By("Deploy Sveltos configuration")
		configuration := fmt.Sprintf(sveltosConfig, configMapGeneratorName, namespaceLabel,
			eventTriggerName, key, value, configMapGeneratorName)
		objects, err := customSplit(configuration)
		Expect(err).To(BeNil())
		for i := range objects {
			o, err := k8s_utils.GetUnstructured([]byte(objects[i]))
			Expect(err).To(BeNil())
			err = k8sClient.Create(context.TODO(), o)
			if err != nil {
				Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
			}
		}

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		eventSourceName := "new-namespace"
		if isAgentLessMode() {
			Byf("Verifying EventSource %s is NOT present in the managed cluster", eventSourceName)
			Consistently(func() bool {
				currentEventSource := &libsveltosv1beta1.EventSource{}
				err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSourceName},
					currentEventSource)
				return err != nil && meta.IsNoMatchError(err) // CRD never installed
			}, timeout/2, pollingInterval).Should(BeTrue())
		} else {
			Byf("Verifying EventSource %s is present in the managed cluster", eventSourceName)
			Eventually(func() error {
				currentEventSource := &libsveltosv1beta1.EventSource{}
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: eventSourceName},
					currentEventSource)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying EventReports %s is present in the managed cluster", eventSourceName)
			Eventually(func() error {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: projectsveltos, Name: eventSourceName},
					currentEventReport)
			}, timeout, pollingInterval).Should(BeNil())
		}

		namespace := namePrefix + randomString()
		Byf("Create a namespace %s with proper label", namespace)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Labels: map[string]string{
					"imagepullsecret": "required",
				},
			},
		}
		Expect(workloadClient.Create(context.TODO(), ns)).To(Succeed())

		if !isAgentLessMode() {
			Byf("Verifying EventReports %s is present in the managed cluster with matching resource", eventSourceName)
			Eventually(func() bool {
				currentEventReport := &libsveltosv1beta1.EventReport{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: projectsveltos, Name: eventSourceName},
					currentEventReport)
				if err != nil {
					return false
				}
				return len(currentEventReport.Spec.MatchingResources) != 0
			}, timeout, pollingInterval).Should(BeTrue())
		}

		Byf("Verifying EventReports %s is present in the management cluster with matching resource", getEventReportName(eventSourceName))
		Eventually(func() bool {
			currentEventReport := &libsveltosv1beta1.EventReport{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: getEventReportName(eventSourceName)},
				currentEventReport)
			if err != nil {
				return false
			}

			return len(currentEventReport.Spec.MatchingResources) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Secret %s is copied to the managed cluster", regcred.GetName())
		Eventually(func() error {
			currentSecret := &corev1.Secret{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: namespace, Name: regcred.GetName()},
				currentSecret)
		}, timeout, pollingInterval).Should(BeNil())

		// Change the ConfigMap referenced in the ConfigMapGenerator section
		// This change will instruct Sveltos to copy the regcred Secret to the default namespace as well
		By("Modify ConfigMap referenced in the ConfigMapGenerator section")
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "default", Name: configMapGeneratorName},
			currentConfigMap)).To(Succeed())
		newConfigMap, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(newConfigMapString, configMapGeneratorName)))
		Expect(err).To(BeNil())
		newConfigMap.SetResourceVersion(currentConfigMap.ResourceVersion)
		Expect(k8sClient.Update(context.TODO(), newConfigMap))

		Byf("Verifying Secret %s is copied to the managed cluster to default namespace as well", regcred.GetName())
		Eventually(func() error {
			currentSecret := &corev1.Secret{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: regcred.GetName()},
				currentSecret)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying Secret %s is copied to the managed cluster (namespace %s)", regcred.GetName(), namespace)
		Consistently(func() error {
			currentSecret := &corev1.Secret{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: namespace, Name: regcred.GetName()},
				currentSecret)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Deleting EventTrigger %s", eventTriggerName)
		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTriggerName},
			currentEventTrigger)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

		Byf("Verifying EventTrigger %s is removed from the management cluster", eventTriggerName)
		Eventually(func() bool {
			err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTriggerName},
				currentEventTrigger)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

	})
})

func customSplit(text string) ([]string, error) {
	result := []string{}

	dec := yaml.NewDecoder(bytes.NewReader([]byte(text)))

	for {
		var value interface{}
		err := dec.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		valueBytes, err := yaml.Marshal(value)
		if err != nil {
			return nil, err
		}
		if valueBytes == nil {
			continue
		}
		result = append(result, string(valueBytes))
	}

	return result, nil
}

func cleanLabelsFromNamespaces() {
	Byf("Getting client to access the workload cluster")
	workloadClient, err := getKindWorkloadClusterKubeconfig()
	Expect(err).To(BeNil())
	Expect(workloadClient).ToNot(BeNil())

	namespaces := &corev1.NamespaceList{}
	Expect(workloadClient.List(context.TODO(), namespaces)).To(Succeed())
	for i := range namespaces.Items {
		removeLabel(workloadClient, &namespaces.Items[i], namespaceLabel)
	}
}

func removeLabel(c client.Client, o client.Object, labelKey string) {
	lbls := o.GetLabels()
	if lbls == nil {
		return
	}

	delete(lbls, labelKey)
	o.SetLabels(lbls)
	Expect(c.Update(context.TODO(), o)).To(Succeed())
}
