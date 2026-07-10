# INC-0002: 죽은 노드의 구 primary Pod가 role=primary 라벨을 계속 보유해 rw 라우팅 8h+ 장애

- Detected: 2026-07-11
- Resolved: Open — 근본 원인 **확정**(§Root Cause Final Confirmation) + fix 코드
  작성·검증·push 완료(PR 생성만 사용자 직접 승인 대기), 클러스터 측 이미지
  롤아웃/수동 복구는 별도 트랙에서 진행
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
  - 그러나 Pod 라벨 `postgres.keiailab.io/role=primary`(라이브 실측 — `kubectl get svc
    postgres-prod-shard-0-rw -o jsonpath='{.spec.selector}'`가 이 키를 포함,
    `kubectl get pod ... -o jsonpath='{.metadata.labels.postgres\.keiailab\.io/role}'`도
    동일 키로 확인)는 **죽은 shard-0-0에만 잔존**, 승격된 shard-0-1에는 **부착되지
    않았다**.
  - 이 라벨에 의존하는 rw Service selector가 매칭 파드 0개(죽은 파드는
    NotReady/Terminating뿐) → **endpoints 0개** → 소비자 전면 장애.
  - **repo 코드 조사 결과**: 이 rw Service를 만들거나 `postgres.keiailab.io/role`을
    primary/replica 값으로 patch하는 코드는 이 repo(및 git 전체 히스토리)
    어디에도 없다(§Root Cause 참조) — 즉 그 Service와 라벨은 이 repo *밖*
    (운영/GitOps 레이어)에서 관리되고 있었다.
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
- `postgres.keiailab.io/role=primary` 라벨은 shard-0-0에 잔존한 채
  shard-0-1로 이전되지 않음(patch하는 코드 자체 부재 — repo 밖 레이어가
  failover-aware하지 않음).
- rw Service(라벨 selector 의존)가 endpoints 0개로 전환 → harbor 등 소비자 8h+ 장애.
- `reconcileStaleReplicas`가 shard-0-0에 대해 10분 간격 `StandbyReseeded` 이벤트를
  반복 발행(delete pod+PVC 시도) — 죽은 노드의 kubelet이 삭제를 확인해주지 않아
  매번 미완결.
- 사용자가 근본 원인 조사 + 수정을 요청(2026-07-11) → 본 INC 작성 + fix 브랜치
  `fix/primary-instance-role-label-sync`(commit `42e4f7f8a6718eba90838dca2dca4b49e21380a4`) push.
- 팀 리드가 라이브 클러스터를 직접 재실측해 라벨 키가 `instance-role`이 아니라
  `role`임을 확인(2026-07-11) → repo 재조사(§Root Cause) 후 dual-key sync로
  fix 갱신, commit(브랜치 동일) push.

## Root Cause

5 Whys:

1. **왜 rw 트래픽이 죽은 shard-0-0으로 계속 라우팅됐는가?** rw Service selector가
   의존하는 `postgres.keiailab.io/role=primary` 라벨이 shard-0-0에 그대로
   남아 있고 shard-0-1에는 부착되지 않았다.
2. **왜 그 라벨이 shard-0-1로 이전되지 않았는가?** `postgres.keiailab.io/role`을
   primary/replica 값으로 patch하는 코드가 이 repo에 **존재하지 않는다**. 다음
   4개 독립 소스를 모두 조사했고 전부 음성이었다:
   1. 현재 `main` HEAD(`25476e1`) 전체 grep + `internal/controller/builders.go`의
      Service 생성 5개 지점(`buildHeadlessService`/`buildClientService`/
      `buildTargetHeadlessService`/pooler 관련 2건) 전수 확인 — `role` 인자는
      항상 리터럴 `"shard"`/`"router"` 고정, `SelectorLabels()`가 위임하는
      `keiailab-commons@v0.12.0` `Set.All()` 구현( `$GOMODCACHE/github.com/keiailab/keiailab-commons@v0.12.0/pkg/labels/labels.go`)도
      `app.kubernetes.io/component`만 만들고 `postgres.keiailab.io/role`은
      만들지 않는다.
   2. git 전체 히스토리 pickaxe(`git log -S 'postgres.keiailab.io/role"' --all`,
      `-S '"-rw"'`, `-S 'RoleLabelKey'`) — 등장은 `internal/controller/tls.go`의
      무관한 TLS 인증서 라벨(`"server-tls"` 값) 단 한 곳뿐.
   3. `internal/controller/names.go`의 `ShardServiceName` **전체 tracked 이력**
      (`git log --all -p -- internal/controller/names.go`) — 이 함수가
      **처음 도입된 시점부터 지금까지 단 한 번도 `-rw`였던 적이 없다**
      (`-headless` 고정) — "예전엔 `-rw`였다가 리네임됐다" 가설도 반증됨.
   4. **라이브 배포 이미지의 정확한 소스 커밋 확인**(registry 조회만 — 클러스터
      접근 없음): `ghcr.io/keiailab/postgres-operator:0.4.0-beta.5-reseed`의
      SLSA provenance attestation(`crane manifest`/`crane blob`로 직접 fetch)이
      `vcs:revision=cd7c4f800cad3230fdbde0ff96182f2ff456d890`,
      `vcs:source=https://github.com/KeiaiLab/postgres-operator.git`,
      빌드 시각 `2026-06-22T22:36:36Z`를 명시한다. 이 정확한 커밋(현재 HEAD의
      조상, `git merge-base --is-ancestor` 확인)에서도 동일하게 `role`/`-rw`
      코드가 0건이다 — "배포 이미지가 HEAD보다 구버전이라 그때는 있었다" 가설도
      이 특정 배포에 한해서는 반증됨(이 이미지가 실측 대상 파드의 이미지인지는
      이후 AI-0006으로 클러스터 측 재확인 완료 — §Root Cause Final
      Confirmation 참조).

   즉 rw Service와 그 selector가 의존하는 `role` 라벨은 이 repo *밖*에서
   관리되고 있다 — 가장 유력한 가설은 RFC-0004(`docs/rfcs/0004-pg-router-architecture.md`)가
   "P5+ 미래 기능"으로 명시한 "shard의 primary Service를 직접 노출하는 bypass
   mode"가 아직 구현되지 않아, 운영팀이 그 gap을 메우려고 Service를 수동
   생성(+ GC를 위해 `ownerReferences`도 수동으로 PostgresCluster CR을 가리키게
   설정)했다는 것이다. 그 레이어는 이 repo가 알지 못하므로 failover 이벤트에도
   반응하지 않는다 — 이 repo만 고쳐서는 근본 해결이 안 되는 부분이 남는다는
   뜻이다(§Resolution 잔여 참조).
   - 별개로, repo 자체의 fencing 결정 로직(`internal/controller/failover/pvc_fence_runbook.go`의
     `PVCFenceMountedPod.InstanceRole`)과 운영 runbook(`docs/runbooks/pvc-fence.md`
     §5.2)은 `postgres.keiailab.io/instance-role`이라는 *다른* 키를 참조해 왔지만,
     이 키 역시 실제로 Pod에 patch하는 코드가 없었다(순수 우연히 사고 원인과
     증상이 유사한 별개의 gap).
   - 참고: `postgres-prod-shard-0-ro`(replica 라우팅) Service는 라이브에
     **존재하지 않는다**(팀 리드 재조회 NotFound) — repo에 "-rw"/"-ro" Service
     builder가 둘 다 없다는 것과 정합(있었다면 보통 쌍으로 만들었을 것).
3. **왜 이 gap이 지금까지 발견되지 않았는가?** `test/e2e/failover_chaos_test.go`가
   한때 `postgres.keiailab.io/instance-role=primary`로 primary를 selector했다
   (그 키에 대한 원래 설계 의도의 증거 — 라이브에서 실제로 쓰이는 `role` 키와는
   다르다). 2026-06-15 commit `65d5819`가 "PG Pod 에 부착되지 않는 label"이라는
   gap을 발견했지만, e2e 테스트만 CR-status/annotation(`postgres.keiailab.io/instance-status`)
   기반으로 우회시켰을 뿐 — 그 키의 근본 원인(아무도 patch하지 않음)도, 라이브
   rw Service가 실제로 의존하는 `role` 키의 존재 자체도 repo 안에서는 드러나지
   않았다.
4. **왜 operator가 자체적으로 이 모순을 감지하지 못했는가?** `ShardsReady`
   condition은 primary Pod의 Ready 상태만 반영하고, 그 Pod가 실제로 rw 라우팅에
   필요한 라벨을 들고 있는지는 전혀 확인하지 않는다 — "CR 상태 정상"과 "데이터플레인
   라우팅 정상"이 서로 다른 신호인데, 후자를 관측하는 코드/condition이 없었다.
5. **왜 복구(StandbyReseeded)마저 무한 반복하며 문제를 해결하지 못했는가?**
   `reseedStandby`가 pod 삭제를 시도할 때 gracePeriodSeconds를 escalate하는
   로직이 없어, 죽은 노드의 kubelet이 삭제를 확인해주지 않는 한 영원히 대기했다 —
   노드가 죽었다는 신호(NotReady)를 활용해 force-delete로 승격하는 경로가 없었다.

기여 요인:

- rw 트래픽이 실제로 의존하는 `role` 라벨의 Service/patch 로직이 이 OSS
  operator repo 밖(운영/GitOps 레이어)에 있어, repo 코드만으로는 "누가 이
  라벨을 patch해야 하는가"라는 소유권 자체가 애매했다. repo 자체적으로는
  `instance-role`이라는 별개의, 문서화만 되고 미구현이던 키가 있어 혼선을
  더했다.
- 라벨 기반 selector(리포 밖에서 관리, 미검증)와 annotation 기반
  aggregation(리포 안에서 구현됨)이라는 *두 갈래* 역할 판정 경로가 공존하면서,
  전자가 failover 미인지 상태로 방치됐다.
- reconcile 순서상 "라벨 동기화" 단계 자체가 없어, 재시딩(delete) 로직과의
  실행 순서 충돌이라는 개념조차 존재하지 않았다(순서 버그가 아니라 완전 부재).

### Root Cause Final Confirmation (팀 리드 라이브 재실측, 2026-07-11)

repo/registry 조사(위 4개 소스)에 더해, 팀 리드가 라이브 클러스터·GitOps repo를
직접 재실측해 "repo 밖 레이어" 가설을 확정으로 승격시켰다:

1. **platform/data(GitOps) repo 음성 확정** — 차트 템플릿 전수 검사에
   Service/role 매니페스트가 0건. `Chart.yaml`에 "shard-0-rw" 문자열 히트가
   있었으나 이는 코드가 아니라 **체인지로그 산문**(과거 다른 NP 관련 사고를
   서술한 텍스트)이었다 — GitOps 쪽에도 이 Service를 선언적으로 관리하는
   코드가 없다는 뜻.
2. **라이브 CR spec 키 확인** — `postgrescluster/postgres-prod`의 실제 spec
   최상위 키는 `[imageCatalogRef, postgresVersion, shardingMode, shards]`
   뿐이다. rw Service 생성이나 role 라벨 부여를 지시할 수 있는 spec 필드가
   없다 — "배포된 operator 바이너리가 이 CR의 특정 spec 필드를 보고 조건부로
   rw Service를 만든다"는 가설도 반증.
3. **rw Service `creationTimestamp = 2026-06-15T09:18:05Z`** — commit
   `65d5819`(라벨 gap을 처음 발견하고 e2e를 CR-status 기반으로 우회시킨 커밋,
   Author 시각 2026-06-15 21:26:30 +09:00 = 2026-06-15T12:26:30Z)와 **같은
   날**. Service 생성이 그 커밋보다 ~3시간 앞선다 — 그날 failover/e2e 작업
   도중 rw 라우팅을 손으로 먼저 구축하고, 나중에 그 작업에서 라벨이 Pod에
   안 붙는 걸 발견해 e2e만 우회 fix했다는 시간순과 정합.

**종합 서사**: 2026-06-15 failover/e2e 작업 중 누군가 rw 라우팅을 **수동
스톱갭**으로 구축했다 — Service를 수동 생성 + `ownerReferences`를 손으로
`postgres-prod` CR을 가리키게 배선(가비지 컬렉션 목적) + 파드에
`postgres.keiailab.io/role` 라벨을 수동 부착. 사실상 RFC-0004 §"P5+ bypass
mode"(shard의 primary Service를 직접 노출하는 미래 기능)를 **손으로 먼저
구현**한 것이다. 이 수동 구성을 이후 갱신(failover 시 라벨 재부착)하는 주체가
아무도 없었고, 26일 후 첫 실제 failover(e122 다운, 2026-07-11)에서 그 gap이
노출됐다.

**AI-0005 = 닫힘** — GitOps 음성 확정 + CR spec 필드 부재 + creationTimestamp
정합으로 "수동 스톱갭" 결론 확정.
**AI-0006 = 닫힘** — 팀 리드가 operator Deployment 이미지가
`0.4.0-beta.5-reseed`이고 그 operator가 `postgres-prod`를 소유하고 있음을
직접 실측 확인.

**잔여 위험(영구 아님)**: 수동 Service의 `ownerReferences`가 `postgres-prod`
CR의 UID에 배선돼 있으므로, 그 CR이 삭제 후 재생성되면(UID 변경)
Kubernetes garbage collection이 이 Service를 **자동 삭제**한다 — 이 fix(Pod
label 유지)로 rw 라우팅 자체는 복구되지만, CR 재생성 시나리오에서는 Service
자체가 사라져 재차 장애가 난다. 영구 해법은 operator가 RFC-0004 §"P5+ bypass
mode" Service 생성을 **정식 구현**하는 것 — 후속 이슈로 제안한다(별도
feature request, 본 fix 범위 밖).

## Resolution

`fix/primary-instance-role-label-sync` 브랜치(commit
`42e4f7f8a6718eba90838dca2dca4b49e21380a4`)에 3가지 수정을 구현·테스트 완료:

1. **`internal/controller/role_label_sync.go` (신규)** — `reconcilePrimaryRoleLabels`가
   매 reconcile마다 각 shard의 관측된 primary/replica를 Pod label로 patch한다.
   **두 키 모두**(`postgres.keiailab.io/role` — 라이브 실측된 rw Service selector의
   실제 소비 키, `postgres.keiailab.io/instance-role` — repo 내부 fencing
   decision-logic/runbook이 이미 문서화한 키) 단일 Get+Patch로 함께 동기화한다 —
   repo 안에서는 어느 외부 레이어가 어느 키를 최종 소비하는지 확정할 수 없으므로
   (§Root Cause), 양쪽 다 정확하게 유지하는 것이 안전한 기본값이다. `Reconcile`에서
   standby 재시딩(`reconcileStaleReplicas`/`reconcileRoguePrimaries`)보다 *먼저*,
   완전히 *독립적으로* 호출된다 — 대상 Pod가 stuck Terminating이어도 Kubernetes는
   실제 삭제(finalizer 해소 + etcd 제거) 완료 전까지 metadata patch를 허용하므로,
   rw 라우팅은 재시딩 delete의 진행 상태와 무관하게 항상 실제 primary로 수렴한다.
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

**잔여 위험 — 해소됨(§Root Cause Final Confirmation)**: `postgres.keiailab.io/role`을
소비하는 rw Service는 GitOps 코드나 별도 컨트롤러/웹훅이 아니라 **2026-06-15
1회성 수동 생성**임이 확정됐다(GitOps 매니페스트 음성 + CR spec 필드 부재 +
creationTimestamp 정합). 즉 본 fix(Pod label 유지)와 충돌할 외부 자동화는
없다 — 다만 그 Service의 `ownerReferences`가 CR UID에 배선돼 있어 CR
재생성 시 GC로 삭제된다는 **별개의, 영구 아닌 잔여 위험**이 있다(§Root Cause
Final Confirmation 잔여 위험 단락 + 후속 이슈 제안 참조).

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
- [x] AI-0005: `postgres-prod-shard-0-rw` Service + `postgres.keiailab.io/role`
      라벨의 관리 주체 확인 — **닫힘**: GitOps repo(platform/data) 차트 템플릿
      전수 검사 음성(매니페스트 0건, "shard-0-rw" 문자열은 체인지로그 산문) +
      라이브 CR spec 필드에 관련 키 부재 + Service `creationTimestamp`가
      commit `65d5819`(라벨 gap 발견 커밋)와 같은 날 · 그보다 ~3시간 이름 —
      **2026-06-15 1회성 수동 생성**으로 확정(팀 리드 실측, §Root Cause Final
      Confirmation). 별도 자동화 없음 → 본 fix와 충돌 위험 없음.
- [x] AI-0006: 배포 이미지가 실제 postgres-prod 운영 이미지인지 확인 — **닫힘**:
      팀 리드가 operator Deployment 이미지 = `ghcr.io/keiailab/postgres-operator:0.4.0-beta.5-reseed`
      (SLSA provenance 확인 소스 = commit `cd7c4f800cad3230fdbde0ff96182f2ff456d890`,
      2026-06-22 빌드)이고 그 operator가 `postgres-prod`를 소유함을 직접 실측.
- [ ] AI-0007 (신규): RFC-0004 §"P5+ bypass mode"(shard의 primary Service를
      operator가 직접 노출)를 **정식 구현** — 현재의 rw Service는 수동 생성물이라
      `ownerReferences`가 CR UID에 배선돼 있어 CR 재생성 시 GC로 삭제되는
      잔여 위험이 있다(영구 아님, 본 fix 범위 밖). 별도 feature request로 제안
      (Owner: TBD).

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
  lease `postgres-prod-shard-0-primary`,
  `postgres.keiailab.io/role` 라벨(팀 리드 2026-07-11 kubectl 직접 재실측 —
  rw Service selector + 죽은 shard-0-0 pod 양쪽에서 확인, 최초 초안의
  `instance-role` 키 추정을 정정), `postgres-prod-shard-0-rw` Service
  `ownerReferences={kind: PostgresCluster, name: postgres-prod,
  controller: true}`(팀 리드 재실측), `postgres-prod-shard-0-ro` Service
  NotFound(팀 리드 재조회)
- 배포 이미지 소스 커밋 확인(registry 조회, 클러스터 접근 없음):
  `ghcr.io/keiailab/postgres-operator:0.4.0-beta.5-reseed` →
  `crane manifest`/`crane blob`로 SLSA provenance attestation fetch →
  `vcs:revision=cd7c4f800cad3230fdbde0ff96182f2ff456d890`
  (`fix(rbac): operator ClusterRole pods delete 권한 추가 (reseedStandby 작동) (#277)`,
  2026-06-23, 현재 HEAD의 조상 — `git merge-base --is-ancestor` 확인)
- RFC-0004 (`docs/rfcs/0004-pg-router-architecture.md`) — "P5+ bypass mode:
  shard의 primary Service를 직접 노출" 미구현 gap, rw Service 수동 생성 가설의
  근거
- 최종 확정 실측(팀 리드, 2026-07-11): platform/data(GitOps) 차트 템플릿 전수
  검사 음성(Service/role 매니페스트 0건) + 라이브 CR spec 최상위 키
  `[imageCatalogRef, postgresVersion, shardingMode, shards]`(rw 생성 지시
  필드 없음) + rw Service `creationTimestamp=2026-06-15T09:18:05Z`(commit
  `65d5819` Author 시각 2026-06-15T12:26:30Z보다 ~3시간 이름, 같은 날)
