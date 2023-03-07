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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("Fetcher", func() {
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		configMapName := types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}
		r, err := controllers.GetConfigMap(context.TODO(), c, configMapName)
		Expect(err).To(BeNil())
		Expect(r).ToNot(BeNil())
		Expect(r.Kind).To(Equal("ConfigMap"))
	})

	It("getSecret fetches secret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
		}

		initObjects := []client.Object{
			secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		secretName := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
		r, err := controllers.GetSecret(context.TODO(), c, secretName)
		Expect(err).To(BeNil())
		Expect(r).ToNot(BeNil())
		Expect(r.Kind).To(Equal("Secret"))
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
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		e := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				PolicyRefs: []libsveltosv1alpha1.PolicyRef{
					{
						Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
						Name:      configMap.Name,
						Namespace: configMap.Namespace,
					},
					{
						Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						Name:      secret.Name,
						Namespace: "", // leaving it empty to use cluster namespace
					},
				},
			},
		}

		initObjects := []client.Object{
			secret,
			configMap,
			e,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}

		result, err := controllers.FetchPolicyRefs(context.TODO(), c, e, getClusterRef(cluster), klogr.New())
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(2))
	})

	It("fetchEventReports fetch EventReports for a given EventSource/Cluster pair", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		eventReport := &libsveltosv1alpha1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventSource.Name,
				Namespace: cluster.Namespace,
				Labels:    libsveltosv1alpha1.GetEventReportLabels(eventSource.Name, cluster.Name, &clusterType),
			},
		}

		initObjects := []client.Object{
			eventSource, eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		eventReports, err := controllers.FetchEventReports(context.TODO(), c, cluster.Namespace, cluster.Name, eventSource.Name,
			clusterType)
		Expect(err).To(BeNil())
		Expect(len(eventReports.Items)).To(Equal(1))
	})

	It("fetchEventSource fetches EventSource", func() {
		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		e := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: eventSource.Name,
			},
		}

		initObjects := []client.Object{eventSource}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		es, err := controllers.FetchEventSource(context.TODO(), c, e.Spec.EventSourceName)
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
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
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

		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		eventReport := &libsveltosv1alpha1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventSource.Name,
				Namespace: cluster.Namespace,
				Labels:    libsveltosv1alpha1.GetEventReportLabels(eventSource.Name, cluster.Name, &clusterType),
			},
		}

		e := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				PolicyRefs: []libsveltosv1alpha1.PolicyRef{
					{
						Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
						Name:      configMap.Name,
						Namespace: "", // leaving it empty to use cluster namespace
					},
					{
						Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						Name:      secret.Name,
						Namespace: secret.Namespace,
					},
				},
				EventSourceName: eventSource.Name,
			},
		}

		initObjects := []client.Object{
			secret,
			configMap,
			e,
			eventSource,
			eventReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		result, err := controllers.FetchReferencedResources(context.TODO(), c, e, getClusterRef(cluster), klogr.New())
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(4)) // EventSource + EventReport + ConfigMap + Secret
	})
})
