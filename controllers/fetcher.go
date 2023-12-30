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
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/projectsveltos/event-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// fetchReferencedResources fetches resources referenced by EventTrigger.
// This includes:
// - EventSource and corresponding EventReports (from the passed in cluster only);
// - ConfigMaps/Secrets
func fetchReferencedResources(ctx context.Context, c client.Client,
	e *v1alpha1.EventTrigger, cluster *corev1.ObjectReference, logger logr.Logger) ([]client.Object, error) {

	result := make([]client.Object, 0)

	if e == nil {
		return nil, fmt.Errorf("nil EventTrigger")
	}

	logger.V(logs.LogDebug).Info("fetch EventSource")
	resource, err := fetchEventSource(ctx, c, e.Spec.EventSourceName)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		// If there is no EventSource, nothing to do
		return nil, nil
	}

	result = append(result, resource)

	logger.V(logs.LogDebug).Info("fetch EventReports")
	var eventReports *libsveltosv1alpha1.EventReportList
	eventReports, err = fetchEventReports(ctx, c, cluster.Namespace, cluster.Name, resource.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		return nil, err
	}
	for i := range eventReports.Items {
		result = append(result, &eventReports.Items[i])
	}

	logger.V(logs.LogDebug).Info("fetch referenced ConfigMaps/Secrets")
	var resources []client.Object
	resources, err = fetchPolicyRefs(ctx, c, e, cluster, logger)
	if err != nil {
		return nil, err
	}
	result = append(result, resources...)

	return result, nil
}

// fetchEventSource fetches referenced EventSource
func fetchEventSource(ctx context.Context, c client.Client, eventSourceName string,
) (*libsveltosv1alpha1.EventSource, error) {

	eventSource := &libsveltosv1alpha1.EventSource{}
	err := c.Get(ctx, types.NamespacedName{Name: eventSourceName}, eventSource)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return eventSource, nil
}

// fetchEventReports returns eventReports for given EventSource in a given cluster
func fetchEventReports(ctx context.Context, c client.Client, clusterNamespace, clusterName, eventSourceName string,
	clusterType libsveltosv1alpha1.ClusterType) (*libsveltosv1alpha1.EventReportList, error) {

	labels := libsveltosv1alpha1.GetEventReportLabels(eventSourceName, clusterName, &clusterType)

	// Fecth all ClusterSummary for this Cluster
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels(labels),
	}

	eventReportList := &libsveltosv1alpha1.EventReportList{}
	err := c.List(ctx, eventReportList, listOptions...)
	return eventReportList, err
}

// fetchPolicyRefs fetches referenced ConfigMaps/Secrets
func fetchPolicyRefs(ctx context.Context, c client.Client, e *v1alpha1.EventTrigger,
	cluster *corev1.ObjectReference, logger logr.Logger) ([]client.Object, error) {

	result := make([]client.Object, 0)

	for i := range e.Spec.PolicyRefs {
		policyRef := &e.Spec.PolicyRefs[i]
		var err error
		var object client.Object

		namespace := getReferenceResourceNamespace(cluster.Namespace, policyRef.Namespace)

		if policyRef.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
			object, err = getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: policyRef.Name})
		} else {
			object, err = getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: policyRef.Name})
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
					policyRef.Kind, policyRef.Namespace, policyRef.Name))
				continue
			}
			return nil, err
		}
		result = append(result, object)
	}

	return result, nil
}

// getReferenceResourceNamespace returns the namespace to use for a referenced resource.
// If namespace is set on referencedResource, that namespace will be used.
// If namespace is not set, cluster namespace will be used
func getReferenceResourceNamespace(clusterNamespace, referencedResourceNamespace string) string {
	if referencedResourceNamespace != "" {
		return referencedResourceNamespace
	}

	return clusterNamespace
}

// getConfigMap retrieves any ConfigMap from the given name and namespace.
func getConfigMap(ctx context.Context, c client.Client, configmapName types.NamespacedName,
) (*corev1.ConfigMap, error) {

	configMap := &corev1.ConfigMap{}
	configMapKey := client.ObjectKey{
		Namespace: configmapName.Namespace,
		Name:      configmapName.Name,
	}
	if err := c.Get(ctx, configMapKey, configMap); err != nil {
		return nil, err
	}

	return configMap, nil
}

// getSecret retrieves any Secret from the given secret name and namespace.
func getSecret(ctx context.Context, c client.Client, secretName types.NamespacedName,
) (*corev1.Secret, error) {

	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: secretName.Namespace,
		Name:      secretName.Name,
	}
	if err := c.Get(ctx, secretKey, secret); err != nil {
		return nil, err
	}

	if secret.Type != libsveltosv1alpha1.ClusterProfileSecretType {
		return nil, libsveltosv1alpha1.ErrSecretTypeNotSupported
	}

	return secret, nil
}
