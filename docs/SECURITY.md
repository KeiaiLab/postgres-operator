<p align="center">
  <b>English</b> |
  <a href="SECURITY.ko.md">한국어</a> |
  <a href="SECURITY.ja.md">日本語</a> |
  <a href="SECURITY.zh.md">中文</a>
</p>

# Security Policy

## Supported versions

| Version | Security patch support |
|---|---|
| Latest minor (v0.x) | ✅ |
| Previous minor | ✅ (60 days) |
| Older | ❌ |

A long-term-support (LTS) policy will be announced separately after the
v1.0.0 GA release.

## Reporting a vulnerability

**Please do not open public issues for security vulnerabilities.** Use the
private channel:

- Email: `security@keiailab.io`
- PGP key fingerprint: `89A4 0947 6828 CB99 2338  C378 651E 51AF 520B CB78`
  (the keiailab Helm chart signing key — identical to the fingerprint in
  the `artifacthub-repo.yml` published on gh-pages).

## Response process

1. **Acknowledgement within 48 hours** of receipt.
2. **Impact/severity assessment within 7 days** (CVSS v3.1).
3. **Agreed patch timeline** shared (typically 14–30 days).
4. **90-day embargo** before public disclosure (negotiable).
5. **CVE assignment** and GitHub Security Advisory publication.
6. **Reporter credit** (optional).

## Disclosure policy

- Advisory is published alongside the patch release.
- CVE assignment when required.
- Reporter credit (optional, opt-in).
- Migration guidance provided to impacted users.

## Recommendations for secure operation

When running this operator:

- Require TLS: `network.tls.mode=required`.
- Recommended: enable Network Policy with `network.networkPolicy.enabled=true`.
- Enforce SCRAM-SHA-256 authentication (default).
- Use cert-manager integration for Secret rotation.
- Track the latest patch version of PostgreSQL on the supported matrix.
- Verify container images with `cosign verify`.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">MIT</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
