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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	sveltosKubeconfigPostfix = "-kubeconfig"
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
	return "prefix-" + util.RandomString(length)
}

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(s); err != nil {
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
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1b2.AddToScheme(s); err != nil {
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

func getEventSourceInstance(name string) *libsveltosv1beta1.EventSource {
	return &libsveltosv1beta1.EventSource{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: libsveltosv1beta1.EventSourceSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Kind:     randomString(),
					Group:    randomString(),
					Version:  randomString(),
					Evaluate: randomString(),
				},
			},
		},
	}
}

func getEventReport(eventSourceName, clusterNamespace, clusterName string) *libsveltosv1beta1.EventReport {
	return &libsveltosv1beta1.EventReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eventSourceName,
			Namespace: clusterNamespace,
			Labels: map[string]string{
				libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
			},
		},
		Spec: libsveltosv1beta1.EventReportSpec{
			ClusterNamespace: clusterNamespace,
			ClusterName:      clusterName,
			EventSourceName:  eventSourceName,
			ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
		},
	}
}

func getEventTriggerReconciler(c client.Client) *controllers.EventTriggerReconciler {
	return &controllers.EventTriggerReconciler{
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
}

func prepareCluster(version string) *clusterv1.Cluster {
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
			Name:      cluster.Name + sveltosKubeconfigPostfix,
		},
		Data: map[string][]byte{
			"value": testEnv.Kubeconfig,
		},
	}
	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

	By("Create the ConfigMap with sveltos-agent version")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: controllers.ReportNamespace,
			Name:      "sveltos-agent-version",
		},
		Data: map[string]string{
			"version": version,
		},
	}
	Expect(testEnv.Client.Create(context.TODO(), cm)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())

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

// prepareClient creates a client with a ClusterSummary, CAPI Cluster and a EventTrigger matching such cluster.
// ClusterSummary has provisioned all add-ons
// Cluster API cluster
func prepareClient(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) client.Client {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName,
		},
		Status: clusterv1.ClusterStatus{
			ControlPlaneReady: true,
			Conditions: []clusterv1.Condition{
				{Type: clusterv1.ControlPlaneInitializedCondition, Status: corev1.ConditionTrue},
			},
		},
	}

	clusterSummary := &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      randomString(),
			Labels: map[string]string{
				configv1beta1.ClusterTypeLabel: string(clusterType),
				configv1beta1.ClusterNameLabel: clusterName,
			},
		},
		Status: configv1beta1.ClusterSummaryStatus{
			FeatureSummaries: []configv1beta1.FeatureSummary{
				{
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
				{
					FeatureID: libsveltosv1beta1.FeatureResources,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
			},
		},
	}

	resource := &v1beta1.EventTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name: randomString(),
		},
		Spec: v1beta1.EventTriggerSpec{
			EventSourceName: randomString(),
		},
		Status: v1beta1.EventTriggerStatus{
			MatchingClusterRefs: []corev1.ObjectReference{
				{
					Kind: "Cluster", APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
				},
			},
			ClusterInfo: []libsveltosv1beta1.ClusterInfo{},
		},
	}

	initObjects := []client.Object{
		clusterSummary,
		resource,
		cluster,
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
		WithObjects(initObjects...).Build()
	return c
}

// createSecretWithKubeconfig creates a secret containing kubeconfig to access CAPI cluster.
// Uses testEnv.
func createSecretWithKubeconfig(clusterNamespace, clusterName string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterNamespace,
			Name:      clusterName + sveltosKubeconfigPostfix,
		},
		Data: map[string][]byte{
			"value": testEnv.Kubeconfig,
		},
	}

	Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
}
