# 분석·작업 문서 지도 (DOCS_MAP)

> 이 저장소의 **한국어 분석/작업/환경 문서군**의 입구. 각 문서의 역할, 어떤 주제의 **단일 출처(SSOT)**인지, 문서 사이 관계, 그리고 추후 보강할 때 "어디에 써야 하는지"를 정의한다.
>
> 영문 공개 문서(`ARCHITECTURE` / `ROADMAP` / ADR / RFC / runbooks 등)의 입구는 [index.md](index.md)다. 이 문서는 그와 별개로, 내부 분석·작업 흐름 문서를 묶는다.

---

## 1. 문서 지도

| 문서 | 역할 (한 줄) | 이 문서가 SSOT인 주제 |
|---|---|---|
| [PROJECT_OVERVIEW.md](PROJECT_OVERVIEW.md) | 프로젝트 **개요** — 목적·스택·아키텍처 요약·CRD 목록·로드맵·디렉토리 | 프로젝트 전반 요약, 디렉토리 구조, 관련 문서 인덱스 |
| [FEATURE_DEEP_DIVE.md](FEATURE_DEEP_DIVE.md) | 기능별 **심층 동작 분석** — 각 CRD/컨트롤러의 reconcile·내부 동작 | 기능 동작의 상세 레퍼런스 (failover/PITR/pooler/DB·User 내부 흐름) |
| [TEST_ANALYSIS.md](TEST_ANALYSIS.md) | **단위·통합 테스트** 분석 — 패키지별 커버리지·TC 상세 | 단위/통합 테스트 결과·커버리지 수치, CRLF/`.gitattributes` 조치 |
| [E2E_TEST_REPORT.ko.md](E2E_TEST_REPORT.ko.md) | **E2E 라이브 드릴 RCA** 누적 로그 — 시간순 검증/원인분석 | E2E(라이브 K8s) 드릴 결과·RCA·PENDING 이력 |
| [WORK_HANDOFF.ko.md](WORK_HANDOFF.ko.md) | 현재 **작업 인수인계** — 브랜치 커밋 구성·검증 요약·남은 일·재현법 | 진행 중 작업 스냅샷(브랜치 `chore/ha-pitr-e2e-consolidation`) |
| [dev-setup-devcontainer.md](dev-setup-devcontainer.md) | **Dev Container** 개발 환경 구성 (현재 권장) | Windows에서 컨테이너로 빌드/테스트하는 절차 |
| [dev-setup-wsl.md](dev-setup-wsl.md) | **WSL2** 개발 환경 구성 (대안) | WSL2 네이티브로 빌드/테스트하는 절차 |

> 이 지도 자체와 위 표는 새 분석/작업 문서가 생길 때마다 갱신한다.

---

## 2. 관계 (계층)

```
입구
└─ DOCS_MAP.ko.md (이 문서)
   ├─ 개요 ────────── PROJECT_OVERVIEW.md ──┐ (요약 → 상세로 내려감)
   │                                        └─→ FEATURE_DEEP_DIVE.md (심층)
   ├─ 테스트 ──────── TEST_ANALYSIS.md (단위·통합)
   │                 E2E_TEST_REPORT.ko.md (E2E 라이브)
   ├─ 작업 ────────── WORK_HANDOFF.ko.md (현재 브랜치 스냅샷)
   └─ 환경 ────────── dev-setup-devcontainer.md (권장) / dev-setup-wsl.md (대안)
```

- **개요 vs 심층**: 같은 기능을 둘 다 다루지만 계층이 다르다 — `PROJECT_OVERVIEW`는 "무엇이 있나"(요약), `FEATURE_DEEP_DIVE`는 "어떻게 동작하나"(상세). 이 분리는 **의도된 것이며 중복이 아니다.**
- **테스트 2분할**: `TEST_ANALYSIS`=호스트 없이 컨테이너에서 도는 단위/통합, `E2E_TEST_REPORT`=실 K8s 라이브 드릴. 경계가 다르다.
- **환경 2종**: `devcontainer`(현재 권장 — 이 머신은 go/make 부재로 컨테이너 필수)와 `wsl`(대안). 두 문서는 **독립 완결**을 목표로 하되, 공통 절차의 출처는 §3 규칙을 따른다.

---

## 3. 주제별 SSOT 규칙 (중복 방지)

새 내용을 쓰거나 기존 내용을 고칠 때, 아래 주제는 **지정된 문서 한 곳에만** 본문을 두고 나머지는 그곳을 **링크**한다.

| 주제 | SSOT (본문은 여기에만) | 다른 문서는 |
|---|---|---|
| 단위/통합 테스트 결과·커버리지 수치 | `TEST_ANALYSIS.md` | "참고치"로 표기하고 링크 (예: `WORK_HANDOFF`, 메모리) |
| E2E 라이브 드릴 결과·RCA | `E2E_TEST_REPORT.ko.md` | 링크만 |
| CRLF / `.gitattributes` 조치 | `TEST_ANALYSIS.md §6` | dev-setup 트러블슈팅은 1줄 요약 + 링크 |
| make 타겟 의미·소요시간 | `dev-setup-devcontainer.md §7` | `dev-setup-wsl`은 동일 표 유지 가능(독립 완결 목적), 단 갱신 시 둘 다 |
| E2E 실행 명령(`make test-e2e-*`) | `WORK_HANDOFF.ko.md §3` | dev-setup은 환경 기동까지만, 시나리오는 링크 |
| 기능 내부 동작 상세 | `FEATURE_DEEP_DIVE.md` | `PROJECT_OVERVIEW`는 요약 + 링크 |
| 현재 작업/브랜치 상태 | `WORK_HANDOFF.ko.md` + 메모리 `analysis-progress` | 다른 문서에 작업 상태를 복제하지 않음 |

---

## 4. 발견된 중복과 처리 (2026-06-25)

| 중복 | 처리 |
|---|---|
| dev-setup-devcontainer ↔ dev-setup-wsl: make 타겟표·e2e 절차·트러블슈팅 | 환경별 독립 완결이 가치 → **표는 유지**, 상단에 상호참조 추가, 공통 SSOT는 §3 명시 |
| 검증 수치가 4곳에 산재 | SSOT=`TEST_ANALYSIS`/`E2E_TEST_REPORT`로 지정, `WORK_HANDOFF`는 "참고치" 명시(이미 반영) |
| CRLF/.gitattributes 가 TEST_ANALYSIS와 dev-setup에 | SSOT=`TEST_ANALYSIS §6`, dev-setup은 요약+링크 |
| 한국어 분석 문서군의 입구 부재 | **이 문서(DOCS_MAP) 신설**로 해소 |

---

## 5. 추후 보강 가이드

- **새 기능을 구현했다**: 요약 1~2줄은 `PROJECT_OVERVIEW`, 동작 상세는 `FEATURE_DEEP_DIVE`에. 새 CRD면 `PROJECT_OVERVIEW §4` 표에도 추가.
- **테스트를 돌렸다**: 단위/통합이면 `TEST_ANALYSIS`, 라이브 E2E면 `E2E_TEST_REPORT`에 날짜 섹션으로 append.
- **작업 브랜치를 진행했다**: `WORK_HANDOFF.ko.md`를 갱신하고 메모리 `analysis-progress`도 같이.
- **환경 절차가 바뀌었다**: 해당 dev-setup 문서. make 타겟·e2e 명령이 바뀌면 §3 SSOT 문서를 먼저 고치고 링크 측은 그대로.
- **새 분석 문서를 추가했다**: 이 DOCS_MAP §1 표와 §2 관계도에 한 줄 추가.
