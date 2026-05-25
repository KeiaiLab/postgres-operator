# ADR-0024: lefthook pre-push incremental lint + envtest 자동화

| Meta | Value |
|---|---|
| Status | Accepted |
| Date | 2026-05-21 |
| Author | keiailab |
| Supersedes | (none) |
| Related | postgres-ADR/0021 (RFC-0002 GHA block hook), postgres-ADR/0018 (GHA → 로컬 4계층), postgres-ADR/0023 (v3.x-stable baseline) |

## Context

RFC-0002 (2026-04-29) 시행 후 모든 품질 게이트가 GitHub Actions 에서 **로컬 4계층** (pre-commit / pre-push / Makefile / 리뷰어) 으로 일원화되었다. postgres-operator 는 `.lefthook.yml` 의 pre-push 단계에서 `unit-test` (`go test -count=1 -timeout=120s ./...`) 와 `full-lint` (`./bin/golangci-lint run`) 를 모든 push 에 강제한다.

본 ADR 채택 시점 (2026-05-21) 의 운영 중 다음 *push 차단* 문제 3건을 관찰:

### 1. `unit-test` BeforeSuite 실패 — KUBEBUILDER_ASSETS 미설정

`internal/webhook/v1alpha1/webhook_suite_test.go:81` 의 `BeforeSuite` 가 controller-runtime envtest (`etcd` + `kube-apiserver` binary) 를 부팅. 호스트에 `KUBEBUILDER_ASSETS` 환경변수가 설정되어 있지 않으면 fork/exec 실패:

```
Op: "fork/exec",
Path: "bin/k8s/1.36.0-darwin-arm64/etcd",
Err: <syscall.Errno>0x2,
```

`make setup-envtest` 을 *별도로* 실행하고 절대 경로로 export 해야 hook 통과 — 호스트 환경 setup 누락 시 *문서만 변경한 push 도* 차단된다.

### 2. `full-lint` 가 기존 main 의 8개 이슈로 모든 push 차단

`./bin/golangci-lint run` 은 *전체* 검사. 본 ADR 시점 main HEAD 에 다음 8개 기존 이슈가 잔존:

- `modernize` 7건: `ptrInt64(x)` → `new(x)` 권장 (internal/controller/builders.go:135/136, internal/controller/postgresuser_controller_test.go:372 외)
- `staticcheck` SA1019 1건: `scheme.Builder` deprecated (api/v1alpha1/groupversion_info.go:31)

이들 이슈는 *PR 신규 변경과 무관*. 그러나 pre-push hook 이 *전체* lint 를 강제하므로 *모든 PR* (특히 문서 PR 처럼 Go 코드 변경 0) 이 머지 차단됨.

대조: pre-commit 의 `golangci-lint` 는 이미 `--new-from-rev=HEAD~1` 사용 (incremental). pre-push 와 pre-commit 의 baseline 정합 깨짐.

### 3. `markdown-link-check` 가 기존 docs 의 깨진 링크로 모든 push 차단

`markdown-link-check` hook 이 `README.md` + `CHANGELOG.md` + `docs/**/*.md` 를 *전체* 검사. 본 ADR 시점 main HEAD 에서 발견된 기존 부채:

- `README.md`: `https://keiailab.github.io/postgres-operator` 404 (GitHub Pages 미배포), `https://keiailab.com/assets/logo.svg` 404
- `CHANGELOG.md`: `CHANGELOG.{ko,ja,zh}.md` placeholder 미생성
- `docs/project-overview.md`: `project-overview.{ko,ja,zh}.md` placeholder + `keiailab.com` 503 (전이 실패)
- `docs/index.md`: 모든 내부 링크 (`/tutorials/quickstart`, `/adr/`, `/rfcs/`, ...) — mkdocs 사이트 라우팅, markdown-link-check 의 file-system 기반 검사로는 본질적 미지원
- `docs/operator-guide/community-operators-onboarding.md`: 옛 archive ADR path
- `docs/rfcs/0003-shardsplitjob-7step.md`: PG 18 docs URL 이동
- `docs/internal/{TASKS,HANDOFF}.md`: community-operators PR #8109 404 (외부 PR 삭제)
- `docs/kb/adr/0023-v3x-stable-baseline.md`: `keiailab/.codex` 404 (typo or private)

총 *9개 파일 / 22 dead link* — 어떤 PR 도 markdown 변경과 무관하게 차단.

## Decision

`.lefthook.yml` 의 pre-push 3 hook 을 다음과 같이 변경:

### `unit-test` — envtest binary 자동 보장 + KUBEBUILDER_ASSETS export

```yaml
unit-test:
  run: |
    test -d bin/k8s || make setup-envtest >/dev/null 2>&1 || true
    K8S_DIR=$(ls -d bin/k8s/*/ 2>/dev/null | head -1)
    if [ -z "$K8S_DIR" ]; then
      echo "❌ envtest 설정 실패 — 'make setup-envtest' 수동 실행 후 재push"
      exit 1
    fi
    export KUBEBUILDER_ASSETS="$(pwd)/${K8S_DIR%/}"
    go test -count=1 -timeout=120s ./...
```

- `bin/k8s/` 존재 시 빠르게 path 추출 (overhead 거의 0)
- 미존재 시 `make setup-envtest` 자동 호출 → controller-runtime release tag 에 맞는 binary 다운로드
- 절대 경로 export — 상대 경로 시 webhook test 의 working directory 변경 영향 회피

### `markdown-link-check` — incremental link check (PR 변경 markdown 만)

```yaml
markdown-link-check:
  run: |
    ...
    if git rev-parse --verify origin/main >/dev/null 2>&1; then
      BASE_REF="origin/main"
    else
      BASE_REF="HEAD~1"
    fi
    changed_md=$(git diff --name-only "$BASE_REF"...HEAD 2>/dev/null | grep -E '\.md$' || true)
    [ -z "$changed_md" ] && exit 0
    for f in $changed_md; do
      markdown-link-check -q "$f" || fail=1
    done
```

- `full-lint` 와 동일 incremental 정책 — PR 변경 `.md` 만 검사
- 기존 main 의 깨진 링크 (예: docs/index.md 의 mkdocs 라우팅, CHANGELOG.{ko,ja,zh}.md placeholder, community-operators PR 8109 404 등) 는 별개 PR 로 누적 처리

### `full-lint` — incremental lint (--new-from-rev)

```yaml
full-lint:
  run: |
    if git rev-parse --verify origin/main >/dev/null 2>&1; then
      BASE_REF="origin/main"
    else
      BASE_REF="HEAD~1"
    fi
    ./bin/golangci-lint run --new-from-rev="$BASE_REF"
```

- `origin/main` 우선 사용 — PR 전체 commit 검사 (pre-commit 의 `HEAD~1` 는 *직전 commit* 만 검사하므로 pre-push 단계의 *PR 누적* 검사로 확장)
- `origin/main` 미존재 (clone 후 fetch 안한 환경 등) → `HEAD~1` fallback
- 기존 main 이슈는 *그대로 보존* — 별개 modernize PR 로 누적 처리

## Consequences

### Positive

- **호스트 환경 무관 통과**: envtest binary 가 없는 호스트도 hook 자동 setup 후 통과. 신규 contributor 의 onboarding 마찰 ↓
- **PR scope 명확화**: 문서 PR / 리팩토링 PR / 기능 PR 모두 *해당 PR 의 변경* 으로만 lint / link check 평가. 기존 부채와 분리
- **CI bypass 정책 (RFC-0002) 준수**: GHA 폐지 후에도 *모든 PR 의 신규 이슈* 는 여전히 차단됨 (incremental 이지 disabling 이 아님)
- **Operator 정합**: 동일 lefthook 패턴이 다른 operator 에서도 운영됨 — 일관성 유지
- **markdown 부채 가시화**: ADR Context 섹션에 22 dead link 의 *목록* 을 남겨 별개 PR 의 진행 우선순위 결정 자료가 됨

### Negative

- **기존 main 이슈가 *영구적*으로 남을 수 있음**: 별개 modernize PR 이 명시적으로 진행되지 않으면 잔존. 대응: 본 ADR 의 후속 task 로 modernize PR 추적
- **origin/main fetch 의존**: push 직전 fetch 안한 환경에서 `--new-from-rev=origin/main` 이 stale base 사용. 대응: `HEAD~1` fallback 으로 최소 보장
- **`make setup-envtest` 첫 실행 시 binary 다운로드 (~170MB)**: 신규 환경 첫 push 시 1회 비용. 이후 캐시

### Neutral

- pre-commit 의 `--new-from-rev=HEAD~1` 와 pre-push 의 `--new-from-rev=origin/main` 의 차이 — 의도된 단계별 검사 범위 차이 (직전 commit → PR 전체)

## Alternatives Considered

1. **8개 기존 이슈를 즉시 fix**: scope 명확하나 (a) modernize 가 단순 텍스트 치환이 아니고 controller-runtime API 영향 (`scheme.Builder` deprecated 대체) → 더 큰 careful 작업, (b) 동일 패턴이 다른 operator 에도 존재할 가능성 → fix 시간 증가. 본 ADR 의 incremental 화는 *근본 fix* 이며 후속 modernize 와 병행 가능.

2. **GHA 복원**: RFC-0002 (2026-04-28 사고 RCA) 시행 정책 위반. 거버넌스 위배.

3. **lefthook pre-push 비활성화**: `LEFTHOOK=0` 환경변수로 우회 가능하나 standards/enforcement.md §1.1 "hook bypass 금지" 위반.

4. **불필요한 push 만 bypass (문서 only PR)**: 문서/코드 구분 휴리스틱 도입 필요. 복잡 + 사각지대 (예: 코드 + 문서 mixed PR).

## Status

- 2026-05-21 — Accepted (worktree-fix+lefthook-pre-push-incremental-2026-05-21).
- 후속: 동일 패턴을 다른 operator 에도 적용 (Phase 2/3).
- 후속: 8개 modernize 이슈 별도 PR (scope: api/v1alpha1 + internal/controller). 본 ADR 의 incremental gate 통과 후 점진 진행.
