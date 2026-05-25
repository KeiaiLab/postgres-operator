<p align="center">
  <b>English</b> |
  <a href="BRANDING.ko.md">한국어</a> |
  <a href="BRANDING.ja.md">日本語</a> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# Branding Guide — `postgres-operator`

> Visual identity, voice, and tone for `postgres-operator`.

This document is the canonical reference for `postgres-operator` branding decisions. It applies to the README, release notes, marketing material, and any third-party communication that represents the project.

## 1. Identity

**Organization**: [keiailab](https://keiailab.com) — Kubernetes-native data platform (Apache-2.0, license-clean, vanilla-upstream compatible).

**Project**: `postgres-operator` — Apache-2.0 PostgreSQL Operator for Kubernetes — vanilla PG18+, license-clean, K8s-native auto-sharding roadmap.

## 2. Logo & Visual Assets

| Asset | URL | Usage |
|---|---|---|
| Primary logo (SVG) | `https://keiailab.com/assets/logo.svg` | README header, slides |
| Mono mark | `https://keiailab.com/assets/mark.svg` | Favicon, social cards |
| Wordmark | `https://keiailab.com/assets/wordmark.svg` | Footer, dark backgrounds |

**Logo placement**: Top-center of README, width 120px. Always link to https://keiailab.com.

**Clear space**: Minimum padding around logo = 25% of logo width.

**Do not**:
- Recolor the logo
- Add drop shadows or filters
- Place on backgrounds with insufficient contrast
- Combine with other logos without keiailab brand approval

## 3. Color Palette

| Role | Hex | Usage |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | Headers, primary actions, links |
| Secondary (deep navy) | `#0F172A` | Dark backgrounds, code blocks |
| Accent (warm amber) | `#F59E0B` | Highlights, badge accents |
| Neutral grey | `#64748B` | Body text on light backgrounds |
| Background light | `#F8FAFC` | Documentation page background |
| Background dark | `#020617` | Code editor theme, dark mode |

GitHub README 의 shield.io badge 는 위 hex 사용 권장.

## 4. Typography

- **Headings**: System default (GitHub 의 default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **Body**: 동일 (GitHub-native 정합)
- **Code**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub 의 default monospace)

별도 webfont 사용 안 함 (GitHub README rendering 정합).

## 5. Voice & Tone

**Audience**: Kubernetes platform engineers / DBAs / SRE.

**Voice principles**:
- **Direct** — bullet-point over paragraph where possible
- **Evidence-based** — claims include benchmark / SLA / link
- **Vendor-neutral** — reference upstream PostgreSQL but do not embed/wrap third-party operators
- **License-aware** — Apache-2.0 + BSD/MIT/PG-license dependencies only

**Avoid**:
- Marketing superlatives ("blazing fast", "revolutionary", "best-in-class")
- Vague comparisons ("X-class quality")  — *qualify with specific metric or benchmark*
- Time-based deadlines in roadmap (use `standards/roadmap.md §1.1` — feature checklist instead)

## 6. README Header Standard

모든 README 의 첫 문단은 다음 형식 (Wave 3 표준):

```markdown
<p align="center">
  <img src="https://keiailab.com/assets/logo.svg" alt="keiailab" width="120"/>
</p>

# postgres-operator

> **Apache-2.0 PostgreSQL Operator for Kubernetes — vanilla PG18+, license-clean, K8s-native auto-sharding roadmap**

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"/></a>
  <!-- 기존 shield.io badges 유지 + 정합 -->
</p>

<p align="center">
  <b>English</b> |
  <a href="README.ko.md">한국어</a> |
  <a href="README.ja.md">日本語</a> |
  <a href="README.zh.md">中文</a>
</p>
```

## 7. README Footer Standard

모든 README + 정책 .md 파일의 마지막에 다음 footer 부착:

```markdown
---

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges 표준 순서

README 의 shield.io badge 순서 (좌→우):

1. License (Apache-2.0)
2. Go Version (1.25+)
3. Database (PostgreSQL 18+)
4. Kubernetes Version (1.26+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/postgres-operator/discussions` — feature ideas, Q&A
- **Issues**: bug reports + concrete feature requests with use case
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md` 표준 (사용자 시나리오 + 검증 명령 인용 의무, `standards/checklist.md §3`)

## 10. Social & External

- **Website**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-postgres-operator
- **GHCR** (Container): https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator

## 11. License & Attribution

- License: [Apache-2.0](../LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party attributions: see [NOTICE](../NOTICE) (if applicable)

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
