<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# keiailab operator family

> Four sister Kubernetes operators built on shared foundations — `operator-commons` (Go library) + Helm partials + Apache-2.0 stack.

You are reading this from the **`postgres-operator`** repository. This page is the canonical cross-link for the entire family.

## Family overview

| Project | Database | Status | Repository |
|---|---|---|---|
| **`postgres-operator`** | PostgreSQL 18+ | active | https://github.com/keiailab/postgres-operator |
| **`mongodb-operator`** | MongoDB 7.0+ | active | https://github.com/keiailab/mongodb-operator |
| **`valkey-operator`** | Valkey 8.0+ (Redis fork, BSD-3) | active | https://github.com/keiailab/valkey-operator |
| **`operator-commons`** | Shared Go library | v0.7.0 | https://github.com/keiailab/operator-commons |

## What we share

All four projects converge on the same operational primitives:

- **Apache-2.0** end-to-end — no SSPL, no copyleft on SaaS surface
- **`operator-commons`** shared Go library (v0.7.0+) — finalizer, labels, status sugars, security context builders, NetworkPolicy / ServiceMonitor partials
- **Helm chart skeleton** — RFC-0027 `default` falsy-toggle prevention, RFC-0026 component-keyed values, cycle 26 hardening 6 markers (priorityClassName / lifecycle / SA / minReadySeconds / automount / revisionHistoryLimit)
- **OLM bundle parity** — scorecard v1alpha3 6-test matrix
- **i18n** — README + 11 canonical docs in English / 한국어 / 日本語 / 中文 (Wave 4 of cleanup supercycle 2026-05-21)

## What we do NOT do

- ❌ **Embed or wrap upstream operators** (PGO, CloudNativePG, MongoDB Community Operator, Sentinel) — license-clean, no copyleft obligations
- ❌ **GitHub Actions for release gates** — local 4-layer + GitLab CI L5 (see RFC-0002, RFC-0043)
- ❌ **Time-based roadmap deadlines** — feature checklist + completion percentages (see `standards/roadmap.md §1.1`)
- ❌ **Bitnami chart / image** — registry deprecation risk, Broadcom acquisition (see ADR-0136 / ADR-0057)

## Where to start

| Task | Entry point |
|---|---|
| Deploy `postgres-operator` on Kubernetes | [README.md](../README.md) Quickstart section |
| Read the architecture | [ARCHITECTURE.md](../ARCHITECTURE.md) |
| File an issue or feature request | https://github.com/keiailab/postgres-operator/issues |
| Discuss design or roadmap | https://github.com/keiailab/postgres-operator/discussions |
| Contribute code | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| Report a security issue | [SECURITY.md](../SECURITY.md) |
| Learn the brand / voice | [BRANDING.md](../BRANDING.md) |
| Track adopters / who uses this | [ADOPTERS.md](../ADOPTERS.md) |
| Find maintainers | [MAINTAINERS.md](../MAINTAINERS.md) |
| Review governance model | [GOVERNANCE.md](../GOVERNANCE.md) |
| Check upcoming work | [ROADMAP.md](../ROADMAP.md) |

## Cross-family compatibility (operator-commons)

All three database operators import `github.com/keiailab/operator-commons` at the matching version (currently `v0.7.0+`):

```go
import (
    "github.com/keiailab/operator-commons/pkg/version"
    "github.com/keiailab/operator-commons/pkg/security"
    "github.com/keiailab/operator-commons/pkg/labels"
    "github.com/keiailab/operator-commons/pkg/monitoring"
    "github.com/keiailab/operator-commons/pkg/finalizer"
    "github.com/keiailab/operator-commons/pkg/status"
)
```

A breaking change in `operator-commons` requires a synchronized bump across all three database operators — verified by the `make cross-validation` target in Wave 5 of the supercycle.

## i18n

This page (and all canonical project docs) is available in four languages:

- **English** (canonical, this file)
- [한국어](family.ko.md)
- [日本語](family.ja.md)
- [中文](family.zh.md)

When in doubt, the English version is authoritative for technical content; localized versions reflect the same decisions in native phrasing.

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
