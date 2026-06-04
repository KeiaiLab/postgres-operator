// Package router — sql_route.go 는 SQL query 에서 라우팅 key 를 추출하여
// single-shard fast-path(scatter-gather broadcast 회피)를 가능케 한다.
//
// pg-router 는 connection-oriented proxy 라 connection 단위로 backend 가 고정된다.
// query 단위 라우팅(키가 단일 shard 에 매핑되는 point query 를 그 shard 로만 보내고
// 나머지 N-1 shard 를 건드리지 않는 것)은 query 에서 routing key 를 뽑아 vindex
// (ResolveShard)로 shard 를 결정하는 것으로 구현한다. key 를 못 뽑으면 caller 는
// scatter-gather(전 shard fan-out)로 fallback 한다.
//
// 본 PoC 는 *경량 토큰 추출*이다 — full PostgreSQL 파서(pg_query_go = libpg_query
// CGO)는 CGO_ENABLED=0 distroless 빌드와 충돌하므로, point-query 의 흔한 형태
// (INSERT ... VALUES('key',...) / SELECT ... WHERE col='key')만 정규식으로 잡는다.
// 복합 predicate / prepared statement / parameterized query 의 완전 파싱은 후속
// (pure-Go 파서 선정 선행).
package router

import "regexp"

// routeKeyPattern 은 point query 의 첫 single-quoted literal 을 routing key 후보로
// 잡는다: `VALUES ('key'` 또는 `WHERE <col> = 'key'` (대소문자 무시).
var routeKeyPattern = regexp.MustCompile(`(?i)(?:VALUES\s*\(\s*|WHERE\s+[a-z_][a-z0-9_]*\s*=\s*)'([^']*)'`)

// ExtractRoutingKey 는 query 에서 single-shard 라우팅 key 를 추출한다. point query
// (VALUES / WHERE 등호)면 (key, true), 아니면 ("", false) — caller 는 false 시
// scatter-gather 로 fallback 한다.
func ExtractRoutingKey(query string) (string, bool) {
	m := routeKeyPattern.FindStringSubmatch(query)
	if len(m) < 2 || m[1] == "" {
		return "", false
	}
	return m[1], true
}
