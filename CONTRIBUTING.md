# Contributing to keiailab/postgres-operator

본 프로젝트에 기여해주셔서 감사합니다. 한국어/영어 모두 환영합니다.

## 기본 원칙

1. **테스트 없는 기능은 머지될 수 없습니다.** 모든 PR은 단위 테스트 또는 e2e 테스트를 동반해야 합니다.
2. **DCO sign-off 필수**: 모든 commit에 `Signed-off-by: Your Name <you@example.com>` 라인이 있어야 합니다(`git commit -s`).
3. **Apache 2.0 라이선스**에 동의하여 기여합니다.
4. **한국어/영어 commit message 모두 허용**. 본문은 가능하면 영어 권장(글로벌 협업).

## 시작하기

### 사전 요구사항
- Go 1.23+
- Docker (buildx 활성화)
- kubectl, kind, kubebuilder v4
- make
- [lefthook](https://github.com/evilmartians/lefthook) (pre-commit hook 관리)

### 로컬 개발
```bash
git clone https://github.com/keiailab/postgres-operator.git
cd postgres-operator
brew install lefthook   # 또는 go install github.com/evilmartians/lefthook@latest
lefthook install        # pre-commit / commit-msg / pre-push hook 설치
make test            # envtest + 단위 테스트
make lint            # golangci-lint
make e2e             # kind 기반 e2e (5~10분)
make build           # operator 바이너리 빌드
make docker-build    # 컨테이너 이미지 (buildx + masblue-builder)
```

## PR 절차

1. **이슈 먼저**: 새 기능은 먼저 이슈를 열어 maintainer와 합의합니다. 사소한 버그픽스/문서 수정은 바로 PR 가능.
2. **브랜치 명명**: `feat/<short>`, `fix/<short>`, `docs/<short>`, `refactor/<short>`.
3. **Commit message**: [Conventional Commits](https://www.conventionalcommits.org/) 권장 (`feat:`, `fix:`, `docs:`, `chore:`).
4. **Sign-off**: `git commit -s -m "feat: ..."`.
5. **PR 본문**: PR 템플릿을 채우고, 관련 이슈를 `Closes #N`으로 링크.
6. **CI 그린**: 모든 워크플로 통과 필수.
7. **리뷰**: CODEOWNERS 자동 할당. 일반 변경은 1 maintainer LGTM, 아키텍처 변경은 2명.

## 큰 변경은 RFC

CRD 추가/변경, 새 reconciler, 보안 모델 변경, 외부 의존성 추가 등 아키텍처 영향이 있는 변경은 [`docs/rfcs/`](docs/rfcs/)에 RFC를 먼저 제출합니다.

- 파일명: `NNNN-short-title.md`
- 7일 코멘트 윈도우
- 합의 후 Status를 `Accepted`로 변경하고 PR로 진입

## 테스트 정책

- **단위 테스트**: `internal/**/*_test.go`, envtest 활용
- **e2e**: `test/e2e/` Ginkgo + chainsaw, kind 클러스터에서 실행
- **chaos**: `test/chaos/`, chaos-mesh 시나리오 (Phase 3+)
- **bench**: `test/bench/` pgbench (Phase 6, 8)
- 라인 커버리지 ≥ 80% (codecov gating)

## 코드 스타일

- `gofmt`, `goimports` 적용 (자동: `make fmt`)
- `golangci-lint` 룰 위반 0건 (자동: `make lint`)
- 한국어 주석은 "왜"를 설명할 때만 사용. 자명한 "무엇"은 식별자로 표현.
- 외부 라이브러리/프레임워크 도입 전 [context7 MCP](https://github.com/upstash/context7) 또는 공식 문서로 최신 API 확인.

## 보안 취약점

[SECURITY.md](SECURITY.md) 참조. 비공개 채널을 통해 신고하시고, 공개 이슈로 올리지 마세요.

## 행동강령

[Contributor Covenant v2.1](CODE_OF_CONDUCT.md)을 따릅니다.

## 라이선스

기여하시는 모든 코드는 [Apache 2.0](LICENSE)으로 라이선스됩니다.
