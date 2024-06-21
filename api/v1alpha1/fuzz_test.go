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

package v1alpha1_test

import (
	"fmt"
	"testing"

	fuzz "github.com/google/gofuzz"
	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/cluster-api/util"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"

	"github.com/projectsveltos/event-manager/api/v1alpha1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

func TestFuzzyConversion(t *testing.T) {
	t.Run("for EventTrigger", utilconversion.FuzzTestFunc(utilconversion.FuzzTestFuncInput{
		Hub:         &v1beta1.EventTrigger{},
		Spoke:       &v1alpha1.EventTrigger{},
		FuzzerFuncs: []fuzzer.FuzzerFuncs{fuzzFuncs},
	}))
}

func fuzzFuncs(_ runtimeserializer.CodecFactory) []interface{} {
	return []interface{}{
		eventTriggerSourceClusterSelectorFuzzer,
		eventTriggerDestinationClusterSelectorFuzzer,
		v1beta1EventTriggerSourceSelectorFuzzer,
		v1beta1EventTriggerDestinationSelectorFuzzer,
	}
}

func eventTriggerSourceClusterSelectorFuzzer(in *v1alpha1.EventTrigger, _ fuzz.Continue) {
	in.Spec.SourceClusterSelector = libsveltosv1alpha1.Selector(
		fmt.Sprintf("%s=%s",
			randomString(), randomString(),
		))
}

func eventTriggerDestinationClusterSelectorFuzzer(in *v1alpha1.EventTrigger, _ fuzz.Continue) {
	selector := libsveltosv1alpha1.Selector(
		fmt.Sprintf("%s=%s",
			randomString(), randomString(),
		))
	in.Spec.DestinationClusterSelector = &selector
}

func v1beta1EventTriggerSourceSelectorFuzzer(in *v1beta1.EventTrigger, _ fuzz.Continue) {
	in.Spec.SourceClusterSelector = libsveltosv1beta1.Selector{
		LabelSelector: metav1.LabelSelector{
			MatchExpressions: nil,
			MatchLabels: map[string]string{
				randomString(): randomString(),
			},
		},
	}
}

func v1beta1EventTriggerDestinationSelectorFuzzer(in *v1beta1.EventTrigger, _ fuzz.Continue) {
	in.Spec.DestinationClusterSelector = libsveltosv1beta1.Selector{
		LabelSelector: metav1.LabelSelector{
			MatchExpressions: nil,
			MatchLabels: map[string]string{
				randomString(): randomString(),
			},
		},
	}
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}
