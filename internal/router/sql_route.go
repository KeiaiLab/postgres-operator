/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — sql_route.go 는 RouteKeyExtractor 의 *정규식(regex) 구현*이다
// (경량·제로 의존성 전략). full PostgreSQL 파서(pg_query_go = libpg_query CGO)는
// CGO_ENABLED=0 distroless 빌드와 충돌하므로, point-query 의 흔한 형태만 토큰
// 추출로 잡는다. 정확한 전략(prepared/복합 predicate/JOIN)은 parserExtractor
// (route_extractor_parser.go) 가 담당하고, auto 가 둘을 조합한다.
package router

import (
	"regexp"
	"strings"
)

// legacyRouteKeyPattern 은 shardKeyColumn 미지정(first-literal) 모드의 패턴이다:
// `VALUES ('key'` 또는 `WHERE <col> = 'key'` 의 첫 single-quoted 리터럴.
var legacyRouteKeyPattern = regexp.MustCompile(`(?i)(?:VALUES\s*\(\s*|WHERE\s+[a-z_][a-z0-9_]*\s*=\s*)'([^']*)'`)

// ExtractRoutingKey 는 query 에서 single-shard 라우팅 key 를 first-literal 모드로
// 추출하는 패키지 편의 함수다 (backward-compat: shardKeyColumn 미지정). 컬럼 지정
// 추출과 전략 선택은 RouteKeyExtractor / NewRouteKeyExtractor 를 사용한다.
func ExtractRoutingKey(query string) (string, bool) {
	return regexExtractor{}.ExtractRoutingKey(query, "")
}

// regexExtractor 는 RouteKeyExtractor 의 정규식 구현이다 (경량 전략).
type regexExtractor struct{}

func (regexExtractor) Name() string { return ExtractorRegex }

// ExtractRoutingKey 는 col 이 빈 문자열이면 first-literal 모드, 아니면 그 컬럼을
// 지정 추출한다 (WHERE/AND `<col> = 'x'` 또는 INSERT 컬럼-값 위치 매칭).
func (regexExtractor) ExtractRoutingKey(query, col string) (string, bool) {
	if col == "" {
		m := legacyRouteKeyPattern.FindStringSubmatch(query)
		if len(m) < 2 || m[1] == "" {
			return "", false
		}
		return m[1], true
	}
	if k, ok := matchColumnEquals(query, col); ok {
		return k, true
	}
	return matchInsertColumn(query, col)
}

// matchColumnEquals 는 query 어디에서든 `<col> = 'literal'` (대소문자 무시)을 잡는다.
// 복합 predicate (`WHERE a=1 AND tenant_id='t9'`) 에서도 지정 컬럼만 추출한다.
func matchColumnEquals(query, col string) (string, bool) {
	pat := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\s*=\s*'([^']*)'`)
	m := pat.FindStringSubmatch(query)
	if len(m) < 2 || m[1] == "" {
		return "", false
	}
	return m[1], true
}

// insertPattern 은 `INSERT INTO t (c1, c2, ...) VALUES (v1, v2, ...)` 의 컬럼 목록과
// 첫 VALUES 튜플을 잡는다.
var insertPattern = regexp.MustCompile(`(?is)INSERT\s+INTO\s+[^\s(]+\s*\(([^)]*)\)\s*VALUES\s*\(([^)]*)\)`)

// matchInsertColumn 은 INSERT 의 컬럼 목록에서 col 의 위치를 찾아 같은 위치의 VALUES
// 리터럴을 반환한다. col 이 목록에 없거나 값이 따옴표 리터럴이 아니면 (",", false).
func matchInsertColumn(query, col string) (string, bool) {
	m := insertPattern.FindStringSubmatch(query)
	if len(m) < 3 {
		return "", false
	}
	cols := splitCSV(m[1])
	vals := splitCSV(m[2])
	if len(cols) != len(vals) {
		return "", false
	}
	for i, c := range cols {
		if strings.EqualFold(c, col) {
			v := strings.TrimSpace(vals[i])
			if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
				inner := v[1 : len(v)-1]
				if inner == "" {
					return "", false
				}
				return inner, true
			}
			return "", false
		}
	}
	return "", false
}

// splitCSV 는 comma 구분 목록을 trim 하여 분리한다 (단순 — 중첩 함수/콤마 미지원,
// regex 전략의 best-effort 한계).
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

var _ RouteKeyExtractor = regexExtractor{}
