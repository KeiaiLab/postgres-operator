---
title: "Roadmap (deprecated — 0.2.x)"
---

# 로드맵 — 14 Pillar × DoD 기반 (DEPRECATED)

> **이 문서는 0.2.x 시점 14-Pillar 로드맵 자료다. 0.3.0-alpha (ADR 0001 자체 분산 SQL keystone) 채택으로 폐기되었다.**
>
> 현재 로드맵은 README §Roadmap 의 **8-Phase (P0~P7)** 를 참조한다. 본 파일은 history 보존용으로만 유지하며, 후속 PR 에서 archive 또는 단계적 재작성 예정.

## Maturity Level (전 Pillar 공통)

| Level | 이름 | 통과 조건 |
|---|---|---|
| **M0** | Spike | PoC 동작, happy path 1개, 인터페이스/CRD 시그니처 동결 |
| **M1** | Alpha | 단위 ≥80%, e2e 1개, 문서 초안, 알려진 한계 명시 |
| **M2** | Beta | e2e 매트릭스, 회귀 테스트, 운영 가이드, 백워드 호환 |
| **M3** | GA | 카오스 통과, 성능 회귀 게이트, CVE SLA, semver 안정 약속 |

## 14 Pillar

| Pillar | 영역 | 핵심 산출 | 의존 |
|---|---|---|---|
| **P1** | Core Lifecycle | `PostgresCluster` CRD, instance manager(Go PID1), StatefulSet/Service | — |
| **P2** | HA / Failover | K8s lease election, fencing(PVC label), failover controller | P1 |
| **P3** | Storage / WAL | PVC 관리, WAL 아카이빙, base restore | P1 |
| **P4** | Backup / PITR | `BackupJob` CRD, pgBackRest 통합, named restore point | P2, P3 |
| **P5** | Connection / Pooling | PgBouncer 사이드카 + 독립 Deployment | P1 |
| **P6** | Observability | pgMonitor 호환 exporter, Grafana 대시보드, PrometheusRule | P1 |
| **P7** | Security / TLS | cert-manager 통합, mTLS, audit log | P1 |
| **P8** | User / DB / RBAC | `PgUser`, `PgDatabase`, GRANT 화이트리스트 | P1, P7 |
| **P9** | Upgrade | `ClusterUpgrade` CRD, in-place + blue/green | P2, P4 |
| **P10** | Extensions | 화이트리스트, lifecycle, `shared_preload_libraries` 우선순위 | P1 |
| **P11** | ⭐ Citus Topology | coord+workers, `pg_dist_node` sync, 4종 분산 CRD | P1, P2, P10 |
| **P12** | ⭐ QueryRouter | stateless 라우터 풀, PgBouncer 사이드카, HPA, metadata lag 메트릭 | P11, P5, P6 |
| **P13** | ⭐ Plugin SDK | 5종 Go 인터페이스 + gRPC out-of-process | P4, P6, P10 |
| **P14** | Distribution | install.yaml, OLM bundle, multi-arch 이미지 (※ Helm chart는 ADR 0007에 따라 **P1 트랙으로 분리 — alpha 사용자 채널 조기 확보**) | 모두 |

## 의존 그래프

```
P13-T1 (인터페이스 동결) ━━━┓
                            ┃ (블로킹 — 다른 모든 Pillar 진입 전제)
P1 ━┳━ P2 ━┓                ┃
    ┃      ┣━━ P4 ━━━━━━━━━┓┃
    ┣━ P3 ━┛               ┃┃
    ┣━ P5                  ┃┃
    ┣━ P6 ━━━━━━━━━━━━━━━━━┃┃━ P13-T2~T6
    ┣━ P7 ━━ P8            ┃┃
    ┗━ P10 ━━ P11 ━━ P12 ━━┛┃
모두 통과 ━━━━ P14 ━━━━━━━━━━┛
```

**크리티컬 패스**: P13-T1 → P1-M2 → P10-M2 → P11-M2 → P12-M2 → P14-T6.

## 작업량 (총 68 task)

| Pillar | Task 수 |
|---|---|
| P1 | 4 |
| P2 | 4 |
| P3 | 3 |
| P4 | 5 |
| P5 | 3 |
| P6 | 5 |
| P7 | 5 |
| P8 | 4 |
| P9 | 5 |
| P10 | 4 |
| **P11** | **8** |
| **P12** | **6** |
| **P13** | **6** |
| P14 | 6 |
| **합계** | **68** |

진척률 계산: `완료 task ÷ 68 × 가중치(M0=0.25, M1=0.5, M2=0.75, M3=1.0)`.

## 마일스톤 — 능력 기반 (날짜 없음)

| 릴리즈 | 통과 조건 |
|---|---|
| **v0.0** (현재) | Phase 0 부트스트랩 완료 |
| **v0.1 alpha** | P1-M1, P10-M0, **P13-T1 동결**, dev quickstart 동작 |
| **v0.3 alpha** | P1-M2, P2/P3/P5/P6-M1, P11-M0 (수동 add_node) |
| **v0.5 beta** | P1~P7 모두 M2, P10-M2, P11-M1, P13-T2 (linter 강제) |
| **v0.7 beta** | P1~P10 M3 후보, P11-M2, P12-M1, P9-M1 (in-place upgrade) |
| **v0.9 RC** | 전 Pillar M2+, P11/P12/P13 M2, P14-T1~T4 |
| **v1.0 GA** | 전 Pillar M3, 전 품질 게이트 통과, semver 약속 |
| **v1.x** | P13-M3 (외부 플러그인 ≥3), OpenShift 인증, 멀티리전 DR |

> 어떤 릴리즈도 "OO년 OO월" 약속 없음. **"DoD 통과 시 릴리즈"** 가 유일한 규칙.

## 작업 큐 (의존 순)

### 큐-A — 즉시 착수 (병렬 가능)
1. **ADR 0004** — Build vs Fork vs Layer 결정 기록 ✅
2. **ADR 0001 갱신** — PGO-class + 4 차별화로 미션 재정의 ✅
3. **`docs/roadmap.md` 재작성** — 14 Phase → 14 Pillar (이 문서) ✅
4. **RFC 0001 commit + 보강** — untracked 해제, QueryRouter 분리 메모

### 큐-B — 큐-A 완료 후, P1+P13 동시 시작
5. **P13-T1**: `internal/plugin/api.go` 인터페이스 5종 동결 ⭐ (가장 중요한 단일 작업)
6. **P1-T1**: `kubebuilder create api --group postgres --version v1alpha1 --kind PostgresCluster`
7. **P1-T3**: `internal/instance/main.go` PID1 골격
8. **CI 보강**: Pillar 라벨(`make test-e2e PILLAR=p1`)

### 큐-C — P1-M0 + P13-T1 동결 후
- P1-T2: reconciler 구현
- P1-T4: webhook 구현
- P10-T1: extension plugin 인터페이스 첫 구현체

### 큐-D — P1-M1 도달 후 (5트랙 동시)
- P2 (HA): T1~T4
- P3 (Storage): T1~T3
- P5 (Pooling): T1~T3
- P6 (Observability): T1~T3
- P10 (Extensions): T2~T4

### 큐-E — P10-M2 + P2-M2 후
- P11 (Citus): T1~T8
- P7 (Security): T1~T5
- P8 (User/DB): T1~T4

### 큐-F — P11-M1 후
- P12 (QueryRouter): T1~T6
- P4-T4 (분산 PITR — P11 의존)

### 큐-G — P4/P6/P10 M2 후
- P13 후속: T2~T6 (gRPC, manifest, 가이드)

### 큐-H — 전 Pillar M3 후
- P14 (Distribution): T1~T6

## 진척 측정

`TASKS.md`(또는 GitHub Project board)에 다음만 기록:
- ✅ 완료 task 번호와 PR 링크
- 🟡 진행 중 task와 owner
- ⚪ 대기 task와 선행 의존
- 누적 진척률(가중치 적용)
- 각 Pillar별 다음 DoD 항목

> "OO월까지 끝낸다"는 표현 금지. "P11-T2 통과 후 P12 트랙 개시"처럼 **선행/후행 관계로만 기술**.

## 품질 게이트 (v1.0 GA 통과 조건)

| 게이트 | 기준 | 도입 시점 |
|---|---|---|
| 단위 테스트 커버리지 | ≥ 80% per package | 즉시 |
| e2e 매트릭스 | PG{16,17}×Citus{12.1,13.0} 6조합 + PG18 (preview) | M2부터 |
| 업그레이드 회귀 | 직전 마이너 → 현 마이너 자동 승격 e2e | v0.5부터 |
| 보안 스캔 | Trivy + Snyk SCA + govulncheck | 즉시/단계 도입 |
| SBOM | Syft 자동 생성 + 릴리즈 첨부 | v0.7 |
| 서명 | cosign keyless (sigstore) 이미지 + 차트 서명 | v0.7 |
| 라이선스 컴플라이언스 | go-licenses 자동 검사 | v0.5 |
| 성능 회귀 | pgbench nightly, 5% regression alert | v0.7 |
| 카오스 테스트 | LitmusChaos: pod kill / network partition / disk full | v0.7 |
| CVE 응대 | Critical 24h, High 7d 패치 약속 | v1.0 |
| API 안정성 | v1 GA 후 deprecated 필드 ≥ 1마이너 유지 | v1.0 |
| 문서 자동 생성 | crd-ref-docs, mintlify 자동 배포 | v0.5 |

## ADR/RFC 백로그

### 기존
- ADR 0001 v2 — 미션 재정의 (PGO-class + 3축) ✅
- ADR 0002 — Patroni 미사용 ✅
- ADR 0003 — QueryRouter Stateless ✅
- ADR 0004 — Build, not Fork/Layer ✅
- RFC 0001 — CRD Schema v1alpha1 (Draft, 큐-A4에서 commit + 보강 예정)

### 신규 (작성 우선순위)
| 번호 | 제목 | Pillar |
|---|---|---|
| ADR 0005 | Plugin SDK 인터페이스 모델 | P13 |
| ADR 0006 | Base image 듀얼 (UBI9 + Debian) | P14 |
| RFC 0002 | Metadata Sync 알고리즘 | P11 |
| RFC 0003 | HA election + fencing 프로토콜 | P2 |
| RFC 0004 | Backup/PITR 모델 + pgBackRest 1차 | P4 |
| RFC 0005 | Router Statelessness Gates | P12 |
| RFC 0006 | Security/RBAC | P7/P8 |
| RFC 0007 | Observability + pgMonitor 호환 | P6 |
| RFC 0008 | DistributedTable 의미론 | P11 |
| RFC 0009 | QueryRouter CRD 분리 vs 서브필드 | P12 |
| RFC 0010 | Upgrade 모델 | P9 |
| RFC 0011 | Extension 우선순위 알고리즘 | P10 |
| RFC 0012 | Plugin SDK 안정화 + 가이드 | P13 |

## PGO 패리티 매트릭스 (v1.0 GA 약속)

| 기능 | PGO 6.0.1 | 본 프로젝트 v1.0 |
|---|---|---|
| 단일 PG HA | ✅ Patroni | ✅ K8s lease + 자체 IM |
| pgBackRest 통합 | ✅ | ✅ |
| WAL-G | 부분 | ✅ (플러그인) |
| PgBouncer | ✅ | ✅ |
| pgMonitor 호환 | ✅ 본가 | ✅ 호환 + 추가 메트릭 |
| TLS/mTLS | ✅ TLS, custom CA | ✅ TLS+mTLS+cert-manager |
| 멀티 K8s standby | ✅ | ✅ P14 |
| Major upgrade | ✅ in-place | ✅ in-place + blue/green |
| 확장 동봉 | 9개+ | 8개 + Citus + pgvector |
| OpenShift 인증 | ✅ | ⚠️ v1.1 목표 |
| OLM bundle | ✅ | ✅ P14 |
| **Citus 1급** | ❌ | ✅ |
| **Stateless QueryRouter** | ❌ | ✅ |
| **분산 PITR (2PC)** | ❌ | ✅ |
| **Plugin SDK** | ❌ | ✅ |

자세한 분석은 `/Users/phil/.claude/plans/squishy-squishing-harp.md` (외부 분석 문서, 미공개) 또는 [ADR 0004](adr/0004-build-not-fork-or-layer.md) 참조.

## 위험·전제

| 위험 | 영향 | 완화 |
|---|---|---|
| 메인테이너 부족 → 일정 1.5배 슬립 | DoD 통과 시점 늦어짐 | 거버넌스에 "Pillar 오너" 모집 명시, contribution ladder 정의 |
| Citus 라이선스 모델 변경 | P11 위험 | Citus 라이선스 동향 분기 모니터링 워크플로 |
| pgBackRest의 Citus 분산 PITR 미지원 | P11+P4 합류 지점 | RFC 0004에서 "분산 named restore point는 우리가 2PC로 강제, pgBackRest는 단일 PG 백업만 위임" |
| 외부 플러그인 보안 표면 (P13 gRPC) | v1.x 위험 | UDS 전용 + cosign 서명 강제 + manifest allowlist |
| PGO 6.x의 OperatorHub 점유율 | 인지도 격차 | "PGO와 다른 슬롯(Citus 1급 + Plugin SDK)" 일관 메시지 |
| K8s API 변경(예: lease semantics) | P2 위험 | controller-runtime/k8s.io 버전 분기당 회귀 e2e |

## 한 줄 결론

> **"PGO를 카피하지 않는다. PGO가 검증한 운영 idiom은 흡수하되, 코드는 처음부터 자체 작성하고, PGO가 안 한 4가지(Citus 1급, Stateless Router, 분산 PITR, Plugin SDK)에 평생 차별화 자원을 집중한다."**
