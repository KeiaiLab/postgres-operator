<p align="center">
  <a href="BRANDING.md">English</a> |
  <b>한국어</b> |
  <a href="BRANDING.ja.md">日本語</a> |
  <a href="BRANDING.zh.md">中文</a>
</p>

# Branding 가이드 — `postgres-operator` (한국어)

> 영문 원본: [BRANDING.md](BRANDING.md) — canonical / 정본

> keiailab operator family 의 시각 identity, voice, tone.

본 문서는 `postgres-operator` 의 브랜딩 결정에 대한 정본 (canonical) 참조다. README, 릴리스 노트, 마케팅 자료, 그리고 프로젝트를 대표하는 모든 third-party 커뮤니케이션에 적용된다.

## 1. Identity

**조직**: [keiailab](https://keiailab.com) — Kubernetes-native data platform operator (Apache-2.0, license-clean, vanilla upstream 호환).

**프로젝트**: `postgres-operator` — Kubernetes 용 Apache-2.0 PostgreSQL Operator — vanilla PG18+, license-clean, K8s-native auto-sharding 로드맵.

**Family**: keiailab operator family 의 5개 sister 프로젝트 중 하나. 3개 database operator 는 [`operator-commons`](https://github.com/keiailab/operator-commons) Go 라이브러리를 공유; [`forgewise`](https://github.com/keiailab/forgewise) 는 동일 family 의 개발 어시스턴트 도구.

| 프로젝트 | 역할 | 저장소 |
|---|---|---|
| `postgres-operator` | PostgreSQL 18+ operator | https://github.com/keiailab/postgres-operator |
| `mongodb-operator` | MongoDB 7.0+ operator | https://github.com/keiailab/mongodb-operator |
| `valkey-operator` | Valkey 8.0+ operator (Redis fork, BSD-3) | https://github.com/keiailab/valkey-operator |
| `operator-commons` | 공유 Go 라이브러리 | https://github.com/keiailab/operator-commons |
| `forgewise` | MCP-native 개발 어시스턴트 (GitLab Duo Enterprise 급) | https://github.com/keiailab/forgewise |

## 2. 로고 & Visual Asset

| Asset | URL | 용도 |
|---|---|---|
| Primary logo (SVG) | `https://keiailab.com/assets/logo.svg` | README 헤더, 슬라이드 |
| Mono mark | `https://keiailab.com/assets/mark.svg` | 파비콘, 소셜 카드 |
| Wordmark | `https://keiailab.com/assets/wordmark.svg` | Footer, dark 배경 |

**로고 배치**: README 상단 중앙, width 120px. 항상 https://keiailab.com 으로 링크.

**Clear space**: 로고 둘레 최소 padding = 로고 너비의 25%.

**금지 사항**:
- 로고 색상 변경
- drop shadow 또는 필터 추가
- 대비가 부족한 배경 위에 배치
- keiailab 브랜드 승인 없이 다른 로고와 결합

## 3. 컬러 팔레트

| 역할 | Hex | 용도 |
|---|---|---|
| Primary (keiailab teal) | `#0EA5A8` | 헤더, primary action, 링크 |
| Secondary (deep navy) | `#0F172A` | 어두운 배경, 코드 블록 |
| Accent (warm amber) | `#F59E0B` | 강조, 배지 accent |
| Neutral grey | `#64748B` | 밝은 배경의 본문 텍스트 |
| Background light | `#F8FAFC` | 문서 페이지 배경 |
| Background dark | `#020617` | 코드 에디터 테마, dark mode |

GitHub README 의 shield.io 배지는 위 hex 사용 권장.

## 4. 타이포그래피

- **헤딩**: System default (GitHub 의 default `-apple-system, BlinkMacSystemFont, Segoe UI, ...`)
- **본문**: 동일 (GitHub-native 정합)
- **코드**: `ui-monospace, SFMono-Regular, Consolas, ...` (GitHub 의 default monospace)

별도 webfont 사용 안 함 (GitHub README rendering 정합).

## 5. Voice & Tone

**대상**: Kubernetes 플랫폼 엔지니어 / DBA / SRE.

**Voice 원칙**:
- **직접적 (Direct)** — 가능하면 단락보다 bullet 사용
- **증거 기반 (Evidence-based)** — claim 은 벤치마크 / SLA / 링크 포함
- **벤더 중립 (Vendor-neutral)** — upstream (PostgreSQL, MongoDB, Valkey) 참조하되 third-party operator 를 embed/wrap 하지 않음
- **License-aware** — Apache-2.0 + BSD/MIT/PG-license 의존성만

**피할 표현**:
- 마케팅 최상급 표현 ("blazing fast", "revolutionary", "best-in-class")
- 모호한 비교 ("X-class quality") — *구체적 메트릭 또는 벤치마크로 qualifying*
- 로드맵의 시간 기반 데드라인 (대신 `standards/roadmap.md §1.1` — feature checklist 사용)

## 6. README 헤더 표준

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

## 7. README Footer 표준

모든 README + root-level .md 파일의 마지막에 다음 footer 부착 (Wave 3 표준):

```markdown
---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a> ·
  <a href="https://github.com/keiailab/forgewise">forgewise</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
```

## 8. Badges 표준 순서

README 의 shield.io badge 순서 (좌→우):

1. License (Apache-2.0)
2. Go Version (1.25+)
3. Database (예: PostgreSQL 18+ / MongoDB 7.0+ / Valkey 8.0+)
4. Kubernetes Version (1.26+)
5. Container Image (ghcr.io/keiailab)
6. Helm Chart (Chart.yaml version + Artifact Hub link)
7. OpenSSF Scorecard
8. GitHub Discussions

## 9. Discussions / Issues / PR Templates

- **Discussions**: `https://github.com/keiailab/postgres-operator/discussions` — 기능 아이디어, Q&A
- **Issues**: 버그 보고 + 유스 케이스를 포함한 구체적 기능 요청
- **PR template**: `.github/PULL_REQUEST_TEMPLATE.md` 표준 (사용자 시나리오 + 검증 명령 인용 의무, `standards/checklist.md §3`)

## 10. Social & External

- **웹사이트**: https://keiailab.com
- **GitHub Org**: https://github.com/keiailab
- **Artifact Hub** (Helm): https://artifacthub.io/packages/search?repo=keiailab-postgres-operator
- **GHCR** (Container): https://github.com/keiailab/postgres-operator/pkgs/container/postgres-operator

## 11. 라이선스 & 출처

- License: [Apache-2.0](LICENSE)
- Copyright: © 2026 keiailab contributors
- Third-party 출처: [NOTICE](NOTICE) 참조 (해당 시)

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a> ·
  <a href="https://github.com/keiailab/forgewise">forgewise</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
