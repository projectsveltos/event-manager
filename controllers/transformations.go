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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func (r *EventBasedAddOnReconciler) requeueEventBasedAddOnForEventReport(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	eventReport := o.(*libsveltosv1alpha1.EventReport)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueEventBasedAddOnForEventReport",
		"namespace",
		eventReport.GetNamespace(),
		"eventReport",
		eventReport.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to eventReport change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Use the EventSource this EventReport is about
	eventSourceInfo := corev1.ObjectReference{APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		Kind: libsveltosv1alpha1.EventSourceKind, Name: eventReport.Spec.EventSourceName}

	// Get all EventBasedAddOns referencing this EventSource
	requests := make([]ctrl.Request, r.getEventSourceMapForEntry(&eventSourceInfo).Len())
	consumers := r.getEventSourceMapForEntry(&eventSourceInfo).Items()

	for i := range consumers {
		l := logger.WithValues("eventBasedAddOn", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing EventBasedAddOn")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	return requests
}

func (r *EventBasedAddOnReconciler) requeueEventBasedAddOnForEventSource(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	eventSource := o.(*libsveltosv1alpha1.EventSource)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueEventBasedAddOnForEventSource",
		"eventSource",
		eventSource.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to eventSource change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	eventSourceInfo := corev1.ObjectReference{APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		Kind: libsveltosv1alpha1.EventSourceKind, Name: eventSource.Name}

	// Get all EventBasedAddOns referencing this EventSource
	requests := make([]ctrl.Request, r.getEventSourceMapForEntry(&eventSourceInfo).Len())
	consumers := r.getEventSourceMapForEntry(&eventSourceInfo).Items()

	for i := range consumers {
		l := logger.WithValues("eventBasedAddOn", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing EventBasedAddOn")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	return requests
}

func (r *EventBasedAddOnReconciler) requeueEventBasedAddOnForCluster(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	cluster := o
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueEventBasedAddOnForCluster",
		"namespace",
		cluster.GetNamespace(),
		"cluster",
		cluster.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to Cluster change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: cluster.GetNamespace(), Name: cluster.GetName()}

	r.ClusterLabels[clusterInfo] = o.GetLabels()

	// Get all EventBasedAddOns previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).Len())
	consumers := r.getClusterMapForEntry(&clusterInfo).Items()

	for i := range consumers {
		l := logger.WithValues("eventBasedAddOn", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing EventBasedAddOn")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Iterate over all current EventBasedAddOn and reconcile the EventBasedAddOn now
	// matching the Cluster
	for k := range r.EventBasedAddOns {
		eventBasedAddOnSelector := r.EventBasedAddOns[k]
		parsedSelector, _ := labels.Parse(string(eventBasedAddOnSelector))
		if parsedSelector.Matches(labels.Set(cluster.GetLabels())) {
			l := logger.WithValues("eventBasedAddOn", k.Name)
			l.V(logs.LogDebug).Info("queuing EventBasedAddOn")
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name: k.Name,
				},
			})
		}
	}

	return requests
}

func (r *EventBasedAddOnReconciler) requeueEventBasedAddOnForMachine(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	machine := o.(*clusterv1.Machine)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueEventBasedAddOnForMachine",
		"namespace",
		machine.Namespace,
		"cluster",
		machine.Name,
	)

	addTypeInformationToObject(r.Scheme, machine)

	logger.V(logs.LogDebug).Info("reacting to CAPI Machine change")

	clusterLabelName, ok := machine.Labels[clusterv1.ClusterNameLabel]
	if !ok {
		logger.V(logs.LogVerbose).Info("Machine has not ClusterNameLabel")
		return nil
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterInfo := corev1.ObjectReference{APIVersion: machine.APIVersion, Kind: "Cluster", Namespace: machine.Namespace, Name: clusterLabelName}

	// Get all EventBasedAddOn previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).Len())
	consumers := r.getClusterMapForEntry(&clusterInfo).Items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Get Cluster labels
	if clusterLabels, ok := r.ClusterLabels[clusterInfo]; ok {
		// Iterate over all current EventBasedAddOn and reconcile the EventBasedAddOn now
		// matching the Cluster
		for k := range r.EventBasedAddOns {
			eventBasedAddOnSelector := r.EventBasedAddOns[k]
			parsedSelector, _ := labels.Parse(string(eventBasedAddOnSelector))
			if parsedSelector.Matches(labels.Set(clusterLabels)) {
				l := logger.WithValues("eventBasedAddOn", k.Name)
				l.V(logs.LogDebug).Info("queuing EventBasedAddOn")
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name: k.Name,
					},
				})
			}
		}
	}

	return requests
}

func (r *EventBasedAddOnReconciler) requeueEventBasedAddOnForReference(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueEventBasedAddOnForReference",
		"reference",
		o.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to configMap/secret change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Following is needed as o.GetObjectKind().GroupVersionKind().Kind is not set
	var key corev1.ObjectReference
	switch o.(type) {
	case *corev1.ConfigMap:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *corev1.Secret:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1alpha1.SecretReferencedResourceKind),
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	default:
		key = corev1.ObjectReference{
			APIVersion: o.GetObjectKind().GroupVersionKind().GroupVersion().String(),
			Kind:       o.GetObjectKind().GroupVersionKind().Kind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("referenced key: %s", key))

	requests := make([]ctrl.Request, r.getReferenceMapForEntry(&key).Len())

	consumers := r.getReferenceMapForEntry(&key).Items()
	for i := range consumers {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("requeue consumer: %s", consumers[i]))
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      consumers[i].Name,
				Namespace: consumers[i].Namespace,
			},
		}
	}

	return requests
}
