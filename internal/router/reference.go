/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — reference.go 는 *reference table* 라우팅을 지원한다. reference
// table 은 모든 샤드에 복제된 작은 공통 테이블이라, 그것만 참조하는 쿼리는 샤딩 키가
// 없어도 임의 샤드로 보낼 수 있다(분산 조인 우회). 본 파일은 쿼리의 테이블 추출과
// reference-only 판정, 임의 샤드 선택을 제공한다 — (E) 쿼리 라우팅에서 결선.
package router

import (
	"fmt"
	"strings"
)

// ExtractTables 는 query 에서 FROM/JOIN/INTO/UPDATE 뒤의 테이블 이름을 추출한다
// (토크나이저 기반 best-effort; schema 한정 `s.t` → "t"). 중복 제거, 등장 순서 유지.
func ExtractTables(query string) []string {
	toks := tokenize(query)
	var out []string
	seen := map[string]bool{}
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].kind != tokIdent {
			continue
		}
		switch strings.ToLower(toks[i].text) {
		case "from", "join", "into", "update":
			if name := tableNameAt(toks, i+1); name != "" {
				key := strings.ToLower(name)
				if !seen[key] {
					seen[key] = true
					out = append(out, name)
				}
			}
		}
	}
	return out
}

// tableNameAt 는 i 위치의 테이블 이름을 반환한다. `schema . table` 이면 table 부분.
func tableNameAt(toks []token, i int) string {
	if i >= len(toks) || toks[i].kind != tokIdent {
		return ""
	}
	name := toks[i].text
	if i+2 < len(toks) && toks[i+1].kind == tokSym && toks[i+1].text == "." && toks[i+2].kind == tokIdent {
		name = toks[i+2].text
	}
	return name
}

// IsReferenceTable 는 name 이 이 토폴로지의 reference 테이블인지 (대소문자 무시) 본다.
func (t Topology) IsReferenceTable(name string) bool {
	for _, r := range t.Spec.ReferenceTables {
		if strings.EqualFold(r, name) {
			return true
		}
	}
	return false
}

// ReferenceOnly 는 query 가 참조하는 테이블이 1개 이상이고 *전부 reference 테이블*인지
// 판정한다 (그렇다면 샤딩 키 없이 AnyShard 로 라우팅 가능). 테이블을 못 뽑으면 false
// (보수적 — 일반 라우팅 경로로).
func (t Topology) ReferenceOnly(query string) bool {
	tables := ExtractTables(query)
	if len(tables) == 0 {
		return false
	}
	for _, tbl := range tables {
		if !t.IsReferenceTable(tbl) {
			return false
		}
	}
	return true
}

// AnyShard 는 reference-only 쿼리를 보낼 임의(결정적: 첫) 샤드를 반환한다.
func (t Topology) AnyShard() (string, error) {
	if len(t.Spec.Ranges) == 0 {
		return "", fmt.Errorf("router: no shards in topology")
	}
	return t.Spec.Ranges[0].Shard, nil
}
