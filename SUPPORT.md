# Support

keiailab/postgres-operator 를 사용 중 문제가 생기면 다음 채널을 이용해주세요.
보안 취약점은 본 문서가 아니라 [SECURITY.md](SECURITY.md) 에 따라 비공개로 신고해주세요.

## 우선 확인할 곳

- **README.md** — quickstart 와 핵심 CRD 표면 요약.
- **docs/operator-guide/** — `deployment.md`, `cross-validation-cnpg.md`,
  `ha-election.md`, `pooler-monitoring.md` 등 운영 가이드.
- **docs/releases/release-process.md** — release/upgrade 절차.
- **CHANGELOG.md** — 버전별 변경 이력.

## 질문 / 토론

- **GitHub Discussions**:
  https://github.com/keiailab/postgres-operator/discussions
  사용법, 설계 결정 배경, 운영 시나리오 질문, RFC 초안 논의에 사용해주세요.

## 버그 / 기능 요청

- **GitHub Issues**:
  https://github.com/keiailab/postgres-operator/issues
  `bug_report.yaml` / `feature_request.yaml` 템플릿을 사용해주세요. 재현 절차,
  operator 버전, K8s 버전, kind/cloud 환경, `kubectl get postgrescluster -oyaml`
  출력, operator manager Pod log 발췌가 있으면 분석이 빠릅니다.

## Pull Request

- [CONTRIBUTING.md](CONTRIBUTING.md) 의 lefthook 설치 + DCO sign-off + 4 계층
  로컬 게이트 통과 증거(`pre-commit run --all-files`, `make test`, `make audit`)를
  PR 본문에 첨부해주세요. PR 템플릿이 자동으로 안내합니다.

## 보안 취약점

본 문서가 아니라 [SECURITY.md](SECURITY.md) 의 비공개 신고 절차를 따라주세요.
공개 이슈/Discussion 에 취약점을 적지 마세요.

## 상용 지원 / SLA

본 프로젝트는 Apache-2.0 OSS 이며 공식 상용 지원은 제공하지 않습니다. 운영
중인 cluster 의 incident response SLA 가 필요하면 별도 컨설팅 계약이 필요할
수 있습니다 — `support@keiailab.io` 로 문의해주세요.

## 응답 기대치

- Issue/Discussion 1차 응답: 영업일 기준 3 일.
- 보안 신고 1차 응답: 48 시간 (SECURITY.md 정책).
- Pull Request 리뷰: maintainer 가용성에 따라 영업일 기준 5 일 이내.

상기 일정은 maintainers 의 best-effort 이며 SLA 가 아닙니다.
