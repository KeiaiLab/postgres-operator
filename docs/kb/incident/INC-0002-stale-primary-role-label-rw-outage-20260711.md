# INC-0002: 죽은 노드의 구 primary Pod가 instance-role=primary 라벨을 계속 보유해 rw 라우팅 8h+ 장애

- Detected: 2026-07-11
- Resolved: Open — 근본 원인 식별 + fix 코드 작성·검증·push 완료(PR 생성 대기), 클러스터 측 이미지 롤아웃/수동 복구는 별도 트랙에서 진행
- Severity: SEV-1 (production, 소비 서비스 8h+ 전면 장애)
- Owners: @phil (라이브 인시던트 실측 + 클러스터 측 복구), keiailab/postgres-operator (코드 fix)
- Tags: [failover, rw-routing, instance-role-label, stuck-terminating, dead-node, ha]

## Impact

- **사용자 영향**: `postgres-prod`를 사용하는 서비스(harbor 등)가 8시간 이상 전면 장애 — CI 파이프라인 완전 차단.
- **시스템 영향**:
  - 노드 e122가 kubelet 무응답으로 완전 다운.
  - operator는 failover를 CR status/lease 수준에서는 정상 완료했다 — `postgrescluster/postgres-prod`
    `.status.shards[0].primary = {pod: postgres-prod-shard-0-1, ready: true}`, lease
    `postgres-prod-shard-0-primary` holder = `shard-0-1`, `FailoverReady` condition =
    `"no failover action required"`.
  - 그러나 Pod 라벨 `postgres.keiailab.io/instance-role=primary`는 **죽은 shard-0-0에만
    잔존**, 승격된 shard-0-1에는 **부착되지 않았다**.
  - 이 라벨에 의존하는 rw Service selector가 매칭 파드 0개(죽은 파드는
    NotReady/Terminating뿐) → **endpoints 0개** → 소비자 전면 장애.
  - 오퍼레이터 이벤트 `StandbyReseeded`(`"standby postgres-prod-shard-0-0 not ready for
    8m0s with a ready primary; re-seeding (delete pod+PVC → fresh pg_basebackup)"`)가
    **10분 간격으로 무한 반복** — 죽은 kubelet 때문에 pod delete가 영원히 미완결이라
    재시딩 루프가 진행 불가.
  - `ShardsReady` condition은 `"1/1 shard primary ready"`로 정상 보고 — 오퍼레이터
    자기 상태는 정상, rw 데이터플레인 공백은 감지하지 못함(내부 상태·외부 라우팅
    사이 모순이 관측 불가능했다).
- **재정/법적 영향**: 본 문서 작성 시점 기준 내부 CI/소비 서비스 장애 위주로 보고됨.

## Timeline

라이브 클러스터 실측 요약(팀 리드 전달) 기반 — 초 단위 정밀 타임라인은 클러스터
이벤트/로그 원본 참조:

- 노드 e122 kubelet 무응답 → 완전 다운.
- operator가 shard-0-1을 promote — CR status/lease 정상 갱신,
  `FailoverReady=True`(`"no failover action required"`).
- `postgres.keiailab.io/instance-role=primary` 라벨은 shard-0-0에 잔존한 채
  shard-0-1로 이전되지 않음(patch하는 코드 자체 부재).
- rw Service(라벨 selector 의존)가 endpoints 0개로 전환 → harbor 등 소비자 8h+ 장애.
- `reconcileStaleReplicas`가 shard-0-0에 대해 10분 간격 `StandbyReseeded` 이벤트를
  반복 발행(delete pod+PVC 시도) — 죽은 노드의 kubelet이 삭제를 확인해주지 않아
  매번 미완결.
- 사용자가 근본 원인 조사 + 수정을 요청(2026-07-11) → 본 INC 작성 + fix 브랜치
  `fix/primary-instance-role-label-sync`(commit `42e4f7f8a6718eba90838dca2dca4b49e21380a4`) push.

## Root Cause

5 Whys:

1. **왜 rw 트래픽이 죽은 shard-0-0으로 계속 라우팅됐는가?** rw Service selector가
   의존하는 `postgres.keiailab.io/instance-role=primary` 라벨이 shard-0-0에 그대로
   남아 있고 shard-0-1에는 부착되지 않았다.
2. **왜 그 라벨이 shard-0-1로 이전되지 않았는가?** operator 코드 어디에도 이
   라벨을 Pod에 patch하는 로직이 없었다 — `internal/controller/failover/pvc_fence_runbook.go`의
   `PVCFenceMountedPod.InstanceRole` 필드 설계와 `docs/runbooks/pvc-fence.md` 운영
   runbook(§5.2 SplitBrain 사후분석 — "모든 PostgresCluster Pod 의 instance-role
   라벨 점검")이 이 라벨의 존재를 전제하고 참조해 왔지만, 실제 patch 코드는 구현된
   적이 없었다.
3. **왜 이 gap이 지금까지 발견되지 않았는가?** `test/e2e/failover_chaos_test.go`가
   한때 정확히 이 라벨(`postgres.keiailab.io/instance-role=primary`)로 primary를
   selector했다(원래 설계 의도의 증거). 2026-06-15 commit `65d5819`가 "PG Pod 에
   부착되지 않는 label"이라는 정확히 동일한 gap을 발견했지만, 그 커밋은 e2e
   테스트만 CR-status/annotation(`postgres.keiailab.io/instance-status`) 기반으로
   우회시켰을 뿐 — 근본 원인(아무도 라벨을 patch하지 않음) 자체는 fix 없이
   방치됐다.
4. **왜 operator가 자체적으로 이 모순을 감지하지 못했는가?** `ShardsReady`
   condition은 primary Pod의 Ready 상태만 반영하고, 그 Pod가 실제로 rw 라우팅에
   필요한 라벨을 들고 있는지는 전혀 확인하지 않는다 — "CR 상태 정상"과 "데이터플레인
   라우팅 정상"이 서로 다른 신호인데, 후자를 관측하는 코드/condition이 없었다.
5. **왜 복구(StandbyReseeded)마저 무한 반복하며 문제를 해결하지 못했는가?**
   `reseedStandby`가 pod 삭제를 시도할 때 gracePeriodSeconds를 escalate하는
   로직이 없어, 죽은 노드의 kubelet이 삭제를 확인해주지 않는 한 영원히 대기했다 —
   노드가 죽었다는 신호(NotReady)를 활용해 force-delete로 승격하는 경로가 없었다.

기여 요인:

- 라벨 기반 selector(미구현)와 annotation 기반 aggregation(구현됨)이라는 *두 갈래*
  역할 판정 경로가 공존하면서, 한쪽(라벨)만 미구현 상태로 방치됐다.
- reconcile 순서상 "라벨 동기화" 단계 자체가 없어, 재시딩(delete) 로직과의
  실행 순서 충돌이라는 개념조차 존재하지 않았다(순서 버그가 아니라 완전 부재).

## Resolution

`fix/primary-instance-role-label-sync` 브랜치(commit
`42e4f7f8a6718eba90838dca2dca4b49e21380a4`)에 3가지 수정을 구현·테스트 완료:

1. **`internal/controller/role_label_sync.go` (신규)** — `reconcilePrimaryRoleLabels`가
   매 reconcile마다 각 shard의 관측된 primary/replica를 Pod label로 patch한다.
   `Reconcile`에서 standby 재시딩(`reconcileStaleReplicas`/`reconcileRoguePrimaries`)
   보다 *먼저*, 완전히 *독립적으로* 호출된다 — 대상 Pod가 stuck Terminating이어도
   Kubernetes는 실제 삭제(finalizer 해소 + etcd 제거) 완료 전까지 metadata patch를
   허용하므로, rw 라우팅은 재시딩 delete의 진행 상태와 무관하게 항상 실제 primary로
   수렴한다.
2. **`internal/controller/stale_replica_reseed.go`** — `reseedStandby`가 대상
   pod의 `DeletionTimestamp`가 2분(`stuckTerminatingForceDeleteThreshold`) 이상
   경과했고 그 Node가 독립적으로 `NotReady`를 보고할 때만 force delete(finalizer
   제거 + `gracePeriodSeconds=0`)로 승격한다. 안전 게이트로 `podAbsentOrNotReady`
   재확인을 추가해, 대상이 실제로 확인 가능한 정상 상태라면 승격하지 않는다. Node
   조회를 위한 `nodes(get/list/watch)` RBAC를 `config/rbac/role.yaml` + Helm chart
   (`charts/postgres-operator/templates/rbac.yaml`) + bundle CSV 3곳에 반영.
3. **`internal/controller/status.go` + `postgrescluster_controller.go`** —
   `ConditionPrimaryRoleSynced` condition 신설(`ShardsReady`/`Ready`의 기존 의미는
   변경하지 않음). primary Pod가 Ready인데 라벨이 아직 그 Pod로 수렴하지 않았으면
   `False/RoleLabelPending`으로 노출 — "CR 상태는 정상인데 데이터플레인 라우팅은
   끊김"이라는 이번 사고의 모순을 상태로 드러낸다.

검증: `go build ./... && go vet ./...` 전체 repo 에러 0. `internal/controller`
envtest 스위트 `Ran 33 of 33 Specs ... SUCCESS! 33 Passed | 0 Failed`. 신규 회귀
테스트 9건 전부 PASS — `TestReconcilePrimaryRoleLabels_FlipsEvenWhenDemotedPrimaryIsStuckTerminating`가
본 사고 메커니즘(구 primary가 finalizer로 stuck Terminating인 상태에서도 라벨이
뒤바뀜)을 직접 재현·고정한다.

**Open 잔여**: PR 생성은 세션 권한 제약(공개 repo에 대한 PR 생성은 사용자 본인
직접 동의 필요)으로 사용자가 직접 수행해야 한다 — Refs의 PR 생성 링크 참조. PR
머지 이후 이미지 릴리스 + 클러스터 롤아웃, 그리고 라이브 클러스터의 수동 복구
명령 2건은 본 fix 작업과 별도 트랙에서 에스컬레이션 중(클러스터 접근 권한 없이
코드만으로 작업했다는 제약상 본 INC 작성자 범위 밖).

## Prevention

- **단기**: 본 INC 등록 + fix 브랜치 push. `ConditionPrimaryRoleSynced`가 향후
  동일 gap을 CR status 자체에서 즉시 관측 가능하게 한다(재발 시 몇 시간이 아니라
  즉시 감지).
- **중기**: `docs/runbooks/pvc-fence.md`가 "instance-role 라벨 점검"을 이미 운영
  절차로 명시하고 있었으나 그 라벨을 채우는 코드가 없었던 것 자체가 이번 근본
  원인 — "운영 runbook이 참조하는 라벨/신호는 반드시 그것을 patch하는 코드가
  실재하는지" 코드리뷰 체크리스트화를 제안.
- **장기**: rw 트래픽이 의존하는 selector 라벨과 그 라벨의 SSOT(CR status/lease)
  사이의 정합성을 지속 검증하는 e2e(라벨 selector로 실제 kubectl 조회 + 기대
  pod 일치 확인)를 `test/e2e/failover_chaos_test.go`에 재도입 검토 — 2026-06-15
  commit `65d5819`가 우회시킨 검증을 근본적으로 복원.

## Action Items

- [ ] AI-0001: fix 브랜치 `fix/primary-instance-role-label-sync`(commit
      `42e4f7f`)의 PR 생성·리뷰·머지 (Owner: @phil — 세션 권한 제약으로 PR 생성은
      사용자 직접 수행 필요, 링크는 Refs 참조)
- [ ] AI-0002: PR 머지 후 이미지 릴리스 + 클러스터 Flux 롤아웃 확인 (Owner: TBD)
- [ ] AI-0003: 라이브 클러스터 수동 복구 명령 2건 (Owner: @phil, 별도 escalate
      중 — 본 INC 작성자 범위 밖)
- [ ] AI-0004: `test/e2e/failover_chaos_test.go`에 라벨 selector 기반 검증
      재도입 검토 (Owner: TBD)

## Refs

- fix 브랜치: `fix/primary-instance-role-label-sync`
  (commit `42e4f7f8a6718eba90838dca2dca4b49e21380a4`)
- PR 생성 링크: <https://github.com/KeiaiLab/postgres-operator/pull/new/fix/primary-instance-role-label-sync>
- 근본 gap 발견 지점: commit `65d5819`
  ("test(e2e): failover_chaos 의 primary/role 판정을 라이브 API 로 정합")
- `internal/controller/failover/pvc_fence_runbook.go` (`PVCFenceMountedPod.InstanceRole` 설계)
- `docs/runbooks/pvc-fence.md` §5.2 (SplitBrain 사후분석 — instance-role 라벨 점검 절차)
- standards/incident-kb.md (Postmortem-lite template)
- 라이브 evidence: `postgrescluster/postgres-prod`, 노드 e122,
  lease `postgres-prod-shard-0-primary`
