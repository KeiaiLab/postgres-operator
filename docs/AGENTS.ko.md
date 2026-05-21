<p align="center">
  <a href="AGENTS.md">English</a> |
  <b>한국어</b> |
  <a href="AGENTS.ja.md">日本語</a> |
  <a href="AGENTS.zh.md">中文</a>
</p>

# postgres-operator - AI Agent 가이드 (한국어)

> 영문 원본: [AGENTS.md](AGENTS.md) — canonical / 정본

## 프로젝트 구조

**Single-group 레이아웃 (기본):**
```
cmd/main.go                    Manager entry (controller/webhook 등록)
api/<version>/*_types.go       CRD 스키마 (+kubebuilder 마커)
api/<version>/zz_generated.*   자동 생성 (편집 금지)
internal/controller/*          Reconciliation 로직
internal/webhook/*             Validation/defaulting (있는 경우)
config/crd/bases/*             생성된 CRD (편집 금지)
config/rbac/role.yaml          생성된 RBAC (편집 금지)
config/samples/*               예시 CR (이쪽을 편집)
Makefile                       Build/test/deploy 명령
PROJECT                        Kubebuilder 메타데이터 자동 생성 (편집 금지)
```

**Multi-group 레이아웃** (복수 API group 프로젝트용):
```
api/<group>/<version>/*_types.go       group 별 CRD 스키마
internal/controller/<group>/*          group 별 controller
internal/webhook/<group>/<version>/*   group + version 별 webhook (있는 경우)
```

Multi-group 레이아웃은 group 이름 (예: `batch`, `apps`) 별로 API 를 정리한다. `PROJECT` 파일에서 `multigroup: true` 인지 확인.

**Multi-group 레이아웃으로 전환:**
1. 실행: `kubebuilder edit --multigroup=true`
2. API 이동: `mkdir -p api/<group> && mv api/<version> api/<group>/`
3. Controller 이동: `mkdir -p internal/controller/<group> && mv internal/controller/*.go internal/controller/<group>/`
4. Webhook 이동 (있는 경우): `mkdir -p internal/webhook/<group> && mv internal/webhook/<version> internal/webhook/<group>/`
5. 모든 파일의 import path 갱신
6. `PROJECT` 파일의 각 resource `path` 수정
7. 테스트 스위트의 CRD path 갱신 (상대 경로에 `..` 한 단계 추가)

## 중요 규칙

### 절대 편집 금지 (자동 생성)
- `config/crd/bases/*.yaml` - `make manifests` 로 생성
- `config/rbac/role.yaml` - `make manifests` 로 생성
- `config/webhook/manifests.yaml` - `make manifests` 로 생성
- `**/zz_generated.*.go` - `make generate` 로 생성
- `PROJECT` - `kubebuilder [OPTIONS]` 로 생성

### Scaffold 마커 삭제 금지
`// +kubebuilder:scaffold:*` 코멘트를 삭제하지 말 것. CLI 가 해당 마커 위치에 코드를 주입한다.

### 프로젝트 구조 유지
파일을 이동하지 말 것. CLI 는 파일이 특정 위치에 있을 것으로 기대한다.

### CLI 명령 사용 필수
스캐폴드에는 `kubebuilder create api` 와 `kubebuilder create webhook` 을 사용. 수동으로 파일 만들지 말 것.

### E2E 테스트는 격리된 Kind 클러스터 필요
e2e 테스트는 격리 환경 (GitHub Actions CI 와 유사) 에서 솔루션을 검증하도록 설계되었다. 전용 [Kind](https://kind.sigs.k8s.io/) 클러스터 (실제 dev/prod 클러스터 아님) 에서 실행.

## 변경 후

**`*_types.go` 또는 마커를 편집한 후:**
```
make manifests  # marker 로부터 CRD/RBAC 재생성
make generate   # DeepCopy 메서드 재생성
```

**`*.go` 파일을 편집한 후:**
```
make lint-fix   # 코드 스타일 자동 수정
make test       # unit 테스트 실행
```

## CLI 명령 치트시트

### API 생성 (자체 타입)
```bash
kubebuilder create api --group <group> --version <version> --kind <Kind>
```

### Deploy Image Plugin (임의의 컨테이너 이미지를 배포/관리하는 scaffold)

컨테이너 이미지 (nginx, redis, memcached, 자체 앱 등) 를 배포/관리하는 controller 생성:

```bash
# 예: memcached 배포
kubebuilder create api --group example.com --version v1alpha1 --kind Memcached \
  --image=memcached:alpine \
  --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

좋은 관습의 코드를 scaffolds: reconciliation 로직, status condition, finalizer, RBAC. 참고 구현으로 활용.


### Webhook 생성
```bash
# Validation + defaulting
kubebuilder create webhook --group <group> --version <version> --kind <Kind> \
  --defaulting --programmatic-validation

# Conversion webhook (multi-version API 용)
kubebuilder create webhook --group <group> --version v1 --kind <Kind> \
  --conversion --spoke v2
```

### Core Kubernetes 타입용 Controller
```bash
# Pod 감시
kubebuilder create api --group core --version v1 --kind Pod \
  --controller=true --resource=false

# Deployment 감시
kubebuilder create api --group apps --version v1 --kind Deployment \
  --controller=true --resource=false
```

### 외부 타입용 Controller (예: 다른 operator 의 타입)

외부 API (cert-manager, Argo CD, Istio 등) 의 리소스 감시:

```bash
# 예: cert-manager Certificate 리소스 감시
kubebuilder create api \
  --group cert-manager --version v1 --kind Certificate \
  --controller=true --resource=false \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

**참고:** 특정 버전이 필요할 때만 `--external-api-module=<module>@<version>` 사용. 그 외에는 `@<version>` 생략하면 go.mod 의 버전을 사용.

### 외부 타입용 Webhook

```bash
# 예: 외부 리소스 validation
kubebuilder create webhook \
  --group cert-manager --version v1 --kind Issuer \
  --defaulting \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

## 테스트 & 개발

```bash
make test              # unit 테스트 실행 (envtest 사용: 실제 K8s API + etcd)
make run               # 로컬 실행 (현재 kubeconfig context 사용)
```

테스트는 **Ginkgo + Gomega** (BDD 스타일) 를 사용한다. 셋업은 `suite_test.go` 참조.

## 배포 워크플로우

```bash
# 1. manifest 재생성
make manifests generate

# 2. 빌드 & 배포
export IMG=<registry>/<project>:tag
make docker-build docker-push IMG=$IMG  # 또는: kind load docker-image $IMG --name <cluster>
make deploy IMG=$IMG

# 3. 테스트
kubectl apply -k config/samples/

# 4. 디버그
kubectl logs -n <project>-system deployment/<project>-controller-manager -c manager -f
```

### API 설계

**`api/<version>/*_types.go` 의 주요 마커:**

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// 필드에:
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern="^[a-z]+$"
// +kubebuilder:default="value"
```

- **`metav1.Condition` 사용** for status (custom string 필드 대신)
- **predefined 타입 사용**: 날짜에는 `string` 대신 `metav1.Time` 사용
- **K8s API 규약 준수**: 표준 필드명 (`spec`, `status`, `metadata`)

### Controller 설계

**`internal/controller/*_controller.go` 의 RBAC 마커:**

```go
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
```

**구현 규칙:**
- **Idempotent reconciliation**: 여러 번 실행해도 안전
- **업데이트 전 재조회**: `r.Update` 전에 `r.Get(ctx, req.NamespacedName, obj)` 으로 conflict 회피
- **구조화 로깅**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner reference**: 자동 garbage collection 활성화 (`SetControllerReference`)
- **보조 리소스 watch**: `RequeueAfter` 만 쓰지 말고 `.Owns()` 또는 `.Watches()` 사용
- **Finalizer**: 외부 리소스 정리 (bucket, VM, DNS entry 등)

### 로깅

**Kubernetes 로깅 메시지 스타일 가이드 따르기:**

- 대문자로 시작
- 마침표로 끝내지 말 것
- 능동태: 주어 있음 (`"Deployment could not create Pod"`) 또는 생략 (`"Could not create Pod"`)
- 과거형: `"Cannot delete Pod"` 아닌 `"Could not delete Pod"`
- 객체 타입 명시: `"Deleted"` 아닌 `"Deleted Pod"`
- 키-값 쌍 균형

```go
log.Info("Starting reconciliation")
log.Info("Created Deployment", "name", deploy.Name)
log.Error(err, "Failed to create Pod", "name", name)
```

**참조:** https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

### Webhook
- **모든 타입을 함께 생성**: `--defaulting --programmatic-validation --conversion`
- **`--force` 사용 시**: 먼저 커스텀 로직을 백업, scaffolding 후 복원
- **Multi-version API 의 경우**: hub-and-spoke 패턴 사용 (`--conversion --spoke v2`)
  - Hub version: 보통 가장 오래된 안정 버전 (v1)
  - Spoke version: hub 로 변환되는/되는 신규 버전 (v2, v3)
  - 예: `--group crew --version v1 --kind Captain --conversion --spoke v2` (v1 이 hub, v2 가 spoke)

### 예제로부터 학습

**deploy-image plugin** 은 좋은 관습을 따르는 완전한 controller 를 scaffold 한다. 참고 구현으로 사용:

```bash
kubebuilder create api --group example --version v1alpha1 --kind MyApp \
  --image=<your-image> --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

생성된 코드 포함: status condition (`metav1.Condition`), finalizer, owner reference, event, idempotent reconciliation.

## 배포 옵션

### Option 1: YAML 번들 (Kustomize)

```bash
# Kustomize manifest 로부터 dist/install.yaml 생성
make build-installer IMG=<registry>/<project>:tag
```

**핵심 포인트:**
- `dist/install.yaml` 은 Kustomize manifest (CRD, RBAC, Deployment) 로부터 생성
- 손쉬운 배포를 위해 본 파일을 저장소에 commit
- 사용자는 `kubectl` 만으로 설치 가능 (추가 도구 불필요)

**예시:** 사용자는 단일 명령으로 설치:
```bash
kubectl apply -f https://raw.githubusercontent.com/<org>/<repo>/<tag>/dist/install.yaml
```

### Option 2: Helm Chart

```bash
kubebuilder edit --plugins=helm/v2-alpha                      # dist/chart/ 생성 (default)
kubebuilder edit --plugins=helm/v2-alpha --output-dir=charts  # charts/chart/ 생성
```

**개발용:**
```bash
make helm-deploy IMG=<registry>/<project>:<tag>          # Helm 으로 manager 배포
make helm-deploy IMG=$IMG HELM_EXTRA_ARGS="--set ..."    # custom value 로 배포
make helm-status                                         # release status 확인
make helm-uninstall                                      # release 제거
make helm-history                                        # release history 확인
make helm-rollback                                       # 이전 버전으로 rollback
```

**최종 사용자/production:**
```bash
helm install my-release ./<output-dir>/chart/ --namespace <ns> --create-namespace
```

**중요:** 초기 chart 생성 후 webhook 을 추가하거나 manifest 를 수정하면:
1. `<output-dir>/chart/values.yaml` 및 `<output-dir>/chart/manager/manager.yaml` 의 커스터마이즈 백업
2. 재실행: `kubebuilder edit --plugins=helm/v2-alpha --force` (커스텀 시 동일한 `--output-dir` 사용)
3. 백업에서 커스텀 값을 수동 복원

### 컨테이너 이미지 publish

```bash
export IMG=<registry>/<project>:<version>
make docker-build docker-push IMG=$IMG
```

## 참조

### 필수 자료
- **Kubebuilder Book**: https://book.kubebuilder.io (종합 가이드)
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md (공통 패턴 + 질문)
- **Good Practices**: https://book.kubebuilder.io/reference/good-practices.html (왜 reconciliation 이 idempotent 한지, status condition 등)
- **Logging Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines (메시지 스타일, verbosity 레벨)

### API 설계 & 구현
- **API Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- **Operator Pattern**: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html

### 도구 & 라이브러리
- **controller-runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **controller-tools**: https://github.com/kubernetes-sigs/controller-tools
- **Kubebuilder Repo**: https://github.com/kubernetes-sigs/kubebuilder

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
