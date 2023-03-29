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

// EventBasedAddOnScopeParams defines the input parameters used to create a new EventBasedAddOn Scope.
type EventBasedAddOnScopeParams struct {
	Client          client.Client
	Logger          logr.Logger
	EventBasedAddOn *v1alpha1.EventBasedAddOn
	ControllerName  string
}

// NewEventBasedAddOnScope creates a new EventBasedAddOn Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewEventBasedAddOnScope(params EventBasedAddOnScopeParams) (*EventBasedAddOnScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a EventBasedAddOnScope")
	}
	if params.EventBasedAddOn == nil {
		return nil, errors.New("failed to generate new scope from nil EventBasedAddOn")
	}

	helper, err := patch.NewHelper(params.EventBasedAddOn, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &EventBasedAddOnScope{
		Logger:          params.Logger,
		client:          params.Client,
		EventBasedAddOn: params.EventBasedAddOn,
		patchHelper:     helper,
		controllerName:  params.ControllerName,
	}, nil
}

// EventBasedAddOnScope defines the basic context for an actuator to operate upon.
type EventBasedAddOnScope struct {
	logr.Logger
	client          client.Client
	patchHelper     *patch.Helper
	EventBasedAddOn *v1alpha1.EventBasedAddOn
	controllerName  string
}

// PatchObject persists the feature configuration and status.
func (s *EventBasedAddOnScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.EventBasedAddOn,
	)
}

// Close closes the current scope persisting the EventBasedAddOn configuration and status.
func (s *EventBasedAddOnScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the EventBasedAddOn name.
func (s *EventBasedAddOnScope) Name() string {
	return s.EventBasedAddOn.Name
}

// ControllerName returns the name of the controller that
// created the EventBasedAddOnScope.
func (s *EventBasedAddOnScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *EventBasedAddOnScope) GetSelector() string {
	return string(s.EventBasedAddOn.Spec.SourceClusterSelector)
}

// SetMatchingClusterRefs sets the MatchingClusterRefs status.
func (s *EventBasedAddOnScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	s.EventBasedAddOn.Status.MatchingClusterRefs = matchingClusters
}

// SetClusterInfo sets the ClusterInfo status.
func (s *EventBasedAddOnScope) SetClusterInfo(clusterInfo []libsveltosv1alpha1.ClusterInfo) {
	s.EventBasedAddOn.Status.ClusterInfo = clusterInfo
}
