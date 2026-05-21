<p align="center">
  <a href="GOVERNANCE.md">English</a> |
  <b>한국어</b> |
  <a href="GOVERNANCE.ja.md">日本語</a> |
  <a href="GOVERNANCE.zh.md">中文</a>
</p>

# 거버넌스 (한국어)

> 영문 원본: [GOVERNANCE.md](GOVERNANCE.md) — canonical / 정본

본 문서는 keiailab/postgres-operator 프로젝트에서 의사결정이 이루어지는 방식을 정의한다.

## 원칙

1. **개방성 (Open)**: 모든 의사결정은 공개 채널 (GitHub issues / PRs / RFCs) 에서 진행한다.
2. **Lazy consensus**: 일상적 변경은 이의 제기가 없으면 진행한다.
3. **Explicit consensus**: 아키텍처 변경, CRD 변경, 보안 모델 변경, 라이선스 변경은 RFC 와 메인테이너 **2/3 supermajority** 가 필요. 더 작은 RFC (단일 컴포넌트 / 도구 채택 / 정책 강화) 는 **단순 다수 (>50%)** 필요. GOVERNANCE 자체의 변경 ("Amendments" 참조) 은 항상 2/3 supermajority 필요.
4. **공동 책임 (Shared accountability)**: 메인테이너는 코드 품질, 사용자 안전, 커뮤니티 건강성에 대해 공동 책임을 진다.

## 결정 클래스

### Routine 변경 (lazy consensus)

- 버그 픽스, 문서 개선, 테스트 추가, 의존성 minor/patch 업그레이드, 그리고 public API 를 변경하지 않는 리팩토링.
- 프로세스: PR → 메인테이너 1 명 이상 LGTM → 머지.
- Window: 별도 요구 없음 (로컬 게이트 통과 즉시 머지 가능; RFC-0002 에 따라 GitHub Actions 사용 안 함 — pre-commit / pre-push hook 과 Makefile 이 게이트 역할).

### Medium 변경 (explicit consensus)

- 신규 CRD 필드, 신규 reconciler, 의존성 major 업그레이드, public API 변경.
- 프로세스: issue 로 제안 → 7 일 comment window → 메인테이너 다수 LGTM → 머지.
- 단일 반대 시 메인테이너 미팅으로 escalation.

### Architectural 변경 (RFC 필수)

- 신규 컴포넌트 도입, 보안 모델 변경, 라이선스 변경, 또는 backward-incompatible 변경.
- 프로세스:
  1. `docs/rfcs/NNNN-title.md` 에 RFC 제출.
  2. 14 일 comment window.
  3. 메인테이너 ≥ 2/3 찬성.
  4. RFC 상태 `Draft → Accepted` 전환 후 구현 PR 오픈.
- 거부된 RFC 는 `Rejected` 상태로 유지 (historical context 보존).

## 역할

### Contributor

누구나. PR 과 issue 제출 가능.

### Reviewer

정기적으로 리뷰하는 contributor. CODEOWNERS 에 추가될 수 있음. 머지 권한 없음.

### Maintainer

[MAINTAINERS.md](MAINTAINERS.md) 참조. 머지 / 승인 권한 보유.

### Lead maintainer

keiailab 조직 대표. 라이선스, 거버넌스, 보안 정책에 대한 최종 결정 권한.

## 메인테이너 미팅

- 월간 cadence (필요 시 ad-hoc 세션).
- 회의록은 `docs/meetings/` 에 게시.
- 안건: 분쟁 해결, RFC 토론, 로드맵 리뷰.

## 분쟁 해결

1. 먼저 PR/issue 코멘트에서 토론.
2. 미해결 시 메인테이너 미팅 안건으로 추가.
3. 메인테이너 다수 투표로 결정.
4. 동수일 경우 lead maintainer 가 캐스팅 보트.

## 라이선스 / 지적 재산권

- 모든 기여는 Apache 2.0 으로 라이선스된다.
- DCO 사인오프 필수.
- 라이선스 변경은 모든 contributor 의 만장일치 동의가 필요 (사실상 불변).

## Amendments (수정)

본 문서는 메인테이너 ≥ 2/3 supermajority 가 있을 때만 수정될 수 있다.

---

<p align="center">
  © 2026 keiailab · <a href="../LICENSE">Apache-2.0</a> · <a href="https://keiailab.com">keiailab.com</a>
</p>
