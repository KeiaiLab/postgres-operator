/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — aggregate_detect.go 는 SELECT 리스트를 분석해 컬럼별 집계 함수를
// 추출한다(scatter 집계 재merge planner 결선). 결과 []AggregateFunc 을
// ScatterGather.Aggregates 로 넘기면 MergeAggregate 가 shard 별 부분 집계를 재결합한다.
//
// 보수적 설계(틀린 재결합 방지): 집계 컬럼은 `fn(...)` 순수 호출 형태(옵션 alias)만
// 인정한다. `1 + count(*)` 같은 표현식·중첩·SELECT * 는 안전하게 재결합 불가로 판단해
// ok=false 를 반환하고, 호출자는 집계 merge 를 쓰지 않는다(일반 scatter 로 degrade).
// AVG 는 부분 평균 재결합 불가(가중 필요)라 감지 시 ok=false.
package router

import "strings"

// DetectAggregates 는 단일 SELECT 문의 출력 컬럼별 집계 함수를 반환한다. 두 번째 반환값
// ok 가 true 면 이 쿼리는 집계 재merge 대상이며 aggs 를 ScatterGather.Aggregates 로 쓸 수
// 있다. 집계 컬럼이 없거나(일반 쿼리) 안전하게 재결합 불가하면 (nil, false).
func DetectAggregates(query string) (aggs []AggregateFunc, ok bool) {
	toks := tokenize(query)
	if len(toks) == 0 {
		return nil, false
	}
	// 다중문(top-level ';')·비 SELECT 는 대상 아님.
	for _, t := range toks {
		if t.kind == tokSym && t.text == ";" {
			return nil, false
		}
	}
	if toks[0].kind != tokIdent || !strings.EqualFold(toks[0].text, "select") {
		return nil, false
	}
	// DISTINCT 는 보수적으로 제외(집계와 결합 시 재결합 의미 복잡).
	start := 1
	if len(toks) > 1 && toks[1].kind == tokIdent && strings.EqualFold(toks[1].text, "distinct") {
		return nil, false
	}

	// SELECT 리스트 = start .. (top-level FROM 직전). FROM 없으면 대상 아님.
	end := -1
	depth := 0
	for i := start; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokSym && t.text == "(" {
			depth++
			continue
		}
		if t.kind == tokSym && t.text == ")" {
			depth--
			continue
		}
		if depth == 0 && t.kind == tokIdent && strings.EqualFold(t.text, "from") {
			end = i
			break
		}
	}
	if end < 0 || end == start {
		return nil, false
	}

	segs := splitTopLevelCommas(toks[start:end])
	if len(segs) == 0 {
		return nil, false
	}

	out := make([]AggregateFunc, 0, len(segs))
	hasAggregate := false
	for _, seg := range segs {
		fn, pure := segmentAggregate(seg)
		if !pure {
			// 순수 집계도 순수 컬럼도 아님(표현식/중첩 집계/AVG/SELECT *) → 안전 불가.
			return nil, false
		}
		if fn != AggNone {
			hasAggregate = true
		}
		out = append(out, fn)
	}
	if !hasAggregate {
		return nil, false // 집계 없는 일반 쿼리 — 집계 merge 대상 아님.
	}
	return out, true
}

// splitTopLevelCommas 는 token 열을 depth 0 의 콤마로 분할한다(괄호 내부 콤마 무시).
func splitTopLevelCommas(toks []token) [][]token {
	var segs [][]token
	depth := 0
	cur := []token{}
	for _, t := range toks {
		switch {
		case t.kind == tokSym && t.text == "(":
			depth++
			cur = append(cur, t)
		case t.kind == tokSym && t.text == ")":
			depth--
			cur = append(cur, t)
		case depth == 0 && t.kind == tokSym && t.text == ",":
			segs = append(segs, cur)
			cur = []token{}
		default:
			cur = append(cur, t)
		}
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	return segs
}

// segmentAggregate 는 하나의 SELECT 항목이 순수 집계 호출인지(그 함수), 순수 컬럼인지
// (AggNone), 아니면 재결합 불가(pure=false)인지 판정한다.
//
// 순수 집계 = `fn ( ... )` 로 시작하고 매칭 닫는 괄호 뒤에는 옵션 alias(`as x` 또는 `x`)
// 만 오는 형태. arithmetic/중첩/추가 토큰이 있으면 pure=false.
func segmentAggregate(seg []token) (fn AggregateFunc, pure bool) {
	// 선행 토큰 없음 → 빈 항목(불가).
	if len(seg) == 0 {
		return AggNone, false
	}
	// SELECT * / t.* 같은 star 는 집계 위치 매핑 불가.
	for _, t := range seg {
		if t.kind == tokSym && t.text == "*" {
			// count(*) 의 '*' 는 괄호 안 — 아래 집계 분기에서 소비된다. 여기서는 top-level
			// '*'(예: SELECT *)만 문제. 괄호 depth 로 구분.
		}
	}

	first := seg[0]
	af, isAgg := aggregateFuncName(first)
	if isAgg && len(seg) >= 2 && seg[1].kind == tokSym && seg[1].text == "(" {
		// 매칭 닫는 괄호 위치.
		depth := 0
		close := -1
		for i := 1; i < len(seg); i++ {
			if seg[i].kind == tokSym && seg[i].text == "(" {
				depth++
			} else if seg[i].kind == tokSym && seg[i].text == ")" {
				depth--
				if depth == 0 {
					close = i
					break
				}
			}
		}
		if close < 0 {
			return AggNone, false // 괄호 불균형.
		}
		// AVG 는 재결합 불가.
		if af == aggAvgMarker {
			return AggNone, false
		}
		// 닫는 괄호 뒤 = 옵션 alias 만 허용.
		if aliasOnly(seg[close+1:]) {
			return af.toAggregateFunc(), true
		}
		return AggNone, false // fn(...) 뒤에 arithmetic 등 → 재결합 불가.
	}

	// 집계 호출이 아님 → 순수 컬럼(group key)로 인정하되, 내부에 집계 토큰이 숨어 있으면
	// (예: `1 + count(*)`) 재결합 불가.
	for _, t := range seg {
		if _, isA := aggregateFuncName(t); isA {
			return AggNone, false
		}
		if t.kind == tokSym && t.text == "*" {
			return AggNone, false // top-level star.
		}
	}
	return AggNone, true // 순수 group-key 컬럼.
}

// aliasOnly 는 남은 토큰이 옵션 alias(`as ident` / `ident` / 빈) 뿐인지 본다.
func aliasOnly(rest []token) bool {
	switch len(rest) {
	case 0:
		return true
	case 1:
		return rest[0].kind == tokIdent
	case 2:
		return rest[0].kind == tokIdent && strings.EqualFold(rest[0].text, "as") && rest[1].kind == tokIdent
	default:
		return false
	}
}

// aggFuncName 은 감지된 집계 함수 이름의 내부 마커.
type aggFuncName int

const (
	aggNoneMarker aggFuncName = iota
	aggCountMarker
	aggSumMarker
	aggMinMarker
	aggMaxMarker
	aggAvgMarker
)

func (m aggFuncName) toAggregateFunc() AggregateFunc {
	switch m {
	case aggCountMarker:
		return AggCount
	case aggSumMarker:
		return AggSum
	case aggMinMarker:
		return AggMin
	case aggMaxMarker:
		return AggMax
	}
	return AggNone
}

// aggregateFuncName 은 토큰이 집계 함수 식별자면 그 마커와 true 를 반환한다.
func aggregateFuncName(t token) (aggFuncName, bool) {
	if t.kind != tokIdent {
		return aggNoneMarker, false
	}
	switch strings.ToLower(t.text) {
	case "count":
		return aggCountMarker, true
	case "sum":
		return aggSumMarker, true
	case "min":
		return aggMinMarker, true
	case "max":
		return aggMaxMarker, true
	case "avg":
		return aggAvgMarker, true
	}
	return aggNoneMarker, false
}
