<p align="center">
  <b>English</b> |
  <a href="SUPPORT.ko.md">한국어</a> |
  <a href="SUPPORT.ja.md">日本語</a> |
  <a href="SUPPORT.zh.md">中文</a>
</p>

# Support

If you run into problems while using keiailab/postgres-operator, please use
the channels below. **Do not** file security vulnerabilities here — follow
the private process in [SECURITY.md](SECURITY.md) instead.

## Start here

- **README.md** — quickstart and a summary of the core CRD surface.
- **docs/operator-guide/** — runtime operations (`deployment.md`,
  `cross-validation-cnpg.md`, `ha-election.md`, `pooler-monitoring.md`).
- **docs/releases/release-process.md** — release and upgrade procedures.
- **CHANGELOG.md** — per-release change history.

## Questions / discussion

- **GitHub Discussions**:
  https://github.com/keiailab/postgres-operator/discussions
  Best for usage questions, design rationale, operational scenarios, and
  RFC drafting.

## Bug reports / feature requests

- **GitHub Issues**:
  https://github.com/keiailab/postgres-operator/issues
  Please use the `bug_report.yaml` / `feature_request.yaml` templates.
  Including reproduction steps, the operator version, Kubernetes version,
  kind/cloud environment, the output of `kubectl get postgrescluster
  -oyaml`, and excerpts from the operator-manager Pod log makes triage
  much faster.

## Pull requests

Follow [CONTRIBUTING.md](CONTRIBUTING.md): install lefthook, sign off your
commits (DCO), and attach evidence that the local 4-layer gate passes
(`pre-commit run --all-files`, `make test`, `make audit`) in the PR body.
The PR template walks you through this.

## Security vulnerabilities

Report through the private channel in [SECURITY.md](SECURITY.md). Do not
write up vulnerabilities in public issues or discussions.

## Commercial support / SLA

This is an Apache-2.0 open-source project; there is no official commercial
support. If you need an incident-response SLA for a production cluster, a
separate consulting engagement may be required — please email
`support@keiailab.io`.

## Response expectations

- First response on Issues / Discussions: **3 business days**.
- First response on a security report: **48 hours** (per SECURITY.md).
- Pull-request review: **5 business days** subject to maintainer
  availability.

These are best-effort targets from the maintainers and are not SLAs.

---

<p align="center">
  <b>keiailab operator family</b><br/>
  <a href="https://github.com/keiailab/postgres-operator">postgres-operator</a> ·
  <a href="https://github.com/keiailab/mongodb-operator">mongodb-operator</a> ·
  <a href="https://github.com/keiailab/valkey-operator">valkey-operator</a> ·
  <a href="https://github.com/keiailab/operator-commons">operator-commons</a>
</p>

<p align="center">
  © 2026 keiailab · <a href="LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
