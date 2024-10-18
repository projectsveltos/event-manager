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

package controllers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	"github.com/projectsveltos/event-manager/controllers"
)

const (
	secretKind    = "Secret"
	configMapKind = "ConfigMap"
)

var _ = Describe("Tracker", func() {
	var eventTriggerRef *corev1.ObjectReference

	BeforeEach(func() {
		eventTriggerRef = &corev1.ObjectReference{
			Kind:       v1beta1.EventTriggerKind,
			Name:       randomString(),
			APIVersion: v1beta1.GroupVersion.String(),
		}
	})

	It("trackResourceForConsumer: single resource for a consumer", func() {
		resourceTracker := controllers.GetTrackerInstance()

		resource := &corev1.ObjectReference{
			Kind:       secretKind,
			Name:       randomString(),
			Namespace:  randomString(),
			APIVersion: "v1",
		}

		controllers.TrackResourceForConsumer(resourceTracker, resource, eventTriggerRef)

		consumers := controllers.GetConsumersForResource(resourceTracker, resource)
		Expect(consumers.Len()).To(Equal(1))
		Expect(consumers.Has(eventTriggerRef)).To(BeTrue())

		controllers.StopTrackingConsumer(resourceTracker, eventTriggerRef)
		consumers = controllers.GetConsumersForResource(resourceTracker, resource)
		Expect(consumers).To(BeNil())
	})

	It("trackResourceForConsumer: multiple resources for a consumer", func() {
		resourceTracker := controllers.GetTrackerInstance()

		resource1 := &corev1.ObjectReference{
			Kind:       secretKind,
			Name:       randomString(),
			Namespace:  randomString(),
			APIVersion: "v1",
		}
		controllers.TrackResourceForConsumer(resourceTracker, resource1, eventTriggerRef)

		resource2 := &corev1.ObjectReference{
			Kind:       secretKind,
			Name:       randomString(),
			Namespace:  randomString(),
			APIVersion: "v1",
		}
		controllers.TrackResourceForConsumer(resourceTracker, resource2, eventTriggerRef)

		consumers := controllers.GetConsumersForResource(resourceTracker, resource1)
		Expect(consumers.Len()).To(Equal(1))
		Expect(consumers.Has(eventTriggerRef)).To(BeTrue())

		consumers = controllers.GetConsumersForResource(resourceTracker, resource2)
		Expect(consumers.Len()).To(Equal(1))
		Expect(consumers.Has(eventTriggerRef)).To(BeTrue())

		controllers.StopTrackingConsumer(resourceTracker, eventTriggerRef)
		consumers = controllers.GetConsumersForResource(resourceTracker, resource1)
		Expect(consumers).To(BeNil())
		consumers = controllers.GetConsumersForResource(resourceTracker, resource2)
		Expect(consumers).To(BeNil())
	})

	It("trackResourceForConsumer: multiple consumers for a resource", func() {
		resourceTracker := controllers.GetTrackerInstance()

		resource := &corev1.ObjectReference{
			Kind:       secretKind,
			Name:       randomString(),
			Namespace:  randomString(),
			APIVersion: "v1",
		}
		controllers.TrackResourceForConsumer(resourceTracker, resource, eventTriggerRef)

		otherEventTriggerRef := &corev1.ObjectReference{
			Kind:       v1beta1.EventTriggerKind,
			Name:       randomString(),
			APIVersion: v1beta1.GroupVersion.String(),
		}
		controllers.TrackResourceForConsumer(resourceTracker, resource, otherEventTriggerRef)

		consumers := controllers.GetConsumersForResource(resourceTracker, resource)
		Expect(consumers.Len()).To(Equal(2))
		Expect(consumers.Has(eventTriggerRef)).To(BeTrue())
		Expect(consumers.Has(otherEventTriggerRef)).To(BeTrue())

		controllers.StopTrackingConsumer(resourceTracker, eventTriggerRef)
		consumers = controllers.GetConsumersForResource(resourceTracker, resource)
		Expect(consumers.Len()).To(Equal(1))
		Expect(consumers.Has(otherEventTriggerRef)).To(BeTrue())

		controllers.StopTrackingConsumer(resourceTracker, otherEventTriggerRef)
		consumers = controllers.GetConsumersForResource(resourceTracker, resource)
		Expect(consumers).To(BeNil())
	})

})
