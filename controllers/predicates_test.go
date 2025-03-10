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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/projectsveltos/event-manager/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	predicates = "predicates"
)

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
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}
	})

	It("Create will reprocesses", func() {
		esPredicate := controllers.EventSourcePredicates(logger)

		e := event.CreateEvent{
			Object: eventSource,
		}

		result := esPredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess ", func() {
		esPredicate := controllers.EventSourcePredicates(logger)

		e := event.DeleteEvent{
			Object: eventSource,
		}

		result := esPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when eventSource spec changes", func() {
		esPredicate := controllers.EventSourcePredicates(logger)

		eventSource.Spec = libsveltosv1beta1.EventSourceSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:     randomString(),
					Kind:      randomString(),
					Namespace: randomString(),
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

		result := esPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update does not reprocesses EventSource spec has not changed", func() {
		esPredicate := controllers.EventSourcePredicates(logger)

		eventSource.Spec = libsveltosv1beta1.EventSourceSpec{
			ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
				{
					Group:     randomString(),
					Kind:      randomString(),
					Namespace: randomString(),
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

		result := esPredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})
