<p align="center">
  <a href="SECURITY.md">English</a> |
  <b>한국어</b> |
  <a href="SECURITY.ja.md">日本語</a> |
  <a href="SECURITY.zh.md">中文</a>
</p>

# 보안 정책 (한국어)

> 영문 원본: [SECURITY.md](SECURITY.md) — canonical / 정본

## 지원 버전

| Version | 보안 패치 지원 |
|---|---|
| 최신 minor (v0.x) | ✅ |
| 직전 minor | ✅ (60일) |
| 그 외 | ❌ |

long-term-support (LTS) 정책은 v1.0.0 GA 출시 이후 별도 공지된다.

## 취약점 신고

**보안 취약점은 공개 issue 로 열지 마세요.** 비공개 채널을 사용:

- 이메일: `security@keiailab.io`
- PGP key fingerprint: `89A4 0947 6828 CB99 2338  C378 651E 51AF 520B CB78`
  (keiailab Helm chart 서명 키 — gh-pages 의 `artifacthub-repo.yml` 에 게시된 fingerprint 와 동일).

## 응답 프로세스

1. 접수 후 **48 시간 이내 수신 확인**.
2. **7 일 이내 영향/심각도 평가** (CVSS v3.1).
3. **합의된 패치 일정** 공유 (보통 14~30 일).
4. 공개 disclosure 까지 **90 일 embargo** (협상 가능).
5. **CVE 할당** 및 GitHub Security Advisory 게시.
6. **신고자 credit** (선택).

## Disclosure 정책

- Advisory 는 패치 릴리스와 함께 게시.
- 필요 시 CVE 할당.
- 신고자 credit (선택, opt-in).
- 영향 받는 사용자에게 마이그레이션 가이드 제공.

## 안전한 운영을 위한 권장 사항

본 operator 운영 시:

- TLS 요구: `network.tls.mode=required`.
- 권장: `network.networkPolicy.enabled=true` 로 Network Policy 활성화.
- SCRAM-SHA-256 인증 강제 (기본값).
- Secret rotation 을 위해 cert-manager 통합 사용.
- 지원 매트릭스의 PostgreSQL 최신 패치 버전 추적.
- 컨테이너 이미지를 `cosign verify` 로 검증.

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
