# ADR-0030: 승격 상태를 파드 템플릿 밖 런타임 ConfigMap 으로

- **Date**: 2026-09-25
- **Status**: Accepted
- **Authors**: @claude (OP2 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.2·§3.6 · #220 · data#119 · ADR-0027/0029(named shard)

## Context

- v0.4.0-beta.11 은 status 의 primary 를 StatefulSet 템플릿 env `PRIMARY_ENDPOINT`(init·main)로
  굽는다. 승격마다 템플릿이 바뀌고 RollingUpdate 가 높은 서수부터 primary 까지 재시작한다 →
  primary 부재가 디바운스를 넘긴다 → 다시 승격 → 루프(2026-09-24: 승격 324회/22h, 재시드 PVC
  삭제로 PV 누수).
- 승격 마커는 빈 파일이라 basebackup 이 복제본에 복사해 복제본도 promoted 로 보였다.
- 부트스트랩은 `primary_conninfo` 를 덧붙이기만 해 멤버마다 수백 줄이 쌓였다.

## Decision

1. shard 마다 런타임 ConfigMap `<cluster>-<shard>-runtime`(ordinal·named 공통, 소유자 = 클러스터):
   `PRIMARY_ENDPOINT` · `PROMOTION_EPOCH` · `CLUSTER_INITIALIZED`. 템플릿은 `configMapKeyRef`
   (Optional=false)로만 참조한다 — init 은 세 키, main 은 `PRIMARY_ENDPOINT`. 리샤드 타깃
   (ShardSplitJob)은 빈 값 인라인, 복제 클러스터는 외부 원천 인라인(로컬 승격 없음).
2. 정본 = CR 애노테이션 `postgres.keiailab.io/shard-runtime`, JSON v1
   `{"version":1,"shards":{"<shard>":{"epoch":N,"initialized":B,"promotionLSN":"X/Y"}}}`.
   epoch 을 줄이는 코드·initialized 를 끄는 코드는 없다. CM 은 사본이다.
3. CM 의 primary·epoch 를 바꾸는 길은 둘뿐이다:
   `promoteAndPublish`(CR epoch E+1 선기록 → 승격 exec 가 epoch 을 마커에 기록 →
   `PROMOTE_RESULT=promoted` 면 CM 쓰기 한 번 → 새 타임라인 분기점을 `promotionLSN` 으로)와
   채택(OP2b). `ensureRuntime` 은 생성만 한다.
4. CM 이 없을 때: 건강한 Role=Primary 가 정확히 하나면 그 파드로 만든다(정본이 없으면 첫 파생 —
   epoch = 멤버 마커 최대, initialized = status 기록). 새 shard(STS·멤버 없음)는 첫 값 폴백
   (status 기록, 없으면 ordinal-0 DNS). 그 밖에는 만들지 않고 STS upsert 를 건너뛴다(옛 템플릿
   유지, condition `RuntimeConfigPending=True`).
5. 부트스트랩(§3.6): standby 는 마커를 지우고 `primary_conninfo` 를 CM 값 한 줄로 바꿔 쓴다
   (같은 디렉터리 임시 파일 뒤 rename) · 마커 epoch > `PROMOTION_EPOCH` 면 primary 유지 ·
   이하면 강등 · 빈 PGDATA ordinal-0 은 `CLUSTER_INITIALIZED=1` 이면 initdb 대신 실패 ·
   basebackup 이 복사한 마커는 지운다.

## Consequences

- 승격이 템플릿을 바꾸지 않는다(I1) — 루프의 첫 고리가 끊긴다. 템플릿은 운영 반영(L1)에서 한 번 바뀐다.
- kubelet 은 컨테이너를 만들 때마다 CM 을 읽는다 — 다시 뜬 멤버는 최신 primary 를 본다.
  CM 이 지워지면 정본에서 같은 epoch·initialized 로 다시 만든다.
- 발행 전 크래시: CR epoch 이 CM 보다 앞선다. 새 primary 는 마커 epoch 이 커서 primary 로
  남고, 채택(OP2b)이 CM 을 맞춘다.
- CM 이 없고 primary 를 하나로 못 정하면 그 shard 의 STS 는 갱신되지 않는다(hibernate 된 채
  업그레이드된 클러스터 포함) — condition 이 O1 경보로 보인다.
- 초기화된 클러스터의 빈 ordinal-0 은 빈 primary 로 뜨지 않는다(I6).

## Alternatives Considered

- Service 이름을 `PRIMARY_ENDPOINT` 로: `PRIMARY_IS_SELF` 가 늘 0 이 돼 부트스트랩 분기가 무너진다.
- 파드가 lease/CR 을 직접 조회: instance SA 는 leases 만 가진다 — 권한 확대.
- aggregate 를 따라 발행: primary 가 둘로 보이면 CM 이 요동한다.
- StatefulSet OnDelete 만: 승격이 템플릿을 바꾸는 입력은 남는다(PL3 가 별도로 한다).

## Status

Accepted (OP2). 채택(OP2b)과 hold 키(OP4b: `MEMBER_HOLD` · `RESEED_REQUESTS`)가 같은 CM 에 더해진다.
