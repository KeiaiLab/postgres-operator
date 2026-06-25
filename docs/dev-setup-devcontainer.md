# Windows 개발 환경 설정 — Dev Container

> Windows 환경에서 VS Code Dev Container를 이용해 postgres-operator 개발 환경을 구성하는 가이드.
> 실제 코드는 Linux 컨테이너 안에서 실행되므로 Makefile, bash 스크립트, envtest가 모두 정상 동작한다.
>
> 📍 문서 지도: [DOCS_MAP.ko.md](DOCS_MAP.ko.md) · 대안 환경: [dev-setup-wsl.md](dev-setup-wsl.md) · E2E 실행 시나리오: [WORK_HANDOFF.ko.md §3](WORK_HANDOFF.ko.md)

## 사전 요구사항

| 항목 | 버전 | 설치 여부 확인 |
|---|---|---|
| Windows | 10 21H2+ 또는 11 | `winver` |
| Docker Desktop | 4.x+ | `docker version` |
| VS Code | 최신 | `code --version` |
| VS Code 확장 — Dev Containers | ms-vscode-remote.remote-containers | VS Code 확장 탭 |

> Docker Desktop이 실행 중이어야 한다. 트레이 아이콘에서 "Running" 상태 확인.

## 1. devcontainer.json 확인

`go.mod`는 `go 1.26.0`을 요구한다. 컨테이너 빌드 전에 `.devcontainer/devcontainer.json`의
Go 이미지 버전이 `golang:1.26`인지 확인한다.

`.devcontainer/devcontainer.json`:

```json
{
  "name": "Kubebuilder DevContainer",
  "image": "golang:1.26",
  ...
}
```

## 2. VS Code에서 컨테이너 열기

1. VS Code에서 저장소 폴더(`C:\keiailab\postgres-operator`) 열기
2. 좌하단 파란 아이콘 클릭 → **"Reopen in Container"** 선택
3. 또는 `Ctrl+Shift+P` → `Dev Containers: Reopen in Container`
4. 컨테이너 빌드 완료까지 대기 (최초 실행 시 이미지 pull + `post-install.sh` 실행으로 5~10분 소요)

`post-install.sh`가 자동으로 아래 도구를 설치한다:

| 도구 | 설명 |
|---|---|
| `kind` | 로컬 Kubernetes 클러스터 (e2e 테스트용) |
| `kubebuilder` | CRD/RBAC 스캐폴딩 CLI |
| `kubectl` | Kubernetes 클라이언트 |

## 3. 설치 확인

컨테이너 터미널에서:

```bash
go version       # go version go1.26.x linux/amd64
kind version     # kind v0.x.x
kubectl version --client
kubebuilder version
docker --version # Docker Desktop DinD
```

## 4. 빌드 및 테스트

```bash
# Go 의존성 다운로드
go mod download

# 단위 테스트 — 클러스터 불필요, 가장 빠름
make test-unit

# CRD/RBAC 생성 + 통합 테스트 (envtest 사용)
make test

# 전체 품질 게이트: lint + test + audit + validate
make gate
```

## 5. 로컬 Kubernetes 클러스터 띄우기 (e2e 테스트)

```bash
# kind 클러스터 생성 (make test-e2e가 자동으로 수행하지만 수동으로도 가능)
kind create cluster --name postgres-operator-dev

# kubeconfig 확인
kubectl cluster-info --context kind-postgres-operator-dev

# e2e 테스트 실행
make test-e2e

# 클러스터 삭제
kind delete cluster --name postgres-operator-dev
```

## 6. Operator 이미지 빌드 및 클러스터 적재

```bash
# 이미지 빌드
make docker-build IMG=postgres-operator:dev

# kind 클러스터에 이미지 로드 (registry push 없이 로컬 테스트)
kind load docker-image postgres-operator:dev --name postgres-operator-dev

# Helm으로 배포
helm install postgres-operator ./charts/postgres-operator \
  --set image.repository=postgres-operator \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

## 7. 주요 Make 타겟 정리

| 타겟 | 동작 | 소요 시간 |
|---|---|---|
| `make test-unit` | 단위 테스트 (envtest 불필요) | ~30초 |
| `make test` | 단위 + 통합 테스트 | ~2분 |
| `make test-e2e` | kind 기반 e2e 테스트 | ~10분 |
| `make lint` | golangci-lint | ~1분 |
| `make gate` | lint + test + audit + validate | ~5분 |
| `make manifests generate` | CRD/RBAC/DeepCopy 재생성 | ~20초 |
| `make build` | manager 바이너리 빌드 | ~1분 |
| `make docker-build` | 컨테이너 이미지 빌드 | ~3분 |

## 트러블슈팅

### Docker Desktop이 컨테이너 안에서 인식 안 될 때

Docker Desktop → Settings → Resources → WSL Integration에서
"Enable integration with my default WSL distro"가 켜져 있는지 확인.

### post-install.sh 실행 중 curl 타임아웃

회사 네트워크 프록시 환경이면 `.devcontainer/devcontainer.json`에 프록시 환경 변수 추가:

```json
"remoteEnv": {
  "GO111MODULE": "on",
  "HTTP_PROXY": "http://proxy.example.com:8080",
  "HTTPS_PROXY": "http://proxy.example.com:8080"
}
```

### go: module lookup disabled by GONOSUMCHECK

```bash
go env -w GONOSUMDB="*"
go env -w GOFLAGS="-mod=mod"
```

### CRD 변경 후 테스트 실패

API 타입(`api/v1alpha1/`) 수정 후에는 반드시 재생성 필요:

```bash
make manifests generate
```
