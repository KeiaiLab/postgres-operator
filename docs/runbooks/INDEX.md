# Runbook Index — postgres-operator

This page lists the operational runbooks for production-grade operation
of `postgres-operator`. Each runbook contains a *target SLO*, verification
commands, and rollback procedure.

## Active runbooks

| Runbook | Scope | SLO |
|---|---|---|
| [backup.md](backup.md) | `BackupJob` + `ScheduledBackup` daily/cron backups | RPO ≤ 5 min, backup completion ≤ 30 min |
| [restore.md](restore.md) | Point-in-time restore from `BackupJob` archives | RTO ≤ 1 h |
| [ha.md](ha.md) | High-availability failover + lease election | Primary failover RTO ≤ 60 s |
| [pvc-fence.md](pvc-fence.md) | StatefulSet PVC fencing on Pod loss | Fence apply ≤ 30 s |
| [upgrade.md](upgrade.md) | Operator + PostgreSQL minor / major version upgrade | Rolling upgrade ≤ 10 min per shard |
| [security.md](security.md) | Restricted PSA, NetworkPolicy, TLS rotation | Daily audit, no privilege escalation findings |
| [migration.md](migration.md) | Data migration into / out of `PostgresCluster` | Cutover SLA target p99 < 500 ms (multi-shard goal) |

## Conventions

- Each runbook is *self-verifying*: the listed commands MUST succeed before declaring the procedure complete.
- The *Rollback* section is mandatory and MUST exit at the same operational state as before the procedure was attempted.
- Where a `kubectl` / `helm` command produces output, the runbook quotes the **expected pattern** (regex or exact string) so the operator can pattern-match in production.

## Reading order

For day-0 operation, read `ha.md` + `backup.md` + `restore.md` first. For ongoing operation, `upgrade.md` and `security.md`. `pvc-fence.md` is invoked from `ha.md` failover and is rarely run standalone.

---

<p align="center">
  © 2026 keiailab · <a href="../../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
