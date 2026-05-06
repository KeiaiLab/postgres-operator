# ADR-0003: 외부 의존 라이선스 정책 (AGPL/BUSL/CSL/SSPL 영구 금지)

- Date: 2026-05-02
- Status: Accepted
- Authors: @phil

## Context

본 operator 는 Apache 2.0 으로 배포되며, 사용자는 *라이선스 청정성* 을 최우선 가치로 명시했다 (2026-05-02 결정). 이전 0.2.0-alpha 단계에서는 Citus (AGPLv3) 를 별도 plugin chart 로 격리하는 ADR-0010 방향이 검토되었으나, 격리만으로는 supply chain·법무 검토·고객 컴플라이언스 (특히 SaaS 호스팅 고객) 부담을 완전히 제거하지 못한다는 판단으로 폐기되었다. 동시에 CockroachDB (BUSL/CSL), MongoDB drivers (SSPL), Elastic (SSPL) 등 *source-available* 라이선스 군 역시 OSS 정의 위배 + 재배포 제약 + 클라우드 사용 제약으로 거부 대상이다. 본 ADR 은 이러한 결정을 영구 정책으로 고정하고 자동 게이트로 강제한다.

## Decision

외부 OSS 의존은 **(허용 라이선스 ∩ API 안정성)** 두 조건을 모두 충족하는 경우에만 채택하고, 그 외 모든 의존을 *영구 금지* 한다.

핵심 매개변수:

- **허용 라이선스 화이트리스트**: BSD-2-Clause, BSD-3-Clause, Apache-2.0, MIT, PostgreSQL License (PGL), ISC, MPL-2.0 (file-level copyleft 한정).
- **금지 라이선스 블랙리스트**: AGPL (모든 버전), BUSL (Business Source License), CSL (Cockroach Community License), SSPL (Server Side Public License), Commons Clause 부착 license, Elastic License (모든 버전), Confluent Community License.
- **API 안정성 조건**: 의존 대상 프로젝트가 v1.0.0 이상의 stability commitment 를 명시했거나, *문서화된 deprecation 정책* (최소 1 minor 사전 통지) 을 가진 경우.
- **소스 차용 정책**: 금지 라이선스 프로젝트의 *논문·블로그·문서* 는 학습·차용 가능하지만, *코드 1줄도 복사·번역·porting 하지 않는다*. AGPL 대상 프로젝트의 README 캡처조차 본 repo 에 포함 금지.
- **구체 허용 예시**:
  - `pg_query_go` (PostgreSQL License) — SQL parser
  - `pgBackRest` (BSD-2-Clause) — backup wrapper
  - `controller-runtime` (Apache-2.0) — operator 골격
  - `KEDA` (Apache-2.0) — autoscaler 트리거
  - `cert-manager` (Apache-2.0) — TLS
  - `prometheus-operator` (Apache-2.0) — monitoring
- **구체 거부 예시**:
  - Citus (AGPLv3) — 라이선스 위배
  - CockroachDB (BUSL → CSL) — 라이선스 위배
  - MongoDB drivers (SSPL) — 라이선스 위배
  - Patroni (MIT) — 라이선스는 호환되나 *DCS 모델 충돌* 로 API 영역에서 거부 (별도 정당화 필요, 본 ADR §Decision 의 두 번째 조건 "API 안정성"이 아닌 *아키텍처 양립성* 영역).
- **자동 게이트**:
  - `scripts/check-license-policy.sh` — `go list -m -json all` 결과를 파싱하여 화이트리스트 외 라이선스 발견 시 exit 1.
  - lefthook L2 pre-push hook 으로 강제.
  - PR 본문 `로컬 게이트 PASS` 블록에 `check-license-policy: PASS` 증거 의무.
- **예외 절차**: 신규 의존 추가는 PR 본문에 라이선스 + URL + 채택 사유 명시. 화이트리스트 외 라이선스는 PR 차단 (override 불가, ADR 우회 금지).

## Consequences

긍정:

- 라이선스 사고 0건 보장 — supply chain 감사·고객 법무 검토에서 즉시 통과.
- SaaS 호스팅 사용자가 본 operator 를 임베드해도 추가 라이선스 의무 없음 (Apache 2.0 그대로).
- ArtifactHub `artifacthub.io/license` annotation 단순화.
- 기여자 onboarding 시 "이 의존을 추가해도 되는가" 판단을 ADR 한 장으로 종료.

부정:

- Citus 의 8년 분산 SQL 자산 (vindex, scatter-gather, online resharding) 을 직접 활용 불가 → §3 자체 구현 약 6년 비용 (PHASE 로드맵 P0~P7).
- CockroachDB 의 검증된 분산 트랜잭션 패턴 (transactional KV, parallel commits) 도 코드 차용 불가 → 논문·문서 차용으로만 학습.
- 일부 강력한 도구 (예: Elastic 검색 통합) 가 필요할 때 대안 부재.

트레이드오프:

- 6년 자체 구현 비용을 *라이선스 청정성 + API 안정성* 가치와 교환. 1인 maintainer 가 OSS contributor 모집을 통해 P2 이후 부담 완화 가능.
- 거부 예시 중 Patroni 처럼 *라이선스는 호환되나 아키텍처 충돌* 케이스는 별도 ADR 으로 정당화. 본 ADR 은 *라이선스 영역만* 다룬다.

## Alternatives Considered

| 대안 | 거절 사유 |
|---|---|
| (a) AGPL 격리 plugin chart 유지 (원 ADR-0010 방향) | 사용자 명시 거부 (2026-05-02). 격리 효과 한계 + 고객 법무 검토 부담 잔존. |
| (b) Dual license (Apache + AGPL — operator 자체) | operator 가 dual license 여도 의존 라이선스 문제는 미해결. 본 ADR 의 issue 와 무관. |
| (c) Source-available license 일부 허용 (BUSL with fair-use clause) | OSS 정의 위배. 클라우드 사용 제약 → SaaS 사용자 배제. |
| (d) 라이선스 case-by-case 판단 (정책 없음) | 1인 maintainer 가 매 의존마다 법무 검토 비용 부담. ADR 폭발. |
| (e) GPL-2.0 / GPL-3.0 도 허용 | network-use 조항은 없으나, file-level copyleft 가 operator 핵심 모듈에 전파될 위험. 명확성을 위해 MPL-2.0 (file-level) 까지만 허용. |
| (f) 화이트리스트 자동화 없이 정책만 문서화 | 1년 내 위반 의존 유입 가능성 높음. lefthook hook + PR 차단으로 강제 의무. |

## References

- ADR-0001 (자체 분산 SQL — 본 정책의 직접 결과)
- ADR-0002 (Helm 단일 chart — 격리 chart 가 더 이상 필요하지 않은 이유)
- 이전 ADR-0010 (Citus AGPL 격리, archive 됨) — 본 ADR 이 supersede
- standards/enforcement.md §1.5 — security scan + audit log
- standards/ci.md §3 — pre-push 게이트 카탈로그 (license check 추가)
- SPDX License List — 라이선스 식별자 표준
- OSI Open Source Definition — 본 정책의 라이선스 분류 근거
