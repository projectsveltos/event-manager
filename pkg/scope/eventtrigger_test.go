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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const eventTriggerNamePrefix = "scope-"

var _ = Describe("EventTriggerScope", func() {
	var eventTrigger *v1beta1.EventTrigger
	var c client.Client
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))

		eventTrigger = &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventTriggerNamePrefix + randomString(),
			},
		}
		scheme := setupScheme()
		initObjects := []client.Object{eventTrigger}
		c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
	})

	It("Return nil,error if EventTrigger is not specified", func() {
		params := scope.EventTriggerScopeParams{
			Client: c,
			Logger: logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.EventTriggerScopeParams{
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Name returns EventTrigger Name", func() {
		params := scope.EventTriggerScopeParams{
			Client:       c,
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(eventTrigger.Name))
	})

	It("GetSelector returns EventTrigger ClusterSelector", func() {
		eventTrigger.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"zone": "east",
				},
			},
		}
		params := scope.EventTriggerScopeParams{
			Client:       c,
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(reflect.DeepEqual(*scope.GetSelector(),
			eventTrigger.Spec.SourceClusterSelector.LabelSelector)).To(BeTrue())
	})

	It("Close updates EventTrigger", func() {
		params := scope.EventTriggerScopeParams{
			Client:       c,
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		eventTrigger.Labels = map[string]string{"clusters": "hr"}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name}, currentEventTrigger)).To(Succeed())
		Expect(currentEventTrigger.Labels).ToNot(BeNil())
		Expect(len(currentEventTrigger.Labels)).To(Equal(1))
		v, ok := currentEventTrigger.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("SetMatchingClusterRefs sets EventTrigger.Status.MatchingCluster", func() {
		params := scope.EventTriggerScopeParams{
			Client:       c,
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: "t-" + randomString(),
				Name:      "c-" + randomString(),
			},
		}
		scope.SetMatchingClusterRefs(matchingClusters)
		Expect(reflect.DeepEqual(eventTrigger.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("SetClusterInfo updates ClusterInfo", func() {
		params := scope.EventTriggerScopeParams{
			Client:       c,
			EventTrigger: eventTrigger,
			Logger:       logger,
		}

		scope, err := scope.NewEventTriggerScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterNamespace := randomString()
		clusterName := randomString()
		hash := []byte(randomString())
		clusterInfo := libsveltosv1beta1.ClusterInfo{
			Cluster: corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName},
			Hash:    hash,
		}
		scope.SetClusterInfo([]libsveltosv1beta1.ClusterInfo{clusterInfo})
		Expect(eventTrigger.Status.ClusterInfo).ToNot(BeNil())
		Expect(len(eventTrigger.Status.ClusterInfo)).To(Equal(1))
		Expect(eventTrigger.Status.ClusterInfo[0].Cluster.Namespace).To(Equal(clusterNamespace))
		Expect(eventTrigger.Status.ClusterInfo[0].Cluster.Name).To(Equal(clusterName))
		Expect(eventTrigger.Status.ClusterInfo[0].Hash).To(Equal(hash))
	})
})
