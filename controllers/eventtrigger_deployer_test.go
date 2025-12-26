/*
Copyright 2023-24. projectsveltos.io. All rights reserved.

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
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/cloudevents/sdk-go/v2/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
	"github.com/projectsveltos/event-manager/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	ClusterKind         = "Cluster"
	separator           = "---\n"
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

var _ = Describe("EventTrigger deployer", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(5)))
	})

	It("processEventTrigger deploy EventSource", func() {
		clusterNamespace := randomString()
		clusterName := randomString()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, ns)).To(Succeed())

		initialized := true
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, cluster)).To(Succeed())

		currentCluster := &clusterv1.Cluster{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterNamespace, Name: clusterName},
			currentCluster)).To(Succeed())
		currentCluster.Status = clusterv1.ClusterStatus{
			Initialization: clusterv1.ClusterInitializationStatus{
				ControlPlaneInitialized: &initialized,
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentCluster)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterNamespace, Name: clusterName},
				currentCluster)
			if err != nil {
				return false
			}
			return currentCluster.Status.Initialization.ControlPlaneInitialized != nil &&
				*currentCluster.Status.Initialization.ControlPlaneInitialized == true
		}, timeout, pollingInterval).Should(BeTrue())

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName + "-kubeconfig",
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		eventTrigger := &v1beta1.EventTrigger{
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
		Expect(testEnv.Create(context.TODO(), eventTrigger)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, eventTrigger)).To(Succeed())

		currentEventTrigger := &v1beta1.EventTrigger{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Name: eventTrigger.Name},
			currentEventTrigger)).To(Succeed())

		clusterRef := corev1.ObjectReference{
			Kind:       clusterv1.ClusterKind,
			APIVersion: clusterv1.GroupVersion.String(),
			Namespace:  clusterNamespace,
			Name:       clusterName,
		}
		currentEventTrigger.Status.MatchingClusterRefs = []corev1.ObjectReference{
			clusterRef,
		}
		currentEventTrigger.Status.ClusterInfo = []libsveltosv1beta1.ClusterInfo{
			{
				Cluster: clusterRef,
				Hash:    []byte(randomString()),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentEventTrigger)).To(Succeed())

		Eventually(func() bool {
			currentChc := &v1beta1.EventTrigger{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name}, currentChc)
			if err != nil {
				return false
			}
			return len(currentChc.Status.ClusterInfo) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		reconciler := controllers.EventTriggerReconciler{
			Client:           testEnv,
			Scheme:           scheme,
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		eScope, err := scope.NewEventTriggerScope(scope.EventTriggerScopeParams{
			Client:         testEnv,
			Logger:         logger,
			EventTrigger:   currentEventTrigger,
			ControllerName: "eventTrigger",
		})
		Expect(err).To(BeNil())

		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, currentCluster)).To(Succeed())

		clusterInfo, err := controllers.ProcessEventTrigger(&reconciler, context.TODO(), eScope,
			controllers.GetKeyFromObject(scheme, currentCluster), logger)
		Expect(err).To(BeNil())
		Expect(clusterInfo).ToNot(BeNil())
		Expect(clusterInfo.FailureMessage).To(BeNil())
		Expect(clusterInfo.Status).To(Equal(libsveltosv1beta1.SveltosStatusProvisioned))
	})

	It("removeClusterInfoEntry removes cluster entry", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		resource := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())

		resource.Status = v1beta1.EventTriggerStatus{
			ClusterInfo: []libsveltosv1beta1.ClusterInfo{
				*getClusterInfo(clusterNamespace, clusterName, clusterType),
				*getClusterInfo(clusterNamespace, randomString(), clusterType),
				*getClusterInfo(randomString(), clusterName, clusterType),
				*getClusterInfo(clusterNamespace, clusterName, libsveltosv1beta1.ClusterTypeSveltos),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), resource)).To(Succeed())
		Eventually(func() bool {
			currentChc := &v1beta1.EventTrigger{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentChc)
			if err != nil {
				return false
			}
			return len(currentChc.Status.ClusterInfo) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		length := len(resource.Status.ClusterInfo)

		Expect(controllers.RemoveClusterInfoEntry(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			clusterType, resource, logger)).To(Succeed())

		Eventually(func() bool {
			currentChc := &v1beta1.EventTrigger{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentChc)
			if err != nil {
				return false
			}
			return len(currentChc.Status.ClusterInfo) == length-1
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("isClusterEntryRemoved returns true when there is no entry for a Cluster in EventTrigger status", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		// Following creates a ClusterSummary and an empty EventTrigger
		c := prepareClient(clusterNamespace, clusterName, clusterType)

		reconciler := controllers.EventTriggerReconciler{
			Client:           c,
			Scheme:           c.Scheme(),
			Mux:              sync.Mutex{},
			ClusterMap:       make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToClusterMap:     make(map[types.NamespacedName]*libsveltosset.Set),
			EventTriggers:    make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			EventSourceMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ToEventSourceMap: make(map[types.NamespacedName]*libsveltosset.Set),
		}

		// Verify eventTrigger has been created
		chcs := &v1beta1.EventTriggerList{}
		Expect(c.List(context.TODO(), chcs)).To(Succeed())
		Expect(len(chcs.Items)).To(Equal(1))

		chc := chcs.Items[0]

		currentCluster := &clusterv1.Cluster{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentCluster)).To(Succeed())
		Expect(addTypeInformationToObject(c.Scheme(), currentCluster)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeTrue())

		chc.Status.ClusterInfo = []libsveltosv1beta1.ClusterInfo{
			{
				Cluster: *controllers.GetKeyFromObject(c.Scheme(), currentCluster),
			},
		}
		Expect(c.Status().Update(context.TODO(), &chc)).To(Succeed())

		Expect(controllers.IsClusterEntryRemoved(&reconciler, &chc, controllers.GetKeyFromObject(c.Scheme(), currentCluster))).To(BeFalse())
	})

	It("eventTriggerHash returns current EventAddBasedAddOn hash", func() {
		clusterNamespace := randomString()

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

		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterType := libsveltosv1beta1.ClusterTypeCapi
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventSource.Name,
				Namespace: cluster.Namespace,
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSource.Name, cluster.Name, &clusterType),
			},
		}

		e := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:      configMap.Name,
						Namespace: configMap.Namespace,
					},
				},
				EventSourceName: eventSource.Name,
			},
		}

		initObjects := []client.Object{
			cluster,
			e,
			configMap,
			eventSource,
			eventReport,
		}

		config := render.AsCode(e.Spec)
		config += render.AsCode(e.Labels)
		config += render.AsCode(eventSource.Spec)
		config += render.AsCode(eventReport.Spec)
		config += render.AsCode(configMap.Data)
		h := sha256.New()
		h.Write([]byte(config))
		expectedHash := h.Sum(nil)

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
		Expect(addTypeInformationToObject(c.Scheme(), cluster)).To(Succeed())

		hash, err := controllers.EventTriggerHash(context.TODO(), c, e, getClusterRef(cluster), logger)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())
		Expect(reflect.DeepEqual(hash, expectedHash)).To(BeTrue())
	})

	It("getResources ", func() {
		//nolint: lll // this is a test
		eventReportYAML := `apiVersion: lib.projectsveltos.io/v1beta1
kind: EventReport
metadata:
  creationTimestamp: "2025-08-08T08:49:40Z"
  generation: 2
  labels:
    eventreport.projectsveltos.io/cluster-name: mgmt
    eventreport.projectsveltos.io/cluster-type: sveltos
    projectsveltos.io/eventsource-name: trust-bundle-propagation
  name: sveltos--trust-bundle-propagation--mgmt
  namespace: mgmt
  resourceVersion: "87772893"
  uid: edd11b32-e77e-459f-aca8-b50cb2af60bd
spec:
  clusterName: mgmt
  clusterNamespace: mgmt
  clusterType: Sveltos
  eventSourceName: trust-bundle-propagation
  matchingResources:
  - apiVersion: v1
    kind: ConfigMap
    name: linkerd-identity-trust-roots
    namespace: linkerd
  resources: eyJhcGlWZXJzaW9uIjoidjEiLCJkYXRhIjp7ImNhLWJ1bmRsZS5jcnQiOiItLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuVENDQVVPZ0F3SUJBZ0lSQUt2KzhzWmpsV2RrdUJ6bnFaTExocnN3Q2dZSUtvWkl6ajBFQXdJd0xqRXNcbk1Db0dBMVVFQXhNaloyeHZZbUZzTFRFdGNtOXZkQzVzYVc1clpYSmtMbU5zZFhOMFpYSXViRzlqWVd3d0hoY05cbk1qVXdPREE0TURRd05qSTBXaGNOTWpVeE1UQTJNRFF3TmpJMFdqQXVNU3d3S2dZRFZRUURFeU5uYkc5aVlXd3Rcbk1TMXliMjkwTG14cGJtdGxjbVF1WTJ4MWMzUmxjaTVzYjJOaGJEQlpNQk1HQnlxR1NNNDlBZ0VHQ0NxR1NNNDlcbkF3RUhBMElBQkJudkQ1Wm5DTExNcGlzY0pheFdZQXYvclhBZ0c2NU9kb3FOUmZsTVdwUWRNcDQyYmZpdlU4d1dcbkwzUFAwYmRMREdlWGNtaE1NNW9HSm5Ob3ZwLzZCdUdqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZcbkhSTUJBZjhFQlRBREFRSC9NQjBHQTFVZERnUVdCQlFoZUpJNWpIamtCaDkxOXFzUmJHdUNDdXJsRERBS0JnZ3Fcbmhrak9QUVFEQWdOSUFEQkZBaUJ4V3dxZDFkK2x2UnBxSytMdTRYSVdjUGQvVjMydlZTV2E4MGVQcHNVa0JnSWhcbkFPZ0xjeWN0WUtSMUFEWFA3UHF3dElIbzhlMU9NN0xGTXp0Q21Cd3hPMks0XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuRENDQVVLZ0F3SUJBZ0lRU3hwRGFkYzRYMWZEQ0licjZVdENyREFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TnkxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk16UmFGdzB5TlRFeE1EWXdOREEyTXpSYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMDNcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFblBSRXlkUjVyQlJDNGwyZzJBTEkrNWd3Qi9QenZzUndYS3pSc3Bsc1pBbkU3MjZMYlRjdUZ3TFFcbnVmUjZ0WHA2ZzhCcEtLWXhBY2plSlJURjVQSW5pYU5DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGRXVyRnoyU0ZDZlFWbm5KajJLM0JjdkNjdlhxTUFvR0NDcUdcblNNNDlCQU1DQTBnQU1FVUNJUUNVR1NjSHJTWmtQblR6UHBtSjdRaGcvQ1RkaEZmL0ZSQVdDTjZHdGc5VERnSWdcbkxXcTRFdGUySFVNQzZObG1WZkxEWmxUcGt1NUpZbnczN0t5Z1lzTUJzRXc9XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuVENDQVVPZ0F3SUJBZ0lSQU91Y0ZQZEVnRHdaMzBLT3N1dm9qdzh3Q2dZSUtvWkl6ajBFQXdJd0xqRXNcbk1Db0dBMVVFQXhNaloyeHZZbUZzTFRrdGNtOXZkQzVzYVc1clpYSmtMbU5zZFhOMFpYSXViRzlqWVd3d0hoY05cbk1qVXdPREE0TURRd05qTTNXaGNOTWpVeE1UQTJNRFF3TmpNM1dqQXVNU3d3S2dZRFZRUURFeU5uYkc5aVlXd3Rcbk9TMXliMjkwTG14cGJtdGxjbVF1WTJ4MWMzUmxjaTVzYjJOaGJEQlpNQk1HQnlxR1NNNDlBZ0VHQ0NxR1NNNDlcbkF3RUhBMElBQkt2dkFvbzkyK3J5NzdmYlpPNlpBdjJ4YmhIN0pkdGMyaE52R29TOGxwVjAxQUFqbkF5MWxRYjRcbjBVQk9heXZpQ2dTZEcwZzBTdnI5Qmh1QWE1TENwcjJqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZcbkhSTUJBZjhFQlRBREFRSC9NQjBHQTFVZERnUVdCQlJQTkp2emh5a1hwQ3RibFlnN2ZoRUhaaGlRd2pBS0JnZ3Fcbmhrak9QUVFEQWdOSUFEQkZBaUFrVlVTWXBmcVpjWFVQSGtQb3VRTnZhTzY5UklZU0xpZEhKcjA5ZHF6dDhnSWhcbkFLVkZZK3ZqWGdSa1ZlWXRKUGdRKzJoUnJXY3dNRUtTdWFwY3NKamJLTDFYXG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuRENDQVVLZ0F3SUJBZ0lRUTUzY2JxUVMwbDh5RVFyWGFQc0pLekFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TlMxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk16RmFGdzB5TlRFeE1EWXdOREEyTXpGYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMDFcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFU0I4Z1dwaGhhWHdaL2hTdVpPYlFaelRTYkk2ZjNGcTRjbmhXY05sVGR3MzdBR2g2Z2E3WStmd3JcbnY2U2VUaVhLdEY4UkIvMktUckVzOXkwNzErMEhKS05DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGSXkzOGJHNmZrU1ZXK1FCTldiSmFoclRsZEx4TUFvR0NDcUdcblNNNDlCQU1DQTBnQU1FVUNJRWNBVHZPL3dLem1BanVZUENJZVhlM0hrZS90Zk9uWG9sWUd6RjBrWmREaEFpRUFcbm9pd2hqckRGLy8ranlOaHB6RkpHTkJCZDNjbXluVFpSc3JCeHR6MFZHYTQ9XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJtekNDQVVLZ0F3SUJBZ0lRT0ZqdFc0L0NmMGEzcnU4MExtbHc1akFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TWkxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk1qZGFGdzB5TlRFeE1EWXdOREEyTWpkYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMHlcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFQ1Z0dlZXSHNqTEdWZGZiMkErZjVkbFg5NGhuNHdrOU81NHhZZzlCR0cxK1dlUklFRWNLOHBtU2VcbjJ1SGc4ZnBoQmxqNTFrNmRwVkdOSUt1MXRqRWZTcU5DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGQTdUSmRhN2xrc3NaSWNCRDhWd2t5eGhKMyszTUFvR0NDcUdcblNNNDlCQU1DQTBjQU1FUUNJRytFYVhWSHFKZGNkbnkvZjFyWVgwZVEvRlpzdmt3clJLdDJsVnNQNms0N0FpQndcblhDZ3RGaGRYYnluS1d5dTAwQisyKzRsOXpCWnpyb3dScXJPVWpaOUo2dz09XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuRENDQVVLZ0F3SUJBZ0lRRFNVV2ZYNkg1anhTSU8rWGpHeTZ0VEFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TUMxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk1qVmFGdzB5TlRFeE1EWXdOREEyTWpWYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMHdcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFb25iUlA5OWUwa3hkRy95MTFnRFNWZUlPTS9zMWpJOWhRRk5JNHhnUE9Pam9tV293SXd0blpuZnVcbk9kaDcyTFBXWkJPNmNZaFRxN01TRGN0ZFRiUEhJcU5DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGRlBDaUUvTUxLK0ltdW54ekd4WkdaeHhjSVdyTUFvR0NDcUdcblNNNDlCQU1DQTBnQU1FVUNJQmNUWkF0QzdxWWZ5QnA4NEtZRGNUQS9RL2RiQ1l3ZnlLc3IvSENVODhBaEFpRUFcbjJlTTlFQlJFSHpEWldhLzdvRGFzbU9KTlFTa1FZWVhZa1pvK3Bzcm9LK3c9XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuVENDQVVPZ0F3SUJBZ0lSQVBudkxTWGhSc2U4Q1RSbnFqNmM4b013Q2dZSUtvWkl6ajBFQXdJd0xqRXNcbk1Db0dBMVVFQXhNaloyeHZZbUZzTFRRdGNtOXZkQzVzYVc1clpYSmtMbU5zZFhOMFpYSXViRzlqWVd3d0hoY05cbk1qVXdPREE0TURRd05qSTJXaGNOTWpVeE1UQTJNRFF3TmpJMldqQXVNU3d3S2dZRFZRUURFeU5uYkc5aVlXd3Rcbk5DMXliMjkwTG14cGJtdGxjbVF1WTJ4MWMzUmxjaTVzYjJOaGJEQlpNQk1HQnlxR1NNNDlBZ0VHQ0NxR1NNNDlcbkF3RUhBMElBQkNOM1JCTksvQUMrcENkbkpoOXdZOSsrOTE3V00wSEpROWpscjRLWWpDMEpFU3h0eGJINHN6cVFcbk1SOU9NbXkrQ1haYUNDSHltYkUxL1FpbzRpOXV2RXVqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZcbkhSTUJBZjhFQlRBREFRSC9NQjBHQTFVZERnUVdCQlNFZ0RvejRtNkR1dlJ1eDE2cUJQdEs2TENvU3pBS0JnZ3Fcbmhrak9QUVFEQWdOSUFEQkZBaUVBNG9tRjIwbHR1YkNpZ0I3WXcxdVNSS2h2MklQMjZFWFUvSEtvZjhjY1N5NENcbklHVUd4ZjZaZzV2QjlEaG9Ma0piY2VtUElLSnRVLzNWdzdOV0tNdnJ6WjlLXG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuRENDQVVLZ0F3SUJBZ0lRT2FaRnc4eVRnWTBqbFlkbkFVK3pwekFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TmkxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk16RmFGdzB5TlRFeE1EWXdOREEyTXpGYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMDJcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFa3VCMFpEblBuUUxyaGVnZ1gwSmpaeHg5SllXcHp2aUx6VVVwWkhtU1VvU29jbC9rbnhzM2JxNWVcbjZnWVBFUk50bXUxZFhxOVZjUlg0WkpzT2k2R0dVS05DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGRXYvRFlxVE1wMExGelI0MDNpTDZEdzZIK1RITUFvR0NDcUdcblNNNDlCQU1DQTBnQU1FVUNJUUNhTmNwK1VDYnNMdmpLbFIwM0VObFROamNZY3JoL0U5eStEeitnVEYzdldnSWdcblN0ZDhlazg4MXNVMGhGRUlTWW04bG5mWGpXM2Zna0tMVXo5UkNNWW5KNDQ9XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuVENDQVVPZ0F3SUJBZ0lSQUtvWitMUEhIejhqS2t1bTJTc1BaL013Q2dZSUtvWkl6ajBFQXdJd0xqRXNcbk1Db0dBMVVFQXhNaloyeHZZbUZzTFRndGNtOXZkQzVzYVc1clpYSmtMbU5zZFhOMFpYSXViRzlqWVd3d0hoY05cbk1qVXdPREE0TURRd05qTTFXaGNOTWpVeE1UQTJNRFF3TmpNMVdqQXVNU3d3S2dZRFZRUURFeU5uYkc5aVlXd3Rcbk9DMXliMjkwTG14cGJtdGxjbVF1WTJ4MWMzUmxjaTVzYjJOaGJEQlpNQk1HQnlxR1NNNDlBZ0VHQ0NxR1NNNDlcbkF3RUhBMElBQkVjbkxtY2JsYmI5TUVWZDN3UjdnOVRYVjlGUStlMzI3RzBMTEZGWjNHamtHcWNxMk9TUVVkdVBcbm5DQTRnc1NlMGhvRXRGOUFJSnF5eXJxT2N1Y2lNbHFqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZcbkhSTUJBZjhFQlRBREFRSC9NQjBHQTFVZERnUVdCQlJ5NG1sZGpRbjNRZkFCcEUrSzhlb1lKYVZiMHpBS0JnZ3Fcbmhrak9QUVFEQWdOSUFEQkZBaUJPbE5oakdpNXVwSkZZVG9yN1hydjgvYURWRFJCNWpYc1MvRHpYL1dwZW9nSWhcbkFNUHc5Mmd4M1A5SU80LzVhaWxpYkpBTm1zekxvd1JIZjlzZERXZmVITkNxXG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tXG4tLS0tLUJFR0lOIENFUlRJRklDQVRFLS0tLS1cbk1JSUJuRENDQVVLZ0F3SUJBZ0lRZmhsUktwcFNkbmgvb2NaNnBGa0FWREFLQmdncWhrak9QUVFEQWpBdU1Td3dcbktnWURWUVFERXlObmJHOWlZV3d0TXkxeWIyOTBMbXhwYm10bGNtUXVZMngxYzNSbGNpNXNiMk5oYkRBZUZ3MHlcbk5UQTRNRGd3TkRBMk1qaGFGdzB5TlRFeE1EWXdOREEyTWpoYU1DNHhMREFxQmdOVkJBTVRJMmRzYjJKaGJDMHpcbkxYSnZiM1F1YkdsdWEyVnlaQzVqYkhWemRHVnlMbXh2WTJGc01Ga3dFd1lIS29aSXpqMENBUVlJS29aSXpqMERcbkFRY0RRZ0FFd2JnWEdDT1J2S0Q2WFpOenhyMlNDUTBodThrMVlQNko1NkJERHpFZjNraTl2bmtNR2xxV0NLaEhcbjBCQUhkMHYycGplSVBFQy9UVER1YnpSUDNaNCt1YU5DTUVBd0RnWURWUjBQQVFIL0JBUURBZ0trTUE4R0ExVWRcbkV3RUIvd1FGTUFNQkFmOHdIUVlEVlIwT0JCWUVGTzE5dmdGbHVlZ0tVQ2dETE4vN3dlNlRHSHozTUFvR0NDcUdcblNNNDlCQU1DQTBnQU1FVUNJUUNlajg1MTRvQU5rb2l0NmxnM1hOZG1oTUg2a3lOblhBUXd1bkJNcHU4TTFRSWdcbkRHekZlMEk0aUpiVkdLM3RQbDc5S2RHSlM3bVZGMnFSWFBONVI0OTlLTVk9XG4tLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tIn0sImtpbmQiOiJDb25maWdNYXAiLCJtZXRhZGF0YSI6eyJhbm5vdGF0aW9ucyI6eyJ0cnVzdC5jZXJ0LW1hbmFnZXIuaW8vaGFzaCI6IjBjMDY3OTExZGYwYjdlNjJlMjc0NjZkOTgxNWYwM2VlYTVlODdkMTczMmE3NzVmMDk5NGI2YTA0NmMzZGQwZWQifSwiY3JlYXRpb25UaW1lc3RhbXAiOiIyMDI1LTA4LTA3VDA2OjQ0OjUzWiIsImxhYmVscyI6eyJ0cnVzdC5jZXJ0LW1hbmFnZXIuaW8vYnVuZGxlIjoibGlua2VyZC1pZGVudGl0eS10cnVzdC1yb290cyJ9LCJtYW5hZ2VkRmllbGRzIjpbeyJhcGlWZXJzaW9uIjoidjEiLCJmaWVsZHNUeXBlIjoiRmllbGRzVjEiLCJmaWVsZHNWMSI6eyJmOmRhdGEiOnsiZjpjYS1idW5kbGUuY3J0Ijp7fX0sImY6bWV0YWRhdGEiOnsiZjphbm5vdGF0aW9ucyI6eyJmOnRydXN0LmNlcnQtbWFuYWdlci5pby9oYXNoIjp7fX0sImY6bGFiZWxzIjp7ImY6dHJ1c3QuY2VydC1tYW5hZ2VyLmlvL2J1bmRsZSI6e319LCJmOm93bmVyUmVmZXJlbmNlcyI6eyJrOntcInVpZFwiOlwiYWQ3Y2Y4MTUtYzZhNy00NDMxLTkzNGQtYTAwM2EwNjk5ODAxXCJ9Ijp7fX19fSwibWFuYWdlciI6InRydXN0LW1hbmFnZXIiLCJvcGVyYXRpb24iOiJBcHBseSIsInRpbWUiOiIyMDI1LTA4LTA4VDA0OjA2OjQwWiJ9XSwibmFtZSI6ImxpbmtlcmQtaWRlbnRpdHktdHJ1c3Qtcm9vdHMiLCJuYW1lc3BhY2UiOiJsaW5rZXJkIiwib3duZXJSZWZlcmVuY2VzIjpbeyJhcGlWZXJzaW9uIjoidHJ1c3QuY2VydC1tYW5hZ2VyLmlvL3YxYWxwaGExIiwiYmxvY2tPd25lckRlbGV0aW9uIjp0cnVlLCJjb250cm9sbGVyIjp0cnVlLCJraW5kIjoiQnVuZGxlIiwibmFtZSI6ImxpbmtlcmQtaWRlbnRpdHktdHJ1c3Qtcm9vdHMiLCJ1aWQiOiJhZDdjZjgxNS1jNmE3LTQ0MzEtOTM0ZC1hMDAzYTA2OTk4MDEifV0sInJlc291cmNlVmVyc2lvbiI6Ijg3MzAwMTAyIiwidWlkIjoiNTU1NzVmZWEtMzA4Yy00ZjM4LTg0MjUtYzYzMWY1YmU2YzAzIn19Ci0tLQ==
status:
  phase: WaitingForDelivery`

		u, err := deployer.GetUnstructured([]byte(eventReportYAML), logger)
		Expect(err).To(BeNil())
		Expect(len(u)).To(Equal(1))

		eventReport := &libsveltosv1beta1.EventReport{}
		err = runtime.DefaultUnstructuredConverter.
			FromUnstructured(u[0].UnstructuredContent(), eventReport)
		Expect(err).To(BeNil())

		resources, err := controllers.GetResources(eventReport, logger)
		Expect(err).To(BeNil())
		Expect(len(resources)).To(Equal(1))
	})

	It("removeStaleEventReports removes all EventReports for a given cluster/eventSource pair", func() {
		eventSourceName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		eventReport1 := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
		}

		eventReport2 := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
		}

		eventReport3 := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, randomString(), &clusterType),
			},
		}

		wrongClusterType := libsveltosv1beta1.ClusterTypeSveltos
		eventReport4 := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &wrongClusterType),
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventReport1.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventReport1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport1)).To(Succeed())

		ns.Name = eventReport2.Namespace
		ns.SetResourceVersion("")
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventReport2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport2)).To(Succeed())

		ns.Name = eventReport3.Namespace
		ns.SetResourceVersion("")
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventReport3)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport3)).To(Succeed())

		ns.Name = eventReport4.Namespace
		ns.SetResourceVersion("")
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventReport4)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport4)).To(Succeed())

		Expect(controllers.RemoveStaleEventReports(context.TODO(), testEnv.Client,
			clusterNamespace, clusterName, eventSourceName, clusterType, logger)).To(Succeed())

		currentEventReport := &libsveltosv1beta1.EventReport{}

		// EventReport1 was coming from this cluster/eventSource => expect it to be gone
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: eventReport1.Namespace, Name: eventReport1.Name}, currentEventReport)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// EventReport2 was coming from different cluster/eventSource (wrong namespace) => expect it to be still present
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: eventReport2.Namespace, Name: eventReport2.Name}, currentEventReport)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		// EventReport3 was coming from different cluster/eventSource (wrong cluster name) => expect it to be still present
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: eventReport3.Namespace, Name: eventReport3.Name}, currentEventReport)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		// EventReport4 was coming from different cluster/eventSource (wrong clusterType) => expect it to be still present
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: eventReport4.Namespace, Name: eventReport4.Name}, currentEventReport)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("removeStaleEventSources removes stale eventSources from managed cluster", func() {
		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.EventSourceSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

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

		resource := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSource.Name,
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterInfo: []libsveltosv1beta1.ClusterInfo{},
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		// Add EventTrigger as owner of EventSource. This indicates previously EventSource was
		// deployed because of this EventTrigger instance
		k8s_utils.AddOwnerReference(eventSource, resource)
		Expect(testEnv.Update(context.TODO(), eventSource)).To(Succeed())

		// Wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, eventSource)
			if err != nil {
				return false
			}

			targetGK := schema.GroupKind{
				Group: v1beta1.GroupVersion.Group,
				Kind:  v1beta1.EventTriggerKind,
			}

			return util.IsOwnedByObject(eventSource, resource, targetGK)
		}, timeout, pollingInterval).Should(BeTrue())

		createSecretWithKubeconfig(clusterNamespace, clusterName)

		// Test created EventSource instance and added EventTrigger as ownerReference, indicating EventSource was deployed
		// because of the EventTrigger instance.
		// Test has EventTrigger instance reference this EventSource instance.
		// RemoveStaleEventSources will not remove the EventSource test created.
		Expect(controllers.RemoveStaleEventSources(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			resource, false, logger)).To(Succeed())

		Consistently(func() bool {
			currentEventSource := &libsveltosv1beta1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		currentResource := &v1beta1.EventTrigger{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)).To(Succeed())
		currentResource.Spec.EventSourceName = randomString()
		Expect(testEnv.Update(context.TODO(), currentResource)).To(Succeed())

		// Wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: resource.Name}, currentResource)
			return err == nil && currentResource.Spec.EventSourceName != eventSource.Name
		}, timeout, pollingInterval).Should(BeTrue())

		// Test created EventSource instance and added EventTrigger as ownerReference, indicating EventSource was deployed
		// because of the EventTrigger instance.
		// Test has EventTrigger instance reference a different EventSource.
		// RemoveStaleEventSources will remove the EventSource test created.
		Expect(controllers.RemoveStaleEventSources(context.TODO(), testEnv.Client, clusterNamespace, clusterName, clusterType,
			currentResource, false, logger)).To(Succeed())

		Eventually(func() bool {
			currentEventSource := &libsveltosv1beta1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name}, currentEventSource)
			if err != nil {
				return apierrors.IsNotFound(err)
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployEventSource deploys referenced EventSource in the managed cluster", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		labelKey := randomString()
		labelValue := "production"

		eventSourceNamePrefix := "eventsource"
		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-%s-%s", eventSourceNamePrefix, clusterName, labelValue),
			},
			Spec: libsveltosv1beta1.EventSourceSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

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
				Labels: map[string]string{
					labelKey: labelValue,
				},
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		resource := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: fmt.Sprintf("%s-{{ .Cluster.metadata.name }}-{{ index .Cluster.metadata.labels %q}}",
					eventSourceNamePrefix, labelKey),
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(), Namespace: clusterNamespace, Name: clusterName,
					},
				},
				ClusterInfo: []libsveltosv1beta1.ClusterInfo{},
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
		// to add EventTrigger as OwnerReference of EventSource and annotation. So test verifies that.
		Expect(controllers.DeployEventSource(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			clusterType, resource, nil, false, logger)).To(Succeed())

		Eventually(func() bool {
			instantiatedEventSourceName := fmt.Sprintf("%s-%s-%s", eventSourceNamePrefix, cluster.Name, labelValue)

			currentEventSource := &libsveltosv1beta1.EventSource{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: instantiatedEventSourceName}, currentEventSource)
			if err != nil {
				return false
			}

			targetGK := schema.GroupKind{
				Group: v1beta1.GroupVersion.Group,
				Kind:  v1beta1.EventTriggerKind,
			}

			if !util.IsOwnedByObject(currentEventSource, resource, targetGK) {
				return false
			}
			if currentEventSource.Annotations == nil {
				return false
			}
			if _, ok := currentEventSource.Annotations[libsveltosv1beta1.DeployedBySveltosAnnotation]; !ok {
				return false
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("processEventTriggerForCluster deploys referenced EventSource", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

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

		// Create an EventTrigger referencing above EventSource
		resource := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: currentEventSourceName,
			},
			Status: v1beta1.EventTriggerStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind: ClusterKind, APIVersion: clusterv1.GroupVersion.String(),
						Namespace: clusterNamespace,
						Name:      clusterName,
					},
				},
				ClusterInfo: []libsveltosv1beta1.ClusterInfo{},
			},
		}

		Expect(testEnv.Create(context.TODO(), resource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resource)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, resource)).To(Succeed())

		eventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: currentEventSourceName,
			},
			Spec: libsveltosv1beta1.EventSourceSpec{
				ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
					{
						Kind:    randomString(),
						Group:   randomString(),
						Version: randomString(),
					},
				},
			},
		}
		Expect(testEnv.Create(context.TODO(), eventSource)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventSource)).To(Succeed())

		Expect(controllers.ProcessEventTriggerForCluster(context.TODO(), testEnv.Client, clusterNamespace, clusterName,
			resource.Name, clusterType, nil, logger)).To(Succeed())

		currentEventSource := &libsveltosv1beta1.EventSource{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Name: currentEventSourceName},
				currentEventSource)
			if err != nil {
				return false
			}

			targetGK := schema.GroupKind{
				Group: v1beta1.GroupVersion.Group,
				Kind:  v1beta1.EventTriggerKind,
			}

			return util.IsOwnedByObject(currentEventSource, resource, targetGK)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("instantiateOneClusterProfilePerAllResource creates clusterProfile for all resource", func() {
		nginxName := nginxDeploymentName
		nginxNamespace := randomString()

		u, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace)))
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
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
			Spec: libsveltosv1beta1.EventReportSpec{
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

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName,
				HelmCharts: []configv1beta1.HelmChart{
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

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: clusterNamespace},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		_, err = controllers.InstantiateOneClusterProfilePerAllResource(context.TODO(), testEnv.Client,
			clusterNamespace, clusterName, clusterType, eventTrigger, eventReport, logger)
		Expect(err).To(BeNil())

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
			eventReport, clusterType)

		listOptions := []client.ListOption{
			client.MatchingLabels(labels),
		}

		Eventually(func() bool {
			clusterProfiles := &configv1beta1.ClusterProfileList{}
			err := testEnv.List(context.TODO(), clusterProfiles, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfiles.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		clusterProfiles := &configv1beta1.ClusterProfileList{}
		Expect(testEnv.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())
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

		u1, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace1)))
		Expect(err).To(BeNil())

		var u2 *unstructured.Unstructured
		u2, err = k8s_utils.GetUnstructured([]byte(fmt.Sprintf(nginxDepl, nginxName, nginxNamespace2)))
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
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
			Spec: libsveltosv1beta1.EventReportSpec{
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

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName,
				HelmCharts: []configv1beta1.HelmChart{
					{
						RepositoryName:   randomString(),
						RepositoryURL:    randomString(),
						ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
						ReleaseName:      randomString(),
						ChartName:        randomString() + "{{ .Cluster.metadata.name }}",
						ChartVersion:     randomString(),
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
					},
				},
				KustomizationRefs: []configv1beta1.KustomizationRef{
					{
						Namespace: "{{ .MatchingResource.Namespace }}",
						Name:      randomString() + "{{ .Cluster.metadata.name }}",
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Path:      randomString() + "{{ .Cluster.metadata.name }}" + "{{ .MatchingResource.Namespace }}",
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: clusterNamespace},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		_, err = controllers.InstantiateOneClusterProfilePerResource(context.TODO(), testEnv.Client,
			clusterNamespace, clusterName, clusterType, eventTrigger, eventReport, logger)
		Expect(err).To(BeNil())

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
			eventReport, clusterType)

		listOptions := []client.ListOption{
			client.MatchingLabels(labels),
		}

		Eventually(func() bool {
			clusterProfiles := &configv1beta1.ClusterProfileList{}
			err := testEnv.List(context.TODO(), clusterProfiles, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfiles.Items) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		clusterProfiles := &configv1beta1.ClusterProfileList{}
		Expect(testEnv.List(context.TODO(), clusterProfiles, listOptions...)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(Equal(2))

		for i := 0; i < 2; i++ {
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs).ToNot(BeNil())
			Expect(len(clusterProfiles.Items[i].Spec.ClusterRefs)).To(Equal(1))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Namespace).To(Equal(clusterNamespace))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Name).To(Equal(clusterName))
			Expect(clusterProfiles.Items[i].Spec.ClusterRefs[0].Kind).To(Equal("Cluster"))
			Expect(len(clusterProfiles.Items[i].Spec.HelmCharts)).To(Equal(1))
			Expect(len(clusterProfiles.Items[i].Spec.KustomizationRefs)).To(Equal(1))
		}
	})

	It("removeClusterProfiles removes stales clusterProfiles", func() {
		eventTriggerName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
					eventReport, clusterType),
			},
		}

		toBeRemovedClusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
					eventReport, clusterType),
			},
		}

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: eventTriggerName},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName,
				HelmCharts: []configv1beta1.HelmChart{
					{
						RepositoryName:   randomString(),
						RepositoryURL:    randomString(),
						ReleaseNamespace: "{{ .MatchingResource.Namespace }}",
						ReleaseName:      randomString(),
						ChartName:        randomString(),
						ChartVersion:     randomString(),
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterProfile, toBeRemovedClusterProfile, eventTrigger,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(controllers.RemoveClusterProfiles(context.TODO(), c, clusterNamespace, clusterName, clusterType, eventTrigger,
			eventReport, []*configv1beta1.ClusterProfile{clusterProfile}, logger)).To(Succeed())

		clusterProfiles := &configv1beta1.ClusterProfileList{}
		Expect(c.List(context.TODO(), clusterProfiles)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(Equal(1))
		Expect(clusterProfiles.Items[0].Name).To(Equal(clusterProfile.Name))
	})

	It("getClusterProfileName returns the correct name for a clusterProfile", func() {
		eventTriggerName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: eventTriggerName},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
			eventReport, clusterType)

		name, err := controllers.GetClusterProfileName(context.TODO(), c, clusterNamespace, clusterName,
			eventTrigger, nil, labels, logger)
		Expect(err).To(BeNil())
		Expect(name).ToNot(BeEmpty())

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
					eventReport, clusterType),
			},
		}

		Expect(c.Create(context.TODO(), clusterProfile)).To(Succeed())

		var currentName string
		currentName, err = controllers.GetClusterProfileName(context.TODO(), c, clusterNamespace, clusterName,
			eventTrigger, nil, labels, logger)
		Expect(err).To(BeNil())
		Expect(currentName).To(Equal(name))
	})

	It("getClusterProfileName use InstantiatedProfileNameFormat when defined", func() {
		eventTriggerName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		eventSourceName := randomString()

		eventResource := corev1.ObjectReference{Kind: "Secret", APIVersion: corev1.SchemeGroupVersion.String(),
			Namespace: randomString(), Name: randomString()}
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
			Spec: libsveltosv1beta1.EventReportSpec{
				MatchingResources: []corev1.ObjectReference{
					eventResource,
				},
			},
		}

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: eventTriggerName},
			Spec: v1beta1.EventTriggerSpec{
				InstantiatedProfileNameFormat: "{{ .Cluster.metadata.namespace }}-{{ .Cluster.metadata.name }}-{{ .MatchingResource.Name }}-my-profile",
				OneForEvent:                   true,
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		initObjects := []client.Object{
			cluster, eventTrigger, eventReport,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		objects, err := controllers.PrepareCurrentObjectList(ctx, c, clusterNamespace, clusterName, clusterType, eventReport, logger)
		Expect(err).To(BeNil())

		labels := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
			eventReport, clusterType)

		name, err := controllers.GetClusterProfileName(context.TODO(), c, clusterNamespace, clusterName,
			eventTrigger, objects[0], labels, logger)
		Expect(err).To(BeNil())
		Expect(name).ToNot(BeEmpty())
		Expect(name).To(Equal(fmt.Sprintf("%s-%s-%s-my-profile", clusterNamespace, clusterName, eventResource.Name)))

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
					eventReport, clusterType),
			},
		}

		Expect(c.Create(context.TODO(), clusterProfile)).To(Succeed())

		var currentName string
		currentName, err = controllers.GetClusterProfileName(context.TODO(), c, clusterNamespace, clusterName,
			eventTrigger, objects[0], labels, logger)
		Expect(err).To(BeNil())
		Expect(currentName).To(Equal(name))
	})

	It("instantiateReferencedPolicies instantiates referenced configMap/secret", func() {
		eventTriggerName := randomString()

		namespace := randomString()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				// Mark resource as template so instantiateReferencedPolicies
				// will generate a new one in projectsveltos namespace
				Annotations: map[string]string{
					v1beta1.InstantiateAnnotation: "ok",
				},
			},
			Data: map[string]string{
				"policy": randomString(),
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				// Mark resource as template so instantiateReferencedPolicies
				// will generate a new one in projectsveltos namespace
				Annotations: map[string]string{
					v1beta1.InstantiateAnnotation: "ok",
				},
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				"policy": []byte(randomString()),
			},
		}

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: eventTriggerName},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName,
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						DeploymentType: configv1beta1.DeploymentTypeLocal,
						Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:           configMap.Name,
						Namespace:      configMap.Namespace,
					},
					{
						DeploymentType: configv1beta1.DeploymentTypeRemote,
						Kind:           string(libsveltosv1beta1.SecretReferencedResourceKind),
						Name:           secret.Name,
						Namespace:      secret.Namespace,
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		clusterRef := &corev1.ObjectReference{
			Namespace:  namespace,
			Name:       cluster.Name,
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventTrigger)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventTrigger)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		object := &controllers.CurrentObject{
			MatchingResource: corev1.ObjectReference{
				Kind:       "Service",
				APIVersion: "v1",
				Namespace:  randomString(),
				Name:       randomString(),
			},
		}

		labels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace, clusterRef.Name, eventTrigger.Name,
			eventReport, libsveltosv1beta1.ClusterTypeCapi)

		localSet, remoteSet, err := controllers.InstantiateReferencedPolicyRefs(context.TODO(), testEnv.Client,
			eventTrigger, randomString(), eventTrigger, clusterRef, object, labels, logger)
		Expect(err).To(BeNil())
		Expect(localSet).ToNot(BeNil())
		Expect(len(localSet)).To(Equal(1))
		Expect(remoteSet).ToNot(BeNil())
		Expect(len(remoteSet)).To(Equal(1))

		expectedLabels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace, clusterRef.Name, eventTrigger.Name,
			eventReport, libsveltosv1beta1.ClusterTypeCapi)

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
			client.MatchingLabels(expectedLabels),
		}

		Eventually(func() bool {
			configMaps := &corev1.ConfigMapList{}
			err := testEnv.List(context.TODO(), configMaps, listOptions...)
			if err != nil {
				return false
			}

			return len(configMaps.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Eventually(func() bool {
			secrets := &corev1.SecretList{}
			err := testEnv.List(context.TODO(), secrets, listOptions...)
			if err != nil {
				return false
			}

			return len(secrets.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		configMaps := &corev1.ConfigMapList{}
		Expect(testEnv.List(context.TODO(), configMaps, listOptions...)).To(Succeed())
		Expect(len(configMaps.Items)).To(Equal(1))
		validateLabels(configMaps.Items[0].Labels, clusterRef, eventTriggerName, configMap)
		Expect(reflect.DeepEqual(configMaps.Items[0].Data, configMap.Data)).To(BeTrue())

		secrets := &corev1.SecretList{}
		Expect(testEnv.List(context.TODO(), secrets, listOptions...)).To(Succeed())
		Expect(len(secrets.Items)).To(Equal(1))
		validateLabels(secrets.Items[0].Labels, clusterRef, eventTriggerName, secret)
		Expect(reflect.DeepEqual(secrets.Items[0].Data, secret.Data)).To(BeTrue())
	})

	It("instantiateReferencedPolicies, one for all resources, instantiates referenced configMap", func() {
		eventTriggerName := randomString()

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
                    {{if or (eq (int .port) 443) (eq (int .port) 8443) }}
                    number: {{ .port }}
                    {{ end }}
                    {{ end }}
            {{ end }}`

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				// Mark resource as template so instantiateReferencedPolicies
				// will generate a new one in projectsveltos namespace
				Annotations: map[string]string{
					v1beta1.InstantiateAnnotation: "ok",
				},
			},
			Data: map[string]string{
				"policy": fmt.Sprintf(ingress, namespace),
			},
		}

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: eventTriggerName},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: randomString(),
				OneForEvent:     false,
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						DeploymentType: configv1beta1.DeploymentTypeRemote,
						Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Name:           configMap.Name,
						Namespace:      configMap.Namespace,
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		clusterRef := &corev1.ObjectReference{
			Namespace:  namespace,
			Name:       cluster.Name,
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		}

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, configMap)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, eventReport)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), eventTrigger)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, eventTrigger)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, cluster)).To(Succeed())

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

		u1, err := k8s_utils.GetUnstructured([]byte(httpsService1))
		Expect(err).To(BeNil())

		var u2 *unstructured.Unstructured
		u2, err = k8s_utils.GetUnstructured([]byte(httpsService2))
		Expect(err).To(BeNil())

		objects := &controllers.CurrentObjects{
			Resources: []map[string]interface{}{
				u1.UnstructuredContent(),
				u2.UnstructuredContent(),
			},
		}

		labels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace, clusterRef.Name, eventTrigger.Name,
			eventReport, libsveltosv1beta1.ClusterTypeCapi)

		localSet, remoteSet, err := controllers.InstantiateReferencedPolicyRefs(context.TODO(), testEnv,
			eventTrigger, randomString(), eventTrigger, clusterRef, objects, labels, logger)
		Expect(err).To(BeNil())
		Expect(localSet).ToNot(BeNil())
		Expect(len(localSet)).To(Equal(0))
		Expect(remoteSet).ToNot(BeNil())
		Expect(len(remoteSet)).To(Equal(1))

		listOptions := []client.ListOption{
			client.InNamespace(controllers.ReportNamespace),
			client.MatchingLabels(labels),
		}

		Eventually(func() bool {
			configMaps := &corev1.ConfigMapList{}
			err := testEnv.List(context.TODO(), configMaps, listOptions...)
			if err != nil {
				return false
			}
			return len(configMaps.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		configMaps := &corev1.ConfigMapList{}
		Expect(testEnv.List(context.TODO(), configMaps, listOptions...)).To(Succeed())
		Expect(len(configMaps.Items)).To(Equal(1))
		Expect(configMaps.Items[0].Data).ToNot(BeEmpty())
		Expect(configMaps.Items[0].Data["policy"]).To(ContainSubstring("443"))
		Expect(configMaps.Items[0].Data["policy"]).To(ContainSubstring("8443"))
	})

	It("removeConfigMaps removes stale ConfigMaps", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		eventTriggerName := randomString()

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventTriggerName, eventReport, clusterType),
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
					clusterName, eventTriggerName, eventReport, clusterType),
			},
			Data: map[string]string{
				randomString(): randomString(),
			},
		}

		initObjects := []client.Object{
			configMap, toBeRemovedConfigMap,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		eventTrigger := v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventTriggerName,
			},
		}
		policyRef := libsveltosv1beta1.PolicyRef{
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
		}
		policyRefs := map[libsveltosv1beta1.PolicyRef]bool{policyRef: true}

		Expect(controllers.RemoveConfigMaps(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			&eventTrigger, eventReport, policyRefs, logger)).To(Succeed())

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
		clusterType := libsveltosv1beta1.ClusterTypeCapi
		eventTriggerName := randomString()

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventTriggerName, eventReport, clusterType),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
			},
		}

		toBeRemovedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: controllers.ReportNamespace,
				Name:      randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace,
					clusterName, eventTriggerName, eventReport, clusterType),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
			},
		}

		initObjects := []client.Object{
			secret, toBeRemovedSecret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		eventTrigger := v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventTriggerName,
			},
		}
		policyRef := libsveltosv1beta1.PolicyRef{
			Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
			Namespace: secret.Namespace,
			Name:      secret.Name,
		}
		policyRefs := map[libsveltosv1beta1.PolicyRef]bool{policyRef: true}

		Expect(controllers.RemoveSecrets(context.TODO(), c, clusterNamespace, clusterName, clusterType,
			&eventTrigger, eventReport, policyRefs, logger)).To(Succeed())

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

		u, err := k8s_utils.GetUnstructured([]byte(service))
		Expect(err).To(BeNil())
		Expect(u).ToNot(BeNil())

		object := controllers.CurrentObject{
			Resource: u.UnstructuredContent(),
		}

		var instantiatedContent map[string]string
		instantiatedContent, err = controllers.InstantiateDataSection(randomString(), content, object, false, logger)
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
				policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
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

	It("instantiateDataSection", func() {
		key := "body"
		content := map[string]string{
			key: `    {{ $data := "hoge.com/fuga" }}
    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: test-config
      namespace: {{ .Resource.metadata.name }}
    data:
      data: {{ $data }}`,
		}

		ns := `apiVersion: v1
kind: Namespace
metadata:
  name: test
  labels:
    test: enabled
  annotations:
    hoge.com/fuga: piyo.com/piyo`

		u, err := k8s_utils.GetUnstructured([]byte(ns))
		Expect(err).To(BeNil())
		Expect(u).ToNot(BeNil())

		object := controllers.CurrentObject{
			Resource: u.UnstructuredContent(),
		}

		result, err := controllers.InstantiateDataSection(randomString(), content, object, false, logger)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(1))

		u, err = k8s_utils.GetUnstructured([]byte(result[key]))
		Expect(err).To(BeNil())

		cm := &corev1.ConfigMap{}
		err = runtime.DefaultUnstructuredConverter.
			FromUnstructured(u.UnstructuredContent(), cm)
		Expect(err).To(BeNil())
		Expect(cm.Namespace).To(Equal("test"))
		Expect(len(cm.Data)).To(Equal(1))
		Expect(cm.Data["data"]).To(Equal("hoge.com/fuga"))
	})

	It("instantiateFromGeneratorsPerResource instantiates from SecretGenerator", func() {
		token := []byte(randomString())
		// This is the resource in the managed cluster that created the event.
		// The eventReport instance test creates later on, contains it in its report.
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Data: map[string][]byte{
				"token": token,
			},
		}

		eventSourceName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeCapi

		// This is the namespace where cluster and eventReport will be
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		// This is the cluster the eventReport is for
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, secret)).To(Succeed())

		uContent, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secret)
		Expect(err).To(BeNil())

		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(uContent)

		result := ""

		tmpJson, jsonErr := u.MarshalJSON()
		Expect(jsonErr).To(BeNil())
		result += string(tmpJson)
		result += separator

		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      randomString(),
				Labels:    libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType),
			},
			Spec: libsveltosv1beta1.EventReportSpec{
				MatchingResources: []corev1.ObjectReference{
					{Kind: "Secret", APIVersion: corev1.SchemeGroupVersion.String(),
						Namespace: secret.Namespace, Name: secret.Name},
				},
				Resources:        []byte(result),
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      clusterType,
				EventSourceName:  eventSourceName,
			},
		}

		Expect(testEnv.Create(context.TODO(), eventReport)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventReport)).To(Succeed())

		//nolint: gosec // just a test
		secretData := `kind: ConfigMap
apiVersion: v1
metadata:
  name: "{{ .Cluster.metadata.name }}"
  namespace: "{{ .Cluster.metadata.namespace }}"
data:
  token: "{{ .Resource.data.token }}"`

		// Create a Secret that EventTrigger will reference in its SecretGenerator
		// Event-manager will generate a new Secret by instantiating this one using
		// event/cluster data
		secretGenerator := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      randomString(),
				Annotations: map[string]string{
					"projectsveltos.io/instantiate": "ok",
				},
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
			Data: map[string][]byte{
				"configmap.yaml": []byte(secretData),
			},
		}

		Expect(testEnv.Create(context.TODO(), secretGenerator)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, secretGenerator)).To(Succeed())

		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: v1beta1.EventTriggerSpec{
				EventSourceName: eventSourceName,
				SecretGenerator: []v1beta1.GeneratorReference{
					{
						Namespace:                      secretGenerator.Namespace,
						Name:                           secretGenerator.Name,
						InstantiatedResourceNameFormat: "{{ .Cluster.metadata.name}}-{{ .MatchingResource.Name }}-generated",
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), eventTrigger)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, eventTrigger)).To(Succeed())

		instantiatedSecrets, err := controllers.InstantiateFromGeneratorsPerResource(context.TODO(), testEnv.Client, eventTrigger, eventReport,
			clusterNamespace, clusterName, clusterType, logger)
		Expect(err).To(BeNil())
		Expect(len(instantiatedSecrets)).To(Equal(1))
		Expect(instantiatedSecrets[0].Name).To(Equal(fmt.Sprintf("%s-%s-generated", clusterName, secret.Name)))
	})

	It("getCloudEvents processes collected CloudEvents", func() {
		//nolint: lll // line with cloudEvent
		ce1String := `{"specversion":"1.0","id":"10001","source":"my.source","type":"my.type","subject":"mgianluc","datacontenttype":"application/json","data":{"message":"Hello message 1!"}}`
		ce1JsonData := []byte(ce1String)

		//nolint: lll // line with cloudEvent
		ce2String := `{"specversion":"1.0","id":"10002","source":"my.source","type":"my.type","subject":"mgianluc","datacontenttype":"application/json","data":{"message":"Hello message 2!"}}`
		ce2JsonData := []byte(ce2String)

		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Spec: libsveltosv1beta1.EventReportSpec{
				CloudEvents: [][]byte{ce1JsonData, ce2JsonData},
			},
		}

		result, err := controllers.GetCloudEvents(eventReport, logger)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(2))
		Expect(result[0]["specversion"]).To(Equal("1.0"))
		Expect(result[0]["id"]).To(Equal("10001"))
		Expect(result[1]["id"]).To(Equal("10002"))
	})

	It("appendCloudEventClusterProfiles appens all clusterProfiles created because of CloudEvents", func() {
		eventTriggerName := randomString()
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		lbls := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
			eventReport, clusterType)
		lbls["eventtrigger.lib.projectsveltos.io/cesource"] = randomString()
		lbls["eventtrigger.lib.projectsveltos.io/cesubject"] = randomString()

		// ClusterProfile created because of CloudEvents have eventtrigger.lib.projectsveltos.io/cesource label
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: lbls,
			},
		}

		// This clusterProfile does not have cloudevent labels
		clusterProfile2 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTriggerName,
					eventReport, clusterType),
			},
		}

		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterProfile)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterProfile2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterProfile2)).To(Succeed())

		clusterProfiles := []*configv1beta1.ClusterProfile{}
		clusterProfiles, err := controllers.AppendCloudEventClusterProfiles(context.TODO(), testEnv.Client,
			clusterNamespace, clusterName, eventTriggerName, clusterType, eventReport, clusterProfiles)
		Expect(err).To(BeNil())
		Expect(len(clusterProfiles)).To(Equal(1))
		Expect(clusterProfiles[0].Name).To(Equal(clusterProfile.Name))
	})

	It("deleteInstantiatedFromGenerators removes ConfigMap and Secret instances created due to Generators", func() {
		eventTrigger := &v1beta1.EventTrigger{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		eventSourceName := randomString()
		eventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					libsveltosv1beta1.EventSourceNameLabel: eventSourceName,
				},
			},
		}

		lbls := controllers.GetInstantiatedObjectLabels(clusterNamespace, clusterName, eventTrigger.Name,
			eventReport, clusterType)

		ce := event.New()
		ce.SetSubject(randomString())
		ce.SetSource(randomString())

		//nolint: lll // line with cloudEvent
		ceString := fmt.Sprintf(`{"specversion":"1.0","id":"10001","source":%q,"type":"my.type","subject":%q,"datacontenttype":"application/json","data":{"message":"Hello message 1!"}}`,
			ce.Context.GetSource(), ce.Context.GetSubject())
		ceJsonData := []byte(ceString)

		var ceMap map[string]interface{}
		Expect(json.Unmarshal(ceJsonData, &ceMap)).To(Succeed())

		lbls["eventtrigger.lib.projectsveltos.io/cesource"] = ce.Context.GetSource()
		lbls["eventtrigger.lib.projectsveltos.io/cesubject"] = ce.Context.GetSubject()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		// Secret not created because of generators, generators label missing
		secret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels:    lbls,
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		// ConfigMap not created because of generators, generators label missing
		configMap1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels:    lbls,
			},
		}

		lbls["eventtrigger.lib.projectsveltos.io/fromgenerator"] = "ok"
		// Secret created because of generators, generators label present
		secret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels:    lbls,
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		// ConfigMap created because of generators, generators label present
		configMap2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				Labels:    lbls,
			},
		}

		Expect(testEnv.Create(context.TODO(), secret1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, secret1)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap1)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), secret2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, secret2)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap2)).To(Succeed())

		Expect(controllers.DeleteInstantiatedFromGenerators(context.TODO(), testEnv.Client, clusterNamespace,
			clusterName, clusterType, eventTrigger, eventReport, ceMap, logger)).To(Succeed())

		// Secret1 must be deleted
		Eventually(func() bool {
			currentSecret := &corev1.Secret{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: secret1.Namespace, Name: secret1.Name}, currentSecret)
			if err == nil {
				return currentSecret.DeletionTimestamp.IsZero()
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// ConfigMap1 must be deleted
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: configMap1.Namespace, Name: configMap1.Name}, currentConfigMap)
			if err == nil {
				return currentConfigMap.DeletionTimestamp.IsZero()
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Secret2 must exist
		currentSecret := &corev1.Secret{}
		err := testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: secret2.Namespace, Name: secret2.Name}, currentSecret)
		Expect(err).To(BeNil())

		// ConfigMap2 must exist
		currentConfigMap := &corev1.ConfigMap{}
		err = testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap2.Namespace, Name: configMap2.Name}, currentConfigMap)
		Expect(err).To(BeNil())
	})
})

func getClusterInfo(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) *libsveltosv1beta1.ClusterInfo {
	var apiVersion, kind string
	if clusterType == libsveltosv1beta1.ClusterTypeCapi {
		apiVersion = clusterv1.GroupVersion.String()
		kind = ClusterKind
	} else {
		apiVersion = libsveltosv1beta1.GroupVersion.String()
		kind = libsveltosv1beta1.SveltosClusterKind
	}

	return &libsveltosv1beta1.ClusterInfo{
		Cluster: corev1.ObjectReference{
			Namespace:  clusterNamespace,
			Name:       clusterName,
			Kind:       kind,
			APIVersion: apiVersion,
		},
		Hash: []byte(randomString()),
	}
}

func validateLabels(labels map[string]string, clusterRef *corev1.ObjectReference,
	eventTriggerName string, referencedResource client.Object) {

	v := labels[controllers.ReferencedResourceNamespaceLabel]
	Expect(v).To(Equal(referencedResource.GetNamespace()))
	v = labels[controllers.ReferencedResourceNameLabel]
	Expect(v).To(Equal(referencedResource.GetName()))

	expectedLabels := controllers.GetInstantiatedObjectLabels(clusterRef.Namespace,
		clusterRef.Name, eventTriggerName, nil, clusterproxy.GetClusterType(clusterRef))

	for k := range expectedLabels {
		v = labels[k]
		Expect(v).To(Equal(expectedLabels[k]))
	}
}
