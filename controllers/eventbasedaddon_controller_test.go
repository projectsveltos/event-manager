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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/controllers"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func getEventBasedAddOnInstance(name, eventSourceName string) *v1alpha1.EventBasedAddOn {
	selector := "bar=foo"
	return &v1alpha1.EventBasedAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.EventBasedAddOnSpec{
			ClusterSelector: libsveltosv1alpha1.Selector(selector),
			EventSourceName: eventSourceName,
		},
	}
}

var _ = Describe("EventBasedAddOn: Reconciler", func() {
	var resource *v1alpha1.EventBasedAddOn
	var eventSourceName string

	BeforeEach(func() {
		eventSourceName = randomString()
		resource = getEventBasedAddOnInstance(randomString(), eventSourceName)
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		controllers.RegisterFeatures(dep, klogr.New())

		reconciler := controllers.EventBasedAddOnReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns: make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		resourceName := client.ObjectKey{
			Name: resource.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentChc := &v1alpha1.EventBasedAddOn{}
		err = c.Get(context.TODO(), resourceName, currentChc)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentChc,
				v1alpha1.EventBasedAddOnFinalizer,
			),
		).Should(BeTrue())
	})

	It("Remove finalizer", func() {
		Expect(controllerutil.AddFinalizer(resource, v1alpha1.EventBasedAddOnFinalizer)).To(BeTrue())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		initObjects := []client.Object{
			resource,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		resourceName := client.ObjectKey{
			Name: resource.Name,
		}

		currentResource := &v1alpha1.EventBasedAddOn{}

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())
		Expect(c.Delete(context.TODO(), currentResource)).To(Succeed())

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())
		currentResource.Status.ClusterInfo = []libsveltosv1alpha1.ClusterInfo{
			{
				Cluster: corev1.ObjectReference{
					Namespace:  cluster.Namespace,
					Name:       cluster.Name,
					APIVersion: cluster.APIVersion,
					Kind:       cluster.Kind,
				},
				Status: libsveltosv1alpha1.SveltosStatusProvisioned,
				Hash:   []byte(randomString()),
			},
		}

		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		Expect(dep.RegisterFeatureID(v1alpha1.FeatureEventBasedAddOn)).To(Succeed())

		reconciler := controllers.EventBasedAddOnReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns: make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		// Because EventBasedAddOn is currently deployed in a Cluster (Status.ClusterCondition is set
		// indicating that) Reconcile won't be removed Finalizer
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), resourceName, currentResource)
		Expect(err).ToNot(HaveOccurred())
		Expect(controllerutil.ContainsFinalizer(currentResource, v1alpha1.EventBasedAddOnFinalizer)).To(BeTrue())

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())

		currentResource.Status.ClusterInfo = []libsveltosv1alpha1.ClusterInfo{}
		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		// Because EventBasedAddOn is currently deployed nowhere (Status.ClusterCondition is set
		// indicating that) Reconcile will be removed Finalizer
		_, err = reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), resourceName, currentResource)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("updateClusterInfo updates EventBasedAddOn Status.ClusterInfo field", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		Expect(dep.RegisterFeatureID(v1alpha1.FeatureEventBasedAddOn)).To(Succeed())

		currentResource := &v1alpha1.EventBasedAddOn{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)).To(Succeed())
		currentResource.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  randomString(),
				Name:       randomString(),
				Kind:       "Cluster",
				APIVersion: clusterv1.GroupVersion.String(),
			},
			{
				Namespace:  randomString(),
				Name:       randomString(),
				Kind:       libsveltosv1alpha1.SveltosClusterKind,
				APIVersion: libsveltosv1alpha1.GroupVersion.String(),
			},
		}

		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		reconciler := controllers.EventBasedAddOnReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns: make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		resourceScope, err := scope.NewEventBasedAddOnScope(scope.EventBasedAddOnScopeParams{
			Client:          c,
			Logger:          klogr.New(),
			EventBasedAddOn: currentResource,
			ControllerName:  "classifier",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterInfo(&reconciler, context.TODO(), resourceScope)).To(Succeed())
		Expect(resourceScope.PatchObject(context.TODO())).To(Succeed())

		Expect(c.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)).To(Succeed())
		Expect(currentResource.Status.ClusterInfo).ToNot(BeNil())
		Expect(len(currentResource.Status.ClusterInfo)).To(Equal(len(currentResource.Status.MatchingClusterRefs)))
	})

	It("cleanMaps cleans EventBasedAddOnReconciler maps", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		Expect(dep.RegisterFeatureID(v1alpha1.FeatureEventBasedAddOn)).To(Succeed())

		reconciler := controllers.EventBasedAddOnReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns: make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		resourceScope, err := scope.NewEventBasedAddOnScope(scope.EventBasedAddOnScopeParams{
			Client:          c,
			Logger:          klogr.New(),
			EventBasedAddOn: resource,
			ControllerName:  "classifier",
		})
		Expect(err).To(BeNil())

		resourceRef := controllers.GetKeyFromObject(scheme, resource)

		clusterInfo := &corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		controllers.GetClusterMapForEntry(&reconciler, clusterInfo).Insert(resourceRef)

		eventSourceInfo := &corev1.ObjectReference{Name: randomString(),
			Kind: libsveltosv1alpha1.EventSourceKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		controllers.GetReferenceMapForEntry(&reconciler, eventSourceInfo).Insert(resourceRef)

		reconciler.EventBasedAddOns[*resourceRef] = resource.Spec.ClusterSelector

		controllers.CleanMaps(&reconciler, resourceScope)

		Expect(len(reconciler.ToClusterMap)).To(Equal(0))
		Expect(len(reconciler.ToEventSourceMap)).To(Equal(0))
		Expect(len(reconciler.EventBasedAddOns)).To(Equal(0))
	})

	It("updateMaps updates EventBasedAddOnReconciler maps", func() {
		esName := randomString()

		resource.Spec.EventSourceName = esName

		clusterNamespace := randomString()
		clusterName := randomString()

		resource.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Kind:       libsveltosv1alpha1.SveltosClusterKind,
				APIVersion: libsveltosv1alpha1.GroupVersion.String(),
				Namespace:  clusterNamespace,
				Name:       clusterName,
			},
		}

		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		Expect(dep.RegisterFeatureID(v1alpha1.FeatureEventBasedAddOn)).To(Succeed())

		reconciler := controllers.EventBasedAddOnReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventBasedAddOns: make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}
		resourceScope, err := scope.NewEventBasedAddOnScope(scope.EventBasedAddOnScopeParams{
			Client:          c,
			Logger:          klogr.New(),
			EventBasedAddOn: resource,
			ControllerName:  "classifier",
		})
		Expect(err).To(BeNil())

		controllers.UpdateMaps(&reconciler, resourceScope)

		clusterInfo := &corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName,
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		Expect(controllers.GetClusterMapForEntry(&reconciler, clusterInfo).Len()).To(Equal(1))

		eventSourceInfo := &corev1.ObjectReference{Name: esName,
			Kind: libsveltosv1alpha1.EventSourceKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		Expect(controllers.GetReferenceMapForEntry(&reconciler, eventSourceInfo).Len()).To(Equal(1))
	})
})
