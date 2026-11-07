# ADR-0038: pg 이미지 Go toolchain 보안 패치는 승인된 I26 변경이다

- **Date**: 2026-11-07
- **Status**: Accepted
- **Authors**: @claude (설계)
- **Refs**: ADR-0037 · 불변식 I26 · `test/chart/dockerfile_go_test.go` · govulncheck

## Context

- `Dockerfile.pg` 의 `instance-builder` 단계는 `golang:1.26-bookworm@sha256:a688600c…`(= Go 1.26.8)에 고정돼 있었다.
  govulncheck 가 그 표준 라이브러리에서 GO-2026-6809 와 1.26.9 묶음(GO-2026-6613·6608·6607·6605·6600·6599)을 찾았다.
- `Dockerfile.pg` 는 I26 보호 경로라 커밋마다 Accepted ADR 의 `I26-ADR:` 트레일러가 든다(ADR-0037). 인스턴스 매니저 소스를
  바꾸지 않는 툴체인 패치에 맞는 ADR 이 없었다.
- `golang:1.26-bookworm` 처럼 패치 판본이 없는 태그는 다이제스트가 어느 패치인지 숨긴다. 차트 시험은 `Dockerfile.pg` 를
  판본 검사에서 뺐다.

## Decision

1. `Dockerfile.pg` 의 Go toolchain · 기반 이미지 다이제스트를 패치 판본만 올리고 인스턴스 매니저 소스(`cmd/instance/` ·
   `internal/instance/`)를 바꾸지 않는 변경은, govulncheck 가 그 CVE 를 지목할 때 승인된 I26 변경이다.
2. 그 커밋은 트레일러 `I26-ADR: 0038` 을 달고, 본문에 GO-ID 를 적는다.
3. golang 단계 태그는 패치 판본을 명시한다(`golang:<major>.<minor>.<patch>-bookworm@sha256:…`).
   `TestDockerfilePgGoAtLeastToolchain` 이 그 판본 ≥ go.mod `toolchain` 을 강제한다 — 같은 판본 강제는 하지 않는다(I26 경로를
   상류 toolchain 동기마다 건드리지 않게).
4. kind 훈련장 `pgDrillBase` 이미지는 영향받지 않는다 — 기점 트리를 굽는 이미지라, 훈련장을 다시 기점 잡을 때까지 그대로다.

## Consequences

- 보안 패치가 새 ADR 없이 기계 판정(가드 + 차트 시험)으로 착지한다.
- toolchain 이 `Dockerfile.pg` 보다 앞서면 차트 시험이 붉다 — 그때 이 ADR 로 패치 커밋을 잇는다.
- 소스 변경이 섞인 커밋은 이 ADR 로 승인되지 않는다 — 별 ADR 이 든다.

## Alternatives Considered

- 커밋마다 새 ADR: 같은 결정을 되풀이 적는다.
- `Dockerfile.pg` 를 다른 Dockerfile 과 같은 판본 · 다이제스트로 강제: 이미지 계열(bookworm)이 달라 다이제스트가 같을 수 없고,
  toolchain 동기마다 I26 커밋이 강제된다.
- 패치 없는 태그 유지: 취약 판본이 태그에 드러나지 않는다.

## Status

Accepted.
