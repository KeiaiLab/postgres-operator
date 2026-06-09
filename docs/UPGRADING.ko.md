<p align="center">
  <a href="UPGRADING.md">English</a> |
  <b>한국어</b> |
  <a href="UPGRADING.ja.md">日本語</a> |
  <a href="UPGRADING.zh.md">中文</a>
</p>

# postgres-operator 업그레이드 (한국어)

> 영문 원본: [UPGRADING.md](UPGRADING.md) — canonical / 정본

본 문서는 postgres-operator 의 minor/major 버전 업그레이드 시 필요한 마이그레이션 작업을 정리한다. Helm 사용자는 chart 업그레이드만으로 모든 변경이 적용되지만, 정적 manifest (`kubectl apply -f`) 사용자는 RBAC 등 일부 항목을 수동으로 patch 해야 한다.

## 0. 버전 정책 (semver)

| 변경 유형 | semver bump | 예시 |
|---|---|---|
| 신규 controller / CR / API 추가 | minor (v1.X → v1.X+1) | PostgresPooler 신설 |
| 기존 API 시그니처 변경 (breaking) | major (v1.X → v2.0) | PostgresCluster.spec.storage struct 변경 |
| bug fix / dependency bump | patch (v1.X.Y → v1.X.Y+1) | controller-runtime 0.19→0.20 |
| operator-commons 의존 bump | minor (commons v0.X → v0.X+1) | pkg/reconcile 신규 채택 |

## 1. v0.1.x → v0.2.x

### Helm 사용자

```bash
helm repo update
helm upgrade postgres-operator <repo>/postgres-operator \
  --namespace postgres-operator-system \
  --version 0.2.x
```

chart 가 RBAC, CRD, Deployment 를 모두 동기화한다. 추가 작업 불필요.

### 정적 manifest 사용자 — RBAC 마이그레이션

`make build-installer` 결과인 `dist/install.yaml` 의 차이 확인:

```bash
kubectl diff -f dist/install.yaml
kubectl apply -f dist/install.yaml
```

기존 ClusterRole 의 신규 권한 (현재 없음 — 본 minor 는 RBAC 변경 없음):

| API group | Resource | 사유 | 추가 시점 |
|---|---|---|---|
| (없음) | — | — | — |

## 2. v0.2.x → v0.3.x (예정)

### operator-commons v0.9.0 채택 (Sprint 1 + S5)

```bash
# go.mod 의 operator-commons 의존 bump 후
go mod tidy
```

- **신규 import**: `github.com/keiailab/operator-commons/pkg/pvc`, `pkg/topology` (Sprint 1)
- **추가 import 예정**: `pkg/reconcile`, `pkg/resources` (S5 후속)
- **중복 코드 제거**: `internal/controller/` 의 자체 helper 가 operator-commons 의 helper 로 교체. 동작 동일.

마이그레이션 영향:
- Reconcile 동작 동일 (refactor 만, 외부 동작 변경 없음)
- CRD spec 변경 없음 (v1alpha2 conversion 별 cycle)
- Helm chart 영향 없음

## 3. v0.3.x → v1.0.0 (예정 — 상용 제품 수준 마일스톤)

CLAUDE.md §7 의 *상용 제품 수준* (P0+P1+P2+OP+C 모두 ✅) 도달 시.

- 모든 CR 의 API stability `Stable` 승격 (v1)
- breaking change 없음 (v0.x → v1.0 은 *명명만* 변경)
- 상용 제품 수준 품질 검증: `make audit-quality`

## 4. GHA dual-track 정책 (ADR-0019)

본 repo 는 RFC-0002 (GitHub Actions 영구 금지) 의 *예외* — public OSS operator 의 external trust gate 필요로 GHA 14 workflow 유지 + 로컬 4 계층 (lefthook) 과 dual-track 운영 (ADR-0019).

업그레이드 시 GHA workflow 변경은 `dependabot/github_actions/*` PR 으로 자동. *사람 PR* 으로 `.github/workflows/` 신규 파일 추가는 *별 ADR* + 사용자 승인 필요.

## 5. 일반 마이그레이션 체크리스트

업그레이드 전:
- [ ] CRD 변경 (`api/v1alpha1/` 의 ObjectMeta 가 v1alpha2 와 호환)
- [ ] `make verify` (lint + test + build + audit) 통과
- [ ] 기존 e2e 스위트 PASS (`make integration-test`)
- [ ] dependabot 의존 bump PR 통합 확인

업그레이드 후:
- [ ] Helm chart 의 `dependencies:` (keiailab-commons library chart) 갱신
- [ ] 각 CR 의 spec 호환성 검증 (특히 storage, resources)
- [ ] reconcile 결과 verify (`kubectl get postgrescluster -A`)
- [ ] 운영 메트릭 (`Reconcile{Total,Latency,Errors}`) 정상

## 6. 비호환 변경 안내 정책

- **Deprecation**: 신규 minor 에서 `// Deprecated:` 주석 + 2 minor 후 제거
- **Breaking**: major bump + 본 UPGRADING.md 의 별 섹션 + ADR 작성
- **사후 통보 안 함**: 모든 breaking 변경은 *최소 1 minor* 사전 deprecation

## 참고

- ADR 목록: `docs/kb/adr/INDEX.md`

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
