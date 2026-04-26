# Maintainers

본 문서는 keiailab/postgres-operator의 의사결정 권한을 가진 메인테이너 명단을 관리합니다.

## 현재 메인테이너

| 이름/팀 | GitHub | 역할 | 담당 영역 |
|---|---|---|---|
| keiailab maintainers | [@keiailab/maintainers](https://github.com/orgs/keiailab/teams/maintainers) | Lead | 전체 |

GitHub team `@keiailab/maintainers`이 본 프로젝트의 모든 영역에 대한 머지/승인 권한을 보유합니다. 개인 메인테이너 추가는 아래 절차에 따라 이뤄집니다.

## 메인테이너 자격

다음 조건을 6개월 이상 만족한 contributor를 메인테이너로 추천할 수 있습니다:

- 머지된 PR ≥ 20건 (의미 있는 코드/문서 기여)
- 리뷰한 PR ≥ 30건 (건설적 피드백 동반)
- 본 프로젝트의 [GOVERNANCE.md](GOVERNANCE.md)와 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) 준수
- 한 개 이상의 핵심 영역(controller, instance manager, backup, citus integration, docs 등)에 깊은 이해

## 추가 절차

1. 기존 메인테이너 또는 candidate 본인이 issue 또는 RFC로 제안
2. `@keiailab/maintainers` 팀의 lazy consensus (7일 코멘트 윈도우)
3. 반대 없으면 GitHub team에 추가, MAINTAINERS.md 갱신 PR

## 비활성 메인테이너

연속 6개월간 활동이 없는 메인테이너는 emeritus로 이동합니다(권한 회수, 명예 명단 유지). 복귀는 신규 추가 절차와 동일.

## 영역별 담당 (CODEOWNERS와 동기화)

`/.github/CODEOWNERS`를 참조하세요. 디렉토리별 자동 리뷰어가 할당됩니다.

## Emeritus

(아직 없음)
