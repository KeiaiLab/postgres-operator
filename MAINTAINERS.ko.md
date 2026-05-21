<p align="center">
  <a href="MAINTAINERS.md">English</a> |
  <b>한국어</b> |
  <a href="MAINTAINERS.ja.md">日本語</a> |
  <a href="MAINTAINERS.zh.md">中文</a>
</p>

# 메인테이너 (한국어)

> 영문 원본: [MAINTAINERS.md](MAINTAINERS.md) — canonical / 정본

본 문서는 keiailab/postgres-operator 의 메인테이너 명단을 추적한다. 메인테이너는 프로젝트에 대한 의사결정 및 머지 권한을 보유한다.

## 현재 메인테이너

| 이름 / 팀 | GitHub | 역할 | 영역 |
|---|---|---|---|
| keiailab maintainers | [@keiailab/maintainers](https://github.com/orgs/keiailab/teams/maintainers) | Lead | 전체 프로젝트 |

GitHub team `@keiailab/maintainers` 는 모든 영역에서 머지 및 승인 권한을 보유한다. 개별 메인테이너는 아래 프로세스에 따라 추가된다.

## 메인테이너 자격

다음 기준을 *최소 6 개월* 충족한 contributor 가 메인테이너로 추천될 수 있다:

- ≥ 20 건의 머지된 PR (의미 있는 코드 또는 문서 기여).
- ≥ 30 건의 PR 리뷰 (건설적 피드백 포함).
- 본 프로젝트의 [GOVERNANCE.md](GOVERNANCE.md) 및 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) 준수.
- 최소 한 가지 핵심 영역 (controller, instance manager, backup, sharding, docs 등) 에 대한 깊은 이해.

## 메인테이너 추가

1. 기존 메인테이너 또는 후보자 본인이 제안서를 연다 (issue 또는 RFC).
2. `@keiailab/maintainers` 팀이 7 일 comment window 동안 lazy consensus 를 적용.
3. 이의 제기가 없으면 신규 메인테이너를 GitHub team 에 추가하고 PR 로 MAINTAINERS.md 를 갱신.

## 비활성 메인테이너

6 개월 연속 비활성 상태인 메인테이너는 emeritus 상태로 전환된다 (권한은 회수, 명단은 emeritus roster 에 보존). 복귀는 신규 추가와 동일한 절차를 따른다.

## 영역 소유 (CODEOWNERS 와 동기)

`/.github/CODEOWNERS` 참조. 디렉토리별 자동 리뷰어가 해당 파일로부터 할당된다.

## Emeritus

(아직 없음)

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
