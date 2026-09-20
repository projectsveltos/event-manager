/*
Copyright 2026. Sveltos SRL. All rights reserved.

This file is part of Sveltos Enterprise. See the LICENSE file at the root
of this repository.
*/

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/projectsveltos/event-manager/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// namespaceAlwaysMatchLuaScript always matches: Namespace is cluster-scoped and always has at
// least one instance (kube-system, default, ...), so this is a guaranteed match as long as
// evaluate does not error out.
const namespaceAlwaysMatchLuaScript = `
function evaluate()
    hs = {}
	hs.matching = true
	hs.message = ""
	return hs
end`

var _ = Describe("EventReport: sveltos-agent evaluation failure is surfaced", func() {
	const namePrefix = "agent-failure-"

	It("AgentFailureMessage is set when sveltos-agent's evaluation fails, and cleared once fixed",
		Label("FV", "PULLMODE"), func() {

			eventSource := &libsveltosv1beta1.EventSource{
				ObjectMeta: metav1.ObjectMeta{
					Name: namePrefix + randomString(),
				},
				Spec: libsveltosv1beta1.EventSourceSpec{
					ResourceSelectors: []libsveltosv1beta1.ResourceSelector{
						{
							Group:    "",
							Version:  coreV1Version,
							Kind:     namespaceKind,
							Evaluate: namespaceAlwaysMatchLuaScript,
						},
					},
				},
			}
			Byf("Creating EventSource %s", eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), eventSource)).To(Succeed())

			eventTrigger := getEventTrigger(namePrefix, eventSource.Name, map[string]string{key: value}, nil)
			Byf("Creating EventTrigger %s referencing EventSource %s", eventTrigger.Name, eventSource.Name)
			Expect(k8sClient.Create(context.TODO(), eventTrigger)).To(Succeed())

			eventReportName := getEventReportName(eventSource.Name)

			Byf("Verifying EventReport %s is present in the management cluster, with no AgentFailureMessage",
				eventReportName)
			verifyAgentFailureMessage(eventReportName, false)

			Byf("Introducing a broken Lua evaluate script, so sveltos-agent's evaluation errors out")
			currentEventSource := &libsveltosv1beta1.EventSource{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			currentEventSource.Spec.ResourceSelectors[0].Evaluate = "this is not valid lua {{{"
			Expect(k8sClient.Update(context.TODO(), currentEventSource)).To(Succeed())

			Byf("Verifying EventReport %s AgentFailureMessage gets set", eventReportName)
			verifyAgentFailureMessage(eventReportName, true)

			Byf("Fixing the Lua evaluate script")
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			currentEventSource.Spec.ResourceSelectors[0].Evaluate = namespaceAlwaysMatchLuaScript
			Expect(k8sClient.Update(context.TODO(), currentEventSource)).To(Succeed())

			Byf("Verifying EventReport %s AgentFailureMessage is cleared once evaluation succeeds again",
				eventReportName)
			verifyAgentFailureMessage(eventReportName, false)

			Byf("Deleting EventTrigger %s", eventTrigger.Name)
			currentEventTrigger := &v1beta1.EventTrigger{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventTrigger.Name},
				currentEventTrigger)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventTrigger)).To(Succeed())

			verifyClusterProfilesAreGone(eventTrigger.Name)

			Byf("Deleting EventSource %s", eventSource.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: eventSource.Name},
				currentEventSource)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentEventSource)).To(Succeed())
		})
})

// verifyAgentFailureMessage waits for EventReport.Status.AgentFailureMessage to be set (or
// cleared, if wantSet is false), on the management cluster's copy of the report.
func verifyAgentFailureMessage(eventReportName string, wantSet bool) {
	Byf("Verifying EventReport %s AgentFailureMessage is set: %t", eventReportName, wantSet)
	Eventually(func() bool {
		currentEventReport := &libsveltosv1beta1.EventReport{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: eventReportName},
			currentEventReport)
		if err != nil {
			return false
		}
		return (currentEventReport.Status.AgentFailureMessage != nil) == wantSet
	}, timeout, pollingInterval).Should(BeTrue())
}
