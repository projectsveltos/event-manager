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

package controllers

var (
	RemoveEventReports                       = removeEventReports
	RemoveEventReportsFromCluster            = removeEventReportsFromCluster
	CollectAndProcessEventReportsFromCluster = collectAndProcessEventReportsFromCluster
	DeleteEventReport                        = deleteEventReport
	ProcessEventBasedAddOnForCluster         = processEventBasedAddOnForCluster
)

var (
	RequeueEventBasedAddOnForCluster = (*EventBasedAddOnReconciler).requeueEventBasedAddOnForCluster
	RequeueEventBasedAddOnForMachine = (*EventBasedAddOnReconciler).requeueEventBasedAddOnForMachine
	UpdateClusterInfo                = (*EventBasedAddOnReconciler).updateClusterInfo
	CleanMaps                        = (*EventBasedAddOnReconciler).cleanMaps
	UpdateMaps                       = (*EventBasedAddOnReconciler).updateMaps
	GetEventSourceMapForEntry        = (*EventBasedAddOnReconciler).getEventSourceMapForEntry
	GetReferenceMapForEntry          = (*EventBasedAddOnReconciler).getReferenceMapForEntry
	GetClusterMapForEntry            = (*EventBasedAddOnReconciler).getClusterMapForEntry
	IsClusterEntryRemoved            = (*EventBasedAddOnReconciler).isClusterEntryRemoved
	ProcessEventBasedAddOn           = (*EventBasedAddOnReconciler).processEventBasedAddOn
	UpdateReferencedResourceMap      = (*EventBasedAddOnReconciler).updateReferencedResourceMap

	GetKeyFromObject      = getKeyFromObject
	GetHandlersForFeature = getHandlersForFeature
)

type (
	CurrentObject  = currentObject
	CurrentObjects = currentObjects
)

var (
	RemoveClusterInfoEntry  = removeClusterInfoEntry
	EventBasedAddOnHash     = eventBasedAddOnHash
	RemoveStaleEventSources = removeStaleEventSources
	DeployEventSource       = deployEventSource

	InstantiateOneClusterProfilePerAllResource = instantiateOneClusterProfilePerAllResource
	InstantiateOneClusterProfilePerResource    = instantiateOneClusterProfilePerResource
	RemoveClusterProfiles                      = removeClusterProfiles

	GetInstantiatedObjectLabels = getInstantiatedObjectLabels
	GetClusterProfileName       = getClusterProfileName

	InstantiateReferencedPolicies = instantiateReferencedPolicies
	InstantiateDataSection        = instantiateDataSection
	RemoveConfigMaps              = removeConfigMaps
	RemoveSecrets                 = removeSecrets
	UnstructuredToTyped           = unstructuredToTyped
)

const (
	ReferencedResourceNamespaceLabel = referencedResourceNamespaceLabel
	ReferencedResourceNameLabel      = referencedResourceNameLabel
)

// fetcher
var (
	GetConfigMap             = getConfigMap
	GetSecret                = getSecret
	FetchPolicyRefs          = fetchPolicyRefs
	FetchEventReports        = fetchEventReports
	FetchEventSource         = fetchEventSource
	FetchReferencedResources = fetchReferencedResources
)
