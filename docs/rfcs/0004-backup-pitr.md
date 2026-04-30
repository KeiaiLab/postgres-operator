# RFC 0004 — Backup/PITR 모델 + pgBackRest 1차 통합

- **상태**: Draft
- **날짜**: 2026-04-30
- **작성**: @keiailab/maintainers
- **관련**: ADR 0005 (Plugin SDK 인터페이스 모델, BackupPlugin 동결), Plan §5 P1-1, P1-6
- **트리거 권장**: P1-1 (BackupJob CRD + reconciler), P1-6 (BackupOptions.ExecutionMode 활용)

## §1 컨텍스트

본 프로젝트의 Pillar **P4 Backup / PITR**은 ADR 0005에서 동결된 `BackupPlugin` 인터페이스를 *어떻게 호출하는가*를 정의하지 않은 상태다. 즉 `BackupPlugin.PerformBackup(ctx, target, opts)` 시그니처는 있지만 *호출자(reconciler)*가 부재하여 본 인터페이스는 *코드 차원에서 dead*이다.

PR #10(P1-6)에서 `BackupOptions.ExecutionMode` 필드를 추가했으나, 이 또한 호출자 없이는 *명시되지만 사용되지 않는* 상태다.

본 RFC는 다음을 정의한다:

1. `BackupJob` CRD의 스펙 — 사용자가 *언제·어떻게* 백업을 명령하는가
2. `BackupJobReconciler`의 reconcile 절차 — Spec → BackupPlugin 호출 → Status 표면화
3. **pgBackRest를 1차 reference plugin**으로 채택 — 이유 + 구현 위치
4. **PITR(Point-in-Time Recovery)** 의미론 — `RestorePoint` CRD vs BackupJob 안의 sub-spec
5. **분산 PITR(Citus 환경)**과의 분리 경계 — RFC 0008(DistributedTable 의미론)에서 별도 처리

본 RFC는 *단일 PG HA의 backup* 영역에 한정된다. Citus 분산 PITR(2PC `citus_create_restore_point` 조정)은 본 프로젝트 차별화 1 영역이며 P4+P11 합류 시점에 별도 RFC(0008 §분산 PITR 또는 신규 RFC 0011)로 분리한다.

## §2 결정 — `BackupJob` CRD 스펙

### §2.1 GVK + 명명

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
metadata:
  name: nightly-fullbackup-2026-04-30
  namespace: prod
spec:
  cluster:
    name: prod-cluster      # 같은 namespace의 PostgresCluster CR 참조
  tool: pgbackrest          # BackupPlugin.Name() — Registry에서 조회
  repo: s3-primary          # Repo 식별자 (BackupSpec.Repo)
  type: full                # full | incremental | differential
  retention:
    keepFull: 7             # 최근 7개 full 보존
    keepIncremental: 30     # 최근 30개 incremental 보존
  executionMode: sidecar    # P1-6 — sidecar | job | "" (plugin default)
  labels:
    env: production
    schedule: nightly
status:
  phase: Succeeded          # Pending | Running | Succeeded | Failed
  backupID: "20260430-021500F"
  startedAt: "2026-04-30T02:15:00Z"
  endedAt: "2026-04-30T02:23:42Z"
  bytes: 12882956841        # 12.0 GiB
  conditions:
    - type: Ready
      status: "True"
      reason: BackupCompleted
      message: "pgBackRest full backup completed in 8m42s"
```

### §2.2 immutable 필드

다음 필드는 BackupJob *생성 후 변경 불가* (webhook이 reject):
- `spec.cluster.name`
- `spec.tool`
- `spec.type`
- `spec.executionMode`

이유: BackupJob은 *단일 백업 호출*의 명세다. 변경하려면 새 BackupJob 생성. `retention`만 mutable (정책 갱신).

### §2.3 ScheduledBackup CRD (별도 신설, 후속 RFC)

cron 기반 정기 백업은 별도 `ScheduledBackup` CRD가 BackupJob 인스턴스를 생성. 본 RFC는 *단일 BackupJob*에 한정하고, ScheduledBackup은 P4-T2 시점에 별도 RFC.

## §3 `BackupJobReconciler` 절차

```
1. BackupJob CR Get
2. Spec 검증 (referenced PostgresCluster 존재, BackupPlugin 등록 여부)
3. Status.Phase = Pending → Running 전이
4. Plugin Registry에서 BackupPlugin 조회 (r.Plugins.Backup(spec.tool))
5. ClusterTarget 합성:
     {Namespace: cluster.namespace, Name: cluster.name, Role: "coordinator", PoolName: ""}
6. BackupOptions 합성 (Type, Repo, ExecutionMode, Labels)
7. plugin.PerformBackup(ctx, target, opts) 호출
8. 결과(BackupResult) → Status에 표면화
   - Phase = Succeeded | Failed
   - BackupID, StartedAt, EndedAt, Bytes
   - Condition Ready/True or False with reason
9. Retention 정책 적용 (다음 reconcile에서 oldBackups cleanup)
```

### §3.1 ExecutionMode 분기

- `sidecar`: PG Pod에 *이미 동거 중인* pgBackRest sidecar 컨테이너에 `kubectl exec`(또는 K8s API 직접) 호출. Operator는 새 자원 생성 없음.
- `job`: K8s `batch/v1.Job` 자원 생성. plugin binary가 standalone 실행. Job의 Status를 polling.
- `""`(빈 문자열): plugin default 사용. pgBackRest는 sidecar, WAL-G는 job.

ExecutionMode는 BackupPlugin이 *직접 분기* — reconciler는 plugin에 위임.

## §4 1차 reference plugin: pgBackRest

### §4.1 채택 이유

| 도구 | 채택 사유 |
|------|-----------|
| **pgBackRest** | PostgreSQL ecosystem *de facto standard*, Crunchy PGO 사용 검증, full+incremental+differential 지원, S3/GCS/Azure native, restore point 명시 명령 |
| WAL-G | Citus distributed가 잘 안됨(주). 후속 plugin |
| Barman | 사용자 풀 작음 |
| Velero (K8s native) | 백업 단위가 PG가 아니라 K8s 자원 — 본 프로젝트 모델과 어긋남 |

(주) WAL-G는 ExecutionMode=`job` 패턴의 reference로 P4-T3에서 별도 plugin 추가.

### §4.2 위치

`internal/plugin/backup/pgbackrest/` (신규 패키지). Plugin SDK *외부* 패키지로 분리 — depguard가 core reconciler의 직접 import를 차단(ADR 0005 §강제 메커니즘).

```
internal/plugin/backup/pgbackrest/
├── plugin.go         # PgBackRestPlugin struct + BackupPlugin 인터페이스 구현
├── plugin_test.go    # 단위 테스트 (mock command runner)
├── sidecar.go        # ExecutionMode=sidecar 분기 — kubectl exec wrapper
├── job.go            # ExecutionMode=job 분기 — K8s Job 생성
└── register.go       # Plugin Registry에 등록 (cmd/main.go에서 호출)
```

### §4.3 1차 구현 범위 (P4-T1)

- `Name() = "pgbackrest"`
- `PerformBackup`: ExecutionMode 분기, sidecar로 `pgbackrest backup --type=full|incr|diff` 실행, 표준 출력 파싱
- `RestorePIT`: `pgbackrest restore --target-time=...` 실행
- `Validate`: `BackupSpec.Tool == "pgbackrest"` + `Settings`에 명시된 repo 형식 검증

후속(P4-T2~):
- ScheduledBackup CRD
- 다중 repo (S3 + 로컬 PVC) hybrid
- 분산 PITR (P4+P11 합류, 별도 RFC)

## §5 PITR 의미론

### §5.1 단일 PG PITR — `BackupJob.Spec.Restore`

```yaml
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
spec:
  cluster:
    name: prod-cluster
  tool: pgbackrest
  repo: s3-primary
  type: restore                     # 신규 type 값
  restore:
    targetTime: "2026-04-30T01:00:00Z"
    # 또는 backupID 직접 지정
    backupID: "20260429-021500F"
```

reconciler가 `BackupPlugin.RestorePIT(ctx, target, ts)` 호출. 결과 → Status.

### §5.2 분산 PITR (Citus, P4+P11 합류)

본 RFC *범위 외*. 후속 RFC(0011 또는 0008 §분산 PITR)에서 `citus_create_restore_point` 2PC 조정 + `RestorePoint` CRD 신설.

## §6 보안

- **Secret 통합**: `BackupJob.Spec.SecretRef` (RFC 0006 §Auth Rotation Hook 통합 시) — pgBackRest의 `--repo1-cipher-pass`, S3 access key 등 plugin이 K8s Secret에서 조회.
- **Plugin이 직접 Secret 참조 금지**: SDK가 Secret을 *미리 resolve해서 byte slice로 전달*. plugin은 secret name 모름. (plan §11 위험 §gRPC 보안과 동일 원칙).

## §7 RBAC

`BackupJobReconciler`가 추가로 요구하는 권한:
- `postgres.keiailab.io/backupjobs`: get/list/watch/update/patch
- `postgres.keiailab.io/backupjobs/status`: update/patch
- `batch/jobs`: get/list/watch/create/update/patch/delete (ExecutionMode=job 시)
- `""/secrets`: get/list/watch (Secret 조회 — RFC 0006과 협업)

## §8 검증 (DoD)

| 단계 | 명령 | 통과 기준 |
|------|------|-----------|
| 단위 | `make test` | `internal/plugin/backup/pgbackrest/` ≥ 80%, mock runner로 모든 분기 cover |
| envtest | `make test` | `internal/controller/backupjob_controller_test.go` PASS — BackupJob 생성 → Status.Phase 전이 검증 |
| e2e (kind) | `make test-e2e PILLAR=p4` | 실 PG container + pgBackRest sidecar로 `BackupJob → Succeeded`, repo에 backup artifact 존재 |
| 보안 회귀 | restricted PSA + NetworkPolicy 적용 namespace에서 BackupJob 작동 | admission 통과 + traffic 차단 회피 |

## §9 트레이드오프

- **pgBackRest 의존**: 1차 reference이지만 *plugin 모델*이라 다른 도구 추가는 1주 작업(ADR 0005 §결과). 의존 lock-in 회피.
- **CRD 스펙 단순화**: `Schedule`, `Retention` 등은 본 RFC v1에서는 정수/cron 문자열로 단순. v2에서 `Storage.PVC` (로컬 백업 repo) 같은 sub-spec 추가 가능.
- **ScheduledBackup 분리**: BackupJob 1개에 cron을 두지 않고 별도 CRD. *atomic 단위*가 BackupJob 1건이라 추적성·디버깅 명료.

## §10 후속 RFC

- **RFC 0008 (DistributedTable 의미론) §분산 PITR**: Citus `citus_create_restore_point` 2PC 조정 + `RestorePoint` CRD
- **RFC 0006 §Auth Rotation Hook**: BackupJob의 Secret 통합
- **RFC 0011 (Extension 우선순위 알고리즘)**: pgBackRest extension 설치 우선순위 (citus<300 사이 100~200)

## §11 결과

- 본 RFC가 **Accepted**되면 P1-1(BackupJob CRD + reconciler) 구현 진입.
- BackupOptions.ExecutionMode (PR #10, P1-6)가 *첫 사용처*를 가짐.
- Plugin SDK 5종 인터페이스 중 **BackupPlugin이 코드 차원에서 활성화** — 차별화 2 (Plugin SDK)의 *첫 호출자*.

## §12 변경 정책

본 RFC 변경(BackupJob CRD 시그니처 추가/제거, ExecutionMode 값 추가)은 *Spec* 수준이라 **CRD v1alpha1 → v1alpha2** 마이그레이션을 동반할 수 있다. v1alpha1 동결 시점은 P4-M1 도달 후.
