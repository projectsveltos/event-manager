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
	conversion "k8s.io/apimachinery/pkg/conversion"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

func Convert_v1beta1_HelmChart_To_v1alpha1_HelmChart(src *configv1beta1.HelmChart, dst *configv1alpha1.HelmChart, s conversion.Scope,
) error {

	return configv1alpha1.Convert_v1beta1_HelmChart_To_v1alpha1_HelmChart(src, dst, s)
}

func Convert_v1alpha1_HelmChart_To_v1beta1_HelmChart(src *configv1alpha1.HelmChart, dst *configv1beta1.HelmChart, s conversion.Scope,
) error {

	return configv1alpha1.Convert_v1alpha1_HelmChart_To_v1beta1_HelmChart(src, dst, s)
}
