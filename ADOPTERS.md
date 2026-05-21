<p align="center">
  <b>English</b> |
  <a href="ADOPTERS.ko.md">한국어</a> |
  <a href="ADOPTERS.ja.md">日本語</a> |
  <a href="ADOPTERS.zh.md">中文</a>
</p>

# Adopters of postgres-operator

This is the *public* list of organizations and projects that use
`keiailab/postgres-operator` in production or evaluation environments.
Self-registration is welcome — please open a PR to add a row.

> The operator is currently in **0.3.0-alpha**. For production deployments,
> consult the SLA guide (ROADMAP.md) first.

## Production users

Users running postgres-operator with a *production-grade SLA*.

| User | Component | Usage pattern | First version | Current version | Listed on |
|---|---|---|---|---|---|
| _none yet (alpha stage)_ | — | Will be added after the G1 milestone (single-shard production). | — | — | — |

## Evaluators

Users running the operator in PoC / evaluation / Day-0 environments.

| User | Component | Stage | Listed on |
|---|---|---|---|
| **platform-data** ([keiailab](https://github.com/keiailab)) | PostgresCluster (single-shard, PG18) | Day-0 deployment. PG18 failover smoke passed with RTO 21 s. HA replicas / backup-restore drill not yet passed — needs ROADMAP G1 before production. | 2026-05-07 |

## How to add yourself

Open a PR and add one row to the table above:

```markdown
| **<organization / project>** ([profile](<URL>)) | <component + topology> | <stage: Day-0 / G1 / G2 / G3> | <listed date YYYY-MM-DD> |
```

If you prefer a private or anonymized entry, contact us via the SECURITY.md
channel and a maintainer will add an *organization-anonymized* row.

## ROADMAP gates

The production-transition stages follow the G1–G4 milestones defined in
ROADMAP.md:

- **G1** — Single-shard production (HA replica + failover drill +
  backup/restore/PITR + upgrade rollback).
- **G2** — Native multi-shard (router + auto-split + cross-shard
  transactions).
- **G3** — pgBackRest integration GA.
- **G4** — chaos-mesh validation + multiple organization adopters.

## CNCF Sandbox reference

This ADOPTERS list is also used as the public reference that satisfies the
CNCF graduation criterion: "≥ 1 public adopter (or evaluator with stated
intent)".

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
