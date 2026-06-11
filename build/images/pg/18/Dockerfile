# PG 18 + Citus PG18-호환 마이너 베이스 이미지 (preview 채널)
#
# 본 Dockerfile은 Citus가 PG18을 공식 지원하는 마이너를 발표하는 시점에 활성화된다.
# .github/workflows/upstream-watch.yml이 새 Citus 릴리스를 감지하면 CITUS_VERSION 인자 기본값을
# 자동 PR로 갱신한다.
#
# 빌드: docker buildx build \
#   --build-arg CITUS_VERSION=<X.Y> \
#   -t ghcr.io/keiailab/pg:18-citus<X.Y>-<rev> -f build/images/pg/18/Dockerfile .

ARG PG_MAJOR=18
# TODO(upstream-watch): Citus가 PG18 호환 마이너를 발표하면 자동 갱신.
ARG CITUS_VERSION=13.2

FROM postgres:${PG_MAJOR}-bookworm

ARG PG_MAJOR
ARG CITUS_VERSION

LABEL org.opencontainers.image.source="https://github.com/keiailab/postgres-operator"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.title="postgres-operator pg18+citus (preview)"

ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg lsb-release \
        postgresql-${PG_MAJOR}-pgaudit \
    ; \
    curl -fsSL https://install.citusdata.com/community/deb.sh | bash; \
    apt-get install -y --no-install-recommends \
        postgresql-${PG_MAJOR}-citus-${CITUS_VERSION} \
    ; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

USER postgres
