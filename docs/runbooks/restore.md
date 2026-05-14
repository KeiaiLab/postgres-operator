- BackupJob.spec.restore from existing backup
- PITR via TargetTime
- Cross-cluster restore (externalClusters bootstrap.pg_basebackup)

## SLO targets

- PITR window: **≥ 7 days** from latest full backup
- Restore drill: monthly automated + checksum verification

## Verify

```bash
kubectl apply -f - <<YAML
apiVersion: postgres.keiailab.com/v1alpha1
kind: BackupRestore
metadata:
  name: smoke-restore
spec:
  postgresCluster: <new-name>
  backupRef:
    name: <previous-backup>
  targetTime: "2026-05-14T12:00:00Z"  # PITR target
YAML
kubectl wait --for=condition=Restored postgrescluster/<new-name> --timeout=900s
# checksum drill:
pg_checksums -D /var/lib/postgresql/data --check
```

## References

- ROADMAP.md G1 (PITR restore [~])
- ADR-0006 (pgBackRest restore --type=time)
