# ADR-0035: PG 설정은 spec 에서 렌더하고 오퍼레이터가 소유한다 — auto.conf 의 렌더 키는 크지 않을 때만 RESET

- **Date**: 2026-09-29
- **Status**: Accepted
- **Authors**: @claude (OP5 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.15·§3.17 · 부록 B-2 · 불변식 I22·I23 · 심판 DS-4·PR-1·FC-soft · ADR-0033(단계화 롤아웃) · ADR-0034(적재 판정)

## Context

- 렌더 postgresql.conf 는 고정 키뿐이었다 — 파라미터 통로가 없어 shared_buffers 는 PG 기본 128MB(limits 8Gi 에서),
  wal_keep_size 는 0 이었다.
- 메인 루프가 2026-09-25 세 멤버에 `ALTER SYSTEM SET wal_keep_size='5GB'` 로 WAL 보존을 급히 넣었다.
  postgresql.auto.conf 는 렌더 conf 뒤에 읽혀 이긴다 — 렌더 키가 auto.conf 에 남으면 spec 이 조용히 무시되고,
  RESET 을 잘못 하면(투영 전 · 더 큰 값) WAL 보존이 조용히 준다.
- auto.conf 는 PGDATA 에 있고 복제되지 않는다(pg_basebackup 은 복사만 한다) — 멤버마다 다르다.
- 단위 없는 auto.conf 값은 GUC 기본 단위다: `wal_keep_size = '10240'` 은 10240MB 인데 `pg_size_bytes('10240')` 은
  10240 바이트다.

## Decision

1. CR 가산 필드 `spec.postgresql.walKeepSize`(quantity) · `maxConnections`(int32, ≥ 1). `spec.shards.resources.limits.memory`
   에서 `shared_buffers` = ÷4 · `effective_cache_size` = ×3/4(MiB 내림), `wal_keep_size` 는 MiB 올림(보존은 요청보다
   줄이지 않는다). 값이 없으면 줄도 없다 — 기존 키는 그대로이고 새 줄은 `hot_standby` 뒤에 붙는다. 설정 해시(conf +
   hba)가 바뀌어 단계화 롤아웃(ADR-0033)이 한 번 돈다. D1 values(limits 8Gi · walKeepSize 5Gi) →
   `'2048MB'` · `'6144MB'` · `'5120MB'`.
2. 렌더 키 소유(`reconcileAutoConf`, 적재가 낡은 멤버 reload 뒤): 멤버마다(스탠바이 포함) 렌더 키 ∩ auto.conf 항목을
   `pg_file_settings`(지금 파일)로 읽는다.
   - 렌더 conf 파일이 그 키를 렌더 값으로 보이지 않거나 문법 오류 → 기다린다(투영 전 RESET 은 옛 파일 값으로 떨어진다).
   - auto 값 ≤ 렌더 값 → `ALTER SYSTEM RESET` + `pg_reload_conf()` · 이벤트 `AutoConfOverrideReset` ·
     `auto_conf_resets_total{key}`. 같은 글이면 참, integer·real 은 `pg_settings.unit` 기본 단위로 비교한다(메모리 =
     바이트 · 시간 = µs). 음수(−1 = 제한 없음 같은 표지)·읽지 못하는 값은 비교하지 않는다.
   - 그 밖(더 크다 · bool·enum·문자열이 다르다 · 비교할 수 없다) → 남긴다 · Warning `AutoConfOverrideKept`(남긴 항목이
     바뀔 때 한 번) · condition `AutoConfOverride`=True(`AutoConfOverrideKept` / False `AutoConfOwned`).
   - `primary_conninfo` · `primary_slot_name` · `restore_command` 는 렌더돼도 대상 밖이다(부트스트랩·재지정·복구가 쓴다).
   - 판정은 설정 해시·파드 UID 가 바뀔 때, 그 밖엔 `autoConfRecheck`(= reseedCooldown) 마다다. exec 실패·투영 전은
     다음 reconcile 이 다시 보고, RESET 뒤 reload 가 실패하면 다음 reconcile 이 reload 를 다시 보낸다.
3. 순서(L1): 비밀번호(ADR-0034) → 설정 CM(hba·conf) → 적재가 낡은 멤버 reload → RESET.

## Consequences

- L1: D1 walKeepSize 5Gi = 메인 루프 5GB — RESET 은 값 중립이다(sourcefile 만 렌더 conf 로 바뀐다). 단계화 롤아웃의
  첫 단계 전 선결(primary wal_keep_size = 렌더)이 실제로 걸리고 5GB = 5120MB 라 통과한다.
- 렌더 값보다 큰 auto.conf 값은 남는다 — primary 면 롤아웃이 PrimaryConfigPending 에서 기다리고 두 condition 이 경보로
  간다(조용한 감소 0). 푸는 길은 spec 을 올리거나 그 값을 내리는 것이다.
- 어떤 경로도 wal_keep_size 를 낮추지 않는다 — RESET 은 크지 않은 값만 지우고, 렌더는 요청을 올린다.
- shared_buffers·max_connections 는 재시작 설정이라 롤아웃이 멤버를 교체할 때 적용된다. 렌더 max_connections 가
  primary 보다 작으면 ParameterDecrease 가 롤아웃을 막는다(I24).
- 오퍼레이터 exec 가 멤버마다 판정 한 번(결론 뒤엔 autoConfRecheck 마다)을 더한다.

## Alternatives Considered

- `pg_size_bytes` 로 비교: 단위 없는 auto.conf 값을 바이트로 읽어 더 큰 값(`'10240'` = 10GB)을 RESET 한다 — 조용한 감소.
- `pg_settings.sourcefile`(적재 상태)로 대상 고르기: RESET 은 파일을 고친다 — ALTER SYSTEM 뒤 reload 전 항목을 놓친다.
- auto.conf 를 무조건 RESET: 투영 전이면 옛 파일 값(PG 기본 0)으로, 더 크면 조용히 줄인다.
- ALTER SYSTEM 으로 운영: 파일 두 곳이 정본을 다툰다 — 렌더가 정본이다.

## Status

Accepted (OP5).
