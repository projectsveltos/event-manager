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
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("EventBasedAddOnReconciler map functions", func() {
	var namespace string

	const upstreamClusterNamePrefix = "transformation-"

	BeforeEach(func() {
		namespace = "map-function" + randomString()
	})

	It("requeueEventBasedAddOnForCluster returns matching EventBasedAddOns", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingEventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				SourceClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		nonMatchingEventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				SourceClusterSelector: libsveltosv1alpha1.Selector("env=qa"),
			},
		}

		initObjects := []client.Object{
			matchingEventBasedAddOn,
			nonMatchingEventBasedAddOn,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.EventBasedAddOnReconciler{
			Client:             c,
			Scheme:             scheme,
			ClusterMap:         make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:       make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap:   make(map[types.NamespacedName]*libsveltosset.Set),
			ClusterLabels:      make(map[corev1.ObjectReference]map[string]string),
			EventBasedAddOnMap: make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			Mux:                sync.Mutex{},
		}

		By("Setting EventBasedAddOnReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: v1alpha1.EventBasedAddOnKind, Name: matchingEventBasedAddOn.Name}
		reconciler.EventBasedAddOns[matchingInfo] = matchingEventBasedAddOn.Spec.SourceClusterSelector
		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: v1alpha1.EventBasedAddOnKind, Name: nonMatchingEventBasedAddOn.Name}
		reconciler.EventBasedAddOns[nonMatchingInfo] = nonMatchingEventBasedAddOn.Spec.SourceClusterSelector

		// ClusterMap contains, per ClusterName, list of EventBasedAddOns matching it.
		eventBasedAddOnSet := &libsveltosset.Set{}
		eventBasedAddOnSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: cluster.Kind,
			Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = eventBasedAddOnSet

		// CHCToClusterMap contains, per EventBasedAddOn, list of matched Clusters.
		clusterSet1 := &libsveltosset.Set{}
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		clusterSet2 := &libsveltosset.Set{}
		clusterSet2.Insert(&clusterInfo)
		reconciler.ToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = clusterSet2

		By("Expect only matchingEventBasedAddOn to be requeued")
		requests := controllers.RequeueEventBasedAddOnForCluster(reconciler, cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingEventBasedAddOn.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing eventBasedAddOn ClusterSelector again to have two EventBasedAddOns match")
		nonMatchingEventBasedAddOn.Spec.SourceClusterSelector = matchingEventBasedAddOn.Spec.SourceClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingEventBasedAddOn)).To(Succeed())

		reconciler.EventBasedAddOns[nonMatchingInfo] = nonMatchingEventBasedAddOn.Spec.SourceClusterSelector

		clusterSet1.Insert(&clusterInfo)
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = clusterSet1

		eventBasedAddOnSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = eventBasedAddOnSet

		requests = controllers.RequeueEventBasedAddOnForCluster(reconciler, cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingEventBasedAddOn.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingEventBasedAddOn.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing eventBasedAddOn ClusterSelector again to have no EventBasedAddOn match")
		matchingEventBasedAddOn.Spec.SourceClusterSelector = libsveltosv1alpha1.Selector("env=qa")
		Expect(c.Update(context.TODO(), matchingEventBasedAddOn)).To(Succeed())
		nonMatchingEventBasedAddOn.Spec.SourceClusterSelector = matchingEventBasedAddOn.Spec.SourceClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingEventBasedAddOn)).To(Succeed())

		emptySet := &libsveltosset.Set{}
		reconciler.ToClusterMap[types.NamespacedName{Name: matchingInfo.Name}] = emptySet
		reconciler.ToClusterMap[types.NamespacedName{Name: nonMatchingInfo.Name}] = emptySet
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.EventBasedAddOns[matchingInfo] = matchingEventBasedAddOn.Spec.SourceClusterSelector
		reconciler.EventBasedAddOns[nonMatchingInfo] = nonMatchingEventBasedAddOn.Spec.SourceClusterSelector

		requests = controllers.RequeueEventBasedAddOnForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(0))
	})

	It("RequeueEventBasedAddOnForMachine returns correct EventBasedAddOns for a CAPI machine", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + randomString(),
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "ok",
				},
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				SourceClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, eventBasedAddOn)).To(Succeed())

		// In this scenario:
		// - EventBasedAddOn added first
		// - Cluster matching EventBasedAddOn added later
		// - First controlplane Machine in Cluster is ready
		// The only information Sveltos has are:
		// - Cluster's labels (stored in ClusterLabels map)
		// - EventBasedAddOn's selector (stored in EventBasedAddOns maps)
		// RequeueEventBasedAddOnForMachine gets cluster from machine and using ClusterLabels
		// and EventBasedAddOns maps finds the EventBasedAddOns that need to be reconciled

		apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		eventBasedAddOnReconciler := getEventBasedAddOnReconciler(testEnv.Client)

		clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
			Namespace: cluster.GetNamespace(), Name: cluster.GetName()}
		eventBasedAddOnReconciler.ClusterLabels[clusterInfo] = cluster.Labels

		apiVersion, kind = eventBasedAddOn.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		eventBasedAddOnInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: eventBasedAddOn.GetName()}
		eventBasedAddOnReconciler.EventBasedAddOns[eventBasedAddOnInfo] = eventBasedAddOn.Spec.SourceClusterSelector

		eventBasedAddOnList := controllers.RequeueEventBasedAddOnForMachine(eventBasedAddOnReconciler,
			cpMachine)
		Expect(len(eventBasedAddOnList)).To(Equal(1))
	})
})
