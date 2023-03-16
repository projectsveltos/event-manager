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
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/event-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	sveltosmanagercontrollers "github.com/projectsveltos/sveltos-manager/controllers"
)

const (
	key   = "env"
	value = "fv"
)

// Byf is a simple wrapper around By.
func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...)) // ignore_by_check
}

func randomString() string {
	const length = 10
	return "a" + util.RandomString(length)
}

func getEventBasedAddOn(namePrefix, eventSourceName string, clusterLabels map[string]string,
	policyRefs []libsveltosv1alpha1.PolicyRef) *v1alpha1.EventBasedAddOn {

	selector := ""
	for k := range clusterLabels {
		if selector != "" {
			selector += ","
		}
		selector += fmt.Sprintf("%s=%s", k, clusterLabels[k])
	}
	resource := &v1alpha1.EventBasedAddOn{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: v1alpha1.EventBasedAddOnSpec{
			ClusterSelector: libsveltosv1alpha1.Selector(selector),
			EventSourceName: eventSourceName,
			PolicyRefs:      policyRefs,
		},
	}

	return resource
}

// getKindWorkloadClusterKubeconfig returns client to access the kind cluster used as workload cluster
func getKindWorkloadClusterKubeconfig() (client.Client, error) {
	kubeconfigPath := "workload_kubeconfig" // this file is created in this directory by Makefile during cluster creation
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: scheme})
}

func verifyClusterSummary(clusterProfile *configv1alpha1.ClusterProfile,
	clusterNamespace, clusterName string) *configv1alpha1.ClusterSummary {

	Byf("Verifying ClusterSummary is created")
	Eventually(func() bool {
		clusterSummary, err := getClusterSummary(context.TODO(),
			clusterProfile.Name, clusterNamespace, clusterName)
		return err == nil &&
			clusterSummary != nil
	}, timeout, pollingInterval).Should(BeTrue())

	clusterSummary, err := getClusterSummary(context.TODO(),
		clusterProfile.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	Byf("Verifying ClusterSummary configuration")
	Eventually(func() error {
		var currentClusterSummary *configv1alpha1.ClusterSummary
		currentClusterSummary, err = getClusterSummary(context.TODO(),
			clusterProfile.Name, clusterNamespace, clusterName)
		if err != nil {
			return err
		}

		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			clusterProfile.Spec.HelmCharts) {

			return fmt.Errorf("helmCharts do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs,
			clusterProfile.Spec.PolicyRefs) {

			return fmt.Errorf("policyRefs do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterNamespace, clusterNamespace) {
			return fmt.Errorf("clusterNamespace does not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterName, clusterName) {
			return fmt.Errorf("clusterName does not match")
		}
		return nil
	}, timeout, pollingInterval).Should(BeNil())

	clusterSummary, err = getClusterSummary(context.TODO(),
		clusterProfile.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	return clusterSummary
}

func verifyFeatureStatusIsProvisioned(clusterSummaryNamespace, clusterSummaryName string, featureID configv1alpha1.FeatureID) {
	Eventually(func() bool {
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName},
			currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == configv1alpha1.FeatureStatusProvisioned {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func getClusterSummary(ctx context.Context,
	clusterProfileName, clusterNamespace, clusterName string) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			sveltosmanagercontrollers.ClusterProfileLabelName: clusterProfileName,
			configv1alpha1.ClusterNameLabel:                   clusterName,
			configv1alpha1.ClusterTypeLabel:                   string(libsveltosv1alpha1.ClusterTypeCapi),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := k8sClient.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1alpha1.GroupVersion.Group, Resource: configv1alpha1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterProfileName)
	}

	return &clusterSummaryList.Items[0], nil
}
