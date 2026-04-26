# Governance

본 문서는 keiailab/postgres-operator 프로젝트의 의사결정 절차를 정의합니다.

## 원칙

1. **개방성**: 모든 의사결정은 공개 채널(GitHub issue/PR/RFC)에서 이뤄집니다.
2. **최소 합의(Lazy Consensus)**: 일상적 변경은 반대 없으면 진행됩니다.
3. **명시적 합의(Explicit Consensus)**: 아키텍처 변경, CRD 변경, 보안 모델 변경, 라이선스 변경은 RFC 후 메인테이너 다수 승인.
4. **공동 책임**: 메인테이너는 코드 품질, 사용자 안전, 커뮤니티 건강에 대해 공동 책임을 집니다.

## 의사결정 분류

### 일상 변경 (Lazy Consensus)
- 버그 픽스, 문서 개선, 테스트 추가, 의존성 minor/patch 업그레이드, 리팩터링(공개 API 무변경)
- 절차: PR → 1명 이상 메인테이너 LGTM → 머지
- 시한: 별도 코멘트 윈도우 없음 (CI 그린이면 즉시 머지 가능)

### 중간 변경 (Explicit Consensus)
- 새 CRD 필드 추가, 새 reconciler, 의존성 major 업그레이드, 공개 API 변경
- 절차: 이슈로 제안 → 7일 코멘트 윈도우 → 메인테이너 다수 LGTM → 머지
- 거부 1건이 있을 시 메인테이너 회의에서 토론

### 아키텍처 변경 (RFC 필수)
- 새 컴포넌트 도입, 보안 모델 변경, 라이선스 변경, 호환성 깨는 변경
- 절차:
  1. `docs/rfcs/NNNN-title.md`에 RFC 제출
  2. 14일 코멘트 윈도우
  3. 메인테이너 2/3 이상 찬성
  4. RFC Status: `Draft → Accepted` 후 구현 PR 진입
- 거부 시 RFC Status: `Rejected`로 보존 (역사적 기록)

## 역할

### Contributor
누구나. PR/Issue 제출 가능.

### Reviewer
정기적으로 리뷰하는 contributor. CODEOWNERS에 등재 가능. 머지 권한 없음.

### Maintainer
[MAINTAINERS.md](MAINTAINERS.md) 참조. 머지/승인 권한 보유.

### Lead Maintainer
keiailab 조직 대표. 라이선스/거버넌스/보안 정책 최종 결정 권한.

## 메인테이너 회의

- 월 1회 (필요 시 임시 소집)
- 의사록은 `docs/meetings/`에 공개
- 안건: 분쟁 해결, RFC 토론, 로드맵 검토

## 분쟁 해결

1. PR/이슈 코멘트로 1차 토론
2. 미해결 시 메인테이너 회의 안건 등재
3. 메인테이너 다수결로 결정
4. Lead Maintainer가 tie-breaker

## 라이선스/지적 재산

- 모든 기여는 Apache 2.0 라이선스
- DCO sign-off 강제
- 라이선스 변경은 모든 contributor 동의 필요 (실질적으로 불변)

## 변경

본 문서는 메인테이너 2/3 이상 찬성으로 변경 가능.
