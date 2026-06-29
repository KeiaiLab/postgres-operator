/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — route_extractor.go 는 SQL query 에서 단일-shard 라우팅 key 를
// 뽑는 전략(strategy)을 *교체 가능한 인터페이스*로 노출한다.
//
// 설계 의도 (RFC-0004 §3.1 단일-shard fast-path): 라우팅 key 추출 전략을 한쪽으로
// 하드코딩하지 않고 *사용자가 런타임에 선택*하게 한다. 두 구현 모두 *제로 외부 의존성*
// 이라 distroless 미니멀리즘을 깨지 않는다 (둘 다 항상 컴파일 → 런타임 선택).
//
//   - regex  : 정규식 토큰 추출 (가장 단순). point query 흔한 형태.
//   - parser : 제로 의존성 *토크나이저 기반* 추출 (정확). 따옴표/이스케이프/주석/
//     복합 predicate/INSERT 컬럼 위치/UPDATE·DELETE 까지 토큰 단위로 정확히 처리.
//   - auto   : parser 우선, 매치 실패 시 regex fallback.
//
// (full PostgreSQL 문법 파서는 아니다 — 라우팅 key 추출 subset 에 집중. auxten/
// pg_query 등 외부 파서는 의존성 폭증·CGO·distroless 충돌로 도입하지 않음, ROUTER-
// GAP-ANALYSIS §5.)
package router

import "fmt"

// RouteKeyExtractor 는 SQL query 에서 shardKeyColumn 에 대한 단일-shard 라우팅 key
// 를 추출한다. point query (단일 shard 로 고정) 면 (key, true), 아니면 ("", false)
// — 호출자는 false 일 때 scatter-gather 로 fallback 한다.
//
// shardKeyColumn 이 "" 이면 *first-literal 모드*(legacy): 컬럼명과 무관하게 처음
// 발견한 라우팅 key 리터럴을 반환한다. vindex 컬럼(ShardRangeSpec.Vindex.Column)을
// 넘기면 그 컬럼을 *지정 추출*한다.
type RouteKeyExtractor interface {
	// Name 은 전략 식별자 (config/log 용).
	Name() string
	// ExtractRoutingKey 는 query 에서 shardKeyColumn 의 라우팅 key 를 추출한다.
	ExtractRoutingKey(query, shardKeyColumn string) (string, bool)
}

// 전략 이름 (pg-router config 로 사용자 선택).
const (
	// ExtractorRegex 는 정규식 추출 (가장 단순).
	ExtractorRegex = "regex"
	// ExtractorParser 는 제로 의존성 토크나이저 기반 추출 (정확).
	ExtractorParser = "parser"
	// ExtractorAuto 는 parser 우선 + regex fallback.
	ExtractorAuto = "auto"
)

// DefaultExtractorName 은 기본 전략이다. 사용자가 override 가능하며 *추후 변경될 수
// 있다*. 현재 기본은 regex (현황 유지 — 가장 단순·검증된 경로). 정확 라우팅이 필요한
// 배포는 parser 또는 auto 를 선택한다.
const DefaultExtractorName = ExtractorRegex

// NewRouteKeyExtractor 는 이름으로 전략을 생성한다. 알 수 없는 이름은 error.
// 빈 문자열은 DefaultExtractorName 으로 해석한다. 세 전략 모두 제로 외부 의존성이라
// 항상 사용 가능하다.
func NewRouteKeyExtractor(name string) (RouteKeyExtractor, error) {
	if name == "" {
		name = DefaultExtractorName
	}
	switch name {
	case ExtractorRegex:
		return regexExtractor{}, nil
	case ExtractorParser:
		return parserExtractor{}, nil
	case ExtractorAuto:
		return autoExtractor{primary: parserExtractor{}, fallback: regexExtractor{}}, nil
	default:
		return nil, fmt.Errorf("router: unknown route-key extractor %q (want regex|parser|auto)", name)
	}
}

// autoExtractor 는 primary(파서)를 먼저 시도하고, 매치 실패 시 fallback(정규식)을
// 시도한다. 파서가 query 를 이해하지 못해도(비표준 문법) 경량 경로로 라우팅 기회를
// 한 번 더 갖는다.
type autoExtractor struct {
	primary  RouteKeyExtractor
	fallback RouteKeyExtractor
}

func (autoExtractor) Name() string { return ExtractorAuto }

func (a autoExtractor) ExtractRoutingKey(query, col string) (string, bool) {
	if k, ok := a.primary.ExtractRoutingKey(query, col); ok {
		return k, true
	}
	return a.fallback.ExtractRoutingKey(query, col)
}
