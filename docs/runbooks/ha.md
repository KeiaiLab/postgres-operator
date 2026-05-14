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

## References

- ADR-0001 (self-built distributed SQL)
- ROADMAP.md G1 (single-shard HA)
- `docs/kb/adr/0006-*` (Repmgr/PgBouncer/Barman parity)
