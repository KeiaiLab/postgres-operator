- pgBackRest full / differential / incremental backup
- BackupJob CRD reconcile + plugin invocation
- WAL archive to S3-compatible storage
- ScheduledBackup CRD for cron-based triggers

## SLO targets

- Full backup: completion within configured `spec.window`
- WAL archive lag: **≤ 5min**

## Verify

```bash
kubectl apply -f - <<YAML
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
metadata:
  name: smoke-backup
spec:
  postgresCluster: <name>
  backupType: full
YAML
kubectl wait --for=condition=Completed backupjob/smoke-backup --timeout=600s
# Verify S3:
aws s3 ls s3://<bucket>/<name>/backup/
```

## References

- ROADMAP.md G1 (backup/restore controller)
- ADR-0006 (Barman parity)
