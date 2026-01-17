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
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("EventTriggerReconciler map functions", func() {
	var namespace string

	const upstreamClusterNamePrefix = "transformation-"

	BeforeEach(func() {
		namespace = "map-function" + randomString()
	})

	It("requeueEventTriggerForCluster returns matching EventTriggers", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingEventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				SourceClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "production",
						},
					},
				},
			},
		}

		nonMatchingEventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				SourceClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "qa",
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			matchingEventTrigger,
			nonMatchingEventTrigger,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.EventTriggerReconciler{
			Client:           c,
			Scheme:           scheme,
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterLabels:    make(map[corev1.ObjectReference]map[string]string),
			Mux:              sync.Mutex{},
		}

		By("Setting EventTriggerReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: v1beta1.EventTriggerKind, Name: matchingEventTrigger.Name}
		reconciler.EventTriggers[matchingInfo] = matchingEventTrigger.Spec.SourceClusterSelector
		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: v1beta1.EventTriggerKind, Name: nonMatchingEventTrigger.Name}
		reconciler.EventTriggers[nonMatchingInfo] = nonMatchingEventTrigger.Spec.SourceClusterSelector

		// ClusterMap contains, per ClusterName, list of EventTriggers matching it.
		eventTriggerSet := &libsveltosset.Set{}
		eventTriggerSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: cluster.Kind,
			Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = eventTriggerSet

		// CHCToClusterMap contains, per EventTrigger, list of matched Clusters.
		clusterSet1 := &libsveltosset.Set{}
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		clusterSet2 := &libsveltosset.Set{}
		clusterSet2.Insert(&clusterInfo)
		reconciler.ToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = clusterSet2

		By("Expect only matchingEventTrigger to be requeued")
		requests := controllers.RequeueEventTriggerForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingEventTrigger.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing eventTrigger ClusterSelector again to have two EventTriggers match")
		nonMatchingEventTrigger.Spec.SourceClusterSelector = matchingEventTrigger.Spec.SourceClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingEventTrigger)).To(Succeed())

		reconciler.EventTriggers[nonMatchingInfo] = nonMatchingEventTrigger.Spec.SourceClusterSelector

		clusterSet1.Insert(&clusterInfo)
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		eventTriggerSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = eventTriggerSet

		requests = controllers.RequeueEventTriggerForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingEventTrigger.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingEventTrigger.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing eventTrigger ClusterSelector again to have no EventTrigger match")
		matchingEventTrigger.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"env": "qa",
				},
			},
		}

		Expect(c.Update(context.TODO(), matchingEventTrigger)).To(Succeed())
		nonMatchingEventTrigger.Spec.SourceClusterSelector = matchingEventTrigger.Spec.SourceClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingEventTrigger)).To(Succeed())

		emptySet := &libsveltosset.Set{}
		reconciler.ToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = emptySet
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = emptySet
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.EventTriggers[matchingInfo] = matchingEventTrigger.Spec.SourceClusterSelector
		reconciler.EventTriggers[nonMatchingInfo] = nonMatchingEventTrigger.Spec.SourceClusterSelector

		requests = controllers.RequeueEventTriggerForCluster(reconciler, context.TODO(), cluster)
		Expect(requests).To(HaveLen(0))
	})
})
