# Image URL to use all building/pushing image targets
IMAGE_REPOSITORY ?= ghcr.io/keiailab/postgres-operator
IMAGE_TAG ?= $(shell awk '/^appVersion:/ { gsub(/"/, "", $$2); print $$2; exit }' charts/postgres-operator/Chart.yaml 2>/dev/null)
IMG ?= $(IMAGE_REPOSITORY):$(IMAGE_TAG)

HELM_CHART ?= charts/postgres-operator
HELM_REPO_URL ?= https://keiailab.github.io/postgres-operator
ARTIFACTHUB_REPOSITORY_NAME ?= keiailab-postgres-operator
ARTIFACTHUB_PACKAGE_NAME ?= postgres-operator
ARTIFACTHUB_ORG ?= keiailab
ARTIFACTHUB_API_URL ?= https://artifacthub.io/api/v1
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
test: manifests generate fmt vet setup-envtest ## Run tests (combined unit + integration with single cover.out).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: test-unit
test-unit: fmt vet ## Run unit tests only (no envtest — fast feedback).
	go test -race ./api/... ./internal/version/... ./internal/plugin/... ./internal/instance/fencing/... ./internal/instance/supervise/... -coverprofile cover-unit.out

.PHONY: test-integration
test-integration: manifests generate fmt vet setup-envtest ## Run integration tests (envtest required).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -race ./internal/controller/... ./internal/webhook/... -coverprofile cover-integration.out

.PHONY: coverage-merge
coverage-merge: ## gocovmerge 로 unit + integration coverage 통합 → cover-final.out
	@command -v gocovmerge >/dev/null 2>&1 || { echo "[error] gocovmerge not installed: go install github.com/wadey/gocovmerge@latest"; exit 1; }
	@test -f cover-unit.out || { echo "[error] cover-unit.out 부재 — make test-unit 먼저"; exit 1; }
	@test -f cover-integration.out || { echo "[error] cover-integration.out 부재 — make test-integration 먼저"; exit 1; }
	gocovmerge cover-unit.out cover-integration.out > cover-final.out
	@echo "✓ cover-final.out (unit + integration merged)"
	@go tool cover -func=cover-final.out | tail -1

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= postgres-operator-test-e2e

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

# RFC 0006 R1+R2 회귀 — PostgresCluster CR 라이프사이클 + Pod annotation status
# + PostgresCluster.status.shards[*].primary.endpoint 실 DNS 반영 + psql round-trip.
# 기존 test-e2e (전체 suite) 의 부분집합. PILLAR=p1 라벨로 필터.
# KEEP=1 시 cleanup skip (로컬 디버그).
.PHONY: test-e2e-pg
test-e2e-pg: setup-test-e2e manifests generate fmt vet ## Run RFC 0006 R1+R2 회귀 e2e (kind 의존, p1 라벨).
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -timeout 30m -v -ginkgo.v -ginkgo.label-filter=p1

# RFC 0006 R3 회귀 — primary kill → 새 primary auto-promote (RTO < 30s) → 옛 primary standby rejoin.
# replicas=1 (Pod 2 개) failover 라이프사이클. KEEP=1 시 cleanup skip.
.PHONY: test-e2e-failover
test-e2e-failover: setup-test-e2e manifests generate fmt vet ## RFC 0006 R3 회귀 e2e (kind 의존, p2 라벨, replicas=1 primary kill).
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -timeout 30m -v -ginkgo.v -ginkgo.label-filter=p2

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: lint-k8s kube-lint
lint-k8s kube-lint: ## kube-linter 로 dist/install.yaml + helm chart 렌더 산출물의 K8s 리소스 보안/best-practice 점검 (lint-k8s/kube-lint alias).
	@command -v kube-linter >/dev/null 2>&1 || { echo "[error] kube-linter not installed: brew install kube-linter (또는 go install golang.stackrox.io/kube-linter/cmd/kube-linter@latest)"; exit 1; }
	@echo "=== kube-linter lint dist/install.yaml ==="
	kube-linter lint dist/install.yaml
	@echo "=== kube-linter lint helm template (default values) ==="
	helm template gate "$(HELM_CHART)" --include-crds | kube-linter lint -
	@echo "✓ kube-linter PASS (dist + helm chart)"

.PHONY: go-licenses
go-licenses: ## Go 의존성 라이선스 검사 — forbidden/restricted 라이선스 차단 (구 GHA go-licenses.yml 대체).
	@command -v go-licenses >/dev/null 2>&1 || { echo "[error] go-licenses not installed: go install github.com/google/go-licenses@latest"; exit 1; }
	@echo "=== go-licenses check (forbidden + restricted) ==="
	go-licenses check ./... --disallowed_types=forbidden,restricted
	@echo "✓ go-licenses PASS"

.PHONY: md-link-check
md-link-check: ## 마크다운 문서 깨진 링크 검사 (구 GHA markdown-link-check.yml 대체).
	@command -v markdown-link-check >/dev/null 2>&1 || { echo "[error] markdown-link-check not installed: npm install -g markdown-link-check"; exit 1; }
	@echo "=== markdown-link-check (README/CHANGELOG/docs) ==="
	@fail=0; for f in README.md CHANGELOG.md $$(find docs -name '*.md' 2>/dev/null); do \
		[ -f "$$f" ] || continue; \
		markdown-link-check -q "$$f" || fail=1; \
	done; \
	[ "$$fail" = "0" ] || { echo "❌ 깨진 markdown link 발견"; exit 1; }
	@echo "✓ markdown-link-check PASS"

.PHONY: hooks-install
hooks-install: ## lefthook 의 pre-commit / commit-msg / pre-push 훅을 git 에 설치 (CONTRIBUTING.md L1/L2).
	@command -v lefthook >/dev/null 2>&1 || { echo "[error] lefthook not installed: brew install lefthook (또는 go install github.com/evilmartians/lefthook@latest)"; exit 1; }
	lefthook install
	@echo "✓ lefthook hooks installed — commit-msg DCO/Conventional Commits + pre-commit lint + pre-push test/audit 활성화"

.PHONY: hooks-check
hooks-check: ## 현재 repo 에 lefthook hook 이 설치되어 있는지 확인 (CI/onboarding 보조).
	@if [ ! -f .git/hooks/commit-msg ] || ! grep -q lefthook .git/hooks/commit-msg 2>/dev/null; then \
		echo "[warn] lefthook hooks 미설치 — 'make hooks-install' 실행 권장"; \
		echo "       DCO Signed-off-by trailer 자동 검사가 비활성화 상태입니다."; \
		exit 0; \
	fi
	@echo "✓ lefthook hooks 설치됨 (.git/hooks/{commit-msg,pre-commit,pre-push})"

.PHONY: audit
audit: ## govulncheck + trivy + gosec — RFC 0002 L3 security 게이트 (3-repo 정합).
	@echo "=== govulncheck (call-graph CVE) ==="
	@command -v $(GOBIN)/govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	$(GOBIN)/govulncheck ./...
	@echo "=== trivy fs (lockfile + base CVE) ==="
	@command -v trivy >/dev/null 2>&1 || { echo "[error] trivy not installed: brew install trivy (or apt install trivy)"; exit 1; }
	trivy fs --severity HIGH,CRITICAL --exit-code 1 --ignore-unfixed --skip-dirs vendor,bin,tmp .
	@echo "=== gosec (HIGH only) ==="
	@command -v $(GOBIN)/gosec >/dev/null 2>&1 || go install github.com/securego/gosec/v2/cmd/gosec@latest
	$(GOBIN)/gosec -quiet -severity high ./internal/... || true

.PHONY: test-scripts
test-scripts: ## shell 스크립트 문법과 smoke helper 동작을 검증.
	bash -n hack/smoke.sh
	bash hack/smoke_shell_test.sh
	bash -n hack/artifacthub_smoke.sh
	bash -n hack/artifacthub_register.sh
	bash hack/artifacthub_smoke_test.sh
	bash hack/artifacthub_register_test.sh

.PHONY: validate
validate: manifests generate kustomize build-installer test-scripts ## CRD, Kustomize, Helm, install bundle을 검증.
	"$(KUSTOMIZE)" build config/crd >/tmp/postgres-operator-crd.yaml
	"$(KUSTOMIZE)" build config/default >/tmp/postgres-operator-default.yaml
	helm lint --strict "$(HELM_CHART)"
	helm template --include-crds gate "$(HELM_CHART)" >/tmp/postgres-operator-helm.yaml
	helm template monitor "$(HELM_CHART)" \
		--set metrics.serviceMonitor.enabled=true \
		--set metrics.prometheusRule.enabled=true \
		--set metrics.grafanaDashboards.enabled=true \
		>/tmp/postgres-operator-helm-monitoring.yaml
	@test "$$(grep -c '^kind: CustomResourceDefinition' /tmp/postgres-operator-helm.yaml)" -ge 8
	@test "$$(grep -c '^kind: CustomResourceDefinition' dist/install.yaml)" -ge 8
	@grep -q 'imagecatalogs.postgres.keiailab.io' /tmp/postgres-operator-crd.yaml
	@grep -q 'imagecatalogs.postgres.keiailab.io' /tmp/postgres-operator-helm.yaml
	@grep -q 'imagecatalogs.postgres.keiailab.io' dist/install.yaml
	@grep -q 'clusterimagecatalogs.postgres.keiailab.io' /tmp/postgres-operator-crd.yaml
	@grep -q 'clusterimagecatalogs.postgres.keiailab.io' /tmp/postgres-operator-helm.yaml
	@grep -q 'clusterimagecatalogs.postgres.keiailab.io' dist/install.yaml
	@grep -q 'poolers.postgres.keiailab.io' /tmp/postgres-operator-crd.yaml
	@grep -q 'poolers.postgres.keiailab.io' /tmp/postgres-operator-helm.yaml
	@grep -q 'poolers.postgres.keiailab.io' dist/install.yaml
	@grep -q 'postgresdatabases.postgres.keiailab.io' /tmp/postgres-operator-crd.yaml
	@grep -q 'postgresdatabases.postgres.keiailab.io' /tmp/postgres-operator-helm.yaml
	@grep -q 'postgresdatabases.postgres.keiailab.io' dist/install.yaml
	@grep -q 'postgresusers.postgres.keiailab.io' /tmp/postgres-operator-crd.yaml
	@grep -q 'postgresusers.postgres.keiailab.io' /tmp/postgres-operator-helm.yaml
	@grep -q 'postgresusers.postgres.keiailab.io' dist/install.yaml
	@grep -q '^kind: ServiceMonitor' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q '^kind: PrometheusRule' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres_operator_backupjob_phase' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres_operator_pooler_phase' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'PostgresPoolerExporterCollectionFailed' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'PostgresPoolerClientWaiting' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'PostgresPoolerClientMaxWaitHigh' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'cnpg_pgbouncer_last_collection_error' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'cnpg_pgbouncer_pools_cl_waiting' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'cnpg_pgbouncer_pools_maxwait' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres_operator_postgrescluster_replication_lag_bytes' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres-operator-cluster-overview.json' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres-operator-pooler.json' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres-operator-cluster-overview' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'postgres-operator-pooler' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'kubelet_volume_stats_available_bytes' /tmp/postgres-operator-helm-monitoring.yaml
	@grep -q 'cnpg_pgbouncer_pools_sv_active' /tmp/postgres-operator-helm-monitoring.yaml
	@if "$(KUBECTL)" version --request-timeout=5s >/dev/null 2>&1; then \
		"$(KUBECTL)" create --dry-run=client --validate=false -f dist/install.yaml >/dev/null; \
	else \
		echo "kubectl API server 미연결: dist/install.yaml client dry-run 생략"; \
	fi
	@if [ -d .github/workflows ] && [ -n "$$(ls .github/workflows/ 2>/dev/null)" ]; then \
		echo "[error] .github/workflows/ 가 비어 있지 않음 — ADR-0009 / RFC-0002 GitHub Actions 영구 금지 정책 위반"; \
		ls -la .github/workflows/; \
		exit 1; \
	fi
	@APP_VERSION="$$(grep -E '^appVersion:' charts/postgres-operator/Chart.yaml | sed -E 's/^appVersion:[[:space:]]*\"?([^\"]*)\"?$$/\1/')"; \
		KUST_TAG="$$(grep -E '^[[:space:]]*newTag:' config/manager/kustomization.yaml | awk '{print $$2}')"; \
		DIST_TAG="$$(grep -m 1 -E 'image:[[:space:]]+ghcr.io/keiailab/postgres-operator:' dist/install.yaml | sed -E 's/.*:([^[:space:]]+)$$/\1/')"; \
		if [ "$$APP_VERSION" != "$$KUST_TAG" ] || [ "$$APP_VERSION" != "$$DIST_TAG" ]; then \
			echo "[error] version drift — Chart appVersion=$$APP_VERSION / kustomize newTag=$$KUST_TAG / dist image=$$DIST_TAG (모두 일치해야 함)"; exit 1; \
		fi
	@test "$$(ls bundle/manifests/postgres.keiailab.io_*.yaml 2>/dev/null | wc -l)" -ge 8 || \
		{ echo "[error] bundle/manifests/ 안 owned CRD 가 8 개 미만 — make bundle VERSION=... 재실행"; exit 1; }
	@if command -v operator-sdk >/dev/null 2>&1; then \
		echo "=== operator-sdk bundle validate ./bundle (default suite) ==="; \
		operator-sdk bundle validate ./bundle; \
		echo "=== operator-sdk bundle validate ./bundle (suite=operatorframework) ==="; \
		operator-sdk bundle validate ./bundle --select-optional suite=operatorframework; \
	else \
		echo "operator-sdk 미설치: bundle validate 생략 (brew install operator-sdk)"; \
	fi
	@if command -v kube-linter >/dev/null 2>&1; then \
		echo "=== kube-linter lint dist/install.yaml ==="; \
		kube-linter lint dist/install.yaml; \
		echo "=== kube-linter lint helm template ==="; \
		helm template gate "$(HELM_CHART)" --include-crds | kube-linter lint -; \
	else \
		echo "kube-linter 미설치: lint-k8s 생략 (brew install kube-linter)"; \
	fi
	@rm -f /tmp/postgres-operator-crd.yaml /tmp/postgres-operator-default.yaml /tmp/postgres-operator-helm.yaml /tmp/postgres-operator-helm-monitoring.yaml

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
	@test -f "$(RELEASE_TMP)/postgres-operator-$$(echo "$(VERSION)" | sed 's/^v//').tgz"
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
	echo "=== image build/push (linux/amd64, default builder): $(IMAGE_REPOSITORY):$(VERSION), $(IMAGE_REPOSITORY):$$TARGET_VER ==="; \
	docker --context=default buildx build --platform linux/amd64 \
		-t "$(IMAGE_REPOSITORY):$(VERSION)" \
		-t "$(IMAGE_REPOSITORY):$$TARGET_VER" \
		--push .
	git tag -a "$(VERSION)" -m "$(VERSION)"
	git push origin "$(VERSION)"
	@PREFLAG=""; case "$(VERSION)" in *alpha*|*beta*|*rc*) PREFLAG="--prerelease";; esac; \
	mkdir -p "$(RELEASE_TMP)"; \
	helm package "$(HELM_CHART)" -d "$(RELEASE_TMP)"; \
	if command -v git-cliff >/dev/null 2>&1; then \
		git-cliff --strip all --tag "$(VERSION)" --unreleased > "/tmp/release-notes-$(VERSION).md" 2>/dev/null && \
			NOTES_FLAG="--notes-file /tmp/release-notes-$(VERSION).md"; \
	else \
		NOTES_FLAG="--notes \"Release $(VERSION). 변경 내역은 CHANGELOG.md 참조.\""; \
	fi; \
	SBOM_ASSET=""; \
	if command -v syft >/dev/null 2>&1; then \
		echo "=== syft SBOM 생성 (T0-2 자동화) ==="; \
		syft scan ghcr.io/keiailab/postgres-operator:$(VERSION) -o spdx-json -q > "/tmp/postgres-operator-$(VERSION).spdx.json" 2>/dev/null && \
			SBOM_ASSET="/tmp/postgres-operator-$(VERSION).spdx.json"; \
	fi; \
	eval gh release create "$(VERSION)" -R keiailab/postgres-operator $$PREFLAG \
		--title "$(VERSION)" \
		$$NOTES_FLAG \
		"$(RELEASE_TMP)/postgres-operator-$$(echo "$(VERSION)" | sed 's/^v//').tgz" \
		dist/install.yaml \
		$$SBOM_ASSET; \
	rm -rf "$(RELEASE_TMP)"
	$(MAKE) helm-publish
	@echo "릴리스 완료: $(VERSION)"

# PGP signing 옵션 — HELM_SIGN=1 시 helm package --sign 으로 .prov 파일 생성.
# HELM_GPG_KEY 가 GnuPG keyring 에 import 되어 있어야 함.
HELM_SIGN     ?= 0
HELM_GPG_KEY  ?= 89A409476828CB992338C378651E51AF520BCB78
HELM_KEYRING  ?= $(HOME)/.gnupg/secring.gpg

.PHONY: release-notes
release-notes: ## git-cliff 로 release notes 자동 생성 — /tmp/release-notes-VERSION.md.
	@command -v git-cliff >/dev/null 2>&1 || { echo "[error] git-cliff not installed: brew install git-cliff"; exit 1; }
	@if [ -z "$(VERSION)" ]; then echo "ERROR: VERSION 필수"; exit 1; fi
	git-cliff --strip all --tag "$(VERSION)" --unreleased > "/tmp/release-notes-$(VERSION).md"
	@echo "✓ release notes: /tmp/release-notes-$(VERSION).md"

.PHONY: bundle
bundle: ## OperatorHub.io bundle 생성 — operator-sdk + kustomize. VERSION 필수 (e.g. 0.3.0-alpha.15). PR-B9 / ADR-0013.
	@command -v operator-sdk >/dev/null 2>&1 || { echo "[error] operator-sdk not installed: brew install operator-sdk"; exit 1; }
	@command -v kustomize >/dev/null 2>&1 || { echo "[error] kustomize not installed"; exit 1; }
	@if [ -z "$(VERSION)" ]; then echo "ERROR: VERSION 필수 (e.g. make bundle VERSION=0.3.0-alpha.15)"; exit 1; fi
	@echo "=== set image controller=ghcr.io/keiailab/postgres-operator:$(VERSION) ==="
	cd config/manager && kustomize edit set image controller=ghcr.io/keiailab/postgres-operator:$(VERSION)
	@echo "=== kustomize build config/manifests | operator-sdk generate bundle ==="
	kustomize build config/manifests | operator-sdk generate bundle \
		--overwrite \
		--version "$(VERSION)" \
		--channels alpha \
		--default-channel alpha \
		--package keiailab-postgres-operator
	@echo "=== scorecard config copy: config/scorecard → bundle/tests/scorecard ==="
	mkdir -p bundle/tests/scorecard
	kustomize build config/scorecard > bundle/tests/scorecard/config.yaml
	@echo "=== operator-sdk bundle validate ==="
	operator-sdk bundle validate ./bundle
	@echo "✓ bundle: ./bundle/ ($(VERSION), channel alpha)"

.PHONY: scorecard
scorecard: ## operator-sdk scorecard 로 OLM bundle 의 basic/olm test suite 실행 (kind cluster + 활성 kubeconfig 필요).
	@command -v operator-sdk >/dev/null 2>&1 || { echo "[error] operator-sdk not installed: brew install operator-sdk"; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "[error] kubectl not installed"; exit 1; }
	@if ! kubectl version --request-timeout=5s >/dev/null 2>&1; then \
		echo "[error] kubectl API server 연결 불가 — scorecard 는 활성 kind/staging cluster 가 필요합니다 (hack/smoke.sh 가 kind cluster 를 띄운 뒤 실행)"; \
		exit 1; \
	fi
	operator-sdk scorecard ./bundle --wait-time 120s

.PHONY: bundle-build
bundle-build: bundle ## bundle image 빌드 — registry push 는 community-operators PR 시점에 별.
	@if [ -z "$(VERSION)" ]; then echo "ERROR: VERSION 필수"; exit 1; fi
	docker buildx build --platform linux/amd64 -f bundle.Dockerfile -t ghcr.io/keiailab/postgres-operator-bundle:$(VERSION) .
	@echo "✓ bundle image: ghcr.io/keiailab/postgres-operator-bundle:$(VERSION)"

.PHONY: sbom
sbom: ## syft 로 SBOM (SPDX-2.3) 생성 — image 의 binary + Go modules. SLSA / EU CRA 표준.
	@command -v syft >/dev/null 2>&1 || { echo "[error] syft not installed: brew install syft"; exit 1; }
	@if [ -z "$(VERSION)" ]; then echo "ERROR: VERSION 필수"; exit 1; fi
	@echo "=== syft scan ghcr.io/keiailab/postgres-operator:$(VERSION) ==="
	syft scan ghcr.io/keiailab/postgres-operator:$(VERSION) -o spdx-json -q > "/tmp/postgres-operator-$(VERSION).spdx.json"
	@SIZE=$$(wc -c < "/tmp/postgres-operator-$(VERSION).spdx.json" | tr -d ' '); \
	echo "✓ SBOM: /tmp/postgres-operator-$(VERSION).spdx.json ($$SIZE bytes)"

.PHONY: helm-publish
helm-publish: ## Helm chart package와 index를 gh-pages에 게시. HELM_SIGN=1 시 PGP .prov 동반.
	@echo "=== helm package ==="
	@rm -rf "$(RELEASE_TMP)" "$(GHPAGES_TMP)"
	@mkdir -p "$(RELEASE_TMP)"
	@if [ "$(HELM_SIGN)" = "1" ]; then \
		echo "INFO- chart 서명 활성 (PGP key $(HELM_GPG_KEY))"; \
		helm package --sign --key "$(HELM_GPG_KEY)" --keyring "$(HELM_KEYRING)" "$(HELM_CHART)" -d "$(RELEASE_TMP)"; \
	else \
		helm package "$(HELM_CHART)" -d "$(RELEASE_TMP)"; \
	fi
	@echo "=== gh-pages worktree ==="
	@if git ls-remote --exit-code --heads origin gh-pages >/dev/null 2>&1; then \
		git clone --branch gh-pages --single-branch "$$(git remote get-url origin)" "$(GHPAGES_TMP)"; \
	else \
		git clone "$$(git remote get-url origin)" "$(GHPAGES_TMP)"; \
		cd "$(GHPAGES_TMP)" && git checkout --orphan gh-pages && git rm -rf . >/dev/null 2>&1 || true; \
	fi
	@echo "=== helm repo index ==="
	cp "$(RELEASE_TMP)"/postgres-operator-*.tgz "$(GHPAGES_TMP)/"
	@cp "$(RELEASE_TMP)"/postgres-operator-*.tgz.prov "$(GHPAGES_TMP)/" 2>/dev/null || true
	@cp "$(CURDIR)/charts/artifacthub-repo.yml" "$(GHPAGES_TMP)/" 2>/dev/null || true
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

.PHONY: artifacthub-smoke
artifacthub-smoke: ## Artifact Hub 등록/검색 상태를 실제 API로 확인.
	@ARTIFACTHUB_API_URL="$(ARTIFACTHUB_API_URL)" \
		ARTIFACTHUB_ORG="$(ARTIFACTHUB_ORG)" \
		ARTIFACTHUB_PACKAGE_NAME="$(ARTIFACTHUB_PACKAGE_NAME)" \
		ARTIFACTHUB_REPOSITORY_NAME="$(ARTIFACTHUB_REPOSITORY_NAME)" \
		HELM_REPO_URL="$(HELM_REPO_URL)" \
		bash hack/artifacthub_smoke.sh

.PHONY: artifacthub-register
artifacthub-register: ## Artifact Hub org repository를 API로 등록. ARTIFACTHUB_API_KEY_ID/SECRET 필요.
	@ARTIFACTHUB_API_URL="$(ARTIFACTHUB_API_URL)" \
		ARTIFACTHUB_ORG="$(ARTIFACTHUB_ORG)" \
		ARTIFACTHUB_REPOSITORY_NAME="$(ARTIFACTHUB_REPOSITORY_NAME)" \
		HELM_REPO_URL="$(HELM_REPO_URL)" \
		bash hack/artifacthub_register.sh

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
docker-build: ## Build docker image with the manager (linux/amd64, default builder).
	# 글로벌 §2: docker buildx 의 기본 빌더 (default) 만 사용. 커스텀 빌더 인스턴스 금지.
	# --platform linux/amd64 명시 — macOS host 에서 native build 시 darwin/arm64 가
	# 되어 cluster (linux) 노드 에 push 시 ImagePullError "no match for platform"
	# (iteration 35 incident 발견). mongodb-operator 패턴 정합.
	docker buildx build --platform linux/amd64 --load -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PG runtime image (instance manager + postgres). PG_MAJOR 는 image tag 와 동일.
# 빌드: make docker-build-pg PG_MAJOR=18 PG_IMG=ghcr.io/keiailab/pg:18
PG_MAJOR ?= 18
PG_IMG ?= ghcr.io/keiailab/pg:$(PG_MAJOR)

.PHONY: docker-build-pg
docker-build-pg: ## Build PG runtime image (instance manager + postgres).
	$(CONTAINER_TOOL) build -f Dockerfile.pg --build-arg PG_MAJOR=$(PG_MAJOR) -t $(PG_IMG) .

.PHONY: docker-push-pg
docker-push-pg: ## Push PG runtime image.
	$(CONTAINER_TOOL) push $(PG_IMG)

# docker-buildx target 제거 (2026-05-11) — CLAUDE.md §2 위반:
# - "기본 빌더 default 사용. 커스텀 빌더 인스턴스 금지" → `buildx create --name
#   postgres-operator-builder` 가 위반.
# - "멀티아키텍처 빌드 금지" → `--platform=$(PLATFORMS)` 다중 플랫폼 의도가 위반.
# linux/amd64 single-arch 빌드는 위 docker-build target (기본 빌더 사용) 이 처리.
# .lefthook.yml 의 platforms-amd64-guard 가 멀티아키 PLATFORMS 재발 차단.
PLATFORMS ?= linux/amd64

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
