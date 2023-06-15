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
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	viewClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
	kind: ClusterRole
	metadata:
	  name: %s
	rules:
	- apiGroups: [""] # "" indicates the core API group
	  resources: ["pods"]
	  verbs: ["get", "watch", "list"]`
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// waitForObject waits for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func waitForObject(ctx context.Context, c client.Client, obj client.Object) error {
	// Makes sure the cache is updated with the new object
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := wait.ExponentialBackoff(
		cacheSyncBackoff,
		func() (done bool, err error) {
			if err := c.Get(ctx, key, objCopy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}); err != nil {
		return errors.Wrapf(err, "object %s, %s is not being added to the testenv client cache", obj.GetObjectKind().GroupVersionKind().String(), key)
	}
	return nil
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("missing apiVersion or kind and cannot assign it; %w", err)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}

	return nil
}

func getEventSourceInstance(name string) *libsveltosv1alpha1.EventSource {
	return &libsveltosv1alpha1.EventSource{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: libsveltosv1alpha1.EventSourceSpec{
			Group:   randomString(),
			Version: randomString(),
			Kind:    randomString(),
			Script:  randomString(),
		},
	}
}

func getEventReport(eventSourceName, clusterNamespace, clusterName string) *libsveltosv1alpha1.EventReport {
	return &libsveltosv1alpha1.EventReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eventSourceName,
			Namespace: clusterNamespace,
			Labels: map[string]string{
				libsveltosv1alpha1.EventSourceLabelName: eventSourceName,
			},
		},
		Spec: libsveltosv1alpha1.EventReportSpec{
			ClusterNamespace: clusterNamespace,
			ClusterName:      clusterName,
			EventSourceName:  eventSourceName,
			ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
		},
	}
}

func getEventBasedAddOnReconciler(c client.Client) *controllers.EventBasedAddOnReconciler {
	return &controllers.EventBasedAddOnReconciler{
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
}

func prepareCluster() *clusterv1.Cluster {
	namespace := randomString()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         cluster.Name,
				clusterv1.MachineControlPlaneLabel: "ok",
			},
		},
	}

	Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
	Expect(testEnv.Create(context.TODO(), machine)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster.Status = clusterv1.ClusterStatus{
		InfrastructureReady: true,
		ControlPlaneReady:   true,
	}
	Expect(testEnv.Status().Update(context.TODO(), cluster)).To(Succeed())

	machine.Status = clusterv1.MachineStatus{
		Phase: string(clusterv1.MachinePhaseRunning),
	}
	Expect(testEnv.Status().Update(context.TODO(), machine)).To(Succeed())

	// Create a secret with cluster kubeconfig

	By("Create the secret with cluster kubeconfig")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name + "-kubeconfig",
		},
		Data: map[string][]byte{
			"data": testEnv.Kubeconfig,
		},
	}
	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

	Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

	return cluster
}

func getClusterRef(cluster client.Object) *corev1.ObjectReference {
	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	return &corev1.ObjectReference{
		Namespace:  cluster.GetNamespace(),
		Name:       cluster.GetName(),
		APIVersion: apiVersion,
		Kind:       kind,
	}
}

// prepareClient creates a client with a ClusterSummary, CAPI Cluster and a EventBasedAddOn matching such cluster.
// ClusterSummary has provisioned all add-ons
// Cluster API cluster
func prepareClient(clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) client.Client {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName,
		},
	}

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      randomString(),
			Labels: map[string]string{
				configv1alpha1.ClusterTypeLabel: string(clusterType),
				configv1alpha1.ClusterNameLabel: clusterName,
			},
		},
		Status: configv1alpha1.ClusterSummaryStatus{
			FeatureSummaries: []configv1alpha1.FeatureSummary{
				{
					FeatureID: configv1alpha1.FeatureHelm,
					Status:    configv1alpha1.FeatureStatusProvisioned,
				},
				{
					FeatureID: configv1alpha1.FeatureResources,
					Status:    configv1alpha1.FeatureStatusProvisioned,
				},
			},
		},
	}

	resource := &v1alpha1.EventBasedAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: v1alpha1.EventBasedAddOnSpec{
			EventSourceName: randomString(),
		},
		Status: v1alpha1.EventBasedAddOnStatus{
			MatchingClusterRefs: []corev1.ObjectReference{
				{
					Kind: "Cluster", APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
				},
			},
			ClusterInfo: []libsveltosv1alpha1.ClusterInfo{},
		},
	}

	initObjects := []client.Object{
		clusterSummary,
		resource,
		cluster,
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
	return c
}

// createSecretWithKubeconfig creates a secret containing kubeconfig to access CAPI cluster.
// Uses testEnv.
func createSecretWithKubeconfig(clusterNamespace, clusterName string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName + "-kubeconfig",
		},
		Data: map[string][]byte{
			"data": testEnv.Kubeconfig,
		},
	}

	Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
}

// createConfigMapWithPolicy creates a configMap with passed in policies.
func createConfigMapWithPolicy(namespace, configMapName string, policyStrs ...string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      configMapName,
		},
		Data: map[string]string{},
	}
	for i := range policyStrs {
		key := fmt.Sprintf("policy%d.yaml", i)
		if utf8.Valid([]byte(policyStrs[i])) {
			cm.Data[key] = policyStrs[i]
		} else {
			cm.BinaryData[key] = []byte(policyStrs[i])
		}
	}

	return cm
}
