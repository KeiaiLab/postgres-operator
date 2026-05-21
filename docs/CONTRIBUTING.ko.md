<p align="center">
  <a href="CONTRIBUTING.md">English</a> |
  <b>한국어</b> |
  <a href="CONTRIBUTING.ja.md">日本語</a> |
  <a href="CONTRIBUTING.zh.md">中文</a>
</p>

# keiailab/postgres-operator 기여 가이드 (한국어)

> 영문 원본: [CONTRIBUTING.md](CONTRIBUTING.md) — canonical / 정본

기여해 주셔서 감사합니다! issue, discussion, PR 본문 작성에는 한국어와 영어 모두 환영하지만, 프로젝트 문서 자체는 영어로 유지된다.

## 기본 원칙

1. **테스트 없이 머지되는 기능은 없다.** 모든 PR 은 unit test 또는 e2e test 를 포함해야 한다.
2. **DCO 사인오프 필수.** 모든 commit 은 `Signed-off-by: Your Name <you@example.com>` trailer 를 포함해야 한다 (`git commit -s` 사용).
3. **Apache 2.0**: 기여함으로써 본 프로젝트 라이선스로 작업물을 라이선스하는 데 동의한다.
4. **Commit message 언어**: 한국어 또는 영어 가능; 크로스팀 협업을 위해 영어 권장.

## 시작하기

### 사전 요구

- Go 1.23+
- Docker (buildx 활성화)
- kubectl, kind, kubebuilder v4
- make
- [lefthook](https://github.com/evilmartians/lefthook) (pre-commit hook 매니저)

### 로컬 개발

```bash
git clone https://github.com/keiailab/postgres-operator.git
cd postgres-operator
brew install lefthook    # 또는: go install github.com/evilmartians/lefthook@latest
make hooks-install       # `lefthook install` 의 wrapper (pre-commit / commit-msg / pre-push)
make hooks-check         # hook 이 설치되었는지 확인 (DCO + Conventional Commits 강제)
make test                # envtest + unit test
make lint                # golangci-lint
make e2e                 # kind 기반 e2e (5~10 분)
make build               # operator 바이너리 빌드
make docker-build        # 컨테이너 이미지 빌드 (docker buildx 기본 빌더)
```

## PR 워크플로우

1. **신규 기능은 먼저 issue 를 열어** 메인테이너와 방향을 정렬하세요. 사소한 버그 픽스 / 문서 수정은 바로 PR 가능.
2. **브랜치 명명**: `feat/<short>`, `fix/<short>`, `docs/<short>`, `refactor/<short>`.
3. **Commit message**: [Conventional Commits](https://www.conventionalcommits.org/) 권장 (`feat:`, `fix:`, `docs:`, `chore:`).
4. **사인오프**: `git commit -s -m "feat: ..."`.
5. **PR 본문**: 템플릿을 채우고 관련 issue 를 `Closes #N` 으로 링크.
6. **로컬 게이트 통과 필수**: `pre-commit run --all-files` 와 `make lint test validate` 모두 통과해야 함 (RFC-0002 에 따라 GitHub Actions 금지).
7. **리뷰**: CODEOWNERS 가 자동 할당. 일반 변경은 메인테이너 1 명의 LGTM, 아키텍처 변경은 2 명 필요.

## 큰 변경에는 RFC 필요

아키텍처 변경 — CRD 추가/변경, 신규 reconciler 도입, 보안 모델 변경, 외부 의존성 추가 — 은 [`docs/rfcs/`](rfcs/) 에 RFC 가 먼저 필요.

- 파일명: `NNNN-short-title.md`.
- 7 일 comment window.
- consensus 후 상태를 `Accepted` 로 전환하고 PR 진행.

## 테스트 정책

- **Unit test**: `internal/**/*_test.go`, 적절한 곳에 envtest 사용.
- **e2e**: `test/e2e/`, Ginkgo + chainsaw on kind cluster.
- **chaos**: `test/chaos/`, chaos-mesh 시나리오 (Phase 3+).
- **bench**: `test/bench/`, pgbench (Phase 6, 8).
- 라인 커버리지 ≥ 80% (codecov-gated).

## 코드 스타일

- `gofmt` / `goimports` (`make fmt` 실행).
- `golangci-lint` 위반 0 건 (`make lint` 실행).
- 주석은 **이유** 를 설명하고, **무엇** 은 이름으로 — 이름이 의도를 담아야 한다.
- 신규 외부 라이브러리/프레임워크 도입 전, 공식 문서 또는 [context7 MCP](https://github.com/upstash/context7) 로 현재 API 를 재확인.

## 보안 취약점

[SECURITY.md](SECURITY.md) 참조, 비공개 채널 사용. 취약점을 공개 issue 로 작성하지 말 것.

## 행동 강령

[Contributor Covenant v2.1](CODE_OF_CONDUCT.md) 을 따른다.

## 라이선스

모든 기여는 [Apache 2.0](../LICENSE) 으로 라이선스된다.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
