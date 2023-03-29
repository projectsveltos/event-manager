/*
Copyright 2023.

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	// EventBasedAddOnFinalizer allows Reconcilers to clean up resources associated with
	// EventBasedAddOn before removing it from the apiserver.
	EventBasedAddOnFinalizer = "eventbasedaddon.finalizer.projectsveltos.io"

	EventBasedAddOnKind = "EventBasedAddOn"

	FeatureEventBasedAddOn = "EventBasedAddOn"
)

// EventBasedAddOnSpec defines the desired state of EventBasedAddOn
type EventBasedAddOnSpec struct {
	// SourceClusterSelector identifies clusters to associate to.
	// This represents the set of clusters where Sveltos will watch for
	// events defined by referenced EventSource
	SourceClusterSelector libsveltosv1alpha1.Selector `json:"sourceClusterSelector"`

	// Multiple resources in a managed cluster can be a match for referenced
	// EventSource. OneForEvent indicates whether a ClusterProfile for all
	// resource (OneForEvent = false) or one per resource (OneForEvent = true)
	// needs to be creted.
	// +optional
	OneForEvent bool `json:"oneForEvent,omitempty"`

	// EventSourceName is the name of the referenced EventSource.
	// Resources contained in the referenced ConfigMaps/Secrets and HelmCharts
	// will be customized using information from resources matching the EventSource
	// in the managed cluster.
	EventSourceName string `json:"eventSourceName"`

	// DestinationClusterSelector identifies the cluster where add-ons will be deployed.
	// By default, this is nil and add-ons will be deployed in the very same cluster the
	// event happened.
	// If DestinationClusterSelector is set though, when an event happens in any of the
	// cluster identified by SourceClusterSelector, add-ons will be deployed in each of
	// the cluster indentified by DestinationClusterSelector.
	// +omitempty
	DestinationClusterSelector *libsveltosv1alpha1.Selector `json:"destinationClusterSelector,omitempty"`

	// SyncMode specifies how features are synced in a matching workload cluster.
	// - OneTime means, first time a workload cluster matches the ClusterProfile,
	// features will be deployed in such cluster. Any subsequent feature configuration
	// change won't be applied into the matching workload clusters;
	// - Continuous means first time a workload cluster matches the ClusterProfile,
	// features will be deployed in such a cluster. Any subsequent feature configuration
	// change will be applied into the matching workload clusters.
	// - DryRun means no change will be propagated to any matching cluster. A report
	// instead will be generated summarizing what would happen in any matching cluster
	// because of the changes made to ClusterProfile while in DryRun mode.
	// +kubebuilder:default:=Continuous
	// +optional
	SyncMode configv1alpha1.SyncMode `json:"syncMode,omitempty"`

	// StopMatchingBehavior indicates what behavior should be when a Cluster stop matching
	// the ClusterProfile. By default all deployed Helm charts and Kubernetes resources will
	// be withdrawn from Cluster. Setting StopMatchingBehavior to LeavePolicies will instead
	// leave ClusterProfile deployed policies in the Cluster.
	// +kubebuilder:default:=WithdrawPolicies
	// +optional
	StopMatchingBehavior configv1alpha1.StopMatchingBehavior `json:"stopMatchingBehavior,omitempty"`

	// PolicyRefs references all the ConfigMaps/Secrets containing kubernetes resources
	// that need to be deployed in the matching clusters based on EventSource.
	// +optional
	PolicyRefs []libsveltosv1alpha1.PolicyRef `json:"policyRefs,omitempty"`

	// Helm charts to be deployed in the matching clusters based on EventSource.
	HelmCharts []configv1alpha1.HelmChart `json:"helmCharts,omitempty"`
}

// EventBasedAddOnStatus defines the observed state of EventBasedAddOn
type EventBasedAddOnStatus struct {
	// MatchingClusterRefs reference all the cluster-api Cluster currently matching
	// ClusterProfile SourceClusterSelector
	// +optional
	MatchingClusterRefs []corev1.ObjectReference `json:"matchingClusters,omitempty"`

	// DestinationMatchingClusterRefs reference all the cluster-api Cluster currently matching
	// ClusterProfile DestinationClusterSelector
	// +optional
	DestinationMatchingClusterRefs []corev1.ObjectReference `json:"destinationMatchingClusterRefs,omitempty"`

	// ClusterInfo represent the deployment status in each managed
	// cluster.
	// +optional
	ClusterInfo []libsveltosv1alpha1.ClusterInfo `json:"clusterInfo,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=eventbasedaddons,scope=Cluster
//+kubebuilder:subresource:status

// EventBasedAddOn is the Schema for the eventbasedaddons API
type EventBasedAddOn struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EventBasedAddOnSpec   `json:"spec,omitempty"`
	Status EventBasedAddOnStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EventBasedAddOnList contains a list of EventBasedAddOn
type EventBasedAddOnList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EventBasedAddOn `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EventBasedAddOn{}, &EventBasedAddOnList{})
}
