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

package scope_test

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const eventBasedAddOnNamePrefix = "scope-"

var _ = Describe("EventBasedAddOnScope", func() {
	var eventBasedAddOn *v1alpha1.EventBasedAddOn
	var c client.Client

	BeforeEach(func() {
		eventBasedAddOn = &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventBasedAddOnNamePrefix + randomString(),
			},
		}
		scheme := setupScheme()
		initObjects := []client.Object{eventBasedAddOn}
		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
	})

	It("Return nil,error if EventBasedAddOn is not specified", func() {
		params := scope.EventBasedAddOnScopeParams{
			Client: c,
			Logger: klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.EventBasedAddOnScopeParams{
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Name returns EventBasedAddOn Name", func() {
		params := scope.EventBasedAddOnScopeParams{
			Client:          c,
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(eventBasedAddOn.Name))
	})

	It("GetSelector returns EventBasedAddOn ClusterSelector", func() {
		eventBasedAddOn.Spec.SourceClusterSelector = libsveltosv1alpha1.Selector("zone=east")
		params := scope.EventBasedAddOnScopeParams{
			Client:          c,
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.GetSelector()).To(Equal(string(eventBasedAddOn.Spec.SourceClusterSelector)))
	})

	It("Close updates EventBasedAddOn", func() {
		params := scope.EventBasedAddOnScopeParams{
			Client:          c,
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		eventBasedAddOn.Labels = map[string]string{"clusters": "hr"}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentEventBasedAddOn := &v1alpha1.EventBasedAddOn{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: eventBasedAddOn.Name}, currentEventBasedAddOn)).To(Succeed())
		Expect(currentEventBasedAddOn.Labels).ToNot(BeNil())
		Expect(len(currentEventBasedAddOn.Labels)).To(Equal(1))
		v, ok := currentEventBasedAddOn.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("SetMatchingClusterRefs sets EventBasedAddOn.Status.MatchingCluster", func() {
		params := scope.EventBasedAddOnScopeParams{
			Client:          c,
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: "t-" + randomString(),
				Name:      "c-" + randomString(),
			},
		}
		scope.SetMatchingClusterRefs(matchingClusters)
		Expect(reflect.DeepEqual(eventBasedAddOn.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("SetClusterInfo updates ClusterInfo", func() {
		params := scope.EventBasedAddOnScopeParams{
			Client:          c,
			EventBasedAddOn: eventBasedAddOn,
			Logger:          klogr.New(),
		}

		scope, err := scope.NewEventBasedAddOnScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterNamespace := randomString()
		clusterName := randomString()
		hash := []byte(randomString())
		clusterInfo := libsveltosv1alpha1.ClusterInfo{
			Cluster: corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName},
			Hash:    hash,
		}
		scope.SetClusterInfo([]libsveltosv1alpha1.ClusterInfo{clusterInfo})
		Expect(eventBasedAddOn.Status.ClusterInfo).ToNot(BeNil())
		Expect(len(eventBasedAddOn.Status.ClusterInfo)).To(Equal(1))
		Expect(eventBasedAddOn.Status.ClusterInfo[0].Cluster.Namespace).To(Equal(clusterNamespace))
		Expect(eventBasedAddOn.Status.ClusterInfo[0].Cluster.Name).To(Equal(clusterName))
		Expect(eventBasedAddOn.Status.ClusterInfo[0].Hash).To(Equal(hash))
	})
})
