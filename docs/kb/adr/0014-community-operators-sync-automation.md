# ADR-0014: community-operators sync 자동화 (RFC 0002 예외 ③ 확장)

- Date: 2026-05-14
- Status: Accepted
- Authors: @eightynine01

## Context

postgres-operator 의 OLM bundle 을 k8s-operatorhub/community-operators upstream 에 sync. 현재 수동 — 라이브 evidence: community-operators 의 postgres-operator latest 1.4.0 ↔ keiailab/postgres-operator latest tag 0.3.0-alpha.17 (별 versioning scheme, drift 정확 측정은 maintainer 영역).

ADR-0013 'operatorhub-bundle-scaffold' bundle infrastructure 완비. 본 ADR 가 upstream sync 자동화 격차 해소.

## Decision

mongodb ADR-0027 + valkey ADR-0047 sister parity. `.github/workflows/release.yml` 의 `github-release` job 후속에 `sync-community-operators` job 신설.

조건:
- release tag push trigger
- prerelease tag (alpha/beta/rc) skip
- secret `COMMUNITY_OPERATORS_PAT` 의무
- AI 자동 머지 0 (외부 maintainer 책임)

## Consequences

긍정:
- bundle drift 영구 차단
- sister operator (mongodb ADR-0027, valkey ADR-0047) 일관 패턴

부정:
- RFC 0002 §2 충돌 (§7 예외 ③ 확장 해석으로 정당화)
- COMMUNITY_OPERATORS_PAT secret 회전 의무

## Refs

- RFC 0002 (no-github-actions)
- ADR-0013 (operatorhub-bundle-scaffold)
- sister: mongodb ADR-0027 (PR #169 MERGED), valkey ADR-0047 (PR #133)
