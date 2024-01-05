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

package scope

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

// EventTriggerScopeParams defines the input parameters used to create a new EventTrigger Scope.
type EventTriggerScopeParams struct {
	Client         client.Client
	Logger         logr.Logger
	EventTrigger   *v1alpha1.EventTrigger
	ControllerName string
}

// NewEventTriggerScope creates a new EventTrigger Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewEventTriggerScope(params EventTriggerScopeParams) (*EventTriggerScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a EventTriggerScope")
	}
	if params.EventTrigger == nil {
		return nil, errors.New("failed to generate new scope from nil EventTrigger")
	}

	helper, err := patch.NewHelper(params.EventTrigger, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &EventTriggerScope{
		Logger:         params.Logger,
		client:         params.Client,
		EventTrigger:   params.EventTrigger,
		patchHelper:    helper,
		controllerName: params.ControllerName,
	}, nil
}

// EventTriggerScope defines the basic context for an actuator to operate upon.
type EventTriggerScope struct {
	logr.Logger
	client         client.Client
	patchHelper    *patch.Helper
	EventTrigger   *v1alpha1.EventTrigger
	controllerName string
}

// PatchObject persists the feature configuration and status.
func (s *EventTriggerScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.EventTrigger,
	)
}

// Close closes the current scope persisting the EventTrigger configuration and status.
func (s *EventTriggerScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the EventTrigger name.
func (s *EventTriggerScope) Name() string {
	return s.EventTrigger.Name
}

// ControllerName returns the name of the controller that
// created the EventTriggerScope.
func (s *EventTriggerScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *EventTriggerScope) GetSelector() string {
	return string(s.EventTrigger.Spec.SourceClusterSelector)
}

// SetMatchingClusterRefs sets the MatchingClusterRefs status.
func (s *EventTriggerScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	s.EventTrigger.Status.MatchingClusterRefs = matchingClusters
}

// SetClusterInfo sets the ClusterInfo status.
func (s *EventTriggerScope) SetClusterInfo(clusterInfo []libsveltosv1alpha1.ClusterInfo) {
	s.EventTrigger.Status.ClusterInfo = clusterInfo
}
