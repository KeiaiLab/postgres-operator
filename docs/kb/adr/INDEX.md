# Architecture Decision Records — INDEX

본 디렉터리는 postgres-operator 의 비역행 결정(architecture decisions)을 Nygard
5섹션 형식으로 보존한다. 결정의 *이유*가 코드보다 오래 살아남도록 한다.

경로 표준: `<repo>/docs/kb/adr/` (글로벌 `standards/adr.md §1`).

## 활성 ADR

| 번호 | 제목 | 상태 | 날짜 |
|------|------|------|------|
| [ADR-0001](0001-self-built-distributed-sql.md) | PostgreSQL 위 자체 분산 SQL 레이어 구축 | Accepted | 2026-04-30 |
| [ADR-0002](0002-single-chart-with-flags.md) | Helm 단일 chart + 컴포넌트 flag 정책 | Accepted | 2026-04-30 |
| [ADR-0003](0003-license-policy-no-agpl-busl.md) | 외부 의존 라이선스 정책 (AGPL/BUSL/CSL/SSPL 영구 금지) | Accepted | 2026-04-30 |
| [ADR-0004](0004-crd-managed-by-operator.md) | CRD 라이프사이클은 operator manager 가 소유 | Accepted | 2026-04-30 |
| [ADR-0005](0005-versioning-and-channels.md) | 릴리스 채널 (alpha/beta/stable) 과 CRD apiVersion 진화 | Accepted | 2026-04-30 |
| [ADR-0006](0006-gitops-deploy-overlay.md) | GitOps deploy 오버레이 도입 (3-repo 정합) | Accepted | 2026-05-06 |
| [ADR-0007](0007-pre-commit-instead-of-lefthook.md) | Hook 도구로 pre-commit 채택 (글로벌 lefthook 표준 분기) | Accepted | 2026-05-06 |

## Archived (v0.x — 재설계 이전 결정 기록)

v0.x 사이클에서 *PGO-class 풀스택 + Citus 1급 + Plugin SDK* 를 목표로 했던 ADR 묶음. 2026-04-30 의 keystone 재설계 (ADR-0001 신규) 이후 *역사적 참고용으로만* 보존. 새 코드 / 새 결정은 본 archive 의 결론을 *의무적으로 따르지 않는다*. 단, 일부 archived ADR (0008 cascade delete, 0009 no-github-actions-rfc-0002) 는 *현행 운영 정책과 여전히 부합* — 본 INDEX 의 비고 컬럼 참조.

| 번호 | 제목 | 비고 |
|------|------|------|
| [v0.x ADR-0001](_archive/v0.x/0001-stateless-query-router-on-citus.md) | 미션 재정의: PGO-class 풀스택 + Citus 1급 + Plugin SDK | Superseded by 활성 ADR-0001 |
| [v0.x ADR-0002](_archive/v0.x/0002-no-patroni-instance-manager.md) | Patroni 미사용, Instance Manager + K8s API as DCS | 활성 — Instance Manager 패턴 유지 |
| [v0.x ADR-0003](_archive/v0.x/0003-queryrouter-stateless-design.md) | QueryRouter 계층의 Stateless 설계 | 활성 — `pg-router` 도 동일 원칙 |
| [v0.x ADR-0004](_archive/v0.x/0004-build-not-fork-or-layer.md) | Build from Scratch (PGO Fork·Soft Layer 모두 거부) | 활성 — 본 repo 의 keystone 정책 |
| [v0.x ADR-0005](_archive/v0.x/0005-plugin-sdk-interface-model.md) | Plugin SDK 인터페이스 모델 (in-process + gRPC) | Superseded — Plugin SDK 폐기 (활성 ADR-0001) |
| [v0.x ADR-0006](_archive/v0.x/0006-security-defaults-rationale.md) | 데이터플레인 PodSecurityContext 기본값 | 활성 — Pod security defaults 유지 |
| [v0.x ADR-0007](_archive/v0.x/0007-helm-chart-promoted-to-p1.md) | Helm chart 을 P14 에서 P1 트랙으로 분리 | Superseded by 활성 ADR-0002 (단일 chart + flag) |
| [v0.x ADR-0008](_archive/v0.x/0008-finalizer-avoidance-policy.md) | Finalizer 회피 정책 (Cascade Delete via OwnerReference) | 활성 — cascade delete 패턴 운영 |
| [v0.x ADR-0009](_archive/v0.x/0009-no-github-actions-rfc-0002.md) | GitHub Actions 폐기 + 로컬 4 계층 게이트 | 활성 — RFC 0002 글로벌 정책의 본 repo 적용 기록 |
| [v0.x ADR-0010](_archive/v0.x/0010-license-and-sharding-strategy.md) | License + Sharding Strategy (Citus AGPL Isolation, vanilla PG default) | Superseded by 활성 ADR-0001 (Citus 의존 0줄) |

## 작성 가이드

- 형식: Nygard 5섹션 (Context / Decision / Consequences / Alternatives Considered / Status).
- 위치: `docs/kb/adr/NNNN-<영어 kebab-case slug>.md` (글로벌 표준).
- 번호 부여: 4자리 0-padded, 한 번 부여한 번호는 *재사용 금지*.
- 본 INDEX.md 는 신규 ADR 추가 시 *수동 갱신 의무* — `standards/enforcement.md §2.1`.
- v0.x archive 는 history 보존 영역 — 새 결정은 *활성 영역* 에만 추가한다.

## 글로벌 참조

- 글로벌 ADR 표준: `~/Documents/ai-dev/standards/adr.md`
- ADR 커버리지 게이트: `scripts/check-adr-coverage.sh` (글로벌)
- 강제 표준: `~/Documents/ai-dev/standards/enforcement.md §2.1`
