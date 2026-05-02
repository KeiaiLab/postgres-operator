# ADR 0006 — 데이터플레인 PodSecurityContext 기본값 (Security Defaults Rationale)

- **상태**: Accepted
- **날짜**: 2026-04-30
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001 v2 (PGO-class 패리티), Bitnami PostgreSQL Helm Chart 비교 (`/Users/phil/.claude/plans/1-https-artifacthub-io-packages-helm-bit-sunny-wozniak.md` §4 P0-2)

## 컨텍스트

본 프로젝트의 *manager Pod*(operator 자체)은 `config/manager/manager.yaml:53-74`에서 강한 SecurityContext를 적용한다 — `runAsNonRoot=true`, `readOnlyRootFilesystem=true`, `seccompProfile=RuntimeDefault`, `capabilities.drop=[ALL]`. 그러나 *데이터플레인 Pod*(`buildPGStatefulSet:184-198`, `buildRouterDeployment:243-256`)에는 SecurityContext가 0개다.

이는 **비대칭 보안 부채**:
- PSS(Pod Security Standards) `restricted` 정책이 적용된 클러스터에서 admission 거부 가능
- `runAsNonRoot=false`(기본) 상태에서 PG 컨테이너가 root로 기동될 수 있음 — 호스트 escape 위험
- Bitnami PostgreSQL Helm Chart가 *기본값*으로 제공하는 보안 설정에 미달 → "PGO-class 패리티" 약속과 모순

## 결정

`buildPGStatefulSet`과 `buildRouterDeployment`가 생성하는 모든 데이터플레인 Pod에 다음 SecurityContext 기본값을 *항상* 적용한다:

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 70           # PG 표준 postgres user UID
  runAsGroup: 70
  fsGroup: 70
  seccompProfile:
    type: RuntimeDefault
container.securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: [ALL]
```

`readOnlyRootFilesystem=true` 동반 변경:
- emptyDir 마운트 추가 — `/tmp`, `/run`, `/var/run/postgresql` (PG가 lock/socket 작성 필요)
- `/var/lib/postgresql/data`는 PVC 마운트라 별도 emptyDir 불필요

## 근거

### 왜 UID 70인가
PostgreSQL 공식 컨테이너 이미지(postgres:*)가 `postgres` user를 UID/GID 70으로 정의. 본 프로젝트의 향후 cmd/instance 이미지도 동일 규약 따름.

### 왜 `readOnlyRootFilesystem=true`인가
- 컨테이너 내 임의 바이너리 작성 차단 → 공급망 공격 완화
- Bitnami chart 기본값
- emptyDir 마운트 비용은 미미(memory backed) → 트레이드오프 ↑

### 왜 *기본값*으로 강제인가
PostgresCluster CR에 `Spec.SecurityContext` override 필드를 두면 *opt-in 보안*이 됨 — 운영자가 잊으면 root 가능 상태. 본 ADR은 *opt-out*으로 강제: 기본값은 항상 위 설정, override는 webhook이 검증.

## 트레이드오프

- **`readOnlyRootFilesystem` 호환성**: 일부 PG extension(예: pg_cron, pg_stat_statements)이 디스크 임시 파일 생성. 해결: `/tmp` emptyDir + extension 별 PVC subpath. 본 ADR은 기본 emptyDir 3개로 충분, extension별 추가는 P10에서 처리.
- **사용자 정의 UID 요구**: 일부 K8s 환경(OpenShift 일부 SCC)이 random UID 강제. 해결: webhook이 `runAsUser`를 nil로 두면 K8s SCC가 채우도록 허용 (P0-2 implementation 시 webhook 추가).
- **기존 PVC 데이터의 ownership**: 기존 PG가 root로 쓴 데이터를 UID 70으로 읽으려면 fsGroup 변경. K8s `fsGroup`이 자동 처리하나 첫 transition은 시간 걸릴 수 있음.

## 결과

- `internal/controller/builders.go`의 `buildPGStatefulSet`과 `buildRouterDeployment`에 SecurityContext 주입 (P0-2 권장 적용 시).
- envtest assertion: 생성된 Pod에 `runAsNonRoot=true`, `runAsUser=70` 확인.
- e2e 회귀: restricted PSA 적용 namespace에서 admission 통과 검증.
- 본 ADR 변경(UID 변경, capabilities 추가)은 RFC 0006 "Security/TLS"의 일부로 처리.

## 강제 메커니즘

| 메커니즘 | 위치 | 도입 시점 |
|---|---|---|
| 기본값 주입 | `internal/controller/builders.go` | P0-2 implementation |
| webhook 검증 | `internal/webhook/v1alpha1/postgrescluster_webhook.go` | P0-2 후속 (override 시 최소값 강제) |
| envtest 회귀 | `internal/controller/builders_test.go` | P0-2 implementation |
| e2e 회귀 | `test/e2e/security_test.go` (신규) | P0-2 implementation |
