# Upgrade Runbook

> ROADMAP G1 L92 + D.2.3. PostgresCluster + operator binary 의 upgrade
> 절차 + rollback 절차 + 사후 검증 SOP.

## 1. Upgrade 분류

| 분류 | 예 | 위험도 | 다운타임 | 권장 절차 |
|---|---|---|---|---|
| **Patch** | 18.1 → 18.2 | LOW | 0 (rolling) | §4 minor patch |
| **Minor** (N → N+1) | 18 → 19 | MEDIUM | 0 (rolling) | §5 minor major |
| **Major** (N → N+2+) | 18 → 20 | HIGH | 1-5 min cutover | §6 major upgrade |
| **Operator binary** | v0.4.0-beta.1 → next | LOW (replica) / MEDIUM (primary) | 0 | §7 operator upgrade |

## 2. Pre-upgrade 체크리스트 (MUST, 모든 분류)

- [ ] `kubectl get postgrescluster <name>` Ready=True
- [ ] 모든 replica `instance-role=replica` + ready=true
- [ ] `kubectl get backupjob -n <ns>` 최신 full backup ≤ 24h ago
- [ ] `kubectl get backupjob <latest> -o jsonpath='{.status.phase}'` = Succeeded
- [ ] `kubectl get pdb` PDB 가 rolling 1 unavailable 허용
- [ ] `pg_dump --schema-only` 로 schema snapshot 보관
- [ ] `pg_stat_replication` lag = 0 (sync) 또는 < 1MB (async)
- [ ] Maintenance window 공지 (Slack / status page / On-call)
- [ ] Rollback plan 점검 — §8 참조
- [ ] 모니터링 대시보드 열어두기 (Grafana cluster overview + Pooler)

## 3. ImageCatalog 준비

operator 는 ImageCatalog CRD 로 *선언적 image 교체* 를 수행한다. 직접 image 변경 금지.

```bash
# 1. ImageCatalog 의 신버전 추가
kubectl patch imagecatalog <catalog> --type=json -p='[
  {"op":"add","path":"/spec/images/-","value":{"major":"19","image":"ghcr.io/keiailab/pg:19.0"}}
]'

# 2. PostgresCluster spec 갱신 (rollout 시작)
kubectl patch postgrescluster <name> --type=merge -p='{
  "spec":{"imageCatalogRef":{"major":"19"}}
}'
```

## 4. Patch upgrade (N.x → N.y, 같은 major)

같은 major 안에서 patch 갱신. binary-compatible, on-disk format 동일.

```bash
# 1. ImageCatalog 의 해당 major 의 image tag 만 갱신
kubectl patch imagecatalog <catalog> --type=json -p='[
  {"op":"replace","path":"/spec/images/0/image","value":"ghcr.io/keiailab/pg:18.2"}
]'

# 2. operator 가 StatefulSet 의 image 자동 patch + rolling restart
# 3. replica → primary 순서로 자동 rolling. PDB 가 보호.

# 4. 검증
kubectl rollout status statefulset/<name>-shard-0
kubectl exec <name>-shard-0-0 -- psql -c 'SELECT version();' | grep 18.2
```

**예상 시간**: 3 replica = ~5 분.
**다운타임**: 0 (writer 는 primary 마지막 restart 시 < 5s pause).

## 5. Minor major (N → N+1, e.g. 18 → 19)

PostgreSQL major 는 *N+1* 사이에서도 catalog format 호환 안 됨 →
`pg_upgrade` 필요. 본 operator 는 *in-place pg_upgrade* 가 아닌
**logical replication 기반 cutover** 채택 (안전성 우선).

절차:

1. **신 cluster 생성** — 별도 PostgresCluster 리소스 (`<name>-v19`) + 동일 storage + replica count
   ```bash
   kubectl apply -f manifests/upgrade/<name>-v19.yaml
   ```
2. **logical replication 설정** — `publication` (old) + `subscription` (new)
   ```sql
   -- on old primary
   CREATE PUBLICATION upgrade_all FOR ALL TABLES;
   -- on new primary
   CREATE SUBSCRIPTION upgrade_all
     CONNECTION 'host=<old-svc> dbname=<db> user=replicator password=...'
     PUBLICATION upgrade_all;
   ```
3. **catch-up 대기** — `pg_stat_subscription` lag = 0 까지
4. **schema 검증** — `pg_dump --schema-only` 양쪽 비교
5. **cutover window** — `pg_stat_activity` writer 차단 + replication lag = 0 재확인
6. **Service endpoint 전환** — `kubectl patch service <name>-primary` selector 갱신
7. **검증** — `psql` write + read sample query
8. **old cluster 보존 기간** — 최소 24h 운영 + 백업 → 그 후 삭제

**예상 시간**: cluster 크기에 비례. 100GB ≈ 30 min (network 의존).
**다운타임**: cutover window < 30s.

## 6. Major upgrade (N → N+2+, e.g. 18 → 20)

§5 minor major 와 동일한 logical replication 절차이나 *주의 사항* 증가:

- **deprecated features** 점검 (release notes 의 `Deprecated` / `Incompatible` 섹션 grep)
- **extension 호환성** — `pg_available_extensions` 신구 비교 (PostGIS / pgvector 등 별도 upgrade)
- **collation** — glibc upgrade 동반 시 ICU 강제 권장
- **TLS / auth** — `scram-sha-256` 강제 여부 확인
- **encoding / locale** — `SHOW SERVER_ENCODING` / `SHOW LC_COLLATE` 양쪽 일치 검증

cluster 크기가 크면 (≥ 1TB) §5 절차 + 다음:

- **shard 별 순차 upgrade** — G3+ 환경에서 `ShardRange` 의 shard 분기로 차례로 진행
- **사전 vacuum freeze** — `VACUUM FREEZE` 사전 실행으로 transaction id wraparound 위험 해소

## 7. Operator binary upgrade (v0.4.0-beta.1 → next)

operator manager 자체 image 교체. Helm chart 의 `appVersion` 갱신.

```bash
# 1. CHANGELOG / breaking changes 확인 (CHANGELOG.md)
# 2. Helm upgrade (실 클러스터)
helm upgrade postgres-operator -n postgres-operator \
  oci://ghcr.io/keiailab/postgres-operator \
  --version <new-version>

# 3. CRD diff 검증 — 기존 PostgresCluster 가 신 CRD 와 호환되는지
kubectl get crd postgresclusters.postgres.keiailab.io -o yaml | yq '.spec.versions[].schema'

# 4. operator manager Pod restart 확인
kubectl rollout status deploy/postgres-operator -n postgres-operator

# 5. 회귀 검증
kubectl get postgrescluster --all-namespaces -o wide
# 모든 cluster Ready=True 여야 함
```

**주의**: alpha 단계 (`0.x.y-alpha`) 는 *CRD breaking change* 가능. release notes
의 *Migration steps* 섹션 필수 확인.

## 8. Rollback 절차

### 8.1 Patch / minor patch (in-place)

```bash
# ImageCatalog 의 image tag 를 이전 버전으로 revert
kubectl patch imagecatalog <catalog> --type=json -p='[
  {"op":"replace","path":"/spec/images/0/image","value":"ghcr.io/keiailab/pg:18.1"}
]'
# StatefulSet rolling restart 자동
```

### 8.2 Major / minor major (logical replication 절차)

cutover **이전** rollback 은 단순 — 신 cluster 삭제 + Service revert.
cutover **이후** rollback 은 *역방향 logical replication* 필요:

1. 신 cluster → 구 cluster 방향 publication / subscription 추가
2. 신 cluster 의 변경분 backfill 까지 대기
3. Service endpoint 를 구 cluster 로 revert
4. 신 cluster 삭제

**중요**: cutover 이후 신 cluster 의 *새 형식 데이터* (e.g. PG19 의 새 type)
가 구 cluster 에 backfill 불가 시 → rollback 불가. 이 경우 *forward fix only*.

### 8.3 Operator binary

```bash
helm rollback postgres-operator <previous-revision> -n postgres-operator
```

CRD 가 incompatible breaking change 였으면 — *CRD 별도 다운그레이드* 필요.
release notes 의 명시적 migration steps 참조.

## 9. 사후 검증 SOP (모든 분류 공통)

upgrade 완료 후 24h 이내 다음 측정 기록:

- [ ] `pg_stat_replication` lag = 0 / sync_state=streaming
- [ ] `pg_stat_database` deadlock / conflict 카운터 변화 없음
- [ ] `pg_stat_statements` top-10 query latency 회귀 없음 (±20%)
- [ ] `postgres_operator_postgrescluster_replication_lag_bytes` baseline 회복
- [ ] Pooler 의 `pgbouncer_pools_*` saturation 정상
- [ ] backup full + restore drill (24h 이내 1회) PASS

기록 위치: `docs/kb/incident/` 또는 `docs/runbooks/upgrade-log.md` (선택).

## 10. 자동화 + e2e

`test/e2e/version_upgrade_e2e_test.go` skeleton 이 존재. D.6.3 으로 마감 예정:

- 14 → 15 → 16 minor major upgrade matrix
- patch upgrade (in-place) verification
- operator binary upgrade rollback drill

실행:

```bash
make test-e2e-version-upgrade
```

## 11. References

- ROADMAP.md G1 L92 (upgrade rollback runbook)
- ROADMAP.md G6 L194 (upgrade matrix N→N+1/N+2/patches, D.11.4)
- `docs/runbooks/ha.md` — failover + RTO/RPO 측정
- `docs/runbooks/restore.md` — PITR restore drill
- `docs/runbooks/backup.md` — pgBackRest 백업 cycle
- ADR-0006 — Repmgr / PgBouncer / Barman 통합 (extension 호환성)
- PostgreSQL release notes — https://www.postgresql.org/docs/release/
- `internal/controller/imagecatalog_controller.go` — ImageCatalog reconciler
