# RFC-0007: HA Leader Election + PVC Fencing protocol

- **Status**: Accepted (2026-05-11 — retrospective document for completed implementation artifacts)
- **Date**: 2026-05-11
- **Authors**: @keiailab/maintainers (republished — the previous 0003 slot was reassigned to shardsplitjob; this RFC is the canonical slot)
- **Refs**: prior v0.x self-built-instance-manager decision (history preserved in git), operations guide [`docs/operator-guide/ha-election.md`](../operator-guide/ha-election.md)
- **Supersedes**: the portion that previously lived as a temporary SSOT in `docs/operator-guide/ha-election.md`

## 1. Summary

Implements PostgreSQL HA's leader election and split-brain prevention via the following two mechanisms:

1. **Leader Election** — based on a K8s `coordination.k8s.io/v1` Lease (no external DCS).
2. **PVC Fencing** — a PVC-label-based fencing protocol that blocks split-brain by an old leader Pod.

This RFC unifies the rationale, parameters, and operational interface of both decisions as a *single SSOT*.

## 2. Context

Several PostgreSQL HA designs rely on an external HA agent + external DCS (etcd / Consul / ZooKeeper). However, for this operator:

- **External DCS dependency**: operating an etcd cluster is a burden; an outage takes the PG HA down with it
- **Foreign runtime**: an external HA agent sidecar in a Go-based operator → increases image size / security surface
- **Dual sources of truth**: divergence risk between state written by the external agent to etcd vs. state written by the operator to K8s
- **Kubernetes-native HA precedent**: using the K8s API itself as the DCS has a production-grade operational track record

Since P2-T2 (2026-04-28), PVC fencing has been enabled, blocking the split-brain scenario of an old leader Pod.

## 3. Decision

### 3.1 Leader Election

Each `PostgresCluster`'s instance manager (`cmd/instance`) enters the following election mode at boot:

| Mode | Use case | CLI flag |
|---|---|---|
| `real` (default) | members ≥ 2 — uses a K8s `coordination.k8s.io/v1` Lease | `--election=real` |
| `null` | single-member scenario — bypass election (development/testing) | `--election=null` |

Lease parameters (`internal/instance/election/lease.go`):

| Parameter | Value | Meaning |
|---|---|---|
| `DefaultLeaseDuration` | **15s** | maximum time the leader is considered alive without renewing the lease |
| `DefaultRenewDeadline` | **10s** | deadline for the leader to attempt lease renewal (must be shorter than Duration) |
| `DefaultRetryPeriod` | follower retry interval | period followers retry to acquire the lease |

Tests (`election_test.go`, `integration_test.go`) use short values (2s/1s/200ms) to shorten regression time.

### 3.2 PVC Fencing (P2-T2)

To prevent split-brain, apply **PVC-label-based fencing**. In scenario C (an old leader Pod returns alive after a partition recovery):

1. The new leader immediately attaches a fence label to the old leader's PVC (`fencing.go` `MarkFenced`)
2. At startup, the old leader Pod checks the fence label on its own PVC (`IsFenced`)
3. If the fence is set, refuse startup + Pod terminating

Operational knob: `--fencing-disabled` (development-only; forbidden in production).

### 3.3 CRD Status is authoritative for topology

`PostgresCluster.status.topology` holds the current RS primary list. Single source of truth for K8s and PG state.

## 4. Consequences

### 4.1 Positive
- **Operational simplification**: etcd dependency removed — the K8s control plane guarantees consensus
- **Image / security**: a single Go static binary, distroless base, zero external runtime
- **Single source of truth**: only CRD status + K8s lease are authoritative — duplication removed
- **Kubernetes-native HA precedent**: production-grade record with the same model
- **Single-owner topology updates**: the instance manager owns updating the new primary's endpoint without coordinating with an external agent

### 4.2 Negative / Trade-offs
- **Depends on K8s API server availability**: an API server outage blocks election
  - Mitigation: the K8s control plane is a precondition for running the cluster. Assumed to be in the same availability class as PG. + PVC fencing covers split-brain.
- **No external HA agent CLI ecosystem**: lacks an operator-friendly CLI
  - Mitigation: provide `kubectl postgres` or our own CLI in Phase 13. For ordinary operations, `kubectl` + CR is sufficient.
- **Permanent maintenance of our own instance manager**: license / maintenance risk
  - Mitigation: implement from K8s primitives under Apache-2.0. Core logic is in the hundreds of lines.

## 5. Alternatives Considered

### 5.1 External HA agent + etcd
Reason for rejection: external DCS operational burden, foreign runtime, dual source of truth — see §2 Context.

### 5.2 Multi-component external HA suite
Reason for rejection: 3+ separate components (keeper / sentinel / proxy class) → increased operational complexity. Not a K8s-friendly model.

### 5.3 K8s Operator + rely only on the default StatefulSet behavior
Reason for rejection: cannot prevent split-brain. No data-consistency guarantee during failover.

## 6. Appendix A — PVC Fencing protocol details (P2-T2, Implemented 2026-04-28)

To be absorbed into this appendix from the operations guide `docs/operator-guide/ha-election.md §10` (at the Draft → Accepted transition).

### 6.1 Label schema

Fence labels attached to the PVC:
- `postgres-operator.keiailab.io/fenced`: `"true"` / not present
- `postgres-operator.keiailab.io/fenced-at`: RFC3339 timestamp
- `postgres-operator.keiailab.io/fenced-by`: name of the new leader Pod

### 6.2 Attach timing

Right after the new leader's OnStartedLeading call (election success transition), attach fence labels to the PVCs of all *non-leader* Pods.

### 6.3 RBAC requirements

The instance manager ServiceAccount needs `get` / `patch` permissions on PVCs in its own namespace.

### 6.4 Recovery procedure

Recovering a fenced Pod is a manual operator task:
1. Validate PVC data integrity (`pg_controldata`, whether WAL replay is possible)
2. Integrity OK → remove the fence label → restart the Pod
3. Integrity NG → discard the PVC + add a new replica

## 7. Implementation Status

- [x] Lease-based leader election (`internal/instance/election/`)
- [x] PVC fencing (`internal/instance/fencing/`, P2-T2 active 2026-04-28)
- [x] `--fencing-disabled` development knob
- [x] Failover **detection + promotion** (`internal/controller/failover` pure-decision
  functions + `executeClusterPromotion`), executed inside the PostgresCluster reconcile
  loop and single-active-gated by the **controller-runtime manager lease** (`--leader-elect`,
  default true). Includes PVC pre-fencing, split-brain reseed (#220), promotion-candidate
  readiness guards, and debounce.
- [ ] `kubectl postgres failover` CLI command (Phase 13)
- [ ] **Dedicated failover-controller lease (P2-T3)** — `internal/controller/failover/lease.go`
  exists and is unit-tested (leader single-ness + handoff) but is **not yet wired into
  production**. It must NOT naively gate the reconcile-loop failover (that holder may differ
  from the manager-lease holder → deadlock). A proper P2-T3 first extracts failover into a
  leader-election-agnostic runnable, then gates that runnable on this lease.
- [ ] `pg_rewind` integration (P2-T4)

## 8. References

- Code: `internal/instance/election/`, `internal/instance/fencing/`, `cmd/instance/`
- Tests: `election_test.go`, `integration_test.go`, `fencing_test.go`
- Operations: `docs/operator-guide/ha-election.md`
- Prior v0.x self-built-instance-manager rationale (history preserved in git).
- Follow-up: P2-T3 (failover controller), P2-T4 (pg_rewind), Phase 13 (kubectl postgres CLI)
