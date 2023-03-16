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
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/gdexlab/go-render/render"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	"github.com/projectsveltos/libsveltos/lib/utils"
	libsveltosutils "github.com/projectsveltos/libsveltos/lib/utils"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	ClusterKind         = "Cluster"
	separator           = "---"
	nginxDeploymentName = "nginx-deployment"
)

var (
	nginxDepl = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: nginx
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80`

	networkPolicy = `kind: NetworkPolicy
apiVersion: networking.k8s.io/v1
metadata:
  name: front-{{ .Resource.metadata.name }}
  namespace: {{ .Resource.metadata.namespace }}
spec:
  podSelector:
    matchLabels:
      {{ range $key, $value := .Resource.spec.selector }}
      {{ $key }}: {{ $value }}
      {{ end }}
  ingress:
    - from:
      - podSelector:
          matchLabels:
            app: wordpress
      ports:
        {{ range $port := .Resource.spec.ports }}
        - port: {{ $port.port }}
        {{ end }}`
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

	It("instantiateOneClusterProfilePerAllResource creates clusterProfile for all resource", func() {
		nginxName := nginxDeploymentName
		nginxNamespace := randomString()

		u, err := libsveltosutils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace)))
		Expect(err).To(BeNil())

		collectedResources := []unstructured.Unstructured{*u}
		result := ""
		for i := range collectedResources {
			r := &collectedResources[i]
			tmpJson, jsonErr := r.MarshalJSON()
			Expect(jsonErr).To(BeNil())

			result += string(tmpJson)
			result += separator
		}

		eventSourceName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		eventReport := &libsveltosv1alpha1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1alpha1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
			Spec: libsveltosv1alpha1.EventReportSpec{
				MatchingResources: []corev1.ObjectReference{
					{Kind: "Deployment", APIVersion: corev1.SchemeGroupVersion.String(),
						Namespace: nginxNamespace, Name: nginxName},
				},
				Resources:        []byte(result),
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      clusterType,
				EventSourceName:  eventSourceName,
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: eventSourceName,
				HelmCharts: []configv1alpha1.HelmChart{
					{
						RepositoryURL:    randomString(),
						ReleaseNamespace: "{{  (index .MatchingResources 0).Namespace }}",
						ReleaseName:      randomString(),
						ChartName:        randomString(),
						ChartVersion:     randomString(),
					},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		_, err = controllers.InstantiateOneClusterProfilePerAllResource(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			eventBasedAddOn, eventReport, klogr.New())
		Expect(err).To(BeNil())

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOn.Name, clusterType)

		listOptions := []client.ListOption{
			client.MatchingLabels(labels),
		}

		clusterProfiles := &configv1alpha1.ClusterProfileList{}
		Expect(c.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(Equal(1))
		Expect(clusterProfiles.Items[0].Spec.ClusterRefs).ToNot(BeNil())
		Expect(len(clusterProfiles.Items[0].Spec.ClusterRefs)).To(Equal(1))
		Expect(clusterProfiles.Items[0].Spec.ClusterRefs[0].Namespace).To(Equal(clusterNamespace))
		Expect(clusterProfiles.Items[0].Spec.ClusterRefs[0].Name).To(Equal(clusterName))
		Expect(clusterProfiles.Items[0].Spec.ClusterRefs[0].Kind).To(Equal("Cluster"))
		Expect(len(clusterProfiles.Items[0].Spec.HelmCharts)).To(Equal(1))
	})

	It("instantiateOneClusterProfilePerResource creates one ClusterProfile for resource", func() {
		nginxName := nginxDeploymentName
		nginxNamespace1 := randomString()
		nginxNamespace2 := randomString()

		u1, err := libsveltosutils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace1)))
		Expect(err).To(BeNil())

		var u2 *unstructured.Unstructured
		u2, err = libsveltosutils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace2)))
		Expect(err).To(BeNil())

		collectedResources := []unstructured.Unstructured{*u1, *u2}
		result := ""
		for i := range collectedResources {
			r := &collectedResources[i]
			tmpJson, jsonErr := r.MarshalJSON()
			Expect(jsonErr).To(BeNil())

			result += string(tmpJson)
			result += separator
		}

		eventSourceName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi

		eventReport := &libsveltosv1alpha1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1alpha1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
			Spec: libsveltosv1alpha1.EventReportSpec{
				MatchingResources: []corev1.ObjectReference{
					{Kind: "Deployment", APIVersion: corev1.SchemeGroupVersion.String(),
						Namespace: nginxNamespace1, Name: nginxName},
					{Kind: "Deployment", APIVersion: corev1.SchemeGroupVersion.String(),
						Namespace: nginxNamespace2, Name: nginxName},
				},
				Resources:        []byte(result),
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      clusterType,
				EventSourceName:  eventSourceName,
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: eventSourceName,
				HelmCharts: []configv1alpha1.HelmChart{
					{
						RepositoryName:   randomString(),
						RepositoryURL:    randomString(),
						ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
						ReleaseName:      randomString(),
						ChartName:        randomString(),
						ChartVersion:     randomString(),
						HelmChartAction:  configv1alpha1.HelmChartActionInstall,
					},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		_, err = controllers.InstantiateOneClusterProfilePerResource(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			eventBasedAddOn, eventReport, klogr.New())
		Expect(err).To(BeNil())

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOn.Name, clusterType)

		listOptions := []client.ListOption{
			client.MatchingLabels(labels),
		}

		clusterProfiles := &configv1alpha1.ClusterProfileList{}
		Expect(c.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(Equal(2))

		for i := 0; i < 2; i++ {
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs).ToNot(BeNil())
			Expect(len(clusterProfiles.Items[i].Spec.ClusterRefs)).To(Equal(1))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Namespace).To(Equal(clusterNamespace))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Name).To(Equal(clusterName))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Kind).To(Equal("Cluster"))
			Expect(len(clusterProfiles.Items[i].Spec.HelmCharts)).To(Equal(1))
		}
	})

	It("removeClusterProfiles removes stales clusterProfiles", func() {
		eventBasedAddOnName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeSveltos

		clusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOnName, clusterType),
			},
		}

		toBeRemovedClusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOnName, clusterType),
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: eventBasedAddOnName},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: randomString(),
				HelmCharts: []configv1alpha1.HelmChart{
					{
						RepositoryName:   randomString(),
						RepositoryURL:    randomString(),
						ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
						ReleaseName:      randomString(),
						ChartName:        randomString(),
						ChartVersion:     randomString(),
						HelmChartAction:  configv1alpha1.HelmChartActionInstall,
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterProfile, toBeRemovedClusterProfile, eventBasedAddOn,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.RemoveClusterProfiles(context.TODO(), c, clusterNamespace, clusterName, clusterType, eventBasedAddOn,
			[]*configv1alpha1.ClusterProfile{clusterProfile}, klogr.New())).To(Succeed())

		clusterProfiles := &configv1alpha1.ClusterProfileList{}
		Expect(c.List(context.TODO(), clusterProfiles)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(Equal(1))
		Expect(clusterProfiles.Items[0].Name).To(Equal(clusterProfile.Name))
	})

	It("getClusterProfileName returns the correct name for a clusterProfile", func() {
		eventBasedAddOnName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeSveltos

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOnName, clusterType)

		name, create, err := controllers.GetClusterProfileName(context.TODO(), c, labels)
		Expect(err).To(BeNil())
		Expect(create).To(BeTrue())
		Expect(name).ToNot(BeEmpty())

		clusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventBasedAddOnName, clusterType),
			},
		}

		Expect(c.Create(context.TODO(), clusterProfile)).To(Succeed())

		var currentName string
		currentName, create, err = controllers.GetClusterProfileName(context.TODO(), c, labels)
		Expect(err).To(BeNil())
		Expect(create).To(BeFalse())
		Expect(currentName).To(Equal(name))
	})

	It("instantiateReferencedPolicies instantiates referenced configMap/secret", func() {
		eventBasedAddOnName := randomString()

		namespace := randomString()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				"policy": randomString(),
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
			Data: map[string][]byte{
				"policy": []byte(randomString()),
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: eventBasedAddOnName},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: randomString(),
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
			},
		}

		clusterRef := &corev1.ObjectReference{
			Namespace:  namespace,
			Name:       randomString(),
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		}

		initObjects := []client.Object{
			secret, configMap, eventBasedAddOn,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		object := &controllers.CurrentObject{
			MatchingResource: corev1.ObjectReference{
				Kind:       "Service",
				APIVersion: "",
				Namespace:  randomString(),
				Name:       randomString(),
			},
		}

		labels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace, clusterRef.Name, eventBasedAddOn.Name,
			libsveltosv1alpha1.ClusterTypeCapi)

		set, err := controllers.InstantiateReferencedPolicies(context.TODO(), c, randomString(), eventBasedAddOn,
			clusterRef, object, labels, klogr.New())
		Expect(err).To(BeNil())
		Expect(set).ToNot(BeNil())
		Expect(set.Len()).To(Equal(2))

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
		}

		configMaps := &corev1.ConfigMapList{}
		Expect(c.List(context.TODO(), configMaps, listOptions...)).To(Succeed())
		Expect(len(configMaps.Items)).To(Equal(1))
		Expect(reflect.DeepEqual(configMaps.Items[0].Data, configMap.Data)).To(BeTrue())

		secrets := &corev1.SecretList{}
		Expect(c.List(context.TODO(), secrets, listOptions...)).To(Succeed())
		Expect(len(secrets.Items)).To(Equal(1))
		Expect(reflect.DeepEqual(secrets.Items[0].Data, secret.Data)).To(BeTrue())
	})

	It("instantiateReferencedPolicies, one for all resources, instantiates referenced configMap", func() {
		eventBasedAddOnName := randomString()

		namespace := randomString()

		ingress := `apiVersion: networking.k8s.io/v1
    kind: Ingress
    metadata:
      name: ingress
      namespace: %s
      annotations:
        nginx.ingress.kubernetes.io/rewrite-target: /
    spec:
      ingressClassName: http-ingress
      rules:
        - http:
            paths:
            {{ range .Resources }}
            - path: /{{ .metadata.name }}
              pathType: Prefix
              backend:
                service:
                  name: {{ .metadata.name }}
                  port:
                    {{ range .spec.ports }}
                    {{if or (eq .port 443 ) (eq .port 8443 ) }}
                    number: {{ .port }}
                    {{ end }}
                    {{ end }}
            {{ end }}`

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				"policy": fmt.Sprintf(ingress, namespace),
			},
		}

		eventBasedAddOn := &v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{Name: eventBasedAddOnName},
			Spec: v1alpha1.EventBasedAddOnSpec{
				EventSourceName: randomString(),
				OneForEvent:     false,
				PolicyRefs: []libsveltosv1alpha1.PolicyRef{
					{
						Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
						Name:      configMap.Name,
						Namespace: configMap.Namespace,
					},
				},
			},
		}

		clusterRef := &corev1.ObjectReference{
			Namespace:  namespace,
			Name:       randomString(),
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		}

		initObjects := []client.Object{
			configMap, eventBasedAddOn,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		httpsService1 := `apiVersion: v1
kind: Service
metadata:
  name: my-service-1
spec:
  selector:
    app.kubernetes.io/name: MyApp
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 9376
    - name: https
      protocol: TCP
      port: 443
      targetPort: 9377`

		httpsService2 := `apiVersion: v1
kind: Service
metadata:
  name: my-service-2
spec:
  selector:
    app.kubernetes.io/name: MyApp
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 9378
    - name: https
      protocol: TCP
      port: 8443
      targetPort: 9379`

		u1, err := libsveltosutils.GetUnstructured([]byte(httpsService1))
		Expect(err).To(BeNil())

		var u2 *unstructured.Unstructured
		u2, err = libsveltosutils.GetUnstructured([]byte(httpsService2))
		Expect(err).To(BeNil())

		objects := &controllers.CurrentObjects{
			Resources: []map[string]interface{}{
				u1.UnstructuredContent(),
				u2.UnstructuredContent(),
			},
		}

		labels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace, clusterRef.Name, eventBasedAddOn.Name,
			libsveltosv1alpha1.ClusterTypeCapi)

		set, err := controllers.InstantiateReferencedPolicies(context.TODO(), c, randomString(), eventBasedAddOn,
			clusterRef, objects, labels, klogr.New())
		Expect(err).To(BeNil())
		Expect(set).ToNot(BeNil())
		Expect(set.Len()).To(Equal(1))

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
		}

		configMaps := &corev1.ConfigMapList{}
		Expect(c.List(context.TODO(), configMaps, listOptions...)).To(Succeed())
		Expect(len(configMaps.Items)).To(Equal(1))
		Expect(configMaps.Items[0].Data).ToNot(BeEmpty())
		Expect(configMaps.Items[0].Data["policy"]).To(ContainSubstring("443"))
		Expect(configMaps.Items[0].Data["policy"]).To(ContainSubstring("8443"))
	})

	It("removeConfigMaps removes stale ConfigMaps", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		eventBasedAddOnName := randomString()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventBasedAddOnName, clusterType),
			},
			Data: map[string]string{
				randomString(): randomString(),
			},
		}

		toBeRemovedConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventBasedAddOnName, clusterType),
			},
			Data: map[string]string{
				randomString(): randomString(),
			},
		}

		initObjects := []client.Object{
			configMap, toBeRemovedConfigMap,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		eventBasedAddOn := v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventBasedAddOnName,
			},
		}
		policyRef := libsveltosv1alpha1.PolicyRef{
			Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
		}
		policyRefs := map[libsveltosv1alpha1.PolicyRef]bool{policyRef: true}

		Expect(controllers.RemoveConfigMaps(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			&eventBasedAddOn, policyRefs, klogr.New())).To(Succeed())

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
		}
		configMapList := &corev1.ConfigMapList{}
		Expect(c.List(context.TODO(), configMapList, listOptions...)).To(Succeed())
		Expect(len(configMapList.Items)).To(Equal(1))
		Expect(configMapList.Items[0].Name).To(Equal(configMap.Name))
	})

	It("removeSecrets removes stale Secrets", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		eventBasedAddOnName := randomString()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventBasedAddOnName, clusterType),
			},
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
			},
		}

		toBeRemovedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventBasedAddOnName, clusterType),
			},
			Type: libsveltosv1alpha1.ClusterProfileSecretType,
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
			},
		}

		initObjects := []client.Object{
			secret, toBeRemovedSecret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		eventBasedAddOn := v1alpha1.EventBasedAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventBasedAddOnName,
			},
		}
		policyRef := libsveltosv1alpha1.PolicyRef{
			Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
			Namespace: secret.Namespace,
			Name:      secret.Name,
		}
		policyRefs := map[libsveltosv1alpha1.PolicyRef]bool{policyRef: true}

		Expect(controllers.RemoveSecrets(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			&eventBasedAddOn, policyRefs, klogr.New())).To(Succeed())

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
		}
		secretList := &corev1.SecretList{}
		Expect(c.List(context.TODO(), secretList, listOptions...)).To(Succeed())
		Expect(len(secretList.Items)).To(Equal(1))
		Expect(secretList.Items[0].Name).To(Equal(secret.Name))
	})

	It("instantiateDataSection properly instantiate Data section", func() {
		content := map[string]string{
			"calico": networkPolicy,
		}

		service := `apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: front-end
spec:
  selector:
    app.kubernetes.io/name: myApp
  ports:
    - protocol: TCP
      port: 80
      targetPort: 9376`

		u, err := libsveltosutils.GetUnstructured([]byte(service))
		Expect(err).To(BeNil())
		Expect(u).ToNot(BeNil())

		object := controllers.CurrentObject{
			Resource: u.UnstructuredContent(),
		}

		var instantiatedContent map[string]string
		instantiatedContent, err = controllers.InstantiateDataSection(randomString(), content, object, klogr.New())
		Expect(err).To(BeNil())
		Expect(instantiatedContent).ToNot(BeEmpty())

		configMap := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Data: instantiatedContent,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		Expect(c.Create(context.TODO(), &configMap)).To(Succeed())

		for k := range configMap.Data {
			elements := strings.Split(configMap.Data[k], separator)
			for i := range elements {
				if elements[i] == "" {
					continue
				}
				policy, err := utils.GetUnstructured([]byte(elements[i]))
				Expect(err).To(BeNil())
				Expect(policy).ToNot(BeNil())

				networkPolicy := &networkingv1.NetworkPolicy{}
				err = runtime.DefaultUnstructuredConverter.
					FromUnstructured(policy.UnstructuredContent(), &networkPolicy)
				Expect(err).To(BeNil())

				Expect(len(networkPolicy.Spec.Ingress)).ToNot(BeZero())
				Expect(len(networkPolicy.Spec.Ingress[0].Ports)).ToNot(BeZero())
			}
		}
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
