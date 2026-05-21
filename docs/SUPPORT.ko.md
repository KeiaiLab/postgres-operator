<p align="center">
  <a href="SUPPORT.md">English</a> |
  <b>한국어</b> |
  <a href="SUPPORT.ja.md">日本語</a> |
  <a href="SUPPORT.zh.md">中文</a>
</p>

# 지원 (한국어)

> 영문 원본: [SUPPORT.md](SUPPORT.md) — canonical / 정본

keiailab/postgres-operator 사용 중 문제가 발생하면 아래 채널을 이용하세요. 보안 취약점은 **여기 보고하지 마세요** — [SECURITY.md](SECURITY.md) 의 비공개 프로세스를 따르세요.

## 먼저 확인할 곳

- **README.md** — quickstart 및 핵심 CRD 표면 요약.
- **docs/operator-guide/** — runtime 운영 (`deployment.md`, `ha-election.md`, `pooler-monitoring.md`).
- **docs/releases/release-process.md** — 릴리스 및 업그레이드 절차.
- **CHANGELOG.md** — 릴리스별 변경 이력.

## 질문 / 토론

- **GitHub Discussions**:
  https://github.com/keiailab/postgres-operator/discussions
  사용 질문, 설계 근거, 운영 시나리오, RFC 드래프트 작성에 적합.

## 버그 보고 / 기능 요청

- **GitHub Issues**:
  https://github.com/keiailab/postgres-operator/issues
  `bug_report.yaml` / `feature_request.yaml` 템플릿을 사용하세요. 재현 단계, operator 버전, Kubernetes 버전, kind/cloud 환경, `kubectl get postgrescluster -oyaml` 출력, operator-manager Pod 로그 발췌를 포함하면 triage 가 훨씬 빠릅니다.

## Pull Request

[CONTRIBUTING.md](CONTRIBUTING.md) 참조: lefthook 설치, DCO 사인오프, 로컬 4-layer gate 통과 evidence (`pre-commit run --all-files`, `make test`, `make audit`) 를 PR 본문에 첨부하세요. PR 템플릿이 이를 안내합니다.

## 보안 취약점

[SECURITY.md](SECURITY.md) 의 비공개 채널을 통해 신고. 공개 issue 나 discussion 에 취약점을 작성하지 마세요.

## 상용 지원 / SLA

본 프로젝트는 Apache-2.0 오픈소스로, 공식 상용 지원은 없다. production 클러스터에 대한 사고 대응 SLA 가 필요하면 별도 컨설팅 engagement 가 필요할 수 있다 — `support@keiailab.io` 로 이메일 부탁.

## 응답 기대치

- Issues / Discussions 첫 응답: **3 영업일**.
- 보안 보고 첫 응답: **48 시간** (SECURITY.md 기준).
- Pull-request 리뷰: 메인테이너 가용 시 **5 영업일**.

위 수치는 메인테이너의 best-effort 목표이며 SLA 가 아니다.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
