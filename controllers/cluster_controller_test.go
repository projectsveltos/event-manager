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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clustercache"
)

var _ = Describe("processCluster", func() {
	It("evicts clustercache when the cluster is deleted", func() {
		// A cluster that is deleted and immediately replaced (same namespace/name, new
		// kubeconfig) must not leave the old rest.Config cached. Deletion is the one case
		// InvalidateOnAuthError never sees.
		clusterNamespace := randomString()
		clusterName := randomString()
		secretName := clusterName + sveltosKubeconfigPostfix

		now := metav1.NewTime(time.Now())
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         clusterNamespace,
				Name:              clusterName,
				Finalizers:        []string{"test.projectsveltos.io/keep-around"},
				DeletionTimestamp: &now,
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

		// Same namespace/name comes back as a brand new cluster with a different endpoint -
		// exactly what processCluster must not leave stale.
		secretKey := types.NamespacedName{Namespace: clusterNamespace, Name: secretName}
		Expect(c.Get(context.TODO(), secretKey, secret)).To(Succeed())
		secret.Data[kubeconfigSecretKey] = buildFakeKubeconfig("https://10.0.0.2:6443")
		Expect(c.Update(context.TODO(), secret)).To(Succeed())

		req := ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: clusterNamespace, Name: clusterName},
		}
		_, err = controllers.ProcessCluster(context.TODO(), c, &clusterv1.Cluster{}, req,
			libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(BeNil())

		config, err = cacheMgr.GetKubernetesRestConfig(context.TODO(), c, clusterNamespace, clusterName,
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(BeNil())
		Expect(config.Host).To(Equal("https://10.0.0.2:6443"))
	})
})
