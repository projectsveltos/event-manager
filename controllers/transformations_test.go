/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clustercache"
)

// buildFakeKubeconfig returns a minimal, syntactically valid kubeconfig pointing at server.
// clientcmd only needs to parse this, never dial it, so no real cert/token data is needed.
func buildFakeKubeconfig(server string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: fake-token
`, server))
}

var _ = Describe("requeueEventTriggerForReference", func() {
	It("evicts clustercache when a cluster's kubeconfig Secret changes", func() {
		// A kubeconfig Secret's content can change (endpoint, credentials) with no auth error
		// and no cluster deletion - clustercache's other eviction paths never fire for that.
		// This is the only mechanism that catches it. See #1954.
		clusterNamespace := randomString()
		clusterName := randomString()
		secretName := clusterName + sveltosKubeconfigPostfix
		secretKey := types.NamespacedName{Namespace: clusterNamespace, Name: secretName}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      clusterName,
			},
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterNamespace,
				Name:      secretName,
			},
			Data: map[string][]byte{
				kubeconfigSecretKey: buildFakeKubeconfig("https://10.0.0.1:6443"),
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, secret).Build()
		logger := textlogger.NewLogger(textlogger.NewConfig())

		cacheMgr := clustercache.GetManager()
		config, err := cacheMgr.GetKubernetesRestConfig(context.TODO(), c, clusterNamespace, clusterName,
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(BeNil())
		Expect(config.Host).To(Equal("https://10.0.0.1:6443"))

		// Point the Secret at a different endpoint - same cluster, no deletion, no auth error.
		Expect(c.Get(context.TODO(), secretKey, secret)).To(Succeed())
		secret.Data[kubeconfigSecretKey] = buildFakeKubeconfig("https://10.0.0.2:6443")
		Expect(c.Update(context.TODO(), secret)).To(Succeed())

		reconciler := controllers.EventTriggerReconciler{
			Client: c,
			Logger: logger,
		}
		controllers.RequeueEventTriggerForReference(&reconciler, context.TODO(), secret)

		config, err = cacheMgr.GetKubernetesRestConfig(context.TODO(), c, clusterNamespace, clusterName,
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(BeNil())
		Expect(config.Host).To(Equal("https://10.0.0.2:6443"))
	})
})
