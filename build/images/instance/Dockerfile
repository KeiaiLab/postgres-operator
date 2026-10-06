# Instance manager 이미지 (PID 1 Go 바이너리)
#
# 빌드:
#   docker buildx build \
#     -t ghcr.io/keiailab/postgres-operator-instance:<rev> \
#     -f build/images/instance/Dockerfile .
#
# ADR 0002 §결과: distroless 기반, 외부 런타임 0, K8s lease로 election 참여.
# Pillar P1-T3 골격. P2~P11 후속 task에서 election/citus_update_node 등 추가.

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS build
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" \
        -o /out/instance ./cmd/instance

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

LABEL org.opencontainers.image.source="https://github.com/keiailab/postgres-operator"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.title="postgres-operator instance manager"
LABEL org.opencontainers.image.description="PID 1 Go binary that supervises postgres and participates in K8s lease election (ADR 0002)"

WORKDIR /
COPY --from=build /out/instance /instance

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/instance"]
