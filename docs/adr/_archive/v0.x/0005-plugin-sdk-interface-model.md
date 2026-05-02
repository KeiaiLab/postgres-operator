# ADR 0005 — Plugin SDK 인터페이스 모델 (in-process + gRPC)

- **상태**: Accepted (인터페이스 동결)
- **날짜**: 2026-04-27
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001 v2 (미션 3축, Plugin SDK가 메타-차별화), ADR 0004 (Build, not Fork/Layer)
- **선행 분석**: `/Users/phil/.claude/plans/squishy-squishing-harp.md` §9.5

## 컨텍스트

ADR 0001 v2의 미션 3축 중 **Plugin SDK**는 본 프로젝트의 메타-차별화다. 그 가치는 다음 두 약속에 있다.

1. **새 백업 도구·exporter·extension·router·auth 메커니즘 추가 = 인터페이스 구현 1주.** 핵심 reconciler 코드 변경 0.
2. **외부 컨트리뷰터가 폐쇄 라이선스의 플러그인을 분리 배포 가능.** 프로젝트 자체는 Apache-2.0이지만 플러그인 분리 모델로 폐쇄 모듈 결합 허용.

이 약속을 코드 차원에서 강제하려면 **인터페이스를 다른 모든 Pillar(P1~P12, P14) 진입 전에 동결**해야 한다. 늦게 도입하면 v1.0 직전 reconciler 대규모 리팩터링이 강제된다.

## 결정

### 5종 인터페이스 동결

`internal/plugin/api.go`에 다음 5개 Go 인터페이스를 동결한다.

| 인터페이스 | 책임 | 사용 Pillar |
|---|---|---|
| `BackupPlugin` | 백업 도구 추상화 (pgBackRest, WAL-G, Barman, custom) | P4 |
| `ExporterPlugin` | Prometheus exporter + Grafana 대시보드 + alert rule | P6 |
| `ExtensionPlugin` | PG extension install/preload/post-init 훅 + `SharedPreloadOrder()` | P10 |
| `RouterPlugin` | QueryRouter Pod spec 빌더 + 도메인 readiness | P12 |
| `AuthPlugin` | 인증 메커니즘 (SCRAM/mTLS/OIDC) + Secret 스키마 | P7 |

각 인터페이스는 `Name() string`을 공통으로 가지며, 이는 `Registry`에서 조회 키로 사용된다.

### 의존성 최소화

본 패키지(`internal/plugin/`)는 다음 두 그룹 외 외부 의존을 갖지 않는다:

- Go stdlib (`context`, `database/sql`, `time`, `sort`, `sync`, `fmt`)
- `k8s.io/api/core/v1` (이미 go.mod에 indirect로 존재)

특히 다음은 **의도적으로 배제**한다:

- **prometheus-operator API** — `AlertRulesYAML()`은 `[]byte`로 노출. 본 SDK가 prometheus-operator 버전에 종속되지 않음.
- **apiextensions-apiserver의 `JSONSchemaProps`** — `SecretSchemaJSON()`도 `[]byte`로 노출. 동일 이유.
- **HashiCorp `go-plugin`** — 아직 import 하지 않음. P13-T4에서 추가될 때 in-process API와 동등한 어댑터로만 진입.

### 등록 모델 — in-process 우선

`Registry` 구조체로 in-process(compile-time) 등록. `init()` 또는 `main()` 시점에 단일 goroutine이 등록하고, reconciler는 `RLock` 보호 조회.

중복 등록 시 `panic`. 이는 init() 시점 결정성을 강제하는 선택이며, dynamic swap이 필요한 시점은 별도 메서드(`Replace*`)로 P13 후속 task에서 추가한다.

### `SharedPreloadOrder()` — Crunchy PGO Issue #3194 회귀 방지

`Registry.Extensions()` 메서드는 등록된 ExtensionPlugin을 다음 순서로 정렬해 반환한다:

1. `SharedPreloadOrder()` 오름차순 (작을수록 앞)
2. 동률 시 `Name()` 사전순

권장 우선순위:

| 우선순위 | extension | 사유 |
|---|---|---|
| 0 | citus | "Citus must be first" 규약 (PostgreSQL hook 등록 순서) |
| 100 | pgaudit | 표준 audit 도구 |
| 100 | pgvector | AI 워크로드 차별화 |
| 200 | pg_cron | 스케줄러 (Citus 위에 안전) |
| 300 | pg_partman, pgnodemx, set_user, postgis | 일반 |

P10 reconciler는 본 메서드 결과를 `strings.Join`으로 직렬화하여 `shared_preload_libraries`에 주입한다. 정렬 정책 위반은 `internal/plugin/api_test.go:TestExtensions_PreloadOrder`로 회귀 차단.

### 강제 메커니즘

1. **golangci-lint custom 규칙(P13-T2)**: 핵심 reconciler(`internal/controller/`, `internal/webhook/`)가 `internal/plugin/<concrete>/` 하위 패키지를 직접 import 하면 PR reject.
2. **컴파일 가드** (`api_test.go`의 `var _ BackupPlugin = (*dummyBackup)(nil)` 등): 인터페이스 시그니처 변경 시 빌드 실패.
3. **회귀 테스트** (`TestExtensions_PreloadOrder`): SharedPreloadOrder 정책 변경 시 테스트 실패.

### 변경 정책

- alpha 단계에서 **추가 메서드(non-breaking)만** 허용. 메서드 제거·시그니처 변경은 RFC 0012("Plugin SDK 안정화")에서 일괄 처리.
- 본 ADR 변경(인터페이스 추가/제거, 의존성 도입)은 RFC 필수.

## 근거

### 왜 5개인가

본 SDK가 추상화하는 모든 확장 지점은 다음 5가지 책임으로 분류 가능하다:

1. **데이터 보호** (BackupPlugin)
2. **관측** (ExporterPlugin)
3. **확장 기능** (ExtensionPlugin)
4. **요청 라우팅** (RouterPlugin)
5. **신원 검증** (AuthPlugin)

PGO와 CNPG의 외부 통합 지점을 분석한 결과(plan §7), 위 5가지 외 추가 분류가 필요한 경우는 발견되지 않았다. 6번째 인터페이스가 필요해지면 ADR 갱신 후 추가.

### 왜 의존성을 최소화하는가

본 SDK는 외부 플러그인이 import 하는 wire-format이다. 의존성이 무거우면:

- 외부 플러그인 빌드 시간 증가
- 플러그인 격리(gRPC 모드)에서 versioning 충돌
- 본 SDK가 모니터링 스택 메이저 변경(예: prometheus-operator v0.x → v1.0)에 끌려감

`[]byte` raw 매니페스트 노출은 일견 약타이핑처럼 보이지만, **인터페이스 stability와의 trade-off에서 후자가 압도적**이다.

### 왜 in-process 우선인가

P13-T4(gRPC out-of-process)는 외부 폐쇄 플러그인이 필요한 v1.x 시점 가치다. v1.0 GA까지는 **본 프로젝트 자체 플러그인(pgbackrest, pgmonitor, citus 등)이 in-process로 충분**하며, 그것을 우선 안정화한다.

### 왜 중복 등록 시 panic인가

플러그인 등록은 init() 시점 결정이다. 중복은 빌드 타임에 잡혀야 할 결함이며, runtime fallback은 다음 두 위험을 만든다:

- "마지막 등록자 승" 정책은 컴파일 순서에 따라 동작이 달라짐 (Go init 순서는 import 그래프에 의존)
- 사용자가 "왜 내 플러그인이 안 동작하지?"를 디버그할 단서가 사라짐

panic은 가혹하지만 결정성을 강제한다.

## 트레이드오프

- **인터페이스 stability 비용**: 한 번 동결한 시그니처는 alpha 단계에서도 추가만 허용. 잘못 설계한 메서드는 RFC 0012까지 묶여 있음.
  - **완화**: P13-T1 시점에 PGO/CNPG의 외부 통합 지점을 충분히 조사 후 동결. 본 ADR이 그 결과.
- **`[]byte` 약타이핑의 검증 부담**: `AlertRulesYAML()` 결과를 reconciler가 직접 unmarshal 할 때 schema 검증 책임이 reconciler 측에 생김.
  - **완화**: P6 reconciler에서 prometheus-operator schema 검증 라이브러리를 별도 import 하여 적용. SDK는 stable, 검증 로직은 갱신 가능.
- **panic 정책의 운영 위험**: production에서 인스턴스가 init() panic 시 즉시 종료.
  - **완화**: 등록은 init() 또는 main() 단일 호출. 외부 입력(CR, ConfigMap)은 등록 시점에 영향 없음.

## 강제 메커니즘 요약

| 메커니즘 | 구현 위치 | 도입 시점 |
|---|---|---|
| 컴파일 가드 (`var _ Iface = (*impl)(nil)`) | `internal/plugin/api_test.go` | P13-T1 (본 ADR 채택과 동시) |
| Registry 중복 panic | `internal/plugin/registry.go` | 동상 |
| SharedPreloadOrder 정렬 회귀 테스트 | `internal/plugin/api_test.go:TestExtensions_PreloadOrder` | 동상 |
| golangci-lint custom 규칙 (구체 import 차단) | `.custom-gcl.yml` | P13-T2 (별도 task) |
| gRPC out-of-process 어댑터 | `internal/plugin/grpc/` (신규) | P13-T4 (v1.x) |

## 결과

- `internal/plugin/api.go` + `internal/plugin/registry.go` + `internal/plugin/api_test.go` 동결.
- 모든 다른 Pillar reconciler(P1~P12, P14)는 본 패키지의 인터페이스만 호출.
- 본 ADR 변경(인터페이스 추가/제거, 의존성 도입)은 RFC 필수.
- 다음 후속 작업: P13-T2(golangci-lint custom 규칙), P13-T3(P4/P6/P10이 실제 인터페이스 구현 형태로 작성됨을 보장), RFC 0012(SDK 안정화 + 외부 가이드).
