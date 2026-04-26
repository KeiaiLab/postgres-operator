# Security Policy

## 지원 버전

| 버전 | 보안 패치 지원 |
|---|---|
| 최신 minor (v0.x) | ✅ |
| 직전 minor | ✅ (60일) |
| 그 이전 | ❌ |

v1.0.0 GA 이후에는 LTS 정책을 별도 공지합니다.

## 취약점 신고

**공개 이슈로 보안 취약점을 보고하지 마세요.** 비공개 채널로 신고해주세요:

- 이메일: `security@keiailab.io`
- PGP 키: (TODO: 키 게시 후 fingerprint 명시)

## 응답 절차

1. **48시간 내 접수 확인** 회신
2. **7일 내 영향도/심각도 평가** (CVSS v3.1)
3. **합의된 패치 일정** 공유 (보통 14~30일)
4. **90일 비공개 윈도우** 후 공개 (조정 가능)
5. **CVE 발급** 및 GitHub Security Advisory 게시
6. **공로자 명시** (원하시면)

## 공개 정책

- 패치 릴리스와 동시에 advisory 공개
- CVE 발급 (필요한 경우)
- 신고자 credit (옵션)
- 영향받은 사용자에게 마이그레이션 가이드 제공

## 안전한 운영을 위한 권장사항

본 오퍼레이터를 운영할 때:

- TLS는 `network.tls.mode=required` 필수
- Network Policy는 `network.networkPolicy.enabled=true` 권장
- SCRAM-SHA-256 인증 강제 (디폴트)
- Secret rotation은 cert-manager 통합 사용
- PostgreSQL/Citus는 항상 매트릭스의 최신 패치 버전 사용
- 컨테이너 이미지는 `cosign verify`로 서명 검증
