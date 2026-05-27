# CNPG → Keiailab Postgres Operator: Gap Analysis

> **Language**: [English](GAP-ANALYSIS.md) | [한국어](GAP-ANALYSIS.ko.md) | [日本語](GAP-ANALYSIS.ja.md) | [中文](GAP-ANALYSIS.zh.md)

## Executive Summary

This document compares CloudNativePG (CNPG) v1.29 — currently running in the keiailab
production cluster — with keiailab/postgres-operator v0.4.0-beta.1. The goal is to
identify feature gaps that must be closed before replacing CNPG in production.

**Key findings:**
- 12 of 16 core components are fully implemented in keiailab operator
- 5 P0 (must-have) gaps block production replacement
- 3 additional P1 gaps are needed for operational confidence
- Estimated effort: 8 sprints to reach production parity
- Deployment readiness score: 7.5/10 (dev-ready today, prod needs hardening)

---

## CNPG Feature Matrix

Based on CloudNativePG v1.29 (CNCF Sandbox project, Apache 2.0 license).

| Category | Status | Key Capabilities |
|----------|--------|-----------------|
| Cluster Lifecycle | GA | Declarative create/delete/scale, rolling minor updates, 3 major upgrade methods |
| High Availability | GA | Quorum-based automatic failover (v1.28+), RPO=0, switchover, I/O fencing |
| Backup & Recovery | GA | Barman Cloud Plugin (CNPG-I gRPC), S3/GCS/Azure, Volume Snapshots, full PITR |
| Replication | GA | Streaming + logical (Publication/Subscription CRD), Distributed Topology, delayed replicas |
| Connection Pooling | GA | Pooler CRD managing PgBouncer with independent lifecycle |
| Monitoring | GA | Built-in Prometheus exporter (:9187), custom queries via ConfigMap/Secret |
| Security | GA | TLS v1.3 default, cert-manager, Pod Security Standards Restricted, non-root |
| Storage | GA | Separate PVCs for PGDATA/WAL/tablespaces, VolumeSnapshot support |
| Configuration | GA | Declarative postgresql.conf/pg_hba.conf, ALTER SYSTEM disabled |
| Database Management | GA | Declarative Database/Role/Extension/Schema/FDW/Server management |
| Maintenance | GA | Hibernation, I/O fencing; VACUUM/REINDEX delegated to PG built-ins |
| Multi-tenancy | Limited | Single operator per K8s cluster; strict isolation = separate clusters |
| Plugins | GA | CNPG-I gRPC interface, ImageVolume (v1.29) for extension binaries |

---

## Keiailab Operator Capabilities

All assessments based on source code audit (2026-05-25).

| Component | Status | Evidence |
|-----------|--------|----------|
| Shard Management | ✅ Complete | StatefulSet + ConfigMap + headless Service per shard |
| Failover / HA | ✅ Complete | Lease-based election, detection, promotion plan |
| Backup Framework | ✅ Complete | Plugin interface, BackupJob CR, sidecar/job modes |
| Scheduled Backups | ✅ Complete | 6-field cron, ConcurrencyForbid policy |
| Connection Pooling | ✅ Complete | PgBouncer Deployment, config reload, pause/resume |
| Database Management | ✅ Complete | CREATE/DROP DATABASE via pod/exec, reclaim policies |
| User/Role Management | ✅ Complete | CREATE/DROP ROLE, password Secret lifecycle |
| Replica Clusters | ✅ Complete | pg_basebackup bootstrap, SSL credential projection |
| Image Catalogs | ✅ Complete | Dynamic resolution, CNPG-compatible surface |
| Metrics | ✅ Complete | 6 Prometheus metrics |
| PDB | ✅ Complete | Auto-creation, minAvailable=members-1 |
| Hibernation | ✅ Complete | Annotation-driven scale-to-zero, PVC retention |
| TLS | 🟡 Partial | Certificate CR generation; STS mounts pending |
| Router | 🟡 Partial | Resources created; image placeholder |
| Backup Store | ❌ Missing | Plugin interface only; zero store code |
| WAL Archiving | ✅ Complete | archive_command wired to pgbackrest archive-push (PR #127) |
| PITR (full) | ❌ Missing | targetTime only; no object store restore |
| Retention Cleanup | ✅ Complete | enforceRetention() deletes old BackupJobs exceeding keepFull (PR #130) |
| Config Hot-Reload | ✅ Complete | pg_reload_conf() on ConfigMap change (PR #126) |
| Volume Snapshots | ❌ Missing | No VolumeSnapshot integration |
| Major Upgrade | ❌ Missing | No pg_upgrade orchestration |

---

## Gap Matrix

### P0 — Production Blockers

| # | Gap | CNPG Equivalent | Effort | Sprint |
|---|-----|-----------------|--------|--------|
| 1 | WAL archiving | ~~barmanObjectStore~~ | ✅ Done (PR #127) | S3 |
| 2 | PITR from object store | spec.backup.recovery | 1 week | S4 |
| 3 | TLS Phase 3 (mounts + ssl=on) | Default behavior | 3 days | S1 |
| 4 | postgresql.conf hot-reload | ~~pg_reload_conf()~~ | ✅ Done (PR #126) | S2 |
| 5 | Backup retention cleanup | ~~retentionPolicy~~ | ✅ Done (PR #130) | S5 |

### P1 — Operational Confidence

| # | Gap | CNPG Equivalent | Effort | Sprint |
|---|-----|-----------------|--------|--------|
| 6 | Switchover | ~~cnpg promote~~ | ✅ Done (PR #129) | S5 |
| 7 | Fencing | cnpg fencing on/off | 3 days | S6 |
| 8 | Synchronous replication | syncReplicas | 2 days | S6 |
| 9 | pg_hba.conf reload | ~~Config reload~~ | ✅ Done (PR #126) | S2 |
| 10 | Custom PG parameters | spec.postgresql.parameters | 2 days | S2 |

### P2 — Enhancement (not blocking)

| # | Gap | Notes |
|---|-----|-------|
| 11 | Volume Snapshots | Backup acceleration |
| 12 | Major Upgrade | pg_upgrade in-place |
| 13 | Delayed Replicas | recovery_min_apply_delay |
| 14 | Tablespace PVCs | Multi-PVC support |
| 15 | Custom Monitoring | Exporter enhancement |

---

## MVP Definition

**Minimum to replace CNPG:** P0 (5) + P1 items 6, 7, 10 = **8 features**

Enables: TLS, WAL archiving, PITR, config reload, switchover, fencing, retention, custom params.

---

## Migration Roadmap

| Sprint | Focus | E2E Verification |
|--------|-------|------------------|
| S1 | TLS Phase 3 | psql sslmode=verify-full success |
| S2 | Config hot-reload | SHOW reflects spec change without restart |
| S3 | WAL archive + backup | WAL segments in object store |
| S4 | PITR restore | Timestamp recovery + data verify |
| S5 | Retention + switchover | Cleanup enforced; primary switches |
| S6 | Fencing + sync repl | Split-brain simulation passes |
| S7 | Integration test | Full e2e on keiailab cluster |
| S8 | CNPG replacement | Zero-downtime migration |

---

## Deployment Readiness (7.5/10)

| Area | Status |
|------|--------|
| Container images | ✅ Multi-stage distroless, non-root |
| Helm chart | ✅ Deployable for basic scenarios |
| Controllers | ✅ 6 controllers, 10 CRDs, plugin arch |
| E2E tests | ✅ Failover, PITR, chaos (kind) |
| Webhook/cert | ⚠️ Disabled by default |
| PG image pin | ⚠️ Floating tag needs sha256 |
| Resource limits | ⚠️ 128Mi too low for >5 shards |
| Infra testing | ❌ Kind-only |

---

## Next Steps

1. Begin S1 (TLS Phase 3) — lowest effort, highest impact
2. Set up object store for S3/S4 backup sprints
3. Schedule keiailab cluster access for S7
