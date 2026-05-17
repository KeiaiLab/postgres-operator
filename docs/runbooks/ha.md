- Primary failure detection + automatic failover (replica promote)
- Sync replication enforcement under `spec.postgresql.synchronous`
- PVC fencing (split-brain prevention)
- HA election distributed lock (K8s Lease)

## SLO targets

- RTO (Recovery Time Objective): **≤ 60s** for primary failure
- RPO (Recovery Point Objective): **0** (sync replication enforced)

## Verify steps

```bash
kubectl exec <primary-pod> -- pg_ctl -D /var/lib/postgresql/data stop -m immediate
kubectl wait --for=condition=Ready postgrescluster/<name> --timeout=120s
# 새 primary 확인:
kubectl get postgrescluster <name> -o jsonpath='{.status.currentPrimary}'
```

## 라이브 측정 명령 (T31 G1 drill)

`hack/smoke.sh` 의 `SMOKE_REJOIN` + `SMOKE_SYNC` 환경변수로 SLO 를 라이브 검증한다.

```bash
# RTO 측정 (primary kill → standby promote)
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 ./hack/smoke.sh

# Rejoin 정합 (basebackup + pg_rewind 양 분기)
SHARD_REPLICAS=2 SMOKE_FAILOVER=1 SMOKE_REJOIN=1 ./hack/smoke.sh

# RPO=0 직접 증명 (1000-row commit 후 sync standby flush_lsn >= commit_lsn)
SHARD_REPLICAS=2 SMOKE_SYNC=1 ./hack/smoke.sh

# sync standby kill 후 primary write 차단 enforce (침습적, opt-in)
SHARD_REPLICAS=2 SMOKE_SYNC=1 SMOKE_SYNC_KILL=1 ./hack/smoke.sh
```

각 단계는 PASS 시 exit 0 + `PASS:` 행 출력. FAIL 시 즉시 exit 1.

## References

- ADR-0001 (self-built distributed SQL)
- ROADMAP.md G1 (single-shard HA)
- `docs/kb/adr/0006-*` (Repmgr/PgBouncer/Barman parity)
