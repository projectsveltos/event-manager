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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
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

func getEventTrigger(namePrefix, eventSourceName string, clusterLabels map[string]string,
	policyRefs []configv1beta1.PolicyRef) *v1beta1.EventTrigger {

	resource := &v1beta1.EventTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: v1beta1.EventTriggerSpec{
			SourceClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: clusterLabels,
				},
			},
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

func verifyClusterSummary(clusterProfile *configv1beta1.ClusterProfile,
	clusterNamespace, clusterName string) *configv1beta1.ClusterSummary {

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
		var currentClusterSummary *configv1beta1.ClusterSummary
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

func verifyFeatureStatusIsProvisioned(clusterSummaryNamespace, clusterSummaryName string, featureID libsveltosv1beta1.FeatureID) {
	Eventually(func() bool {
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName},
			currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == libsveltosv1beta1.FeatureStatusProvisioned {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func getClusterSummary(ctx context.Context,
	clusterProfileName, clusterNamespace, clusterName string) (*configv1beta1.ClusterSummary, error) {

	clusterType := string(libsveltosv1beta1.ClusterTypeCapi)
	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		clusterType = string(libsveltosv1beta1.ClusterTypeSveltos)
	}
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			"projectsveltos.io/cluster-profile-name": clusterProfileName,
			configv1beta1.ClusterNameLabel:           clusterName,
			configv1beta1.ClusterTypeLabel:           clusterType,
		},
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	if err := k8sClient.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1beta1.GroupVersion.Group, Resource: configv1beta1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterProfileName)
	}

	return &clusterSummaryList.Items[0], nil
}

func getClusterSet(namePrefix string, clusterLabels map[string]string) *libsveltosv1beta1.ClusterSet {
	clusterSet := &libsveltosv1beta1.ClusterSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: libsveltosv1beta1.Spec{
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: clusterLabels,
				},
			},
		},
	}

	return clusterSet
}

func verifyClusterSetMatches(clusterSet *libsveltosv1beta1.ClusterSet) {
	Byf("Verifying Cluster %s/%s is a match for ClusterSet %s",
		kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), clusterSet.Name)
	Eventually(func() bool {
		currentClusterSet := &libsveltosv1beta1.ClusterSet{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)
		if err != nil {
			return false
		}
		for i := range currentClusterSet.Status.MatchingClusterRefs {
			if currentClusterSet.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.GetNamespace() &&
				currentClusterSet.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.GetName() &&
				currentClusterSet.Status.MatchingClusterRefs[i].APIVersion == kindWorkloadCluster.GetAPIVersion() {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyEventTriggerMatches(eventTrigger *v1beta1.EventTrigger) {
	Byf("Verifying Cluster %s/%s is a match for EventTrigger %s",
		kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), eventTrigger.Name)
	Eventually(func() bool {
		currentEventTrigger := &v1beta1.EventTrigger{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name}, currentEventTrigger)
		if err != nil {
			return false
		}
		for i := range currentEventTrigger.Status.MatchingClusterRefs {
			if currentEventTrigger.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.GetNamespace() &&
				currentEventTrigger.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.GetName() &&
				currentEventTrigger.Status.MatchingClusterRefs[i].APIVersion == kindWorkloadCluster.GetAPIVersion() {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}
