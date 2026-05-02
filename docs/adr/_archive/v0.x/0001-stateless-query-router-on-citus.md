# ADR 0001 — 미션 재정의: PGO-class 풀스택 + Citus 1급 + Plugin SDK

- **상태**: Accepted (재정의, 2026-04-27 갱신)
- **날짜**: 2026-04-26 (최초) → 2026-04-27 (재정의)
- **결정자**: @keiailab/maintainers
- **대체 이력**:
  - v1 (2026-04-26): "MongoDB sharded topology on Citus" 폐기 → "Citus 표준 + Stateless QueryRouter 단일 차별화" 채택
  - **v2 (2026-04-27, 본 문서)**: "단일 차별화" 좁은 포지셔닝 폐기 → "PGO-class 풀스택 + Citus 1급 + Plugin SDK" 3축 채택
- **관련**: ADR 0002 (Patroni 미사용), ADR 0003 (QueryRouter Stateless), **ADR 0004 (Build, not Fork/Layer)**
- **선행 분석**: `/Users/phil/.claude/plans/squishy-squishing-harp.md` §7~§10

## 컨텍스트

v1 ADR은 "Citus + Stateless QueryRouter 단일 차별화"라는 좁은 포지셔닝을 채택했다. 그러나 사용자 비전이 **"확장성 높고 유연한, 상용 제품 수준의 오픈소스 PG 쿠버네티스 오퍼레이터"** 로 확장되면서 좁은 포지셔닝은 다음 모순을 일으킨다.

1. **상용 품질 약속 vs 좁은 차별화의 충돌**: "단일 PG HA로 경쟁하지 않는다"는 v1 입장은 PGO/CNPG가 그 영역을 덮는 동안에만 유효하다. 사용자가 "PGO 수준 품질"을 약속한 이상 단일 PG HA도 직접 책임져야 한다.
2. **확장성의 의미 부재**: "유연한"의 본질은 사용자가 보는 CRD 필드 다양성이 아니라 **새 백업 도구·exporter·extension·router를 1주 안에 추가할 수 있는 SDK 구조**다. v1에는 이 메타-차별화가 없었다.
3. **Citus를 "유일 차별점"으로 두면 Citus 라이선스 동향 변화 시 프로젝트 전체가 위험**하다. Citus를 1급 기능으로 두되, 단일 PG HA 운영 + Plugin SDK라는 추가 축으로 위험을 분산해야 한다.

## 결정

본 오퍼레이터의 미션을 다음 세 축으로 재정의한다.

### 미션 (한 문장)

> Apache-2.0 Go 단일 오퍼레이터로, **PGO 수준의 단일 PG HA 운영 품질** + **Citus 분산 토폴로지 1급 지원** + **플러그인 SDK 기반 확장성**을 한 번에 제공한다.

### 3축의 의미

1. **PGO-class 풀스택 (Pillar P1~P10, P14)**
   - Crunchy PGO 6.0.1과 패리티 가능한 단일 PG HA 운영 품질을 자체 코드로 제공
   - HA, 백업/PITR, 풀러, 모니터링, 보안, 업그레이드, 확장 관리, 멀티 K8s standby
   - 직접 코드 책임 — 외부 오퍼레이터 의존 없음 (ADR 0004)
2. **Citus 1급 (Pillar P11~P12, 본 프로젝트의 첫 번째 차별화)**
   - `coordinator + workers[]` 단일 CR 표현
   - `pg_dist_node` 자동 메타데이터 sync
   - `DistributedTable`/`ReferenceTable`/`RebalanceJob`/`ShardPlacementPolicy` 선언적 분산 테이블
   - **Stateless QueryRouter 계층** (ADR 0003 유지)
   - **분산 PITR** (`citus_create_restore_point` 2PC 조정자)
3. **Plugin SDK (Pillar P13, 메타 차별화)**
   - `BackupPlugin`/`ExporterPlugin`/`ExtensionPlugin`/`RouterPlugin`/`AuthPlugin` 5종 인터페이스
   - 핵심 reconciler는 인터페이스만 호출 (구체 구현 import 금지 — linter 강제)
   - in-process(compile-time) + out-of-process(gRPC over UDS) 두 모델

### 양보할 수 없는 품질 기준 (PGO 6.0.1 기준선 vs 본 프로젝트 v1.0 GA 약속)

| 차원 | PGO 6.0.1 기준선 | 본 프로젝트 v1.0 GA 약속 |
|---|---|---|
| 지원 PG 메이저 | 14, 15, 16, 17, 18 | **16, 17, 18** (14/15는 EOL 임박, P10 재검토) |
| HA 메커니즘 | Patroni 분산합의 + Pod anti-affinity | **K8s API as DCS + 자체 instance manager** (ADR 0002) |
| 백업 | pgBackRest 2.58, multi-repo (local/S3/GCS/Azure) | **pgBackRest 1차 GA, WAL-G/Barman 플러그인** (P4) |
| 풀러 | PgBouncer 1.25 | **PgBouncer 1.25+** (사이드카 + 독립 모드) |
| 모니터링 | pgMonitor (Prometheus + Grafana + Alertmanager) | **pgMonitor 호환 대시보드 + Citus 특화 메트릭** |
| 보안 | TLS 전체, custom CA | **TLS + mTLS + cert-manager 통합** |
| 업그레이드 | major in-place | **major in-place + blue/green 양방향** |
| 멀티 K8s standby | ✅ | **P14, async + sync 옵션** |
| 베이스 이미지 | UBI 9 (Red Hat) | **UBI 9 + Debian bookworm 듀얼** |
| 아키텍처 | amd64 + arm64 | amd64 + arm64 (매트릭스 빌드) |
| 확장 동봉 | pgaudit, pg_cron, pg_partman, pgnodemx, set_user, pgvector, postgis, timescaledb, wal2json | **첫 7개 + Citus + pgvector** (timescaledb·orafce는 v1.x) |

### PGO가 안 하는 4가지 (본 프로젝트의 존재 이유)

1. **Citus 1급 토폴로지** — `coordinator + workers[]` 단일 CR + 자동 메타데이터 sync
2. **Stateless QueryRouter** — Citus 11+ metadata-synced PG + PgBouncer 사이드카 + HPA
3. **분산 PITR** — `citus_create_restore_point` 2PC 조정자
4. **Plugin SDK** — backup/exporter/extension/router-plugin이 인터페이스로 추상화돼 외부 모듈로 추가 가능

### 토폴로지 (변동 없음 — v1 ADR 유지)

- **Coordinator** (HA replica set): Citus 표준 coordinator. `pg_dist_*` 메타데이터 권위, 분산 DDL 게이트웨이.
- **Worker** (HA replica set per pool): Citus 표준 worker. 분산 테이블 shard 보유.
- **QueryRouter** (신규, stateless): Citus 11+ `metadata_synced=true` PG + PgBouncer 사이드카. 무상태, HPA. ADR 0003 참조.

### CRD 명명 — Citus 표준 유지 (v1 ADR 유지)

- API Group: `postgres.keiailab.io`
- Root CR: `PostgresCluster` (PGO와 명명 충돌하지만 그룹이 다르므로 안전)
- 부가 CRD: `DistributedTable`, `ReferenceTable`, `RebalanceJob`, `ShardPlacementPolicy`, `BackupJob`, `PgUser`, `PgDatabase`, `ClusterUpgrade`
- `QueryRouter` CRD 분리 vs `PostgresCluster.routers` 서브필드: **RFC 0009로 위임**

## 근거

### 좁은 차별화에서 풀스택+SDK로 확장하는 이유

- **상용 품질 약속이 통제권을 요구**한다 (ADR 0004 §옵션 B 거부 사유): 우리가 "PGO 수준" 약속을 한 이상 그 품질의 코드 경로 전체를 보유해야 한다.
- **Plugin SDK가 "유연성"의 진짜 의미**: CRD 필드를 늘리는 게 아니라, 새 백업 도구를 1주 안에 추가할 수 있는 구조가 진짜 유연성이다. 이 구조는 컨트롤러 코드 전체에 인터페이스 호출 규약을 강제할 때만 의미 있다.
- **Citus 단일 의존 위험 분산**: Citus가 라이선스 변경(예: AGPL 강화)되더라도 본 프로젝트의 PGO-class 영역은 가치 보존된다.

### v1의 "단일 차별화" 명제가 여전히 유효한 부분

- **자원 집중 우선순위**: P11/P12/P13 세 차별화 영역에 메인테이너 시간의 60% 이상 투입을 목표로 한다. P1~P10/P14는 PGO 패리티가 목표이므로 검증된 패턴 차용이 가능하다.
- **포지셔닝 메시지**: 외부 마케팅에는 "PGO-class 품질 + Citus 1급 + Plugin SDK"의 3축을 균등 노출하지 않는다. **차별화 4가지(§3축의 2,3)를 메시지의 70%로 고정**, PGO 패리티는 "기본 품질"로 단 한 줄 언급.

## 트레이드오프

- **단기 작업량 3~4배 증가**: 옵션 B(soft layer) 대비. v1.0 GA까지 18~21개월 → 24~30개월 가능 (메인테이너 가용성 의존).
  - **완화**: 14 Pillar 의존 그래프 기반 6트랙 병렬 진행, Pillar 오너 컨트리뷰터 모집(거버넌스 명시).
- **단일 PG HA 영역에서 PGO와 직접 비교**: 사용자는 "왜 PGO 안 쓰고 이걸?"을 묻는다.
  - **완화**: README 비교표에 "단일 PG HA만 필요하면 PGO/CNPG 권장"을 정직하게 노출. 우리 청중은 Citus 분산 + Plugin 확장 필요한 팀.
- **마케팅 메시지 복잡도 증가**: "단일 차별화"는 한 문장으로 설명되지만 3축은 그렇지 않다.
  - **완화**: README 첫 문장은 "Citus + Plugin SDK가 차별화. 단일 PG HA는 PGO 수준 품질을 자체 제공"의 두 문장 패턴으로 고정.

## 강제 메커니즘

1. **README의 "왜 또 다른 PG Operator인가" 표 갱신** — 본 ADR 채택과 동시 반영 (큐-A2 작업).
2. **`docs/roadmap.md` 14 Pillar 구조 반영** — 큐-A3 작업.
3. **품질 게이트** (plan §9.8): 단위 ≥80%, e2e 매트릭스, 카오스 테스트, SBOM, cosign 서명, CVE SLA — v1.0 GA 조건.
4. **Plugin SDK 인터페이스 동결**(P13-T1)을 모든 다른 Pillar 진입의 선행 조건으로 명시 (plan §10.3).
5. **본 ADR 변경(미션 축 추가/제거)은 RFC 필수** — GOVERNANCE.md "아키텍처 변경" 절차.

## 결과

- v1.0 GA 시점 PGO 패리티 매트릭스(plan §9.12) 약속.
- Pillar P1~P14 모두 자체 코드로 작성 (ADR 0004).
- Plugin SDK 5종 인터페이스(P13-T1)가 다른 Pillar의 사실상 선행 조건.
- 외부 마케팅: "Citus 1급 + Plugin SDK" 메시지 70% / "PGO-class 품질" 30%.

---

## 부록 A — v1 ADR 본문 (역사 보존)

> v1은 "Citus 표준 + Stateless QueryRouter 단일 차별화"를 채택했다. v2가 이를 확장했지만 v1의 토폴로지·CRD 명명 결정은 모두 유효하므로 본 부록에 보존한다.

### v1 결정 요약 (변동 없음)

- **Coordinator + Worker 토폴로지는 Citus 표준 명명 유지**
- **CRD 루트 표현**: `apiVersion: postgres.keiailab.io/v1alpha1`, `kind: PostgresCluster`, `spec.{coordinator, workers, routers}`
- **부가 CRD**: `RebalanceJob`, `ShardPlacementPolicy`, `DistributedTable`, `ReferenceTable`
- **MongoDB 토폴로지 모델 폐기 사유**: "shard" 명명 충돌, CSS/SS의 Citus 표준 재명명에 불과, 진짜 가치는 Router 분리 한 곳뿐. 이 부분은 v2에서도 그대로 적용되며, "Router 분리"는 차별화 4가지 중 2번으로 명시.

### v1과 v2의 명시적 차이

| 항목 | v1 (2026-04-26) | v2 (2026-04-27) |
|---|---|---|
| 차별화 정의 | 단일 (Stateless QueryRouter) | 4가지 (Citus 1급 + Router + 분산 PITR + Plugin SDK) |
| 단일 PG HA 책임 | "경쟁 안 함" | **"PGO 수준 직접 책임"** |
| 외부 오퍼레이터 의존 | 미정 | **금지 (ADR 0004)** |
| Plugin SDK | 미언급 | **P13으로 1급 도입** |
| 마케팅 메시지 | "Citus 분산 PG K8s 오퍼레이터" | "Citus + Plugin SDK 차별화 + PGO-class 품질" |
| 로드맵 | 14 Phase × 10개월 시간선 | **14 Pillar × DoD 기반 (날짜 제거)** |
