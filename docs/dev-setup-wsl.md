# Windows 개발 환경 설정 — WSL2

> Windows WSL2(Ubuntu) 환경에서 postgres-operator 개발 환경을 구성하는 가이드.
> WSL2의 네이티브 Linux 파일시스템을 사용하므로 빌드 속도와 도구 호환성이 네이티브에 가깝다.
>
> 📍 문서 지도: [DOCS_MAP.ko.md](DOCS_MAP.ko.md) · 권장 환경: [dev-setup-devcontainer.md](dev-setup-devcontainer.md) · E2E 실행 시나리오: [WORK_HANDOFF.ko.md §3](WORK_HANDOFF.ko.md)

## 사전 요구사항

| 항목 | 확인 명령 (PowerShell) |
|---|---|
| Windows 10 21H2+ 또는 11 | `winver` |
| WSL2 활성화 | `wsl --status` |
| Ubuntu 배포판 | `wsl --list --verbose` |
| Docker Desktop (선택) | 트레이 아이콘 |

### WSL2 신규 설치가 필요한 경우

PowerShell (관리자)에서:

```powershell
wsl --install
# 재부팅 후 Ubuntu 초기 사용자 설정 완료
```

이미 WSL2 + Ubuntu가 설치된 경우 이 단계는 건너뛴다.

## 1. 소스 파일 위치 결정 (중요)

`/mnt/c/...`(Windows 파일시스템)에서 직접 빌드하면 크로스 파일시스템 I/O 오버헤드로
**빌드 속도가 10배 이상 저하**된다. WSL 네이티브 파일시스템에 소스를 두어야 한다.

WSL 터미널에서:

```bash
# 방법 A: Windows에서 클론한 소스를 WSL 홈으로 복사
cp -r /mnt/c/keiailab/postgres-operator ~/postgres-operator

# 방법 B: WSL 안에서 직접 클론 (권장)
git clone https://github.com/KeiaiLab/postgres-operator ~/postgres-operator

cd ~/postgres-operator
```

> Windows에서 편집한 파일을 WSL에서 사용할 경우 방법 A 이후 변경사항은
> `cp` 또는 `rsync`로 동기화하거나, VS Code Remote WSL 확장으로 WSL 경로를 직접 편집하는 것을 권장한다.

## 2. Go 1.26 설치

```bash
GO_VERSION=1.26.4
curl -Lo /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
rm /tmp/go.tar.gz

# PATH 등록
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc

# 확인
go version  # go version go1.26.4 linux/amd64
```

## 3. kubectl 설치

```bash
KUBECTL_VERSION=$(curl -Ls https://dl.k8s.io/release/stable.txt)
curl -Lo /tmp/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
chmod +x /tmp/kubectl
sudo mv /tmp/kubectl /usr/local/bin/kubectl

kubectl version --client
```

## 4. Helm 설치

```bash
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

helm version
```

## 5. kind 설치 (e2e 테스트용)

```bash
curl -Lo /tmp/kind "https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64"
chmod +x /tmp/kind
sudo mv /tmp/kind /usr/local/bin/kind

kind version
```

## 6. Docker 연동 확인

Docker는 두 가지 방법으로 사용할 수 있다.

### 방법 A: Docker Desktop WSL2 통합 (권장)

Docker Desktop이 설치되어 있고 WSL2 통합이 활성화된 경우, WSL 안에서 별도 설치 없이 docker 명령을 사용할 수 있다.

Docker Desktop → Settings → Resources → WSL Integration:
- "Enable integration with my default WSL distro" 켜기
- Ubuntu 배포판 토글 활성화
- "Apply & Restart"

```bash
# WSL 터미널에서 확인
docker version
docker info
```

### 방법 B: WSL 안에 Docker Engine 직접 설치

Docker Desktop 없이 독립적으로 사용하려면:

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker

docker version
```

## 7. 설치 전체 확인

```bash
go version      # go version go1.26.x linux/amd64
docker version  # Client/Server 모두 출력되어야 함
kubectl version --client
helm version
kind version
make --version  # GNU Make (Ubuntu 기본 포함)
```

## 8. 빌드 및 테스트

```bash
cd ~/postgres-operator

# Go 의존성 다운로드
go mod download

# 단위 테스트 — 클러스터 불필요, 가장 빠름
make test-unit

# CRD/RBAC 생성 + 통합 테스트 (envtest 사용)
make test

# 전체 품질 게이트: lint + test + audit + validate
make gate
```

## 9. 로컬 Kubernetes 클러스터 띄우기 (e2e 테스트)

```bash
# kind 클러스터 생성
kind create cluster --name postgres-operator-dev

# kubeconfig 확인
kubectl cluster-info --context kind-postgres-operator-dev

# e2e 테스트 실행
make test-e2e

# 클러스터 삭제
kind delete cluster --name postgres-operator-dev
```

## 10. Operator 이미지 빌드 및 클러스터 적재

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

## 11. VS Code에서 WSL 경로 편집

VS Code에 **Remote - WSL** 확장(`ms-vscode-remote.remote-wsl`)을 설치하면
WSL 파일시스템을 Windows VS Code에서 직접 편집할 수 있다.

```bash
# WSL 터미널에서 프로젝트 폴더를 VS Code로 열기
code ~/postgres-operator
```

WSL 경로에서 열린 VS Code는 Go 언어 서버(gopls), lint, 디버거가 모두 Linux 환경에서 실행되어
정확한 IntelliSense와 오류 표시를 제공한다.

## 주요 Make 타겟 정리

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

### /mnt/c/ 경로에서 빌드가 매우 느릴 때

소스가 `/mnt/c/` 아래에 있으면 반드시 WSL 네이티브 경로로 이동:

```bash
cp -r /mnt/c/keiailab/postgres-operator ~/postgres-operator
cd ~/postgres-operator
```

### Docker daemon에 연결 안 될 때

Docker Desktop WSL 통합이 활성화되어 있는지 확인.
Docker Desktop이 Windows에서 실행 중이어야 한다.

```bash
# daemon 소켓 확인
ls /var/run/docker.sock
docker info
```

### WSL 메모리 제한으로 빌드 실패

기본값으로 WSL2는 시스템 RAM의 50%를 사용한다. 부족할 경우 `%USERPROFILE%\.wslconfig` 생성:

```ini
[wsl2]
memory=6GB
processors=4
```

PowerShell에서 WSL 재시작:

```powershell
wsl --shutdown
wsl
```

### envtest 바이너리 다운로드 실패 (프록시 환경)

```bash
export HTTP_PROXY=http://proxy.example.com:8080
export HTTPS_PROXY=http://proxy.example.com:8080
make test
```

### CRD 변경 후 테스트 실패

API 타입(`api/v1alpha1/`) 수정 후에는 반드시 재생성 필요:

```bash
make manifests generate
```

### go: cannot find module providing package

```bash
go mod tidy
go mod download
```
