# RFC 0003 — HA Election + Fencing 프로토콜

- **상태**: Draft
- **제출일**: 2026-04-27
- **작성자**: @keiailab/maintainers
- **코멘트 윈도우**: 14일 (마감 2026-05-11)
- **승인 기준**: 메인테이너 2/3
- **관련**: ADR 0002 (Patroni 미사용, K8s API as DCS), ADR 0003 (QueryRouter Stateless), RFC 0002 (Metadata Sync)

## 컨텍스트

ADR 0002는 Patroni 대신 **K8s API as DCS + 자체 instance manager(Go PID1)** 모델을 채택했다. 본 RFC는 그 모델의 핵심 구성요소인 **leader election**의 인터페이스·동작·운영 매개변수를 동결한다.

election은 다음 두 시나리오의 정답이 된다.

1. **단일 RS 내 primary 결정**: coordinator(1+ standby) 또는 worker pool(1+ standby) 안에서 어느 Pod가 read/write를 받는 PG primary인가.
2. **메타데이터 정합성의 출처**: P11(RFC 0002) reconciler가 `pg_dist_node`에 등록할 hostname을 결정할 때 lease holder의 Pod DNS를 따른다.

본 RFC는 **election 인터페이스 + Real(client-go leaderelection 기반) + Null/Mock**을 동결한다. 실제 PG 프로세스 supervise는 P2-T4(`pg_rewind` 통합)와 별도 task. PVC fencing(split-brain 방지)은 P2-T2와 별도 RFC로 위임.

## 결정

### 1. Lease 명명 규약

ADR 0002 §결과를 본 RFC에서 동결한다.

| 역할 | Lease 이름 | namespace |
|---|---|---|
| Coordinator primary | `<cluster>-coordinator-primary` | PostgresCluster CR과 동일 |
| Worker pool primary | `<cluster>-worker-<pool>-primary` | 동일 |

namespace 분리하지 않음 — PostgresCluster가 Namespaced이므로 동일 namespace 내 lease가 자연스러운 격리를 제공.

### 2. Lease 매개변수 (운영 상수)

| 파라미터 | 값 | 근거 |
|---|---|---|
| LeaseDuration | **15초** | client-go 기본값. PG primary 전환에 충분히 짧고, 일시적 네트워크 지터에 충분히 김 |
| RenewDeadline | **10초** | LeaseDuration보다 짧아야 함. holder는 이 시간 내 갱신 시도 |
| RetryPeriod | **2초** | 비-leader가 lease 변화를 폴링하는 간격 |

본 값은 **CLI 플래그로 override 가능**: `--lease-duration`, `--renew-deadline`, `--retry-period`. 운영자는 망 안정성 차이에 따라 조정.

후속 task(P2-T2 fencing): LeaseDuration보다 긴 PVC fence-out timeout으로 split-brain 방지.

### 3. Identity (Pod 이름)

각 instance manager의 lease holder identity는 **`<POD_NAME>`** (downward API).

```yaml
env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: POSTGRES_CLUSTER     # PostgresCluster CR 이름
  - name: POSTGRES_ROLE        # "coordinator" | "worker"
  - name: POSTGRES_POOL        # worker 일 때만 의미
```

lease holder의 identity 문자열을 P11(RFC 0002) reconciler가 읽어 `pg_dist_node` 등록 시 hostname으로 변환한다.

### 4. 역할 전이 모델

| 현재 | 이벤트 | 다음 |
|---|---|---|
| follower | OnStartedLeading | leader (PG primary로 promote — 본 RFC 범위 외) |
| leader | OnStoppedLeading | follower (PG read-only로 demote — 본 RFC 범위 외) |
| 임의 | OnNewLeader (다른 Pod) | follower (이미 follower면 변동 없음) |

본 RFC는 콜백 시그니처만 동결한다. PG 프로세스 supervise(promote/demote의 실 동작)는 후속 P2-T3 + P2-T4에서 LeaderCallbacks 안에 구현된다.

### 5. Election 인터페이스

```go
// internal/instance/election/election.go

type Status string

const (
    StatusLeader   Status = "Leader"
    StatusFollower Status = "Follower"
    StatusStarting Status = "Starting"
)

type Election interface {
    // Run은 ctx 종료 시까지 blocking으로 election 루프를 실행한다.
    // 호출자(main)는 별도 goroutine에서 실행해야 한다.
    Run(ctx context.Context) error

    // Status는 현재 상태를 atomic하게 반환한다.
    Status() Status

    // Identity는 본 인스턴스의 lease identity(보통 POD_NAME)다.
    Identity() string
}
```

### 6. 구현체 3종

- **Real** (`internal/instance/election/lease.go`): `client-go/tools/leaderelection` + `resourcelock.LeaseLock` 사용. cmd/instance가 production에서 사용.
- **Null** (`internal/instance/election/null.go`): 항상 Leader. 단일 노드 development 모드 또는 election 비활성 환경 테스트용.
- **Mock** (`internal/instance/election/mock.go`): 테스트가 명시적으로 Status를 set 가능. 단위 테스트용.

### 7. PVC Fencing — 위임 메모

split-brain 방지의 두 번째 방어선(PVC label 기반 fencing)은 본 RFC 범위 외. **P2-T2 + RFC 0003 부록 A** 후속 작업.

핵심 아이디어 (스케치만):
- lease 잃은 Pod의 PVC에 `postgres.keiailab.io/fenced=true` label 부여
- StorageClass의 ReclaimPolicy + PV controller가 fenced PVC의 mount를 거절
- 새 primary는 fenced label 부재 PVC만 마운트 가능

### 8. 동작 시나리오

#### 시나리오 A — 정상 부팅
1. 모든 instance manager Pod이 동시에 election 시작
2. 한 Pod가 LeaseLock 획득 → OnStartedLeading 콜백 → Status=Leader
3. 다른 Pod들은 OnNewLeader 콜백 → Status=Follower
4. 새 follower들은 RetryPeriod 마다 lease 갱신 시도(실패) → follower 유지

#### 시나리오 B — leader 종료(graceful)
1. leader Pod에 SIGTERM
2. instance manager가 ctx cancel → election.Run return → LeaseLock release
3. follower 중 하나가 RetryPeriod 내 새 lease 획득
4. 새 leader가 OnStartedLeading 콜백 → P11 reconcile 시 lease holder 변경 감지

#### 시나리오 C — leader 응답 불가(노드 장애)
1. leader Pod 응답 불가 (OOM, 노드 다운)
2. LeaseDuration(15초) 동안 갱신 없음
3. follower가 lease 만료 감지 후 새 lease 획득 시도
4. 시나리오 B와 동일 후속

### 9. 시그널·로깅

- 모든 전이는 structured log(`slog.Info("Leadership transition", "from", ..., "to", ..., "identity", ...)`)
- Pod readyz: leader=200, follower=200, starting=503 (election 부트스트랩 중)
- Prometheus 메트릭(P6 통합 시점에 활성): `instance_election_status{status=...}` gauge

## 강제 메커니즘

1. Election 인터페이스 변경 시 RFC 갱신
2. `var _ election.Election = (*real.Real)(nil)` 컴파일 가드
3. lease 매개변수 단위 테스트로 RenewDeadline < LeaseDuration 검증
4. cmd/instance/main.go의 env 변수 기대값을 build/images/instance/Dockerfile 또는 reconciler가 주입하는 PodSpec에서 확인

## 트레이드오프

- **client-go leaderelection 의존**: 외부 라이브러리(이미 controller-runtime 의존이라 추가 비용 0)
- **lease holder identity = POD_NAME 단일화**: 한 노드의 두 PG 인스턴스가 같은 lease를 점유 불가(K8s가 POD_NAME unique 보장)
- **15초 LeaseDuration**: 5초로 줄이면 더 빠른 failover지만 K8s API server 부하 증가. 운영자가 CLI로 override 가능하므로 디폴트 보수적

## 결과

- Pillar P2 M0(spike) 도달 가능
- internal/instance/election/ 신규 패키지 + cmd/instance 통합
- 후속: P2-T2 fencing, P2-T3 failover controller(RS primary down 감지), P2-T4 pg_rewind

## 검증

```bash
# 1) election 인터페이스 + 3개 구현 단위 테스트
go test ./internal/instance/election/... -v

# 2) cmd/instance 빌드
go build ./cmd/instance/...

# 3) lease 매개변수 sanity 회귀 (RenewDeadline < LeaseDuration)
go test ./internal/instance/election/... -run TestLeaseParameters
```
