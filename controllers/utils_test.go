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

package controllers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Utils", func() {
	It("InitScheme registers EventTrigger", func() {
		scheme, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		gvks, _, err := scheme.ObjectKinds(&v1beta1.EventTrigger{})
		Expect(err).To(BeNil())
		Expect(gvks).ToNot(BeEmpty())
		Expect(gvks[0].Kind).To(Equal("EventTrigger"))
	})

	It("InitScheme registers ConfigMap and Secret", func() {
		scheme, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		_, _, err = scheme.ObjectKinds(&corev1.ConfigMap{})
		Expect(err).To(BeNil())

		_, _, err = scheme.ObjectKinds(&corev1.Secret{})
		Expect(err).To(BeNil())
	})

	It("InitScheme registers cluster-api Cluster and CustomResourceDefinition", func() {
		scheme, err := controllers.InitScheme()
		Expect(err).To(BeNil())

		_, _, err = scheme.ObjectKinds(&clusterv1.Cluster{})
		Expect(err).To(BeNil())

		_, _, err = scheme.ObjectKinds(&apiextensionsv1.CustomResourceDefinition{})
		Expect(err).To(BeNil())
	})

	It("getPolicyRef returns a ConfigMap reference for a ConfigMap", func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "my-cm"},
		}

		ref := controllers.GetPolicyRef(cm)
		Expect(ref.Namespace).To(Equal("ns"))
		Expect(ref.Name).To(Equal("my-cm"))
		Expect(ref.Kind).To(Equal(string(libsveltosv1beta1.ConfigMapReferencedResourceKind)))
	})

	It("getPolicyRef returns a Secret reference for a Secret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "my-secret"},
		}

		ref := controllers.GetPolicyRef(secret)
		Expect(ref.Namespace).To(Equal("ns"))
		Expect(ref.Name).To(Equal("my-secret"))
		Expect(ref.Kind).To(Equal(string(libsveltosv1beta1.SecretReferencedResourceKind)))
	})

	It("getPolicyRef panics for an unsupported object type", func() {
		unsupported := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "et"},
		}

		Expect(func() { controllers.GetPolicyRef(unsupported) }).To(Panic())
	})
})
