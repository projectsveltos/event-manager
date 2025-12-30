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

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

var (
	RemoveEventReports                       = removeEventReports
	RemoveEventReportsFromCluster            = removeEventReportsFromCluster
	CollectAndProcessEventReportsFromCluster = collectAndProcessEventReportsFromCluster
	DeleteEventReport                        = deleteEventReport
	ProcessEventTriggerForCluster            = processEventTriggerForCluster
)

var (
	RequeueEventTriggerForCluster = (*EventTriggerReconciler).requeueEventTriggerForCluster
	RequeueEventTriggerForMachine = (*EventTriggerReconciler).requeueEventTriggerForMachine
	UpdateClusterInfo             = (*EventTriggerReconciler).updateClusterInfo
	CleanMaps                     = (*EventTriggerReconciler).cleanMaps
	UpdateMaps                    = (*EventTriggerReconciler).updateMaps
	ProcessEventTrigger           = (*EventTriggerReconciler).processEventTrigger
	GetClustersFromClusterSets    = (*EventTriggerReconciler).getClustersFromClusterSets

	GetKeyFromObject      = getKeyFromObject
	GetHandlersForFeature = getHandlersForFeature
	GetConsumersForEntry  = getConsumersForEntry

	GetCloudEvents = getCloudEvents
	GetResources   = getResources
)

type (
	CurrentObject  = currentObject
	CurrentObjects = currentObjects
)

var (
	EventTriggerHash                = eventTriggerHash
	RemoveStaleEventSources         = removeStaleEventSources
	RemoveStaleEventReports         = removeStaleEventReports
	DeployEventSource               = deployEventSource
	AppendCloudEventClusterProfiles = appendCloudEventClusterProfiles

	InstantiateOneClusterProfilePerAllResource = instantiateOneClusterProfilePerAllResource
	InstantiateOneClusterProfilePerResource    = instantiateOneClusterProfilePerResource
	RemoveClusterProfiles                      = removeClusterProfiles

	InstantiateCloudEventAction = instantiateCloudEventAction
	PrepareCurrentObjectList    = prepareCurrentObjectList

	InstantiateFromGeneratorsPerResource = instantiateFromGeneratorsPerResource
	DeleteInstantiatedFromGenerators     = deleteInstantiatedFromGenerators

	GetInstantiatedObjectLabels = getInstantiatedObjectLabels
	GetClusterProfileName       = getClusterProfileName

	InstantiateReferencedPolicyRefs = instantiateReferencedPolicyRefs
	InstantiateDataSection          = instantiateDataSection
	RemoveConfigMaps                = removeConfigMaps
	RemoveSecrets                   = removeSecrets
	UnstructuredToTyped             = unstructuredToTyped

	BuildEventTriggersForEventSourceMap = buildEventTriggersForEventSourceMap
	BuildEventTriggersForClusterMap     = buildEventTriggersForClusterMap
	ShouldIgnore                        = shouldIgnore
	ShouldReprocess                     = shouldReprocess
	IsEventTriggerMatchingTheCluster    = isEventTriggerMatchingTheCluster
)

var (
	GetTrackerInstance       = getTrackerInstance
	StopTrackingConsumer     = (*tracker).stopTrackingConsumer
	TrackResourceForConsumer = (*tracker).trackResourceForConsumer
	GetConsumersForResource  = (*tracker).getConsumersForResource
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

func SetSchema(s *runtime.Scheme) {
	mgmtClusterSchema = s
}

func SetConfig(config *rest.Config) {
	mgmtClusterConfig = config
}
