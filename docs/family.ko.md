<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# keiailab operator family

> 공유 기반 위에 구축된 네 개의 자매 Kubernetes operator — `operator-commons` (Go 라이브러리) + Helm partials + Apache-2.0 스택.

본 문서는 **`postgres-operator`** 저장소에서 보고 계시며, 전체 family 의 표준 cross-link 페이지입니다.

## Family 개요

| 프로젝트 | 데이터베이스 | 상태 | 저장소 |
|---|---|---|---|
| **`postgres-operator`** | PostgreSQL 18+ | active | https://github.com/keiailab/postgres-operator |
| **`mongodb-operator`** | MongoDB 7.0+ | active | https://github.com/keiailab/mongodb-operator |
| **`valkey-operator`** | Valkey 8.0+ (Redis fork, BSD-3) | active | https://github.com/keiailab/valkey-operator |
| **`operator-commons`** | 공유 Go 라이브러리 | v0.7.0 | https://github.com/keiailab/operator-commons |

## 공유 사항

네 프로젝트 모두 동일한 운영 원칙으로 수렴합니다:

- **Apache-2.0** — SSPL 없음, SaaS 표면 copyleft 없음
- **`operator-commons`** 공유 Go 라이브러리 (v0.7.0+) — finalizer, labels, status sugars, security context builders, NetworkPolicy / ServiceMonitor partials
- **Helm chart skeleton** — RFC-0027 `default` falsy-toggle 예방, RFC-0026 component-keyed values, cycle 26 hardening 6 markers (priorityClassName / lifecycle / SA / minReadySeconds / automount / revisionHistoryLimit)
- **OLM bundle parity** — scorecard v1alpha3 6-test matrix
- **i18n** — README + 11 canonical docs 가 English / 한국어 / 日本語 / 中文 (cleanup supercycle 2026-05-21 Wave 4)

## 하지 않는 것

- ❌ **upstream operator 포함 또는 wrap** (PGO, CloudNativePG, MongoDB Community Operator, Sentinel) — license-clean, copyleft 의무 0
- ❌ **release gate 용 GitHub Actions** — 로컬 4-layer + GitLab CI L5 (RFC-0002, RFC-0043)
- ❌ **시간 기반 roadmap deadline** — 기능 체크리스트 + 완성도 % (`standards/roadmap.md §1.1`)
- ❌ **Bitnami chart / image** — registry deprecation risk, Broadcom 인수 (ADR-0136 / ADR-0057)

## 시작점

| 작업 | 진입점 |
|---|---|
| Kubernetes 에 `postgres-operator` 배포 | [README.md](../README.md) Quickstart 섹션 |
| 아키텍처 이해 | [ARCHITECTURE.md](../ARCHITECTURE.md) |
| 이슈 또는 기능 요청 | https://github.com/keiailab/postgres-operator/issues |
| 설계 또는 roadmap 논의 | https://github.com/keiailab/postgres-operator/discussions |
| 코드 기여 | [CONTRIBUTING.md](../CONTRIBUTING.md) |
| 보안 이슈 보고 | [SECURITY.md](../SECURITY.md) |
| 브랜드 / 보이스 학습 | [BRANDING.md](../BRANDING.md) |
| Adopter 추적 / 사용 사례 | [ADOPTERS.md](../ADOPTERS.md) |
| 메인테이너 확인 | [MAINTAINERS.md](../MAINTAINERS.md) |
| Governance 모델 검토 | [GOVERNANCE.md](../GOVERNANCE.md) |
| 다가오는 작업 추적 | [ROADMAP.md](../ROADMAP.md) |

## Cross-family 호환성 (operator-commons)

세 데이터베이스 operator 모두 `github.com/keiailab/operator-commons` 를 같은 버전 (현재 `v0.7.0+`) 으로 import 합니다:

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

`operator-commons` 의 breaking change 는 세 데이터베이스 operator 모두에서 동기화 된 bump 필요 — supercycle Wave 5 의 `make cross-validation` target 으로 검증.

## i18n

본 페이지 (및 모든 표준 프로젝트 문서) 는 네 가지 언어로 제공됩니다:

- [English](family.md) (canonical)
- **한국어** (본 file)
- [日本語](family.ja.md)
- [中文](family.zh.md)

기술적 내용은 영문 버전이 권위 있으며, 다국어 버전은 동일 결정을 native 표현으로 반영합니다.

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
