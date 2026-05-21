<p align="center">
  <b>English</b> |
  <a href="CONTRIBUTING.ko.md">한국어</a> |
  <a href="CONTRIBUTING.ja.md">日本語</a> |
  <a href="CONTRIBUTING.zh.md">中文</a>
</p>

# Contributing to keiailab/postgres-operator

Thanks for contributing! Both English and Korean text are welcome in
issues, discussions, and PR bodies; project documentation itself is
maintained in English.

## Ground rules

1. **No feature lands without tests.** Every PR must include unit tests or
   e2e tests.
2. **DCO sign-off is mandatory.** Every commit must carry a
   `Signed-off-by: Your Name <you@example.com>` trailer (use
   `git commit -s`).
3. **Apache 2.0**: by contributing you agree to license your work under the
   project license.
4. **Commit message language**: Korean or English is fine; English is
   preferred for cross-team collaboration.

## Getting started

### Prerequisites

- Go 1.23+
- Docker (buildx enabled)
- kubectl, kind, kubebuilder v4
- make
- [lefthook](https://github.com/evilmartians/lefthook) (pre-commit hook manager)

### Local development

```bash
git clone https://github.com/keiailab/postgres-operator.git
cd postgres-operator
brew install lefthook    # or: go install github.com/evilmartians/lefthook@latest
make hooks-install       # wrapper around `lefthook install` (pre-commit / commit-msg / pre-push)
make hooks-check         # confirm hooks are wired (DCO + Conventional Commits enforcement)
make test                # envtest + unit tests
make lint                # golangci-lint
make e2e                 # kind-based e2e (5–10 minutes)
make build               # build the operator binary
make docker-build        # build the container image (docker buildx default builder)
```

## PR workflow

1. **Open an issue first** for new features so a maintainer can align with
   you on direction. Trivial bug fixes / documentation tweaks may go
   straight to a PR.
2. **Branch naming**: `feat/<short>`, `fix/<short>`, `docs/<short>`,
   `refactor/<short>`.
3. **Commit message**: [Conventional Commits](https://www.conventionalcommits.org/)
   is recommended (`feat:`, `fix:`, `docs:`, `chore:`).
4. **Sign-off**: `git commit -s -m "feat: ..."`.
5. **PR body**: fill in the template and link the related issue with
   `Closes #N`.
6. **Local gate must be green**: `pre-commit run --all-files` plus
   `make lint test validate` must all pass (GitHub Actions is forbidden
   per RFC-0002).
7. **Review**: CODEOWNERS is auto-assigned. Normal changes need 1 maintainer
   LGTM; architectural changes need 2.

## RFCs for substantial changes

Architectural changes — adding/changing a CRD, introducing a new
reconciler, changing the security model, adding an external dependency —
require an RFC in [`rfcs/`](rfcs/) first.

- Filename: `NNNN-short-title.md`.
- 7-day comment window.
- After consensus, flip the status to `Accepted` and proceed with the PR.

## Testing policy

- **Unit tests**: `internal/**/*_test.go`, using envtest where appropriate.
- **e2e**: `test/e2e/` with Ginkgo + chainsaw on a kind cluster.
- **chaos**: `test/chaos/` with chaos-mesh scenarios (Phase 3+).
- **bench**: `test/bench/` with pgbench (Phase 6, 8).
- Line coverage ≥ 80% (codecov-gated).

## Code style

- `gofmt` / `goimports` (run `make fmt`).
- 0 `golangci-lint` violations (run `make lint`).
- Use comments to explain **why**, not **what** — names should carry
  intent.
- Before introducing a new external library/framework, double-check the
  current API against the official docs or
  [context7 MCP](https://github.com/upstash/context7).

## Security vulnerabilities

See [SECURITY.md](SECURITY.md) and use the private channel. Do not file
vulnerabilities as public issues.

## Code of conduct

We follow [Contributor Covenant v2.1](CODE_OF_CONDUCT.md).

## License

All contributions are licensed under [Apache 2.0](../LICENSE).

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
