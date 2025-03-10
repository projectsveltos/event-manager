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
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// EventReportPredicates predicates for EventReport. EventTriggerReconciler watches sveltos
// EventReport events and react to those by reconciling itself based on following predicates
func EventReportPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newER := e.ObjectNew.(*libsveltosv1beta1.EventReport)
			oldER := e.ObjectOld.(*libsveltosv1beta1.EventReport)
			log := logger.WithValues("predicate", "updateEvent",
				"namespace", newER.Namespace,
				"eventReport", newER.Name,
			)

			if oldER == nil {
				log.V(logs.LogVerbose).Info("Old EventReport is nil. Reconcile EventTrigger")
				return true
			}

			// return true if EventReport Spec has changed
			if !reflect.DeepEqual(oldER.Spec, newER.Spec) {
				log.V(logs.LogVerbose).Info(
					"EventReport changed. Will attempt to reconcile associated EventTriggers.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"EventReport did not match expected conditions.  Will not attempt to reconcile associated EventTriggers.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			log := logger.WithValues("predicate", "createEvent",
				"namespace", e.Object.GetNamespace(),
				"eventReport", e.Object.GetName(),
			)

			log.V(logs.LogVerbose).Info(
				"EventReport did match expected conditions.  Will attempt to reconcile associated EventTriggers.")
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"eventReport", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"EventReport deleted.  Will attempt to reconcile associated EventTriggers.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"eventReport", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"EventReport did not match expected conditions.  Will not attempt to reconcile associated EventTriggers.")
			return false
		},
	}
}

// EventSourcePredicates predicates for EventSource. EventTriggerReconciler watches sveltos
// EventSource and react to those by reconciling itself based on following predicates
func EventSourcePredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newES := e.ObjectNew.(*libsveltosv1beta1.EventSource)
			oldES := e.ObjectOld.(*libsveltosv1beta1.EventSource)
			log := logger.WithValues("predicate", "updateEvent",
				"eventSource", newES.Name,
			)

			if oldES == nil {
				log.V(logs.LogVerbose).Info("Old EventSource is nil. Reconcile EventTrigger")
				return true
			}

			// return true if EventSource Spec has changed
			if !reflect.DeepEqual(oldES.Spec, newES.Spec) {
				log.V(logs.LogVerbose).Info(
					"EventSource changed. Will attempt to reconcile associated EventTriggers.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"EventSource did not match expected conditions.  Will not attempt to reconcile associated EventTriggers.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			log := logger.WithValues("predicate", "createEvent",
				"eventSource", e.Object.GetName(),
			)

			log.V(logs.LogVerbose).Info(
				"EventSource did match expected conditions.  Will attempt to reconcile associated EventTriggers.")
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"eventSource", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"EventSource deleted.  Will attempt to reconcile associated EventTriggers.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"eventSource", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"EventSource did not match expected conditions.  Will not attempt to reconcile associated EventTriggers.")
			return false
		},
	}
}

// ClusterSetPredicates predicates for ClusterSet. EventTriggerReconciler watches ClusterSet events
// and react to those by reconciling itself based on following predicates
func ClusterSetPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newObject := e.ObjectNew.(*libsveltosv1beta1.ClusterSet)
			oldObject := e.ObjectOld.(*libsveltosv1beta1.ClusterSet)

			log := logger.WithValues("predicate", "updateEvent",
				"namespace", e.ObjectNew.GetNamespace(),
				"set", e.ObjectNew.GetName(),
			)

			if oldObject == nil {
				log.V(logs.LogVerbose).Info("Old ClusterSet is nil. Reconcile EventTrigger")
				return true
			}

			// if ClusterSet selected clusters have changed, reconcile
			if !reflect.DeepEqual(oldObject.Status.SelectedClusterRefs, newObject.Status.SelectedClusterRefs) {
				log.V(logs.LogVerbose).Info(
					"ClusterSet selected clusters has changed. Will attempt to reconcile associated EventTrigger.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"ClusterSet did not match expected conditions.  Will not attempt to reconcile associated EventTrigger.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"set", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"ClusterSet did match expected conditions.  Will attempt to reconcile associated EventTrigger.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"set", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"ClusterSet did not match expected conditions.  Will not attempt to reconcile associated EventTrigger.")
			return false
		},
	}
}

// ConfigMapPredicates predicates for ConfigMaps. ClusterSummaryReconciler watches ConfigMap events
// and react to those by reconciling itself based on following predicates
func ConfigMapPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newConfigMap := e.ObjectNew.(*corev1.ConfigMap)
			oldConfigMap := e.ObjectOld.(*corev1.ConfigMap)
			log := logger.WithValues("predicate", "updateEvent",
				"configmap", newConfigMap.Name,
			)

			if oldConfigMap == nil {
				log.V(logs.LogVerbose).Info("Old ConfigMap is nil. Reconcile EvenTriggers.")
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.Data, newConfigMap.Data) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap Data changed. Will attempt to reconcile associated EvenTriggers.",
				)
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.Annotations, newConfigMap.Annotations) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap Annotation changed. Will attempt to reconcile associated EvenTriggers.",
				)
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.BinaryData, newConfigMap.BinaryData) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap BinaryData changed. Will attempt to reconcile associated EvenTriggers.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"ConfigMap did not match expected conditions.  Will not attempt to reconcile associated EvenTriggers.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return CreateFuncTrue(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return DeleteFuncTrue(e, logger)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return GenericFuncFalse(e, logger)
		},
	}
}

// SecretPredicates predicates for Secrets. ClusterSummaryReconciler watches Secret events
// and react to those by reconciling itself based on following predicates
func SecretPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newSecret := e.ObjectNew.(*corev1.Secret)
			oldSecret := e.ObjectOld.(*corev1.Secret)
			log := logger.WithValues("predicate", "updateEvent",
				"secret", newSecret.Name,
			)

			if oldSecret == nil {
				log.V(logs.LogVerbose).Info("Old Secret is nil. Reconcile EventTriggers.")
				return true
			}

			if !reflect.DeepEqual(oldSecret.Data, newSecret.Data) {
				log.V(logs.LogVerbose).Info(
					"Secret Data changed. Will attempt to reconcile associated EvenTriggers.",
				)
				return true
			}

			if !reflect.DeepEqual(oldSecret.Annotations, newSecret.Annotations) {
				log.V(logs.LogVerbose).Info(
					"Secret Annotation changed. Will attempt to reconcile associated EvenTriggers.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"Secret did not match expected conditions.  Will not attempt to reconcile associated EvenTriggers.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return CreateFuncTrue(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return DeleteFuncTrue(e, logger)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return GenericFuncFalse(e, logger)
		},
	}
}

var (
	CreateFuncTrue = func(e event.CreateEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "createEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)

		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did match expected conditions.  Will attempt to reconcile associated EvenTriggers.",
			e.Object.GetObjectKind()))
		return true
	}

	DeleteFuncTrue = func(e event.DeleteEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "deleteEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)

		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did match expected conditions.  Will attempt to reconcile associated EvenTriggers.",
			e.Object.GetObjectKind()))
		return true
	}

	GenericFuncFalse = func(e event.GenericEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "genericEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)
		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did not match expected conditions.  Will not attempt to reconcile associated EvenTriggers.",
			e.Object.GetObjectKind()))
		return false
	}
)
