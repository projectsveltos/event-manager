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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func getEventTriggerInstance(name, eventSourceName string) *v1beta1.EventTrigger {
	return &v1beta1.EventTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1beta1.EventTriggerSpec{
			SourceClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "foo",
					},
				},
			},
			EventSourceName: eventSourceName,
		},
	}
}

var _ = Describe("EventTrigger: Reconciler", func() {
	var resource *v1beta1.EventTrigger
	var eventSourceName string
	var logger logr.Logger

	BeforeEach(func() {
		eventSourceName = randomString()
		resource = getEventTriggerInstance(randomString(), eventSourceName)
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), logger, c)
		controllers.RegisterFeatures(dep, logger)

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSetMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		}
		resourceName := client.ObjectKey{
			Name: resource.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentChc := &v1beta1.EventTrigger{}
		err = c.Get(context.TODO(), resourceName, currentChc)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentChc,
				v1beta1.EventTriggerFinalizer,
			),
		).Should(BeTrue())
	})

	It("Remove finalizer", func() {
		Expect(controllerutil.AddFinalizer(resource, v1beta1.EventTriggerFinalizer)).To(BeTrue())

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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		resourceName := client.ObjectKey{
			Name: resource.Name,
		}

		currentResource := &v1beta1.EventTrigger{}

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())
		Expect(c.Delete(context.TODO(), currentResource)).To(Succeed())

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())
		currentResource.Status.ClusterInfo = []libsveltosv1beta1.ClusterInfo{
			{
				Cluster: corev1.ObjectReference{
					Namespace:  cluster.Namespace,
					Name:       cluster.Name,
					APIVersion: cluster.APIVersion,
					Kind:       cluster.Kind,
				},
				Status: libsveltosv1beta1.SveltosStatusProvisioned,
				Hash:   []byte(randomString()),
			},
		}

		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), logger, c)
		Expect(dep.RegisterFeatureID(v1beta1.FeatureEventTrigger)).To(Succeed())

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSetMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		}

		// Because EventTrigger is currently deployed in a Cluster (Status.ClusterCondition is set
		// indicating that) Reconcile won't be removed Finalizer
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), resourceName, currentResource)
		Expect(err).ToNot(HaveOccurred())
		Expect(controllerutil.ContainsFinalizer(currentResource, v1beta1.EventTriggerFinalizer)).To(BeTrue())

		Expect(c.Get(context.TODO(), resourceName, currentResource)).To(Succeed())

		currentResource.Status.ClusterInfo = []libsveltosv1beta1.ClusterInfo{}
		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		// Because EventTrigger is currently deployed nowhere (Status.ClusterCondition is set
		// indicating that) Reconcile will be removed Finalizer
		_, err = reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: resourceName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), resourceName, currentResource)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("updateClusterInfo updates EventTrigger Status.ClusterInfo field", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), logger, c)
		Expect(dep.RegisterFeatureID(v1beta1.FeatureEventTrigger)).To(Succeed())

		currentResource := &v1beta1.EventTrigger{}
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
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
			},
		}

		Expect(c.Status().Update(context.TODO(), currentResource)).To(Succeed())

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSetMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		}

		resourceScope, err := scope.NewEventTriggerScope(scope.EventTriggerScopeParams{
			Client:         c,
			Logger:         logger,
			EventTrigger:   currentResource,
			ControllerName: "classifier",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterInfo(&reconciler, context.TODO(), resourceScope)).To(Succeed())
		Expect(resourceScope.PatchObject(context.TODO())).To(Succeed())

		Expect(c.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)).To(Succeed())
		Expect(currentResource.Status.ClusterInfo).ToNot(BeNil())
		Expect(len(currentResource.Status.ClusterInfo)).To(Equal(len(currentResource.Status.MatchingClusterRefs)))
	})

	It("cleanMaps cleans EventTriggerReconciler maps", func() {
		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), logger, c)
		Expect(dep.RegisterFeatureID(v1beta1.FeatureEventTrigger)).To(Succeed())

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSetMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		}
		resourceScope, err := scope.NewEventTriggerScope(scope.EventTriggerScopeParams{
			Client:         c,
			Logger:         logger,
			EventTrigger:   resource,
			ControllerName: "classifier",
		})
		Expect(err).To(BeNil())

		resourceRef := controllers.GetKeyFromObject(scheme, resource)

		clusterInfo := &corev1.ObjectReference{Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		controllers.GetConsumersForEntry(reconciler.ClusterMap, clusterInfo).Insert(resourceRef)

		eventSourceInfo := &corev1.ObjectReference{Name: randomString(),
			Kind: libsveltosv1beta1.EventSourceKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		controllers.GetConsumersForEntry(reconciler.ReferenceMap, eventSourceInfo).Insert(resourceRef)

		clusterSetInfo := &corev1.ObjectReference{Name: randomString(),
			Kind: libsveltosv1beta1.ClusterSetKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		controllers.GetConsumersForEntry(reconciler.ClusterSetMap, clusterSetInfo).Insert(resourceRef)

		reconciler.EventTriggers[*resourceRef] = resource.Spec.SourceClusterSelector

		controllers.CleanMaps(&reconciler, resourceScope)

		Expect(len(reconciler.ToClusterMap)).To(Equal(0))
		Expect(len(reconciler.ToEventSourceMap)).To(Equal(0))
		Expect(len(reconciler.EventTriggers)).To(Equal(0))
		Expect(len(reconciler.ClusterSetMap)).To(Equal(0))
	})

	It("updateMaps updates EventTriggerReconciler maps", func() {
		esName := randomString()

		resource.Spec.EventSourceName = esName

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterSetName := randomString()

		resource.Spec.ClusterSetRefs = []string{clusterSetName}
		resource.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Namespace:  clusterNamespace,
				Name:       clusterName,
			},
		}

		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), logger, c)
		Expect(dep.RegisterFeatureID(v1beta1.FeatureEventTrigger)).To(Succeed())

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Deployer:         dep,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSetMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		}
		resourceScope, err := scope.NewEventTriggerScope(scope.EventTriggerScopeParams{
			Client:         c,
			Logger:         logger,
			EventTrigger:   resource,
			ControllerName: "classifier",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateMaps(&reconciler, resourceScope, logger)).To(Succeed())

		clusterInfo := &corev1.ObjectReference{Namespace: clusterNamespace, Name: clusterName,
			Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		Expect(controllers.GetConsumersForEntry(reconciler.ClusterMap, clusterInfo).Len()).To(Equal(1))

		eventSourceInfo := &corev1.ObjectReference{Name: esName,
			Kind: libsveltosv1beta1.EventSourceKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		Expect(controllers.GetConsumersForEntry(reconciler.EventSourceMap, eventSourceInfo).Len()).To(Equal(1))

		clusterSetInfo := &corev1.ObjectReference{Name: clusterSetName,
			Kind: libsveltosv1beta1.ClusterSetKind, APIVersion: libsveltosv1beta1.GroupVersion.String()}
		Expect(controllers.GetConsumersForEntry(reconciler.ClusterSetMap, clusterSetInfo).Len()).To(Equal(1))
	})

	It("getClustersFromClusterSets gets cluster selected by referenced clusterSet", func() {
		clusterSet1 := &libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		clusterSet2 := &libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		resource.Spec.ClusterSetRefs = []string{clusterSet1.Name, clusterSet2.Name}

		initObjects := []client.Object{
			clusterSet1,
			clusterSet2,
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.EventTriggerReconciler{
			Client:           c,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggerMap:  make(map[types.NamespacedName]*libsveltosset.Set),
			ReferenceMap:     make(map[corev1.ObjectReference]*libsveltosset.Set),
		}

		clusters, err := controllers.GetClustersFromClusterSets(reconciler, context.TODO(),
			resource.Spec.ClusterSetRefs, logger)
		Expect(err).To(BeNil())
		Expect(clusters).ToNot(BeNil())

		for i := range clusterSet1.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  clusterSet1.Status.SelectedClusterRefs[i].Namespace,
				Name:       clusterSet1.Status.SelectedClusterRefs[i].Name,
				Kind:       clusterSet1.Status.SelectedClusterRefs[i].Kind,
				APIVersion: clusterSet1.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
		for i := range clusterSet2.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  clusterSet2.Status.SelectedClusterRefs[i].Namespace,
				Name:       clusterSet2.Status.SelectedClusterRefs[i].Name,
				Kind:       clusterSet2.Status.SelectedClusterRefs[i].Kind,
				APIVersion: clusterSet2.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
	})
})
