# ADR-0034: pg_hba 는 어떤 네트워크 대역도 trust 하지 않는다 — superuser 비밀번호는 오퍼레이터가 Secret 에 맞춘다

- **Date**: 2026-09-29
- **Status**: Accepted
- **Authors**: @claude (SEC1 구현)
- **Refs**: 설계 `2026-09-25-postgres-operator-loop-fix-design.md` §3.18·§3.6-0 · 불변식 I25·I27 · 심판 SEC-1~7 · ADR-0033(단계화 롤아웃)

## Context

- 오퍼레이터가 렌더한 pg_hba 는 `host all postgres {10/8,172.16/12,192.168/16} trust` 와
  `host replication all … trust` 를 가졌다. postgres 는 superuser·replication 을 가진 유일한 역할이고 rolpassword
  가 NULL 이라, 그 대역에서 오는 누구나 비밀번호 없이 superuser 가 된다. 노드 IP 도 10/8 이라 hostNetwork 파드가
  이 경로를 쓴다 — NetworkPolicy 로는 못 막는다.
- 복제·pg_rewind·pg_basebackup 은 인스턴스 매니저가 user=postgres 로 붙고(무변, I26) 전부 이 trust 로 돌았다.
- 오퍼레이터 SQL 은 전부 exec + 로컬 소켓(local trust)이다.
- `pg_hba_file_rules` 는 파일을 읽는다 — 적재된 규칙이 아니다. 파일이 바뀌어도 reload 전까지 옛 규칙이 산다.

## Decision

1. pg_hba = `local all all trust` + `host{ssl} all all <CIDR> scram-sha-256` +
   `host replication postgres <CIDR> scram-sha-256`. `<CIDR>` = `spec.postgresql.hostAuthCIDRs`(CRD cidr 형식,
   표준 표기로 렌더), 비면 사설 대역 셋. 비-local trust 는 어떤 판도 렌더하지 않는다.
2. 비밀번호 원천 = `spec.superuserSecretRef`(username = postgres · password = 비지 않은 인쇄 가능 ASCII).
   `ensureSuperuserAuth`(샤드 루프 맨 앞)가 실제 primary 의 `pg_authid.rolpassword` 를 그 salt·iteration 으로 다시
   계산해 StoredKey·ServerKey 를 대조한다. 다르거나 NULL 이면 새 salt(16 B)·4096 반복 verifier
   `SCRAM-SHA-256$4096:<salt>$<StoredKey>:<ServerKey>` 를 로컬 exec `ALTER ROLE postgres PASSWORD '<verifier>'` 로
   싣고 다시 읽어 대조한다. 평문은 exec 인자·로그·이벤트·condition 에 없다.
3. 확인되기 전엔 멤버가 도는 샤드의 설정 CM·STS 를 바꾸지 않는다(없는 것은 만든다 — 지킬 라이브 객체가 없다).
   멤버를 0 으로 줄이는 반영(하이버네이션·복원·비활성 샤드)은 막지 않는다. 멤버 파드가 없는 샤드(새 클러스터)는
   대조할 곳이 없어 막지 않는다 — 첫 primary 가 뜨면 다음 reconcile 이 맞춘다. condition `SuperuserAuthPending`
   (SuperuserSecretUnset · SuperuserSecretMissing · SuperuserSecretInvalid · SuperuserAuthUnverified /
   SuperuserAuthVerified), 맞춘 뒤 이벤트 `SuperuserAuthUpdated`. 복제 클러스터는 대상 밖(NotApplicable).
4. 자격 전달: init env `LOCAL_SUPERUSER_PASSWORD`(secretKeyRef, optional false) → 부트스트랩이 hold 검사 직후
   모든 길에서 `/tmp/primary.pgpass` = `*:*:*:postgres:<이스케이프>`(0600) 를 쓴다. 두 컨테이너 env
   `PGPASSFILE=/tmp/primary.pgpass` — walreceiver·pg_rewind·pg_basebackup 은 매니저 환경을 이어받는다. HA
   conninfo 에는 password·passfile 을 싣지 않는다. Secret 볼륨을 passfile 로 쓰지 않는다(fsGroup 그룹 비트).
5. 적재 판정(멤버마다): `date_trunc('second', pg_conf_load_time()) > (pg_stat_file(hba_file)).modification`
   이면 적재 = 파일 → 게이지 `hba_network_trust_rules` = 비-local trust 줄 수. 적재가 이르면 `pg_reload_conf()`
   (스탠바이 포함 — primary 에만 매 reconcile 보내던 reload 를 대신한다) · 파일 오류 · 판정 실패는 −1.

## Consequences

- 네트워크 경로(파드·hostNetwork·노드)로는 비밀번호 없이 superuser 가 될 수 없다(I25). 로컬 소켓 trust 는 남는다
  — 파드 exec 권한이 곧 superuser 다.
- L1: 첫 reconcile 이 비밀번호를 맞춘 뒤 hba·템플릿을 바꾼다 → 적재가 낡은 멤버가 reload → 옛 revision 멤버의
  재접속은 인증 실패(passfile 없음)라 끊긴 멤버가 되고 단계화 롤아웃(ADR-0033)이 그 멤버부터 교체한다.
- superuserSecretRef 가 없는 클러스터는 멤버가 도는 동안 설정·템플릿이 그대로다(Pending) — trust 를 걷은 hba 를
  비밀번호 없이 적재하면 복제가 끊긴다. 새 HA 클러스터를 Secret 없이 만들면 replica 가 basebackup 에서 인증에
  실패한다(condition 이 사유를 말한다).
- Secret 비밀번호가 바뀌면 오퍼레이터가 verifier 를 다시 맞추지만 돌던 파드의 passfile 은 파드가 다시 만들어질
  때까지 옛 값이다.
- ShardSplitJob 복사 DSN 은 네트워크 trust 로 붙어 SEC1 뒤 인증에 실패한다(CR 0 — 후속).
- 비상 정지는 ConfigMap 을 건드리지 않아 hba 가 유지된다. SEC1 뒤 어떤 판도 trust 를 렌더하지 않는다 — 교정
  이미지도 렌더 시험(TestHBANoNetworkTrust)을 통과해야 한다.

## Alternatives Considered

- hba 만 바꾸고 비밀번호는 사람이 맞춘다: 사람 게이트이고, 순서가 틀리면 복제가 끊긴다.
- 평문 `ALTER ROLE … PASSWORD '<평문>'` exec: exec 인자·apiserver 감사·log_statement 에 평문이 남는다.
- Secret 을 passfile 로 마운트: fsGroup 이 그룹 비트를 더해 libpq 가 파일을 무시한다.
- 전용 복제 역할: 인스턴스 매니저가 user=postgres 를 하드코딩(I26) — postgres 복제 접근을 못 걷어 두 번째 경로만 는다.
- `pg_hba_file_rules` 만으로 판정: 파일이지 적재 상태가 아니다 — reload 전 멤버를 거짓 초록으로 본다.

## Status

Accepted (SEC1). OP5 는 같은 적재 판정·reload(`reloadStaleMembers`)로 렌더 설정을 소유한다.
