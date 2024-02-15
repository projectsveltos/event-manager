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
	"k8s.io/apimachinery/pkg/util/intstr"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	// EventTriggerFinalizer allows Reconcilers to clean up resources associated with
	// EventTrigger before removing it from the apiserver.
	EventTriggerFinalizer = "eventtrigger.finalizer.projectsveltos.io"

	EventTriggerKind = "EventTrigger"

	FeatureEventTrigger = "EventTrigger"
)

// EventTriggerSpec defines the desired state of EventTrigger
type EventTriggerSpec struct {
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

	// The maximum number of clusters that can be updated concurrently.
	// Value can be an absolute number (ex: 5) or a percentage of desired pods (ex: 10%).
	// Defaults to 100%.
	// Example: when this is set to 30%, when list of add-ons/applications in ClusterProfile
	// changes, only 30% of matching clusters will be updated in parallel. Only when updates
	// in those cluster succeed, other matching clusters are updated.
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:validation:Pattern="^((100|[0-9]{1,2})%|[0-9]+)$"
	// +optional
	MaxUpdate *intstr.IntOrString `json:"maxUpdate,omitempty"`

	// StopMatchingBehavior indicates what behavior should be when a Cluster stop matching
	// the ClusterProfile. By default all deployed Helm charts and Kubernetes resources will
	// be withdrawn from Cluster. Setting StopMatchingBehavior to LeavePolicies will instead
	// leave ClusterProfile deployed policies in the Cluster.
	// +kubebuilder:default:=WithdrawPolicies
	// +optional
	StopMatchingBehavior configv1alpha1.StopMatchingBehavior `json:"stopMatchingBehavior,omitempty"`

	// Reloader indicates whether Deployment/StatefulSet/DaemonSet instances deployed
	// by Sveltos and part of this ClusterProfile need to be restarted via rolling upgrade
	// when a ConfigMap/Secret instance mounted as volume is modified.
	// When set to true, when any mounted ConfigMap/Secret is modified, Sveltos automatically
	// starts a rolling upgrade for Deployment/StatefulSet/DaemonSet instances mounting it.
	// +kubebuilder:default:=false
	// +optional
	Reloader bool `json:"reloader,omitempty"`

	// TemplateResourceRefs is a list of resource to collect from the management cluster.
	// Those resources' values will be used to instantiate templates contained in referenced
	// PolicyRefs and Helm charts
	// +patchMergeKey=identifier
	// +patchStrategy=merge,retainKeys
	// +optional
	TemplateResourceRefs []configv1alpha1.TemplateResourceRef `json:"templateResourceRefs,omitempty"`

	// PolicyRefs references all the ConfigMaps/Secrets containing kubernetes resources
	// that need to be deployed in the matching clusters based on EventSource.
	// +optional
	PolicyRefs []configv1alpha1.PolicyRef `json:"policyRefs,omitempty"`

	// Helm charts to be deployed in the matching clusters based on EventSource.
	HelmCharts []configv1alpha1.HelmChart `json:"helmCharts,omitempty"`

	// Kustomization refs
	KustomizationRefs []configv1alpha1.KustomizationRef `json:"kustomizationRefs,omitempty"`

	// ValidateHealths is a slice of Lua functions to run against
	// the managed cluster to validate the state of those add-ons/applications
	// is healthy
	// +optional
	ValidateHealths []configv1alpha1.ValidateHealth `json:"validateHealths,omitempty"`

	// ExtraLabels: These labels will be added by Sveltos to all Kubernetes resources deployed in
	// a managed cluster based on this ClusterProfile/Profile instance.
	// **Important:** If a resource deployed by Sveltos already has a label with a key present in
	// `ExtraLabels`, the value from `ExtraLabels` will override the existing value.
	// +optional
	ExtraLabels map[string]string `json:"extraLabels,omitempty"`

	// ExtraAnnotations: These annotations will be added by Sveltos to all Kubernetes resources
	// deployed in a managed cluster based on this ClusterProfile/Profile instance.
	// **Important:** If a resource deployed by Sveltos already has a annotation with a key present in
	// `ExtraAnnotations`, the value from `ExtraAnnotations` will override the existing value.
	// +optional
	ExtraAnnotations map[string]string `json:"extraAnnotations,omitempty"`
}

// EventTriggerStatus defines the observed state of EventTrigger
type EventTriggerStatus struct {
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
//+kubebuilder:resource:path=eventtriggers,scope=Cluster
//+kubebuilder:subresource:status

// EventTrigger is the Schema for the eventtriggers API
type EventTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EventTriggerSpec   `json:"spec,omitempty"`
	Status EventTriggerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EventTriggerList contains a list of EventTrigger
type EventTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EventTrigger `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EventTrigger{}, &EventTriggerList{})
}
