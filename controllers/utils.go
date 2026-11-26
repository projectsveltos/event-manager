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
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	version string
)

// logExpectedFailure logs err at debug level when isExpected(err) is true, as an error otherwise.
// Some failures are an expected outcome of a race: logging those as errors only adds noise.
func logExpectedFailure(logger logr.Logger, err error, isExpected func(error) bool, msg string) {
	if isExpected(err) {
		logger.V(logs.LogDebug).Info(msg, "reason", err.Error())
		return
	}

	logger.V(logs.LogInfo).Error(err, msg)
}

// logFetchFailure logs a failure to fetch an object. An object which does not exist is not logged as an error:
// it is expected, for instance when a cluster is deleted while an EventTrigger is being processed.
func logFetchFailure(logger logr.Logger, err error, msg string) {
	logExpectedFailure(logger, err, apierrors.IsNotFound, msg)
}

// logUpdateFailure logs a failure to update an object. A conflict is not logged as an error: someone else changed
// the object after it was read, so the update is not applied and the object is processed again.
func logUpdateFailure(logger logr.Logger, err error, msg string) {
	logExpectedFailure(logger, err, apierrors.IsConflict, msg)
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

func InitScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(1)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}
}

// getKeyFromObject returns the Key that can be used in the internal reconciler maps.
func getKeyFromObject(scheme *runtime.Scheme, obj client.Object) *corev1.ObjectReference {
	addTypeInformationToObject(scheme, obj)

	apiVersion, kind := obj.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	return &corev1.ObjectReference{
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		Kind:       kind,
		APIVersion: apiVersion,
	}
}

func getPolicyRef(o client.Object) *libsveltosv1beta1.PolicyRef {
	policyRef := &libsveltosv1beta1.PolicyRef{
		Namespace: o.GetNamespace(),
		Name:      o.GetName(),
	}

	switch o.(type) {
	case *corev1.ConfigMap:
		policyRef.Kind = string(libsveltosv1beta1.ConfigMapReferencedResourceKind)
	case *corev1.Secret:
		policyRef.Kind = string(libsveltosv1beta1.SecretReferencedResourceKind)
	default:
		panic(1)
	}

	return policyRef
}

func SetVersion(v string) {
	version = v
}

func getVersion() string {
	return version
}

func stringPtr(s string) *string {
	return &s
}
