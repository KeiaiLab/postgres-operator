# Image URL to use all building/pushing image targets
IMAGE_REPOSITORY ?= ghcr.io/keiailab/postgres-operator
IMAGE_TAG ?= $(shell awk '/^appVersion:/ { gsub(/"/, "", $$2); print $$2; exit }' charts/postgresql-operator/Chart.yaml 2>/dev/null)
IMG ?= $(IMAGE_REPOSITORY):$(IMAGE_TAG)

HELM_CHART ?= charts/postgresql-operator
HELM_REPO_URL ?= https://keiailab.github.io/postgres-operator
RELEASE_TMP ?= /tmp/postgres-operator-release
GHPAGES_TMP ?= /tmp/postgres-operator-gh-pages

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
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
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	$(MAKE) sync-crds

.PHONY: sync-crds
sync-crds: ## config/crd/bases를 Helm chart crds로 동기화.
	@echo "=== sync CRD bundles (config/crd/bases -> $(HELM_CHART)/crds) ==="
	@rm -rf "$(HELM_CHART)/crds"
	@mkdir -p "$(HELM_CHART)/crds"
	@cp config/crd/bases/*.yaml "$(HELM_CHART)/crds/"
	@echo "CRD bundles synced"

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= postgresql-operator-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

# Pillar 라벨 — Pillar 단위로 e2e 시나리오를 좁혀 실행한다(roadmap.md 참조).
# 사용 예: make test-e2e PILLAR=p1
# 빈 값(디폴트)은 전체 e2e 실행. CI 매트릭스가 본 변수를 채워 Pillar 단위 통과율을
# 추적한다(.github/workflows/ci.yml의 e2e job).
PILLAR ?=

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind. Use PILLAR=p1 to narrow to one Pillar.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) PILLAR=$(PILLAR) go test -tags=e2e ./test/e2e/ -v -ginkgo.v $(if $(PILLAR),-ginkgo.label-filter=$(PILLAR),)
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: audit
audit: ## Run vulnerability scan (trivy fs, HIGH+CRITICAL severity, ignore-unfixed). RFC 0002 / ADR 0009.
	@command -v trivy >/dev/null 2>&1 || { echo "[error] trivy not installed: brew install trivy (or apt install trivy)"; exit 1; }
	trivy fs --severity HIGH,CRITICAL --exit-code 1 --ignore-unfixed --skip-dirs vendor,bin,tmp .

.PHONY: validate
validate: manifests generate kustomize build-installer ## CRD, Kustomize, Helm, install bundle을 검증.
	"$(KUSTOMIZE)" build config/crd >/tmp/postgres-operator-crd.yaml
	"$(KUSTOMIZE)" build config/default >/tmp/postgres-operator-default.yaml
	helm lint --strict "$(HELM_CHART)"
	helm template --include-crds gate "$(HELM_CHART)" >/tmp/postgres-operator-helm.yaml
	@test "$$(grep -c '^kind: CustomResourceDefinition' /tmp/postgres-operator-helm.yaml)" -ge 2
	@test "$$(grep -c '^kind: CustomResourceDefinition' dist/install.yaml)" -ge 2
	@if "$(KUBECTL)" version --request-timeout=5s >/dev/null 2>&1; then \
		"$(KUBECTL)" apply --dry-run=client --validate=false -f dist/install.yaml >/dev/null; \
	else \
		echo "kubectl API server 미연결: dist/install.yaml client dry-run 생략"; \
	fi
	@rm -f /tmp/postgres-operator-crd.yaml /tmp/postgres-operator-default.yaml /tmp/postgres-operator-helm.yaml

.PHONY: gate
gate: lint test audit validate ## 로컬 릴리스 품질 게이트 실행.
	@echo ""
	@echo "로컬 게이트 통과"

.PHONY: require-version
require-version:
	@if [ -z "$(VERSION)" ]; then echo "ERROR: VERSION 필수 (예: make release-preflight VERSION=v0.1.0-alpha)"; exit 1; fi
	@case "$(VERSION)" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "ERROR: VERSION은 vX.Y.Z 형식이어야 함: $(VERSION)"; exit 1;; esac

.PHONY: release-preflight
release-preflight: require-version gate ## push 없이 릴리스 메타데이터와 산출물 검증.
	@echo "=== release preflight: version metadata ==="
	@CHART_VER=$$(awk '/^version:/ { print $$2; exit }' "$(HELM_CHART)/Chart.yaml"); \
	APP_VER=$$(awk '/^appVersion:/ { gsub(/"/, "", $$2); print $$2; exit }' "$(HELM_CHART)/Chart.yaml"); \
	TARGET_VER=$$(echo "$(VERSION)" | sed 's/^v//'); \
	if [ "$$CHART_VER" != "$$TARGET_VER" ]; then echo "ERROR: Chart.yaml version=$$CHART_VER, VERSION=$$TARGET_VER"; exit 1; fi; \
	if [ "$$APP_VER" != "$$TARGET_VER" ]; then echo "ERROR: Chart.yaml appVersion=$$APP_VER, VERSION=$$TARGET_VER"; exit 1; fi
	@test -f CHANGELOG.md
	@grep -q "\[$$(echo "$(VERSION)" | sed 's/^v//')\]" CHANGELOG.md || { echo "ERROR: CHANGELOG.md에 $(VERSION) 항목이 없음"; exit 1; }
	@git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null && { echo "ERROR: tag $(VERSION) 이미 존재"; exit 1; } || true
	@echo "=== release preflight: helm package ==="
	@rm -rf "$(RELEASE_TMP)"
	@mkdir -p "$(RELEASE_TMP)"
	helm package "$(HELM_CHART)" -d "$(RELEASE_TMP)"
	@test -f "$(RELEASE_TMP)/postgresql-operator-$$(echo "$(VERSION)" | sed 's/^v//').tgz"
	@rm -rf "$(RELEASE_TMP)"
	@echo "=== release preflight: clean tree ==="
	@git diff --quiet
	@git diff --cached --quiet
	@test -z "$$(git status --short --untracked-files=all)"
	@echo "릴리스 preflight 통과: $(VERSION)"

.PHONY: release
release: require-version ## 전체 로컬 릴리스 파이프라인. VERSION=vX.Y.Z 필수.
	$(MAKE) release-preflight VERSION="$(VERSION)"
	@TARGET_VER=$$(echo "$(VERSION)" | sed 's/^v//'); \
	echo "=== image build/push: $(IMAGE_REPOSITORY):$(VERSION), $(IMAGE_REPOSITORY):$$TARGET_VER ==="; \
	$(CONTAINER_TOOL) build -t "$(IMAGE_REPOSITORY):$(VERSION)" -t "$(IMAGE_REPOSITORY):$$TARGET_VER" .; \
	$(CONTAINER_TOOL) push "$(IMAGE_REPOSITORY):$(VERSION)"; \
	$(CONTAINER_TOOL) push "$(IMAGE_REPOSITORY):$$TARGET_VER"
	git tag -a "$(VERSION)" -m "$(VERSION)"
	git push origin "$(VERSION)"
	@PREFLAG=""; case "$(VERSION)" in *alpha*|*beta*|*rc*) PREFLAG="--prerelease";; esac; \
	mkdir -p "$(RELEASE_TMP)"; \
	helm package "$(HELM_CHART)" -d "$(RELEASE_TMP)"; \
	gh release create "$(VERSION)" -R keiailab/postgres-operator $$PREFLAG \
		--title "$(VERSION)" \
		--notes "Release $(VERSION). 변경 내역은 CHANGELOG.md 참조." \
		"$(RELEASE_TMP)/postgresql-operator-$$(echo "$(VERSION)" | sed 's/^v//').tgz" \
		dist/install.yaml; \
	rm -rf "$(RELEASE_TMP)"
	$(MAKE) helm-publish
	@echo "릴리스 완료: $(VERSION)"

.PHONY: helm-publish
helm-publish: ## Helm chart package와 index를 gh-pages에 게시.
	@echo "=== helm package ==="
	@rm -rf "$(RELEASE_TMP)" "$(GHPAGES_TMP)"
	@mkdir -p "$(RELEASE_TMP)"
	helm package "$(HELM_CHART)" -d "$(RELEASE_TMP)"
	@echo "=== gh-pages worktree ==="
	@if git ls-remote --exit-code --heads origin gh-pages >/dev/null 2>&1; then \
		git clone --branch gh-pages --single-branch "$$(git remote get-url origin)" "$(GHPAGES_TMP)"; \
	else \
		git clone "$$(git remote get-url origin)" "$(GHPAGES_TMP)"; \
		cd "$(GHPAGES_TMP)" && git checkout --orphan gh-pages && git rm -rf . >/dev/null 2>&1 || true; \
	fi
	@echo "=== helm repo index ==="
	cp "$(RELEASE_TMP)"/postgresql-operator-*.tgz "$(GHPAGES_TMP)/"
	@if [ -f "$(GHPAGES_TMP)/index.yaml" ]; then \
		cd "$(GHPAGES_TMP)" && helm repo index . --merge index.yaml --url "$(HELM_REPO_URL)"; \
	else \
		cd "$(GHPAGES_TMP)" && helm repo index . --url "$(HELM_REPO_URL)"; \
	fi
	@echo "=== commit + push ==="
	@cd "$(GHPAGES_TMP)" && git add -A && \
		(git diff --cached --quiet || git commit -m "chore(helm): publish $$(awk '/^version:/ { print $$2; exit }' "$(CURDIR)/$(HELM_CHART)/Chart.yaml")") && \
		git push origin gh-pages
	@rm -rf "$(RELEASE_TMP)" "$(GHPAGES_TMP)"
	@echo "Helm chart 게시 완료"

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name postgresql-operator-builder
	$(CONTAINER_TOOL) buildx use postgresql-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm postgresql-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.8.0
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
