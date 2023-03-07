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
	"crypto/sha256"
	"reflect"
	"sync"

	"github.com/gdexlab/go-render/render"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/controllers"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	ClusterKind = "Cluster"
)

var _ = Describe("EventBasedAddOn deployer", func() {
	It("processEventBasedAddOn queues job", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		// Following creates a ClusterSummary and an EventBasedAddOn
		c := prepareClient(clusterNamespace, clusterName, clusterType)

		// Add machine to mark Cluster ready
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             clusterName,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		Expect(c.Create(context.TODO(), cpMachine)).To(Succeed())

		// Verify eventBasedAddOn has been created
		resources := &v1alpha1.EventBasedAddOnList{}
		Expect(c.List(context.TODO(), resources)).To(Succeed())
		Expect(len(resources.Items)).To(Equal(1))

		resource := resources.Items[0]

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
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

		eScope, err := scope.NewEventBasedAddOnScope(scope.EventBasedAddOnScopeParams{
			Client:          c,
			Logger:          klogr.New(),
			EventBasedAddOn: &resource,
			ControllerName:  "eventBasedAddOn",
		})
		Expect(err).To(BeNil())

		currentCluster := &clusterv1.Cluster{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(c.Scheme(), currentCluster)).To(Succeed())

		f := controllers.GetHandlersForFeature(v1alpha1.FeatureEventBasedAddOn)
		clusterInfo, err := controllers.ProcessEventBasedAddOn(&reconciler, context.TODO(), eScope,
			controllers.GetKeyFromObject(c.Scheme(), currentCluster), f, klogr.New())
		Expect(err).To(BeNil())

		Expect(clusterInfo).ToNot(BeNil())
		Expect(clusterInfo.Status).To(Equal(libsveltosv1alpha1.SveltosStatusProvisioning))

		// Expect job to be queued
		Expect(dep.IsInProgress(clusterNamespace, clusterName, resource.Name, v1alpha1.FeatureEventBasedAddOn,
			clusterType, false)).To(BeTrue())
	})

	It("removeClusterInfoEntry removes cluster entry", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		resource := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: v1alpha1.EventBasedAddOnStatus{
				ClusterInfo: []libsveltosv1alpha1.ClusterInfo{
					*getClusterInfo(clusterNamespace, clusterName, clusterType),
					*getClusterInfo(clusterNamespace, randomString(), clusterType),
					*getClusterInfo(randomString(), clusterName, clusterType),
					*getClusterInfo(clusterNamespace, clusterName, libsveltosv1alpha1.ClusterTypeSveltos),
				},
			},
		}

		initObjects := []client.Object{
			resource,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		length := len(resource.Status.ClusterInfo)

		Expect(controllers.RemoveClusterInfoEntry(context.TODO(), c, clusterNamespace, clusterName,
			clusterType, resource, klogr.New())).To(Succeed())

		currentChc := &v1alpha1.EventBasedAddOn{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentChc)).To(Succeed())

		Expect(len(currentChc.Status.ClusterInfo)).To(Equal(length - 1))
	})

	It("isClusterEntryRemoved returns true when there is no entry for a Cluster in EventBasedAddOn status", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		// Following creates a ClusterSummary and an empty EventBasedAddOn
		c := prepareClient(clusterNamespace, clusterName, clusterType)

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
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

		// Verify eventBasedAddOn has been created
		chcs := &v1alpha1.EventBasedAddOnList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		chc := chcs.Items[0]

		currentCluster := &clusterv1.Cluster{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(c.Scheme(), currentCluster)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeTrue())

		chc.Status.ClusterInfo = []libsveltosv1alpha1.ClusterInfo{
			{
				Cluster: *controllers.GetKeyFromObject(c.Scheme(), currentCluster),
			},
		}
		Expect(c.Status().Update(context.TODO(), &chc)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeFalse())
	})

	It("eventBasedAddOnHash returns current EventAddBasedAddOn hash", func() {
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
				Namespace: randomString(),
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
						Namespace: configMap.Namespace,
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

		config := render.AsCode(e.Spec)
		config += render.AsCode(eventSource.Spec)
		config += render.AsCode(eventReport.Spec)
		config += render.AsCode(configMap.Data)
		config += render.AsCode(secret.Data)
		h := sha256.New()
		h.Write([]byte(config))
		expectedHash := h.Sum(nil)

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		hash, err := controllers.EventBasedAddOnHash(context.TODO(), c, e, getClusterRef(cluster), klogr.New())
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())
		Expect(reflect.DeepEqual(hash, expectedHash)).To(BeTrue())
	})

	It("removeStaleEventSources removes stale eventSources from managed cluster", func() {
		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				Kind:    randomString(),
				Group:   randomString(),
				Version: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		resource := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: eventSource.Name,
			},
			Status: v1alpha1.EventBasedAddOnStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterInfo: []libsveltosv1alpha1.ClusterInfo{},
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		// Add EventBasedAddOn as owner of EventSource. This indicates previously EventSource was
		// deployed because of this EventBasedAddOn instance
		deployer.AddOwnerReference(eventSource, resource)
		Expect(testEnv.Client.Update(context.TODO(), eventSource)).To(Succeed())

		// Wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, eventSource)
			if err != nil {
				return false
			}
			return util.IsOwnedByObject(eventSource, resource)
		}, timeout, pollingInterval).Should(BeTrue())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// Test created EventSource instance and added EventBasedAddOn as ownerReference, indicating EventSource was deployed
		// because of the EventBasedAddOn instance.
		// Test has EventBasedAddOn instance reference this EventSource instance.
		// RemoveStaleEventSources will not remove the EventSource test created.
		Expect(controllers.RemoveStaleEventSources(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			resource, klogr.New())).To(Succeed())

		Consistently(func() bool {
			currentEventSource := &libsveltosv1alpha1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		currentResource := &v1alpha1.EventBasedAddOn{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)).To(Succeed())
		currentResource.Spec.EventSourceName = randomString()
		Expect(testEnv.Update(context.TODO(), currentResource)).To(Succeed())

		// Wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)
			return err == nil && currentResource.Spec.EventSourceName != eventSource.Name
		}, timeout, pollingInterval).Should(BeTrue())

		// Test created EventSource instance and added EventBasedAddOn as ownerReference, indicating EventSource was deployed
		// because of the EventBasedAddOn instance.
		// Test has EventBasedAddOn instance reference a different EventSource.
		// RemoveStaleEventSources will remove the EventSource test created.
		Expect(controllers.RemoveStaleEventSources(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			currentResource, klogr.New())).To(Succeed())

		Eventually(func() bool {
			currentEventSource := &libsveltosv1alpha1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
			if err != nil {
				return apierrors.IsNotFound(err)
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployEventSource deploys referenced EventSource in the managed cluster", func() {
		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				Kind:    randomString(),
				Group:   randomString(),
				Version: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		resource := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: eventSource.Name,
			},
			Status: v1alpha1.EventBasedAddOnStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterInfo: []libsveltosv1alpha1.ClusterInfo{},
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// DeployEventSource creates referenced EventSource in the managed cluster.
		// We are using testEnv as both management cluster (where this test has already created EventSource)
		// and managed cluster (where EventSource is supposed to be created).
		// Existence of EventSource does not verify DeployEventSource. But DeployEventSource is also supposed
		// to add EventBasedAddOn as OwnerReference of EventSource. So test verifies that.
		Expect(controllers.DeployEventSource(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			clusterType, resource, klogr.New())).To(Succeed())

		Eventually(func() bool {
			currentEventSource := &libsveltosv1alpha1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
			if err != nil {
				return false
			}
			if !util.IsOwnedByObject(currentEventSource, resource) {
				return false
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("processEventBasedAddOnForCluster deploys referenced EventSource and remove stale EventSources", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		// Create a Cluster (of type CAPI)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.GetScheme(), cluster)).To(Succeed())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		currentEventSourceName := randomString()
		staleEventSourceName := randomString()

		// Create an EventBasedAddOn referencing above EventSource
		resource := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: currentEventSourceName,
			},
			Status: v1alpha1.EventBasedAddOnStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(),
						Namespace: clusterNamespace,
						Name:      clusterName,
					},
				},
				ClusterInfo: []libsveltosv1alpha1.ClusterInfo{},
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		// Create a stale EventSource
		staleEventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: staleEventSourceName,
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				Kind:    randomString(),
				Group:   randomString(),
				Version: randomString(),
			},
		}
		Expect(testEnv.Create(context.TODO(), staleEventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, staleEventSource)).To(Succeed())

		// Add EventBasedAddOn as OwnerReference of the staleEventSource
		deployer.AddOwnerReference(staleEventSource, resource)
		Expect(testEnv.Update(context.TODO(), staleEventSource)).To(Succeed())

		eventSource := &libsveltosv1alpha1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: currentEventSourceName,
			},
			Spec: libsveltosv1alpha1.EventSourceSpec{
				Kind:    randomString(),
				Group:   randomString(),
				Version: randomString(),
			},
		}
		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		// Test created staleEventSource pretending it was created by EventBasedAddOn instance (set as OwnerReference)
		// Test created eventSource
		// EventBasedAddOn is now referencing eventSource, so ProcessEventBasedAddOnForCluster will:
		// - remove staleEventSource
		// - add EventBasedAddOn as OwnerReference for eventSource
		Expect(controllers.ProcessEventBasedAddOnForCluster(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			resource.Name, v1alpha1.FeatureEventBasedAddOn, clusterType, deployer.Options{}, klogr.New())).To(Succeed())

		currentEventSource := &libsveltosv1alpha1.EventSource{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: currentEventSourceName}, currentEventSource)
			if err != nil {
				return false
			}
			if !util.IsOwnedByObject(currentEventSource, resource) {
				return false
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: staleEventSourceName}, currentEventSource)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func getClusterInfo(clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) *libsveltosv1alpha1.ClusterInfo {
	var apiVersion, kind string
	if clusterType == libsveltosv1alpha1.ClusterTypeCapi {
		apiVersion = clusterv1.GroupVersion.String()
		kind = ClusterKind
	} else {
		apiVersion = libsveltosv1alpha1.GroupVersion.String()
		kind = libsveltosv1alpha1.SveltosClusterKind
	}

	return &libsveltosv1alpha1.ClusterInfo{
		Cluster: corev1.ObjectReference{
			Namespace:  clusterNamespace,
			Name:       clusterName,
			Kind:       kind,
			APIVersion: apiVersion,
		},
	}
}
