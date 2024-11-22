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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	v1beta1 "github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

// fetchReferencedResources fetches resources referenced by EventTrigger.
// This includes:
// - EventSource and corresponding EventReports (from the passed in cluster only);
// - ConfigMaps referenced in the ConfigMapGenerator section, in the PolicyRefs section and ValuesFrom
// - Secrets referenced in the SecretGenerator section, in the PolicyRefs section and ValuesFrom
func fetchReferencedResources(ctx context.Context, c client.Client,
	e *v1beta1.EventTrigger, cluster *corev1.ObjectReference, logger logr.Logger) ([]client.Object, error) {

	result := make([]client.Object, 0)

	if e == nil {
		return nil, fmt.Errorf("nil EventTrigger")
	}

	logger.V(logs.LogDebug).Info("fetch EventSource")
	resource, err := fetchEventSource(ctx, c, cluster.Namespace, cluster.Name, e.Spec.EventSourceName,
		clusterproxy.GetClusterType(cluster), logger)
	if err != nil {
		return nil, err
	}
	if resource == nil {
		// If there is no EventSource, nothing to do
		return nil, nil
	}

	result = append(result, resource)

	logger.V(logs.LogDebug).Info("fetch EventReports")
	var eventReports *libsveltosv1beta1.EventReportList
	eventReports, err = fetchEventReports(ctx, c, cluster.Namespace, cluster.Name, resource.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		return nil, err
	}

	for i := range eventReports.Items {
		result = append(result, &eventReports.Items[i])
	}

	clusterType := clusterproxy.GetClusterType(cluster)
	clusterObj, err := fecthClusterObjects(ctx, c, cluster.Namespace, cluster.Name, clusterType, logger)
	if err == nil {
		objects := currentObjects{
			Cluster: clusterObj,
		}

		templateName := getTemplateName(cluster.Namespace, cluster.Name, e.Name)

		referencedResources, err := collectResourcesFromConfigMapGenerators(ctx, c, objects, e,
			cluster.Namespace, templateName, logger)
		if err != nil {
			return nil, err
		}
		result = append(result, referencedResources...)

		referencedResources, err = collectResourcesFromSecretGenerators(ctx, c, objects, e,
			cluster.Namespace, templateName, logger)
		if err != nil {
			return nil, err
		}
		result = append(result, referencedResources...)

		local, remote, _ := fetchPolicyRefs(ctx, c, e, cluster, objects, templateName, logger)
		result = append(result, local...)
		result = append(result, remote...)

		for i := range e.Spec.HelmCharts {
			valuesFrom := getValuesFrom(ctx, c, e.Spec.HelmCharts[i].ValuesFrom, cluster.Namespace,
				templateName, objects, logger)
			result = append(result, valuesFrom...)
		}

		for i := range e.Spec.KustomizationRefs {
			valuesFrom := getValuesFrom(ctx, c, e.Spec.KustomizationRefs[i].ValuesFrom, cluster.Namespace,
				templateName, objects, logger)
			result = append(result, valuesFrom...)
		}
	}

	return result, nil
}

func collectResourcesFromConfigMapGenerators(ctx context.Context, c client.Client, objects any,
	e *v1beta1.EventTrigger, templateName, clusterNamespace string, logger logr.Logger) ([]client.Object, error) {

	results := make([]client.Object, len(e.Spec.ConfigMapGenerator))

	for i := range e.Spec.ConfigMapGenerator {
		generator := &e.Spec.ConfigMapGenerator[i]
		namespace := libsveltostemplate.GetReferenceResourceNamespace(clusterNamespace, generator.Namespace)

		// The name of the referenced resource can be expressed as a template
		referencedName, err := instantiateSection(templateName, []byte(generator.Name), objects, logger)
		if err != nil {
			return nil, err
		}

		var referencedResource client.Object
		referencedResource, err = getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
		if err != nil {
			return nil, err
		}

		results[i] = referencedResource
	}

	return results, nil
}

func collectResourcesFromSecretGenerators(ctx context.Context, c client.Client, objects any,
	e *v1beta1.EventTrigger, templateName, clusterNamespace string, logger logr.Logger) ([]client.Object, error) {

	results := make([]client.Object, len(e.Spec.SecretGenerator))

	for i := range e.Spec.SecretGenerator {
		generator := &e.Spec.SecretGenerator[i]
		namespace := libsveltostemplate.GetReferenceResourceNamespace(clusterNamespace, generator.Namespace)

		// The name of the referenced resource can be expressed as a template
		referencedName, err := instantiateSection(templateName, []byte(generator.Name), objects, logger)
		if err != nil {
			return nil, err
		}

		var referencedResource client.Object
		referencedResource, err = getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
		if err != nil {
			return nil, err
		}

		results[i] = referencedResource
	}

	return results, nil
}

// fetchEventSource fetches referenced EventSource
func fetchEventSource(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, eventSourceName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) (*libsveltosv1beta1.EventSource, error) {

	instantiatedEventSourceName, err := libsveltostemplate.GetReferenceResourceName(clusterNamespace, clusterName,
		string(clusterType), eventSourceName)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get EventSource Name %s: %v",
			eventSourceName, err))
		return nil, err
	}

	eventSource := &libsveltosv1beta1.EventSource{}
	err = c.Get(ctx, types.NamespacedName{Name: instantiatedEventSourceName}, eventSource)
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
	clusterType libsveltosv1beta1.ClusterType) (*libsveltosv1beta1.EventReportList, error) {

	labels := libsveltosv1beta1.GetEventReportLabels(eventSourceName, clusterName, &clusterType)

	// Fecth all ClusterSummary for this Cluster
	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels(labels),
	}

	eventReportList := &libsveltosv1beta1.EventReportList{}
	err := c.List(ctx, eventReportList, listOptions...)
	return eventReportList, err
}

// fetchPolicyRefs fetches referenced ConfigMaps/Secrets
func fetchPolicyRefs(ctx context.Context, c client.Client, e *v1beta1.EventTrigger,
	cluster *corev1.ObjectReference, objects any, templateName string, logger logr.Logger,
) (local, remote []client.Object, err error) {

	local = make([]client.Object, 0)
	remote = make([]client.Object, 0)

	for i := range e.Spec.PolicyRefs {
		policyRef := &e.Spec.PolicyRefs[i]
		var err error
		var object client.Object

		namespace := libsveltostemplate.GetReferenceResourceNamespace(cluster.Namespace, policyRef.Namespace)

		referencedName, err := instantiateSection(templateName, []byte(policyRef.Name), objects, logger)
		if err != nil {
			return nil, nil, err
		}

		if policyRef.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			object, err = getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
		} else {
			object, err = getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: string(referencedName)})
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
					policyRef.Kind, namespace, referencedName))
				return nil, nil, fmt.Errorf("referenced %s %s/%s does not exist",
					policyRef.Kind, namespace, string(referencedName))
			}
			return nil, nil, err
		}

		if policyRef.DeploymentType == configv1beta1.DeploymentTypeLocal {
			local = append(local, object)
		} else {
			remote = append(remote, object)
		}
	}

	return local, remote, nil
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

	if secret.Type != libsveltosv1beta1.ClusterProfileSecretType {
		return nil, libsveltosv1beta1.ErrSecretTypeNotSupported
	}

	return secret, nil
}
