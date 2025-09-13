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

package controllers_test

import (
	"context"
	"fmt"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Fetcher", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	It("getConfigMap fetches configMap", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		initObjects := []client.Object{
			configMap,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		configMapName := types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}
		r, err := controllers.GetConfigMap(context.TODO(), c, configMapName)
		Expect(err).To(BeNil())
		Expect(r).ToNot(BeNil())
	})

	It("getSecret fetches secret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		initObjects := []client.Object{
			secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		secretName := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
		r, err := controllers.GetSecret(context.TODO(), c, secretName)
		Expect(err).To(BeNil())
		Expect(r).ToNot(BeNil())
	})

	It("fetchPolicyRefs fetches referenced Secrets and ConfigMaps", func() {
		clusterNamespace := randomString()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace, // this is put in cluster namespace on purpose
				// PolicyRef does not set namespace when referencing Secret. So cluster namespace
				// is used
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						DeploymentType: configv1beta1.DeploymentTypeLocal,
						Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:           configMap.Name,
						Namespace:      configMap.Namespace,
					},
					{
						DeploymentType: configv1beta1.DeploymentTypeLocal,
						Kind:           string(libsveltosv1beta1.SecretReferencedResourceKind),
						Name:           secret.Name,
						Namespace:      "", // leaving it empty to use cluster namespace
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		initObjects := []client.Object{
			secret,
			configMap,
			e,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		local, remote, err := controllers.FetchPolicyRefs(context.TODO(), c, e, getClusterRef(cluster), nil,
			randomString(), logger)
		Expect(err).To(BeNil())
		Expect(len(local)).To(Equal(2))
		Expect(len(remote)).To(Equal(0))
	})

	It("fetchPolicyRefs fetches referenced Secrets and ConfigMaps (names are expressed as templates)", func() {
		clusterNamespace := randomString()
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		namePrefix := randomString()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", namePrefix, cluster.Name),
				Namespace: randomString(),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", namePrefix, cluster.Name),
				Namespace: clusterNamespace, // this is put in cluster namespace on purpose
				// PolicyRef does not set namespace when referencing Secret. So cluster namespace
				// is used
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						DeploymentType: configv1beta1.DeploymentTypeLocal,
						Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:           namePrefix + "-{{ .Cluster.metadata.name }}",
						Namespace:      "", // leaving it empty to use cluster namespace
					},
					{
						DeploymentType: configv1beta1.DeploymentTypeRemote,
						Kind:           string(libsveltosv1beta1.SecretReferencedResourceKind),
						Name:           namePrefix + "-{{ .Cluster.metadata.name }}",
						Namespace:      secret.Namespace,
					},
				},
			},
		}

		initObjects := []client.Object{
			secret,
			configMap,
			e,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
		Expect(err).To(BeNil())

		object := &controllers.CurrentObject{
			Cluster: content,
		}

		local, remote, err := controllers.FetchPolicyRefs(context.TODO(), c, e, getClusterRef(cluster),
			object, randomString(), logger)
		Expect(err).To(BeNil())
		Expect(len(local)).To(Equal(1))
		Expect(len(remote)).To(Equal(1))
	})

	It("fetchPolicyRefs fetches referenced Flux Source (names/paths are expressed as templates)", func() {
		clusterNamespace := randomString()
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		namePrefix := randomString()
		gitRepository := &sourcev1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", namePrefix, cluster.Name),
				Namespace: randomString(),
			},
		}

		ociRegistry := &sourcev1b2.OCIRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", namePrefix, cluster.Name),
				Namespace: clusterNamespace, // this is put in cluster namespace on purpose
				// PolicyRef does not set namespace when referencing Secret. So cluster namespace
				// is used
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						DeploymentType: configv1beta1.DeploymentTypeLocal,
						Kind:           string(sourcev1b2.OCIRepositoryKind),
						Name:           namePrefix + "-{{ .Cluster.metadata.name }}",
						Namespace:      "", // leaving it empty to use cluster namespace
						Path:           "charts/{{ .Cluster.metadata.name }}",
					},
					{
						DeploymentType: configv1beta1.DeploymentTypeRemote,
						Kind:           string(sourcev1.GitRepositoryKind),
						Name:           namePrefix + "-{{ .Cluster.metadata.name }}",
						Namespace:      gitRepository.Namespace,
						Path:           "charts/{{ .Cluster.metadata.name }}",
					},
				},
			},
		}

		initObjects := []client.Object{
			ociRegistry,
			gitRepository,
			e,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
		Expect(err).To(BeNil())

		object := &controllers.CurrentObject{
			Cluster: content,
		}

		local, remote, err := controllers.FetchPolicyRefs(context.TODO(), c, e, getClusterRef(cluster),
			object, randomString(), logger)
		Expect(err).To(BeNil())
		Expect(len(local)).To(Equal(1))
		Expect(len(remote)).To(Equal(1))

		for k := range local {
			Expect(k.Path).To(Equal(fmt.Sprintf("charts/%s", cluster.Name)))
		}

		for k := range remote {
			Expect(k.Path).To(Equal(fmt.Sprintf("charts/%s", cluster.Name)))
		}
	})

	It("fetchEventReports fetch EventReports for a given EventSource/Cluster pair", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterType := libsveltosv1beta1.ClusterTypeCapi
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventSource.Name,
				Namespace: cluster.Namespace,
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSource.Name, cluster.Name, &clusterType),
			},
		}

		initObjects := []client.Object{
			eventSource, eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		eventReports, err := controllers.FetchEventReports(context.TODO(), c, cluster.Namespace, cluster.Name, eventSource.Name,
			clusterType)
		Expect(err).To(BeNil())
		Expect(len(eventReports.Items)).To(Equal(1))
	})

	It("fetchEventSource fetches EventSource", func() {
		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSource.Name,
			},
		}

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		initObjects := []client.Object{eventSource, cluster}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		es, err := controllers.FetchEventSource(context.TODO(), c, cluster.Namespace, cluster.Name,
			e.Spec.EventSourceName, libsveltosv1beta1.ClusterTypeSveltos, logger)
		Expect(err).To(BeNil())
		Expect(es).ToNot(BeNil())
	})

	It("fetchReferencedResources fetches EventSource, EventReport and referenced ConfigMap/Secrets", func() {
		clusterNamespace := randomString()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace, // this is put in cluster namespace on purpose
				// PolicyRef does not set namespace when referencing Secret. So cluster namespace
				// is used
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}

		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterType := libsveltosv1beta1.ClusterTypeCapi
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventSource.Name,
				Namespace: cluster.Namespace,
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSource.Name, cluster.Name, &clusterType),
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:      configMap.Name,
						Namespace: "", // leaving it empty to use cluster namespace
					},
					{
						Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
						Name:      secret.Name,
						Namespace: secret.Namespace,
					},
				},
				EventSourceName: eventSource.Name,
			},
		}

		initObjects := []client.Object{
			secret,
			cluster,
			configMap,
			e,
			eventSource,
			eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		result, err := controllers.FetchReferencedResources(context.TODO(), c, e, getClusterRef(cluster), logger)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(4)) // EventSource + EventReport + Referenced Resources
	})
})
