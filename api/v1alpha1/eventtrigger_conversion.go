/*
Copyright 2024. projectsveltos.io. All rights reserved.

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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryconversion "k8s.io/apimachinery/pkg/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v1beta1 "github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// ConvertTo converts v1alpha1 to the Hub version (v1beta1).
func (src *EventTrigger) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1beta1.EventTrigger)

	err := Convert_v1alpha1_EventTrigger_To_v1beta1_EventTrigger(src, dst, nil)
	if err != nil {
		return err
	}

	if src.Spec.SourceClusterSelector == "" {
		dst.Spec.SourceClusterSelector.LabelSelector = metav1.LabelSelector{}
	}

	if src.Spec.DestinationClusterSelector == nil {
		dst.Spec.DestinationClusterSelector.LabelSelector = metav1.LabelSelector{}
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this v1alpha1.
func (dst *EventTrigger) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1beta1.EventTrigger)

	err := Convert_v1beta1_EventTrigger_To_v1alpha1_EventTrigger(src, dst, nil)
	if err != nil {
		return err
	}

	if src.Spec.SourceClusterSelector.MatchLabels == nil {
		dst.Spec.SourceClusterSelector = ""
	}

	if src.Spec.DestinationClusterSelector.MatchLabels == nil {
		dst.Spec.DestinationClusterSelector = nil
	}

	return nil
}

func Convert_v1alpha1_EventTriggerSpec_To_v1beta1_EventTriggerSpec(srcSpec *EventTriggerSpec,
	dstSpec *v1beta1.EventTriggerSpec, scope apimachineryconversion.Scope) error {

	if err := autoConvert_v1alpha1_EventTriggerSpec_To_v1beta1_EventTriggerSpec(srcSpec, dstSpec, nil); err != nil {
		return err
	}

	labelSelector, err := metav1.ParseToLabelSelector(string(srcSpec.SourceClusterSelector))
	if err != nil {
		return fmt.Errorf("error converting labels.Selector to metav1.Selector: %w", err)
	}
	dstSpec.SourceClusterSelector = libsveltosv1beta1.Selector{LabelSelector: *labelSelector}

	if srcSpec.DestinationClusterSelector != nil {
		labelSelector, err = metav1.ParseToLabelSelector(string(*srcSpec.DestinationClusterSelector))
		if err != nil {
			return fmt.Errorf("error converting labels.Selector to metav1.Selector: %w", err)
		}
		dstSpec.DestinationClusterSelector = libsveltosv1beta1.Selector{LabelSelector: *labelSelector}
	}

	return nil
}

func Convert_v1beta1_EventTriggerSpec_To_v1alpha1_EventTriggerSpec(srcSpec *v1beta1.EventTriggerSpec, dstSpec *EventTriggerSpec,
	scope apimachineryconversion.Scope,
) error {

	if err := autoConvert_v1beta1_EventTriggerSpec_To_v1alpha1_EventTriggerSpec(srcSpec, dstSpec, nil); err != nil {
		return err
	}

	labelSelector, err := srcSpec.SourceClusterSelector.ToSelector()
	if err != nil {
		return fmt.Errorf("failed to convert : %w", err)
	}

	dstSpec.SourceClusterSelector = libsveltosv1alpha1.Selector(labelSelector.String())

	labelSelector, err = srcSpec.DestinationClusterSelector.ToSelector()
	if err != nil {
		return fmt.Errorf("failed to convert : %w", err)
	}

	selector := libsveltosv1alpha1.Selector(labelSelector.String())
	dstSpec.DestinationClusterSelector = &selector

	return nil
}
