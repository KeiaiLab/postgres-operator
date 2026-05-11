# RFC-0007: HA Leader Election + PVC Fencing 프로토콜

- **Status**: Draft
- **Date**: 2026-05-11
- **Authors**: @keiailab/maintainers (재발행 — 이전 0003 슬롯은 shardsplitjob 으로 재할당, 본 RFC 가 정식 슬롯)
- **Refs**: archived [ADR 0002 — Patroni 미사용](../kb/adr/_archive/v0.x/0002-no-patroni-instance-manager.md), 운영 가이드 [`docs/operator-guide/ha-election.md`](../operator-guide/ha-election.md)
- **Supersedes**: 이전 본문이 `docs/operator-guide/ha-election.md` 의 임시 SSOT 로 위치했던 부분

## 1. Summary

PostgreSQL HA 의 leader election 과 split-brain 방지를 다음 두 메커니즘으로 구현한다:

1. **Leader Election** — K8s `coordination.k8s.io/v1` Lease 기반 (외부 DCS 없음).
2. **PVC Fencing** — PVC label 기반 fencing 프로토콜로 옛 leader Pod 의 split-brain 차단.

본 RFC 는 두 결정의 근거·매개변수·운영 인터페이스를 *단일 SSOT* 로 통합한다.

## 2. Context

PostgreSQL HA 의 사실상 표준은 Patroni (Python) + 외부 DCS (etcd / Consul / ZooKeeper). 그러나:

- **외부 DCS 의존**: etcd 클러스터 운영 부담, 장애 시 PG HA 전체 실패
- **Python 런타임**: Go 기반 오퍼레이터에서 Patroni sidecar → 이미지 크기 / 보안 표면 증가
- **이중 진실원**: Patroni 가 etcd 에 쓰는 상태 vs. operator 가 K8s 에 쓰는 상태의 분기 위험
- **CloudNativePG 검증**: K8s API 자체를 DCS 로 사용하는 모델이 production-grade 운영 실적 보유

P2-T2 (2026-04-28) 부터 PVC fencing 이 활성화되어 옛 leader Pod 의 split-brain 시나리오를 차단.

## 3. Decision

### 3.1 Leader Election

각 `PostgresCluster` 의 instance manager (`cmd/instance`) 가 부팅 시 다음 election 모드로 진입:

| 모드 | 용도 | CLI 플래그 |
|---|---|---|
| `real` (기본) | 멤버 ≥ 2 — K8s `coordination.k8s.io/v1` Lease 사용 | `--election=real` |
| `null` | 단일 멤버 시나리오 — election 우회 (개발/테스트) | `--election=null` |

Lease 매개변수 (`internal/instance/election/lease.go`):

| 매개변수 | 값 | 의미 |
|---|---|---|
| `DefaultLeaseDuration` | **15s** | leader 가 lease 갱신 없이 살아있다고 간주되는 최대 시간 |
| `DefaultRenewDeadline` | **10s** | leader 가 lease 갱신을 시도하는 deadline (Duration 보다 짧아야) |
| `DefaultRetryPeriod` | follower retry 간격 | follower 가 lease 획득 재시도 주기 |

테스트 (`election_test.go`, `integration_test.go`) 는 짧은 값 (2s/1s/200ms) 사용으로 회귀 시간 단축.

### 3.2 PVC Fencing (P2-T2)

split-brain 차단을 위해 **PVC label 기반 fencing** 적용. 시나리오 C (옛 leader Pod 가 partition 복구 후 살아 돌아옴) 에서:

1. 새 leader 가 즉시 옛 leader 의 PVC 에 fence label 부착 (`fencing.go` `MarkFenced`)
2. 옛 leader Pod 가 startup 시 자기 PVC 의 fence label 확인 (`IsFenced`)
3. fence 가 set 되어 있으면 startup 거부 + Pod terminating

운영 노브: `--fencing-disabled` (개발 모드 전용, 프로덕션 사용 금지).

### 3.3 CRD Status 가 Topology 권위

`PostgresCluster.status.topology` 가 현재 RS primary 명단을 보유. K8s 와 PG 상태의 단일 진실원.

## 4. Consequences

### 4.1 Positive
- **운영 단순화**: etcd 의존 제거 — K8s control plane 이 합의 보장
- **이미지 / 보안**: Go static 바이너리 단일, distroless 베이스, 외부 런타임 0
- **단일 진실원**: CRD status + K8s lease 만 권위 — 이중화 제거
- **CNPG 선례**: 동일 모델로 production-grade 실적
- **Citus 통합 자연스러움**: instance manager 가 직접 `citus_update_node` 호출 → 새 primary IP 전파를 단일 책임자가 처리

### 4.2 Negative / Trade-offs
- **K8s API server 가용성 의존**: API server 장애 시 election 차단
  - 완화: K8s control plane 은 클러스터 운영의 전제. PG 와 동일 가용성 클래스 가정. + PVC fencing 으로 split-brain 보완.
- **Patroni 생태계 도구 (patronictl 등) 미적용**: 운영자 친숙한 CLI 부재
  - 완화: `kubectl pgo` 또는 자체 CLI 를 Phase 13 에서 제공. 일반 운영은 `kubectl` + CR 로 충분.
- **자체 instance manager 영구 유지보수**: 라이센스 / 유지보수 위험
  - 완화: CNPG 의 Apache-2.0 코드 패턴 참고 (라이센스 호환). 핵심 로직 수백 줄 수준.

## 5. Alternatives Considered

### 5.1 Patroni + etcd
거절 사유: 외부 DCS 운영 부담, Python 런타임, 이중 진실원 — §2 Context.

### 5.2 Stolon
거절 사유: keeper / sentinel / proxy 3 컴포넌트 → 운영 복잡도 증가. K8s 친화 모델 아님.

### 5.3 K8s Operator + StatefulSet 기본 동작에만 의존
거절 사유: split-brain 방지 불가. failover 동안 데이터 정합성 보장 부재.

## 6. 부록 A — PVC Fencing 프로토콜 상세 (P2-T2, Implemented 2026-04-28)

운영 가이드 `docs/operator-guide/ha-election.md §10` 의 내용을 본 부록으로 흡수 예정 (Draft → Accepted 전환 시).

### 6.1 라벨 스키마

PVC 에 부착되는 fence 라벨:
- `postgres-operator.keiailab.io/fenced`: `"true"` / 미존재
- `postgres-operator.keiailab.io/fenced-at`: RFC3339 timestamp
- `postgres-operator.keiailab.io/fenced-by`: 새 leader Pod 이름

### 6.2 부착 시점

새 leader 가 OnStartedLeading 호출 직후 (election success transition) 모든 *non-leader* Pod 의 PVC 에 fence label 부착.

### 6.3 RBAC 요구

instance manager ServiceAccount 는 자기 namespace 의 PVC `get` / `patch` 권한 필요.

### 6.4 회복 절차

fence 된 Pod 의 회복은 운영자 수동 작업:
1. PVC 데이터 무결성 검증 (`pg_controldata`, WAL replay 가능 여부)
2. 무결성 OK → fence label 제거 → Pod restart
3. 무결성 NG → PVC 폐기 + 새 replica 추가

## 7. Implementation Status

- [x] Lease 기반 leader election (`internal/instance/election/`)
- [x] PVC fencing (`internal/instance/fencing/`, P2-T2 활성 2026-04-28)
- [x] `--fencing-disabled` 개발 노브
- [ ] `kubectl pgo failover` CLI 명령 (Phase 13)
- [ ] failover controller (P2-T3)
- [ ] `pg_rewind` 통합 (P2-T4)

## 8. References

- Code: `internal/instance/election/`, `internal/instance/fencing/`, `cmd/instance/`
- Tests: `election_test.go`, `integration_test.go`, `fencing_test.go`
- Operations: `docs/operator-guide/ha-election.md`
- Archived decision: `docs/kb/adr/_archive/v0.x/0002-no-patroni-instance-manager.md`
- 후속 작업: P2-T3 (failover controller), P2-T4 (pg_rewind), Phase 13 (kubectl pgo CLI)
