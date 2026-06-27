/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — route_extractor_parser.go 는 RouteKeyExtractor 의 *제로 의존성
// 토크나이저 기반 구현*이다 (정확 전략). 정규식보다 견고하다 — single-quote 리터럴의
// ” 이스케이프, 주석(`--`, `/* */`), 임의 공백, 복합 predicate(AND/OR), 컬럼의
// 테이블 한정(`t.col`), INSERT 의 컬럼-값 위치 정렬을 토큰 단위로 정확히 처리한다.
//
// full PostgreSQL 문법 파서는 아니다 — 라우팅 key 추출 subset(`<col> = '리터럴'` /
// INSERT 위치)에 집중한다. 외부 SQL 파서(auxten 등)는 의존성 폭증·genproto 충돌·
// distroless 비호환으로 도입하지 않는다 (ROUTER-GAP-ANALYSIS §5). 토큰화로 정확도를
// 얻으면서 의존성 0 을 지킨다 (murmur3 자체 구현과 동일 철학).
package router

import (
	"strconv"
	"strings"
)

// ExtractParamRef 는 query 에서 `<col> = $N`(extended protocol 파라미터 참조)을 찾아
// 1-based 파라미터 인덱스 N 을 반환한다. 라우팅 키 값이 Parse 가 아닌 후속 Bind 메시지에
// 있는 parameterized 쿼리를 라우팅하기 위함. 인라인 리터럴은 ExtractRoutingKey 가 잡으므로
// 본 함수는 $N 만 다룬다.
func ExtractParamRef(query, col string) (int, bool) {
	if col == "" {
		return 0, false
	}
	toks := tokenize(query)
	for i := 0; i+3 < len(toks); i++ {
		if toks[i].kind == tokIdent && strings.EqualFold(toks[i].text, col) &&
			toks[i+1].kind == tokSym && toks[i+1].text == "=" &&
			toks[i+2].kind == tokSym && toks[i+2].text == "$" &&
			toks[i+3].kind == tokNum {
			if n, err := strconv.Atoi(toks[i+3].text); err == nil && n >= 1 {
				return n, true
			}
		}
	}
	return 0, false
}

// parserExtractor 는 RouteKeyExtractor 의 토크나이저 기반 구현이다.
type parserExtractor struct{}

func (parserExtractor) Name() string { return ExtractorParser }

// ExtractRoutingKey 는 query 를 토큰화하여 shardKeyColumn 의 등호 리터럴을 추출한다.
// col 이 ""(first-literal 모드)이면 컬럼 의미가 없으므로 ("", false) 로 regex 전략에
// 위임한다 (auto 가 그 fallback 을 수행).
func (parserExtractor) ExtractRoutingKey(query, col string) (string, bool) {
	if col == "" {
		return "", false
	}
	toks := tokenize(query)
	if len(toks) == 0 {
		return "", false
	}
	switch strings.ToLower(toks[0].text) {
	case "insert":
		return insertValueTok(toks, col)
	case "select", "update", "delete", "with":
		return whereEqTok(toks, col)
	}
	return "", false
}

// IsReadOnlyQuery 는 query 가 *읽기 전용*인지 토큰 단위로 보수적으로 판정한다 — 읽기를
// replica 로 보내기 위한 분류. 안전 기본은 *false(쓰기→primary)*: 확실히 읽기인
// 문장만 true. SELECT 의 `FOR UPDATE/SHARE`(잠금)나 DML 을 포함한 WITH(CTE)는 쓰기로
// 본다. 오분류 비용이 비대칭이기 때문 — 쓰기를 replica 로 보내면 치명적, 읽기를
// primary 로 보내면 성능만 손해.
func IsReadOnlyQuery(query string) bool {
	toks := tokenize(query)
	if len(toks) == 0 {
		return false
	}
	switch strings.ToLower(toks[0].text) {
	case "select", "show", "values", "table":
		// SELECT ... FOR UPDATE/SHARE 는 잠금 획득 → 쓰기로 취급.
		for _, t := range toks {
			if t.kind == tokIdent && strings.EqualFold(t.text, "for") {
				return false
			}
		}
		return true
	}
	return false
}

type tokKind int

const (
	tokIdent tokKind = iota // 식별자 / 키워드 / 따옴표 식별자
	tokStr                  // single-quote 문자열 리터럴 (text = 언쿼트 값)
	tokNum                  // 숫자
	tokSym                  // 연산자 / 구두점 ( ) , ; . = < > 등
)

type token struct {
	kind tokKind
	text string
}

// tokenize 는 SQL 을 라우팅 추출에 필요한 최소 토큰열로 분해한다. 주석/공백은 버린다.
func tokenize(s string) []token {
	var toks []token
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '-' && i+1 < n && s[i+1] == '-': // 라인 주석
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*': // 블록 주석
			i += 2
			for i+1 < n && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			if i > n {
				i = n
			}
		case c == '\'': // 문자열 리터럴 ('' 이스케이프)
			i++
			var b strings.Builder
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						b.WriteByte('\'')
						i += 2
						continue
					}
					i++ // 닫는 따옴표
					break
				}
				b.WriteByte(s[i])
				i++
			}
			toks = append(toks, token{tokStr, b.String()})
		case c == '"': // 따옴표 식별자
			i++
			var b strings.Builder
			for i < n {
				if s[i] == '"' {
					if i+1 < n && s[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
			toks = append(toks, token{tokIdent, b.String()})
		case c == '$': // dollar-quote ($$...$$ / $tag$...$tag$) 또는 $1 파라미터
			if inner, end, ok := dollarQuoted(s, i); ok {
				// dollar-quote 본문은 하나의 문자열 리터럴 — 내부 텍스트가 식별자/predicate
				// 로 새어나와 false-match 되는 것을 막는다.
				toks = append(toks, token{tokStr, inner})
				i = end
			} else {
				toks = append(toks, token{tokSym, "$"}) // $1 등 파라미터/stray.
				i++
			}
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, token{tokIdent, s[i:j]})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			toks = append(toks, token{tokNum, s[i:j]})
			i = j
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '.':
			toks = append(toks, token{tokSym, string(c)})
			i++
		default: // 연산자 문자 묶음 (=, <=, >=, != 등)
			j := i
			for j < n && isOpChar(s[j]) {
				j++
			}
			if j == i {
				j = i + 1 // 미지 단일 문자
			}
			toks = append(toks, token{tokSym, s[i:j]})
			i = j
		}
	}
	return toks
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '$'
}

// dollarQuoted 는 s[i]=='$' 에서 PostgreSQL dollar-quote 를 파싱한다. 여는 구분자는
// `$<tag>$`(tag = [A-Za-z0-9_]*). 성공 시 내부 본문 + 닫는 구분자 다음 인덱스를 반환.
// `$1`(파라미터)이나 단독 `$` 는 (,,false).
func dollarQuoted(s string, i int) (inner string, end int, ok bool) {
	j := i + 1
	for j < len(s) && isTagChar(s[j]) {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return "", 0, false // $1 / stray $.
	}
	open := s[i : j+1] // "$<tag>$"
	rest := s[j+1:]
	if idx := strings.Index(rest, open); idx >= 0 {
		return rest[:idx], j + 1 + idx + len(open), true
	}
	return rest, len(s), true // 미종료 → 나머지를 본문으로 소비.
}

func isTagChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isOpChar(c byte) bool {
	switch c {
	case '=', '<', '>', '!', '+', '-', '*', '/', '%', '~', '&', '|', '^':
		return true
	}
	return false
}

// whereEqTok 는 토큰열에서 `<col> = '리터럴'` 패턴을 찾는다 (복합 predicate 에서 지정
// 컬럼만). 우변이 문자열 리터럴이 아니면(파라미터/숫자/범위) 무시.
//
// *모호성 안전*: 같은 샤딩 컬럼에 서로 다른 리터럴이 둘 이상 보이면(예: 서브쿼리의
// `tenant_id='a'` 와 외부 `tenant_id='b'`, 또는 `OR`) 어느 샤드인지 모호하다. 잘못된
// 샤드로 라우팅(특히 쓰기)하는 것보다, 추출 실패로 두어 호출자가 scatter/거부하게 하는
// 편이 안전하다. 동일 리터럴만 반복되면 그 값을 쓴다.
func whereEqTok(toks []token, col string) (string, bool) {
	found := ""
	got := false
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].kind == tokIdent && strings.EqualFold(toks[i].text, col) &&
			toks[i+1].kind == tokSym && toks[i+1].text == "=" &&
			toks[i+2].kind == tokStr {
			v := toks[i+2].text
			if v == "" {
				continue
			}
			if got && v != found {
				return "", false // 모호 → 추측 거부.
			}
			found, got = v, true
		}
	}
	return found, got
}

// insertValueTok 는 INSERT 의 컬럼 목록에서 col 위치를 찾아 같은 위치의 VALUES 리터럴을
// 반환한다. 컬럼/값 개수가 다르거나 값이 단일 문자열 리터럴이 아니면 ("", false).
func insertValueTok(toks []token, col string) (string, bool) {
	vi := indexOfKeyword(toks, "values")
	if vi < 0 {
		return "", false
	}
	colElems := parenSplit(toks, 0)  // 첫 괄호 그룹 = 컬럼 목록
	valElems := parenSplit(toks, vi) // values 뒤 괄호 그룹 = 값 목록
	if colElems == nil || valElems == nil || len(colElems) != len(valElems) {
		return "", false
	}
	for idx, ce := range colElems {
		name := lastIdent(ce)
		if name == "" || !strings.EqualFold(name, col) {
			continue
		}
		ve := valElems[idx]
		if len(ve) == 1 && ve[0].kind == tokStr && ve[0].text != "" {
			return ve[0].text, true
		}
		return "", false
	}
	return "", false
}

func indexOfKeyword(toks []token, kw string) int {
	for i, t := range toks {
		if t.kind == tokIdent && strings.EqualFold(t.text, kw) {
			return i
		}
	}
	return -1
}

// parenSplit 은 from 이후 첫 '(' 에서 시작하는 괄호 그룹을 top-level 콤마로 분할한
// 각 요소(토큰열)를 반환한다. 균형 괄호가 없으면 nil.
func parenSplit(toks []token, from int) [][]token {
	open := -1
	for i := from; i < len(toks); i++ {
		if toks[i].kind == tokSym && toks[i].text == "(" {
			open = i
			break
		}
	}
	if open < 0 {
		return nil
	}
	var elems [][]token
	var cur []token
	depth := 0
	for i := open; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokSym && t.text == "(" {
			depth++
			if depth == 1 {
				continue // 바깥 여는 괄호 skip
			}
		}
		if t.kind == tokSym && t.text == ")" {
			depth--
			if depth == 0 {
				elems = append(elems, cur)
				return elems
			}
		}
		if depth == 1 && t.kind == tokSym && t.text == "," {
			elems = append(elems, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	return nil // 불균형
}

// lastIdent 는 요소 토큰열의 마지막 식별자 텍스트를 반환한다 (`t.col` → "col").
func lastIdent(elem []token) string {
	name := ""
	for _, t := range elem {
		if t.kind == tokIdent {
			name = t.text
		}
	}
	return name
}

var _ RouteKeyExtractor = parserExtractor{}
