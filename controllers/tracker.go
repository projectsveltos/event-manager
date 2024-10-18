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

package controllers

import (
	"sync"

	corev1 "k8s.io/api/core/v1"

	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

type tracker struct {
	mux sync.RWMutex

	// key: Referenced object; value: set of all EventTriggers referencing the resource
	referenceMap map[corev1.ObjectReference]*libsveltosset.Set
}

var (
	instance *tracker
	lock     = &sync.Mutex{}
)

// GetChartManagerInstance return chartManager instance
func getTrackerInstance() *tracker {
	if instance == nil {
		lock.Lock()
		defer lock.Unlock()
		if instance == nil {
			instance = &tracker{
				mux:          sync.RWMutex{},
				referenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			}
		}
	}

	return instance
}

func (t *tracker) trackResourceForConsumer(resource, consumer *corev1.ObjectReference) {
	t.mux.Lock()
	defer t.mux.Unlock()

	// For each referenced resource, add consumer
	consumers := t.referenceMap[*resource]
	if consumers == nil {
		consumers = &libsveltosset.Set{}
	}
	consumers.Insert(consumer)
	t.referenceMap[*resource] = consumers
}

func (t *tracker) stopTrackingConsumer(consumer *corev1.ObjectReference) {
	t.mux.Lock()
	defer t.mux.Unlock()

	for r := range t.referenceMap {
		consumerSet := t.referenceMap[r]
		consumerSet.Erase(consumer)
		if consumerSet.Len() == 0 {
			delete(t.referenceMap, r)
		}
	}
}

func (t *tracker) getConsumersForResource(resource *corev1.ObjectReference) *libsveltosset.Set {
	t.mux.RLock()
	defer t.mux.RUnlock()

	return t.referenceMap[*resource]
}
