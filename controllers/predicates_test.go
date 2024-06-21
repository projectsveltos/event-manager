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

package controllers_test

import (
	"encoding/base64"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	predicates = "predicates"
)

var _ = Describe("EventTrigger Predicates: SvelotsClusterPredicates", func() {
	var logger logr.Logger
	var cluster *libsveltosv1beta1.SveltosCluster

	const upstreamClusterNamePrefix = "sveltoscluster-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		cluster = &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}
	})

	It("Create reprocesses when sveltos Cluster is unpaused", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when sveltos Cluster is paused", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.CreateEvent{
			Object: cluster,
		}

		result := clusterPredicate.Create(e)
		Expect(result).To(BeFalse())
	})
	It("Delete does reprocess ", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		e := event.DeleteEvent{
			Object: cluster,
		}

		result := clusterPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when sveltos Cluster paused changes from true to false", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false

		oldCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = true
		oldCluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when sveltos Cluster paused changes from false to true", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
		oldCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when sveltos Cluster paused has not changed", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Spec.Paused = false
		oldCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when sveltos Cluster labels change", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Labels = map[string]string{"department": "eng"}

		oldCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
		}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when sveltos Cluster Status Ready changes", func() {
		clusterPredicate := controllers.SveltosClusterPredicates(logger)

		cluster.Status.Ready = true

		oldCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
			Status: libsveltosv1beta1.SveltosClusterStatus{
				Ready: false,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: cluster,
			ObjectOld: oldCluster,
		}

		result := clusterPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("EventTrigger Predicates: ClusterPredicates", func() {
	var logger logr.Logger
	var cluster *clusterv1.Cluster

	const upstreamClusterNamePrefix = "cluster-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}
	})

	It("Create reprocesses when v1Cluster is unpaused", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Spec.Paused = false

		result := clusterPredicate.Create(event.TypedCreateEvent[*clusterv1.Cluster]{Object: cluster})
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when v1Cluster is paused", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		result := clusterPredicate.Create(event.TypedCreateEvent[*clusterv1.Cluster]{Object: cluster})
		Expect(result).To(BeFalse())
	})
	It("Delete does reprocess ", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		result := clusterPredicate.Delete(event.TypedDeleteEvent[*clusterv1.Cluster]{Object: cluster})
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when v1Cluster paused changes from true to false", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Spec.Paused = false

		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = true
		oldCluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}

		result := clusterPredicate.Update(event.TypedUpdateEvent[*clusterv1.Cluster]{
			ObjectNew: cluster, ObjectOld: oldCluster})
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when v1Cluster paused changes from false to true", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Spec.Paused = true
		cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		result := clusterPredicate.Update(event.TypedUpdateEvent[*clusterv1.Cluster]{
			ObjectNew: cluster, ObjectOld: oldCluster})
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when v1Cluster paused has not changed", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Spec.Paused = false
		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		oldCluster.Spec.Paused = false

		result := clusterPredicate.Update(event.TypedUpdateEvent[*clusterv1.Cluster]{
			ObjectNew: cluster, ObjectOld: oldCluster})
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when v1Cluster labels change", func() {
		clusterPredicate := controllers.ClusterPredicate{Logger: logger}

		cluster.Labels = map[string]string{"department": "eng"}

		oldCluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{},
			},
		}

		result := clusterPredicate.Update(event.TypedUpdateEvent[*clusterv1.Cluster]{
			ObjectNew: cluster, ObjectOld: oldCluster})
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("EventTrigger Predicates: MachinePredicates", func() {
	var logger logr.Logger
	var machine *clusterv1.Machine

	const upstreamMachineNamePrefix = "machine-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		machine = &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamMachineNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}
	})

	It("Create reprocesses when v1Machine is Running", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}

		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		result := machinePredicate.Create(event.TypedCreateEvent[*clusterv1.Machine]{Object: machine})
		Expect(result).To(BeTrue())
	})
	It("Create does not reprocess when v1Machine is not Running", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}

		result := machinePredicate.Create(event.TypedCreateEvent[*clusterv1.Machine]{Object: machine})
		Expect(result).To(BeFalse())
	})
	It("Delete does not reprocess ", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}

		result := machinePredicate.Delete(event.TypedDeleteEvent[*clusterv1.Machine]{Object: machine})
		Expect(result).To(BeFalse())
	})
	It("Update reprocesses when v1Machine Phase changed from not running to running", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}

		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}

		result := machinePredicate.Update(event.TypedUpdateEvent[*clusterv1.Machine]{
			ObjectNew: machine, ObjectOld: oldMachine})
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when v1Machine Phase changes from not Phase not set to Phase set but not running", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}

		machine.Status.Phase = "Provisioning"

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}

		result := machinePredicate.Update(event.TypedUpdateEvent[*clusterv1.Machine]{
			ObjectNew: machine, ObjectOld: oldMachine})
		Expect(result).To(BeFalse())
	})
	It("Update does not reprocess when v1Machine Phases does not change", func() {
		machinePredicate := controllers.MachinePredicate{Logger: logger}
		machine.Status.Phase = string(clusterv1.MachinePhaseRunning)

		oldMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
			},
		}
		oldMachine.Status.Phase = machine.Status.Phase

		result := machinePredicate.Update(event.TypedUpdateEvent[*clusterv1.Machine]{
			ObjectNew: machine, ObjectOld: oldMachine})
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("EventTrigger Predicates: EventReportPredicates", func() {
	var logger logr.Logger
	var eventReport *libsveltosv1beta1.EventReport

	const upstreamClusterNamePrefix = "eventreport-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		eventReport = &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}
	})

	It("Create will reprocesses", func() {
		hcrPredicate := controllers.EventReportPredicates(logger)

		e := event.CreateEvent{
			Object: eventReport,
		}

		result := hcrPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess ", func() {
		hcrPredicate := controllers.EventReportPredicates(logger)

		e := event.DeleteEvent{
			Object: eventReport,
		}

		result := hcrPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when EventReport spec changes", func() {
		hcrPredicate := controllers.EventReportPredicates(logger)

		eventReport.Spec = libsveltosv1beta1.EventReportSpec{
			MatchingResources: []corev1.ObjectReference{
				{
					Kind:       randomString(),
					APIVersion: randomString(),
					Name:       randomString(),
					Namespace:  randomString(),
				},
			},
		}

		oldEventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventReport.Name,
				Namespace: eventReport.Namespace,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: eventReport,
			ObjectOld: oldEventReport,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses EventReport spec has not changed", func() {
		hcrPredicate := controllers.EventReportPredicates(logger)

		eventReport.Spec = libsveltosv1beta1.EventReportSpec{
			MatchingResources: []corev1.ObjectReference{
				{
					Kind:       randomString(),
					APIVersion: randomString(),
					Name:       randomString(),
					Namespace:  randomString(),
				},
			},
		}
		oldEventReport := &libsveltosv1beta1.EventReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      eventReport.Name,
				Namespace: eventReport.Namespace,
			},
			Spec: eventReport.Spec,
		}

		e := event.UpdateEvent{
			ObjectNew: eventReport,
			ObjectOld: oldEventReport,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("EventTrigger Predicates: EventSourcePredicates", func() {
	var logger logr.Logger
	var eventSource *libsveltosv1beta1.EventSource

	const upstreamClusterNamePrefix = "eventsource-predicates-"

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		eventSource = &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: upstreamClusterNamePrefix + randomString(),
			},
		}
	})

	It("Create will reprocesses", func() {
		hcrPredicate := controllers.EventSourcePredicates(logger)

		e := event.CreateEvent{
			Object: eventSource,
		}

		result := hcrPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess ", func() {
		hcrPredicate := controllers.EventSourcePredicates(logger)

		e := event.DeleteEvent{
			Object: eventSource,
		}

		result := hcrPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when EventSource spec changes", func() {
		hcrPredicate := controllers.EventSourcePredicates(logger)

		eventSource.Spec = libsveltosv1beta1.EventSourceSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:    randomString(),
					Version:  randomString(),
					Kind:     randomString(),
					Evaluate: randomString(),
				},
			},
		}

		oldEventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventSource.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: eventSource,
			ObjectOld: oldEventSource,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses EventSource spec has not changed", func() {
		hcrPredicate := controllers.EventSourcePredicates(logger)

		eventSource.Spec = libsveltosv1beta1.EventSourceSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:    randomString(),
					Version:  randomString(),
					Kind:     randomString(),
					Evaluate: randomString(),
				},
			},
		}

		oldEventSource := &libsveltosv1beta1.EventSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: eventSource.Name,
			},
			Spec: eventSource.Spec,
		}

		e := event.UpdateEvent{
			ObjectNew: eventSource,
			ObjectOld: oldEventSource,
		}

		result := hcrPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("EventTrigger Predicates: ConfigMapPredicates", func() {
	var logger logr.Logger
	var configMap *corev1.ConfigMap

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
	})

	It("Create returns true", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)

		e := event.CreateEvent{
			Object: configMap,
		}

		result := configMapPredicate.Create(e)
		Expect(result).To(BeTrue())
	})

	It("Delete returns true", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)

		e := event.DeleteEvent{
			Object: configMap,
		}

		result := configMapPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns true when data has changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap.Data = map[string]string{"change": "now"}

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when Data has not changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap = createConfigMapWithPolicy("default", configMap.Name, fmt.Sprintf(viewClusterRole, randomString()))

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   configMap.Name,
				Labels: map[string]string{"env": "testing"},
			},
			Data: configMap.Data,
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})

var _ = Describe("EventTrigger Predicates: SecretPredicates", func() {
	var logger logr.Logger
	var secret *corev1.Secret

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
	})

	It("Create returns true", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		e := event.CreateEvent{
			Object: secret,
		}

		result := secretPredicate.Create(e)
		Expect(result).To(BeTrue())
	})

	It("Delete returns true", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		e := event.DeleteEvent{
			Object: secret,
		}

		result := secretPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns true when data has changed", func() {
		secretPredicate := controllers.SecretPredicates(logger)
		str := base64.StdEncoding.EncodeToString([]byte("password"))
		secret.Data = map[string][]byte{"change": []byte(str)}

		oldSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: secret.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: secret,
			ObjectOld: oldSecret,
		}

		result := secretPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when Data has not changed", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		oldSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   secret.Name,
				Labels: map[string]string{"env": "testing"},
			},
		}

		e := event.UpdateEvent{
			ObjectNew: secret,
			ObjectOld: oldSecret,
		}

		result := secretPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})
