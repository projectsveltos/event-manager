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

package fv_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/TwiN/go-color"
	ginkgotypes "github.com/onsi/ginkgo/v2/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var (
	k8sClient           client.Client
	scheme              *runtime.Scheme
	kindWorkloadCluster *unstructured.Unstructured // This is the name of the kind workload cluster, in the form namespace/name
)

const (
	timeout         = 2 * time.Minute
	pollingInterval = 5 * time.Second
)

const (
	deplNamespace        = "projectsveltos"
	deplName             = "event-manager"
	managerContainerName = "manager"
)

func TestFv(t *testing.T) {
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.FullTrace = true
	reporterConfig.JSONReport = "out.json"
	report := func(report ginkgotypes.Report) {
		for i := range report.SpecReports {
			specReport := report.SpecReports[i]
			if specReport.State.String() == "skipped" {
				GinkgoWriter.Printf(color.Colorize(color.Blue, fmt.Sprintf("[Skipped]: %s\n", specReport.FullText())))
			}
		}
		for i := range report.SpecReports {
			specReport := report.SpecReports[i]
			if specReport.Failed() {
				GinkgoWriter.Printf(color.Colorize(color.Red, fmt.Sprintf("[Failed]: %s\n", specReport.FullText())))
			}
		}
	}
	ReportAfterSuite("report", report)

	RunSpecs(t, "FV Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	ctrl.SetLogger(klog.Background())

	restConfig := ctrl.GetConfigOrDie()
	// To get rid of the annoying request.go log
	restConfig.QPS = 100
	restConfig.Burst = 100

	scheme = runtime.NewScheme()

	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(clusterv1.AddToScheme(scheme)).To(Succeed())
	Expect(v1beta1.AddToScheme(scheme)).To(Succeed())
	Expect(configv1beta1.AddToScheme(scheme)).To(Succeed())
	Expect(libsveltosv1beta1.AddToScheme(scheme)).To(Succeed())
	Expect(networkingv1.AddToScheme(scheme)).To(Succeed())
	Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())

	var err error
	k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	if isCAPIInstalled(context.TODO(), k8sClient) {
		verifyCAPICluster()
	} else {
		verifySveltosCluster()
	}
})

func verifySveltosCluster() {
	clusterList := &libsveltosv1beta1.SveltosClusterList{}
	listOptions := []client.ListOption{
		client.MatchingLabels(
			map[string]string{"cluster-name": "clusterapi-workload"}, // This label is added by Makefile
		),
	}

	Expect(k8sClient.List(context.TODO(), clusterList, listOptions...)).To(Succeed())
	Expect(len(clusterList.Items)).To(Equal(1))
	unstructuredMap, err :=
		runtime.DefaultUnstructuredConverter.ToUnstructured(&clusterList.Items[0])
	Expect(err).To(BeNil())

	kindWorkloadCluster = &unstructured.Unstructured{Object: unstructuredMap}

	Byf("Set Cluster %s:%s unpaused and add label %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), key, value)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentCluster := &libsveltosv1beta1.SveltosCluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
			currentCluster)).To(Succeed())

		currentLabels := currentCluster.Labels
		if currentLabels == nil {
			currentLabels = make(map[string]string)
		}
		currentLabels[key] = value
		currentCluster.Labels = currentLabels
		currentCluster.Spec.Paused = false

		return k8sClient.Update(context.TODO(), currentCluster)
	})
	Expect(err).To(BeNil())
}

func verifyCAPICluster() {
	clusterList := &clusterv1.ClusterList{}
	listOptions := []client.ListOption{
		client.MatchingLabels(
			map[string]string{clusterv1.ClusterNameLabel: "clusterapi-workload"},
		),
	}

	Expect(k8sClient.List(context.TODO(), clusterList, listOptions...)).To(Succeed())
	Expect(len(clusterList.Items)).To(Equal(1))

	unstructuredMap, err :=
		runtime.DefaultUnstructuredConverter.ToUnstructured(&clusterList.Items[0])
	Expect(err).To(BeNil())

	kindWorkloadCluster = &unstructured.Unstructured{Object: unstructuredMap}

	Byf("Wait for machine in cluster %s/%s to be ready", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
	Eventually(func() bool {
		machineList := &clusterv1.MachineList{}
		listOptions = []client.ListOption{
			client.InNamespace(kindWorkloadCluster.GetNamespace()),
			client.MatchingLabels{clusterv1.ClusterNameLabel: kindWorkloadCluster.GetName()},
		}
		err := k8sClient.List(context.TODO(), machineList, listOptions...)
		if err != nil {
			return false
		}
		for i := range machineList.Items {
			m := machineList.Items[i]
			if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Set Cluster %s:%s unpaused and add label %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), key, value)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
			currentCluster)).To(Succeed())

		currentLabels := currentCluster.Labels
		if currentLabels == nil {
			currentLabels = make(map[string]string)
		}
		currentLabels[key] = value
		currentCluster.Labels = currentLabels
		currentCluster.Spec.Paused = false

		return k8sClient.Update(context.TODO(), currentCluster)
	})
	Expect(err).To(BeNil())
}

func createNamespaceAndService(c client.Client, serviceNamespace string) {
	Byf("Create namespace %s in the managed cluster", serviceNamespace)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceNamespace,
		},
	}
	Expect(c.Create(context.TODO(), ns)).To(Succeed())

	Byf("Creating a Service in namespace %s in the managed cluster", serviceNamespace)
	var port int32 = 5467
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: serviceNamespace, // EventSource filters service in this namespace
			Name:      randomString(),
			Labels: map[string]string{
				"sveltos": "fv", // those labels are needed to match lua script
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				randomString(): randomString(),
				randomString(): randomString(),
			},
			Ports: []corev1.ServicePort{
				{
					Name: randomString(),
					Port: port,
				},
			},
		},
	}
	Expect(c.Create(context.TODO(), service)).To(Succeed())
}

func isAgentLessMode() bool {
	By("Getting event manager pod")
	classfierDepl := &appsv1.Deployment{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: deplNamespace, Name: deplName},
		classfierDepl)).To(Succeed())

	Expect(len(classfierDepl.Spec.Template.Spec.Containers)).To(Equal(1))

	for i := range classfierDepl.Spec.Template.Spec.Containers[0].Args {
		if strings.Contains(classfierDepl.Spec.Template.Spec.Containers[0].Args[i], "agent-in-mgmt-cluster=true") {
			By("Event-manager in agentless mode")
			return true
		}
	}

	return false
}

// isCAPIInstalled returns true if CAPI is installed, false otherwise
func isCAPIInstalled(ctx context.Context, c client.Client) bool {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "clusters.cluster.x-k8s.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		Expect(err).To(BeNil())
	}

	return true
}
