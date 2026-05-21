---
title: "HA Leader Election"
---

# HA Leader Election — operations guide (P2-M1)

> This document describes the operational interface of the first stable
> Pillar P2 (HA / Failover) deliverable: **K8s lease-based leader
> election**. Rationale and frozen parameters live in
> [RFC-0007 — HA Election + PVC Fencing protocol (Draft)](../rfcs/0007-ha-election-and-fencing.md)
> and the prior v0.x self-built-instance-manager decision (history preserved in git).

## 1. What runs

Each `PostgresCluster` instance manager (`cmd/instance`) enters one of
two election modes on boot.

| Mode | Use case | CLI flag |
|---|---|---|
| `real` (default) | members ≥ 2 — leader election via a K8s `coordination.k8s.io/v1` Lease | `--election=real` |
| `disabled` (Null) | single-node development — always leader | `--election=disabled` |

In `real` mode we delegate to client-go's
`leaderelection.LeaderElector`. The operator wraps it with an
`Election` interface so unit and integration tests share the same
signature (`internal/instance/election`).

## 2. Lease naming (RFC 0003 §1)

| Role | Lease name |
|---|---|
| Coordinator primary | `<cluster>-coordinator-primary` |
| Worker-pool primary | `<cluster>-worker-<pool>-primary` |

Namespace matches the PostgresCluster CR.

```bash
# Query leases for the orders cluster
kubectl get lease -n <ns> | grep '^orders-'
```

## 3. Lease parameters — operational knobs

| Parameter | Default | CLI flag |
|---|---|---|
| LeaseDuration | 15 s | `--lease-duration` |
| RenewDeadline | 10 s | `--renew-deadline` |
| RetryPeriod | 2 s | `--retry-period` |

**Constraint**: `RetryPeriod < RenewDeadline < LeaseDuration`. Validated
at startup; a violation errors out
(`internal/instance/election/lease.go:Validate`).

### Recommended tuning

| Environment | Recommended LeaseDuration | Why |
|---|---|---|
| Stable LAN, single AZ | 10 s | Faster failover |
| Multi-AZ / multi-region | 20–30 s | Absorb network jitter |
| Chaos / fault injection | 5 s | Faster regression loop |

Shrinking LeaseDuration linearly increases lease-renewal traffic on the
K8s API server. Be careful in clusters with 100+ nodes.

## 4. Identity (POD_NAME unification)

The instance manager's lease-holder identity is **`$POD_NAME`** (downward
API). PodSpec excerpt:

```yaml
env:
  - name: POD_NAME
    valueFrom: { fieldRef: { fieldPath: metadata.name } }
  - name: POD_NAMESPACE
    valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
  - name: POSTGRES_CLUSTER
    value: orders
```

Because Kubernetes guarantees `POD_NAME` uniqueness within a namespace,
two instances with the same identity cannot become lease-holder
simultaneously.

## 5. Operational scenarios

### A. Normal startup

1. All instance-manager Pods enter election.
2. One Pod acquires the LeaseLock → `OnStartedLeading` callback →
   status = `Leader`.
3. The other Pods receive `OnNewLeader` callback and transition to
   `Follower`.
4. Followers poll for lease changes every RetryPeriod (2 s).

### B. Leader graceful shutdown (SIGTERM)

1. `kubectl delete pod <leader-pod>` or a deployment rollout.
2. The instance manager cancels its context → election Run returns →
   **`ReleaseOnCancel=true`, so the lease is released immediately**.
3. A follower acquires the new lease within one RetryPeriod.
4. The new leader fires `OnStartedLeading`.

### C. Leader unresponsive (node failure)

1. The leader Pod stops responding (OOM, kubelet disconnect, …).
2. No lease renewal happens for one **LeaseDuration (15 s)**.
3. A follower observes expiry and attempts to take the lease.
4. The rest matches scenario B.

**PVC fencing (P2-T2, active since 2026-04-28)** — in scenario C the old
leader returning from the dead would cause split-brain; PVC-label-based
fencing prevents it. See
[RFC-0007 Appendix A — PVC fencing protocol](../rfcs/0007-ha-election-and-fencing.md#6-appendix-a--pvc-fencing-protocol-details-p2-t2-implemented-2026-04-28)
for the full protocol. Knob: `--fencing-disabled` (development only,
forbidden in production).

## 6. Observability

### Logs

Every transition is emitted as structured log (`slog.Info`):

```
{"msg":"Leadership transition", "from":"Starting", "to":"Leader", "identity":"orders-coordinator-0"}
```

### Pod /readyz

- Leader: 200
- Follower: 200
- Starting (election bootstrap): 503

### Prometheus metrics (active once P6 lands)

- `instance_election_status{cluster, role, pool, status}` — gauge.

## 7. Troubleshooting

| Symptom | Likely cause | Diagnose |
|---|---|---|
| No Pod becomes leader | RBAC: missing `coordination.k8s.io/leases` permissions | `kubectl auth can-i update lease.coordination.k8s.io -n <ns> --as system:serviceaccount:<ns>:<sa>` |
| Two Pods both claim Leader | duplicate identity (downward API missing) | `kubectl exec <pod> -- env \| grep POD_NAME` |
| Failover takes 5+ minutes | LeaseDuration too high or K8s API server latency | inspect `renewTime` via `kubectl get lease -n <ns> -o yaml` |
| Boot panics with "invalid lease parameters" | RenewDeadline ≥ LeaseDuration | recheck CLI flags |

## 8. Known limitations (M1 + P2-T2)

- **Failover controller verification is bounded** — controller-layer
  promotion exec and status convergence are implemented, but live chaos
  for network partition / STONITH classes is F05 follow-up.
- **`pg_rewind` live drill not finished** — when the former-primary
  marker is found, the operator runs `pg_rewind --target-pgdata ...
  --source-server ...`, and on failure falls back to a fresh
  `pg_basebackup`, recording the failure reason/message in status. The
  divergent-WAL → rewind success/failure scenarios have not been
  verified on kind / chaos yet.
- **Synchronous-replication live drill not finished** —
  `spec.postgresql.synchronous` renders `required/preferred` settings
  and wires standby `application_name`, but commit-block / continue
  behavior under fault injection and an RPO=0 measurement remain F05
  follow-up.
- **Protection outside the PVC mount is weak** — NFS, S3FS, and other
  PVs without strong exclusive-mount guarantees need StorageClass-level
  protection (potential future ADR).
- **Prometheus metrics not wired in yet** — `instance_election_status`,
  `instance_fencing_violations_total` are activated by P6
  (Observability).
- **Event recorder is a dummy** — `record.FakeRecorder` is in use; the
  real EventRecorder lands with P6.

## 9. Verification commands

```bash
# Unit + integration regression (envtest auto-boots)
make test

# Election package only
go test ./internal/instance/election/... -v -count=1

# Integration regression alone (lease transitions)
go test ./internal/instance/election/... -run TestIntegration -v
```

## 10. PVC-fencing operations (P2-T2)

### 10.1 Normal flow

- On leader shutdown, the instance manager attaches
  `postgres.keiailab.io/fenced=true` to its own PVC.
- A new Pod claims its PVC and tries to promote → after confirming no
  fence is present, proceeds.
- A zombie old Pod waking up sees its PVC fenced → promotion refused →
  `exit(2)` → CrashLoopBackOff (operator-intervention signal).

### 10.2 Removing the fence

```bash
# 1) Verify data integrity
kubectl exec -it <leader-pod> -- pg_controldata /var/lib/postgresql/data

# 2) Confirm the old leader Pod has terminated
kubectl get pod <old-pod> -o jsonpath='{.status.phase}'

# 3) After verification, clear the fence
kubectl label pvc data-<old-pod> postgres.keiailab.io/fenced-
```

### 10.3 Operational knob

- `--fencing-disabled`: development-only. Disabling in production
  invites split-brain.

### 10.4 RBAC

The instance-manager ServiceAccount needs `get` / `patch` permission on
PVCs in its own namespace (RFC 0003 Appendix A §7).

## 11. References

- [RFC-0007 — HA Election + PVC Fencing protocol (Draft)](../rfcs/0007-ha-election-and-fencing.md) (Appendix A: PVC fencing detail).
- Prior v0.x self-built-instance-manager rationale (history preserved in git).
- Code: `internal/instance/election/`, `internal/instance/fencing/`.
- Follow-up: F05 chaos E2E / live divergent-WAL `pg_rewind` drill.
