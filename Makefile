
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# KUBEBUILDER_ENVTEST_KUBERNETES_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
KUBEBUILDER_ENVTEST_KUBERNETES_VERSION = 1.31.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif
GO_INSTALL := ./scripts/go_install.sh

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

# Directories.
ROOT_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
TOOLS_DIR := hack/tools
BIN_DIR := bin
TOOLS_BIN_DIR := $(abspath $(TOOLS_DIR)/$(BIN_DIR))

GOBUILD=go build

GENERATED_FILES:=./manifest/manifest.yaml

# Define Docker related variables.
REGISTRY ?= projectsveltos
IMAGE_NAME ?= event-manager
ARCH ?= $(shell go env GOARCH)
OS ?= $(shell uname -s | tr A-Z a-z)
K8S_LATEST_VER ?= $(shell curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)
export CONTROLLER_IMG ?= $(REGISTRY)/$(IMAGE_NAME)
TAG ?= v0.42.1

## Tool Binaries
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
ENVSUBST := $(TOOLS_BIN_DIR)/envsubst
GOIMPORTS := $(TOOLS_BIN_DIR)/goimports
GOLANGCI_LINT := $(TOOLS_BIN_DIR)/golangci-lint
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
SETUP_ENVTEST := $(TOOLS_BIN_DIR)/setup_envs
KIND := $(TOOLS_BIN_DIR)/kind
KUBECTL := $(TOOLS_BIN_DIR)/kubectl
CLUSTERCTL := $(TOOLS_BIN_DIR)/clusterctl

GOLANGCI_LINT_VERSION := "v1.61.0"
CLUSTERCTL_VERSION := "v1.8.5"

KUSTOMIZE_VER := v5.3.0
KUSTOMIZE_BIN := kustomize
KUSTOMIZE := $(abspath $(TOOLS_BIN_DIR)/$(KUSTOMIZE_BIN)-$(KUSTOMIZE_VER))
KUSTOMIZE_PKG := sigs.k8s.io/kustomize/kustomize/v5
$(KUSTOMIZE): # Build kustomize from tools folder.
	CGO_ENABLED=0 GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(KUSTOMIZE_PKG) $(KUSTOMIZE_BIN) $(KUSTOMIZE_VER)

CONVERSION_GEN_VER := v0.31.0
CONVERSION_GEN_BIN := conversion-gen
# We are intentionally using the binary without version suffix, to avoid the version
# in generated files.
CONVERSION_GEN := $(abspath $(TOOLS_BIN_DIR)/$(CONVERSION_GEN_BIN))
CONVERSION_GEN_PKG := k8s.io/code-generator/cmd/conversion-gen

.PHONY: $(CONVERSION_GEN_BIN)
$(CONVERSION_GEN_BIN): $(CONVERSION_GEN) ## Build a local copy of conversion-gen.

## We are forcing a rebuilt of conversion-gen via PHONY so that we're always using an up-to-date version.
## We can't use a versioned name for the binary, because that would be reflected in generated files.
.PHONY: $(CONVERSION_GEN)
$(CONVERSION_GEN): # Build conversion-gen from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(CONVERSION_GEN_PKG) $(CONVERSION_GEN_BIN) $(CONVERSION_GEN_VER)

SETUP_ENVTEST_VER := v0.0.0-20240522175850-2e9781e9fc60
SETUP_ENVTEST_BIN := setup-envtest
SETUP_ENVTEST := $(abspath $(TOOLS_BIN_DIR)/$(SETUP_ENVTEST_BIN)-$(SETUP_ENVTEST_VER))
SETUP_ENVTEST_PKG := sigs.k8s.io/controller-runtime/tools/setup-envtest
setup-envtest: $(SETUP_ENVTEST) ## Set up envtest (download kubebuilder assets)
	@echo KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS)

$(SETUP_ENVTEST_BIN): $(SETUP_ENVTEST) ## Build a local copy of setup-envtest.

$(SETUP_ENVTEST): # Build setup-envtest from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(SETUP_ENVTEST_PKG) $(SETUP_ENVTEST_BIN) $(SETUP_ENVTEST_VER)

$(CONTROLLER_GEN): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) sigs.k8s.io/controller-tools/cmd/controller-gen

$(ENVSUBST): $(TOOLS_DIR)/go.mod # Build envsubst from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) github.com/a8m/envsubst/cmd/envsubst

$(GOLANGCI_LINT): # Build golangci-lint from tools folder.
	cd $(TOOLS_DIR); ./get-golangci-lint.sh $(GOLANGCI_LINT_VERSION)

$(GOIMPORTS):
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) golang.org/x/tools/cmd/goimports

$(GINKGO): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) github.com/onsi/ginkgo/v2/ginkgo

$(KIND): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) sigs.k8s.io/kind

$(CLUSTERCTL): $(TOOLS_DIR)/go.mod ## Build clusterctl binary
	curl -L https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CLUSTERCTL_VERSION)/clusterctl-$(OS)-$(ARCH) -o $@
	chmod +x $@
	mkdir -p $(HOME)/.cluster-api # create cluster api init directory, if not present	

$(KUBECTL):
	curl -L https://storage.googleapis.com/kubernetes-release/release/$(K8S_LATEST_VER)/bin/$(OS)/$(ARCH)/kubectl -o $@
	chmod +x $@

.PHONY: tools
tools: $(CONTROLLER_GEN) $(ENVSUBST) $(KUSTOMIZE) $(GOLANGCI_LINT) $(SETUP_ENVTEST) $(GOIMPORTS) $(GINKGO) ## build all tools

.PHONY: clean
clean: ## Remove all built tools
	rm -rf $(TOOLS_BIN_DIR)/*

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: $(CONTROLLER_GEN) $(KUSTOMIZE) $(ENVSUBST) generate generate-go-conversions fmt vet ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(KUSTOMIZE) build config/default | $(ENVSUBST) > manifest/manifest.yaml
	./scripts/extract_deployment.sh manifest/manifest.yaml manifest/deployment-shard.yaml

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: generate-go-conversions
generate-go-conversions: $(CONVERSION_GEN) ## Generate conversions go code for config.projectsveltos.io api
	$(MAKE) clean-generated-conversions SRC_DIRS="./api/v1alpha1"
	$(CONVERSION_GEN) \
		--output-file=zz_generated.conversion.go \
		--go-header-file=./hack/boilerplate.generatego.txt \
		./api/v1alpha1

.PHONY: clean-generated-conversions
clean-generated-conversions: ## Remove files generated by conversion-gen from the mentioned dirs. Example SRC_DIRS="./api/v1alpha4"
	(IFS=','; for i in $(SRC_DIRS); do find $$i -type f -name 'zz_generated.conversion*' -exec rm -f {} \;; done)

.PHONY: fmt
fmt: $(GOIMPORTS) ## Run go fmt against code.
	$(GOIMPORTS) -local github.com/projectsveltos -w .
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) generate ## Lint codebase
	$(GOLANGCI_LINT) run -v --fast=false --max-issues-per-linter 0 --max-same-issues 0 --timeout 5m	

.PHONY: check-manifests
check-manifests: manifests ## Verify manifests file is up to date
	test `git status --porcelain $(GENERATED_FILES) | grep -cE '(^\?)|(^ M)'` -eq 0 || (echo "The manifest file changed, please 'make manifests' and commit the results"; exit 1)
	test `git status --porcelain ./api/v1alpha1/zz_generated.conversion.go | grep -cE '(^\?)|(^ M)'` -eq 0 || (echo "The conversion generated file changed, please 'make generate-go-conversions' and commit the results"; exit 1)

##@ Testing

ifeq ($(shell go env GOOS),darwin) # Use the darwin/amd64 binary until an arm64 version is available
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path --arch amd64 $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
else
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
endif

# K8S_VERSION for the Kind cluster can be set as environment variable. If not defined,
# this default value is used
ifndef K8S_VERSION
K8S_VERSION := v1.31.2
endif

KIND_CONFIG ?= kind-cluster.yaml
CONTROL_CLUSTER_NAME ?= sveltos-management
WORKLOAD_CLUSTER_NAME ?= clusterapi-workload
KIND_CLUSTER_YAML ?= test/$(WORKLOAD_CLUSTER_NAME).yaml
TIMEOUT ?= 10m
NUM_NODES ?= 5

.PHONY: kind-test
kind-test: test create-cluster fv ## Build docker image; start kind cluster; load docker image; install all cluster api components and run fv

.PHONY: fv
fv: $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: fv-sharding
fv-sharding: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	curl https://raw.githubusercontent.com/projectsveltos/addon-controller/$(TAG)/manifest/deployment-shard.yaml -o ac-deployment-shard.yaml
	sed -e "s/{{.SHARD}}/shard1/g"  ac-deployment-shard.yaml > tmp-ac-deployment-shard.yaml
	$(KUBECTL) apply -f tmp-ac-deployment-shard.yaml
	rm ac-deployment-shard.yaml
	rm tmp-ac-deployment-shard.yaml

	$(KUBECTL) patch cluster  clusterapi-workload  -n default --type json -p '[{ "op": "add", "path": "/metadata/annotations/sharding.projectsveltos.io~1key", "value": "shard1" }]'
	sed -e "s/{{.SHARD}}/shard1/g"  manifest/deployment-shard.yaml > test/em-deployment-shard.yaml
	$(KUBECTL) apply -f test/em-deployment-shard.yaml
	rm -f test/em-deployment-shard.yaml
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: test
test: manifests generate generate-go-conversions fmt vet check-manifests $(SETUP_ENVTEST) ## Run uts.
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" go test $(shell go list ./... |grep -v test/fv |grep -v test/helpers) $(TEST_ARGS) -coverprofile cover.out 

.PHONY: create-cluster
create-cluster: $(KIND) $(CLUSTERCTL) $(KUBECTL) $(ENVSUBST) ## Create a new kind cluster designed for development
	$(MAKE) create-control-cluster

	@echo wait for capd-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capd-controller-manager -n capd-system --timeout=$(TIMEOUT)

	@echo wait for capi-kubeadm-bootstrap-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-bootstrap-controller-manager -n capi-kubeadm-bootstrap-system --timeout=$(TIMEOUT)

	@echo wait for capi-kubeadm-control-plane-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-control-plane-controller-manager -n capi-kubeadm-control-plane-system --timeout=$(TIMEOUT)

	@echo "sleep allowing webhook to be ready"
	sleep 10

	@echo "Create a workload cluster"
	$(KUBECTL) apply -f $(KIND_CLUSTER_YAML)

	@echo "Start projectsveltos event-manager"
	$(MAKE) deploy-projectsveltos

	@echo "sleep allowing control plane to be ready"
	sleep 60

	@echo "get kubeconfig to access workload cluster"
	$(KIND) get kubeconfig --name $(WORKLOAD_CLUSTER_NAME) > test/fv/workload_kubeconfig

	@echo "install calico on workload cluster"
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.29.0/manifests/calico.yaml

	@echo wait for calico pod
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig wait --for=condition=Available deployment/calico-kube-controllers -n kube-system --timeout=$(TIMEOUT)

	@echo "install sveltos-agent"
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_debuggingconfigurations.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_classifiers.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_healthchecks.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_healthcheckreports.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_eventsources.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_eventreports.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_reloaders.lib.projectsveltos.io.yaml
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/sveltos-agent/$(TAG)/manifest/manifest.yaml

.PHONY: delete-cluster
delete-cluster: $(KIND) ## Deletes the kind cluster $(CONTROL_CLUSTER_NAME)
	$(KIND) delete cluster --name $(CONTROL_CLUSTER_NAME)
	$(KIND) delete cluster --name $(WORKLOAD_CLUSTER_NAME)

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build --load --build-arg BUILDOS=linux --build-arg TARGETARCH=amd64 -t $(CONTROLLER_IMG):$(TAG) .
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

.PHONY: docker-buildx
docker-buildx: ## docker build for multiple arch and push to docker hub
	docker buildx build --push --platform linux/amd64,linux/arm64 -t $(CONTROLLER_IMG):$(TAG) .

.PHONY: load-image
load-image: docker-build $(KIND)
	$(KIND) load docker-image $(CONTROLLER_IMG):$(TAG) --name $(CONTROL_CLUSTER_NAME)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests $(CONTROLLER_GEN) $(KUSTOMIZE) ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: $(CONTROLLER_GEN) $(KUSTOMIZE) ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##
set-manifest-image:
	$(info Updating kustomize image patch file for manager resource)
	sed -i'' -e 's@image: .*@image: '"docker.io/${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml
	sed -i'' -e 's@--version=.*@--version=$(TAG)"@' ./config/default/manager_auth_proxy_patch.yaml

set-manifest-pull-policy:
	$(info Updating kustomize pull policy file for manager resource)
	sed -i'' -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml


## fv helpers

# In order to avoid this error
# Error: failed to read "cluster-template-development.yaml" from provider's repository "infrastructure-docker": failed to get GitHub release v1.2.0: rate limit for github api has been reached. 
# Please wait one hour or get a personal API token and assign it to the GITHUB_TOKEN environment variable
#
# add this target. It needs to be run only when changing cluster-api version. create-cluster target uses the output of this command which is stored within repo
# It requires control cluster to exist. So first "make create-control-cluster" then run this target before any workload cluster is created.
 # Once generated, remove
 #      enforce: "{{ .podSecurityStandard.enforce }}"
 #      enforce-version: "latest"
create-clusterapi-kind-cluster-yaml: $(CLUSTERCTL) 
	CLUSTER_TOPOLOGY=ok KUBERNETES_VERSION=$(K8S_VERSION) SERVICE_CIDR=["10.225.0.0/16"] POD_CIDR=["10.220.0.0/16"] $(CLUSTERCTL) generate cluster $(WORKLOAD_CLUSTER_NAME) --flavor development \
		--control-plane-machine-count=1 \
  		--worker-machine-count=1 > $(KIND_CLUSTER_YAML)

create-control-cluster:
	sed -e "s/K8S_VERSION/$(K8S_VERSION)/g"  test/$(KIND_CONFIG) > test/$(KIND_CONFIG).tmp
	$(KIND) create cluster --name=$(CONTROL_CLUSTER_NAME) --config test/$(KIND_CONFIG).tmp
	@echo "Create control cluster with docker as infrastructure provider"
	CLUSTER_TOPOLOGY=true $(CLUSTERCTL) init --infrastructure docker

	@echo wait for capd-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capd-controller-manager -n capd-system --timeout=$(TIMEOUT)
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-control-plane-controller-manager -n capi-kubeadm-control-plane-system --timeout=$(TIMEOUT)
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-bootstrap-controller-manager -n capi-kubeadm-bootstrap-system --timeout=$(TIMEOUT)

	@echo "sleep allowing webhook to be ready"
	sleep 10

deploy-projectsveltos: $(KUSTOMIZE)
	# Load projectsveltos image into cluster
	@echo 'Load projectsveltos image into cluster'
	$(MAKE) load-image
	
	@echo 'Install libsveltos CRDs'
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_debuggingconfigurations.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_eventsources.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_eventreports.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_sveltosclusters.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_clustersets.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_sets.lib.projectsveltos.io.yaml

	# Install projectsveltos addon-controller
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/addon-controller/$(TAG)/manifest/manifest.yaml

	@echo "Waiting for projectsveltos addon-controller to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/addon-controller -n projectsveltos --timeout=$(TIMEOUT)

	# Install projectsveltos event-manager components
	@echo 'Install projectsveltos event-manager components'
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(ENVSUBST) | $(KUBECTL) apply -f-

	@echo "Waiting for projectsveltos event-manager to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/event-manager -n projectsveltos --timeout=$(TIMEOUT)

	@echo "Install sveltos conversion webhook (use dev for conversion-webhook)"
	$(KUBECTL) apply -f  https://raw.githubusercontent.com/projectsveltos/conversion-webhook/dev/manifest/manifest.yaml

	@echo "Waiting for projectsveltos conversion webhook to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/conversion-webhook -n projectsveltos --timeout=$(TIMEOUT)
