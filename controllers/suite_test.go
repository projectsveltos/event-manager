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
	"context"
	"fmt"
	"path"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/projectsveltos/event-manager/controllers"
	"github.com/projectsveltos/event-manager/internal/test/helpers"
	libsveltoscrd "github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

var (
	testEnv *helpers.TestEnvironment
	cancel  context.CancelFunc
	ctx     context.Context
	scheme  *runtime.Scheme
)

const (
	timeout         = 1 * time.Minute
	pollingInterval = 5 * time.Second
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controllers Suite")
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")

	ctrl.SetLogger(klog.Background())

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	scheme, err = setupScheme()
	Expect(err).To(BeNil())

	testEnvConfig := helpers.NewTestEnvironmentConfiguration([]string{
		path.Join("config", "crd", "bases"),
	}, scheme)
	testEnv, err = testEnvConfig.Build(scheme)
	if err != nil {
		panic(err)
	}

	go func() {
		By("Starting the manager")
		err = testEnv.StartManager(ctx)
		if err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	var sveltosCRD *unstructured.Unstructured
	sveltosCRD, err = utils.GetUnstructured(libsveltoscrd.GetSveltosClusterCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), sveltosCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, sveltosCRD)).To(Succeed())

	var eventSourceCRD *unstructured.Unstructured
	eventSourceCRD, err = utils.GetUnstructured(libsveltoscrd.GetEventSourceCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), eventSourceCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, eventSourceCRD)).To(Succeed())

	var eventReportCRD *unstructured.Unstructured
	eventReportCRD, err = utils.GetUnstructured(libsveltoscrd.GetEventReportCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), eventReportCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, eventReportCRD)).To(Succeed())

	var dcCRD *unstructured.Unstructured
	dcCRD, err = utils.GetUnstructured(libsveltoscrd.GetDebuggingConfigurationCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), dcCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, dcCRD)).To(Succeed())

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: controllers.ReportNamespace,
		},
	}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, ns)).To(Succeed())

	time.Sleep(time.Second)

	controllers.SetSchema(scheme)
	controllers.SetConfig(testEnv.Config)

	if synced := testEnv.GetCache().WaitForCacheSync(ctx); !synced {
		time.Sleep(time.Second)
	}
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
