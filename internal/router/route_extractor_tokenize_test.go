/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"reflect"
	"testing"
)

// TestTokenize_Characterization 은 tokenize 의 현재 토큰열을 고정한다 — 주석·따옴표
// 이스케이프·dollar-quote·숫자·연산자 묶음·미종료 입력이 리팩터 뒤에도 같아야 한다.
func TestTokenize_Characterization(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []token
	}{
		{
			name:  "comments quotes ops",
			query: "SELECT \"A\"\"b\", t.x -- c\n/* b */ FROM t WHERE v<=3.5 AND s='it''s';",
			want: []token{
				{tokIdent, "SELECT"}, {tokIdent, "A\"b"}, {tokSym, ","},
				{tokIdent, "t"}, {tokSym, "."}, {tokIdent, "x"},
				{tokIdent, "FROM"}, {tokIdent, "t"}, {tokIdent, "WHERE"},
				{tokIdent, "v"}, {tokSym, "<="}, {tokNum, "3.5"},
				{tokIdent, "AND"}, {tokIdent, "s"}, {tokSym, "="}, {tokStr, "it's"},
				{tokSym, ";"},
			},
		},
		{
			name:  "dollar quote and param",
			query: "SELECT $tag$ x = 'y' $tag$, a$1 FROM f($1) WHERE k != $2 @",
			want: []token{
				{tokIdent, "SELECT"}, {tokStr, " x = 'y' "}, {tokSym, ","},
				{tokIdent, "a$1"}, {tokIdent, "FROM"}, {tokIdent, "f"},
				{tokSym, "("}, {tokSym, "$"}, {tokNum, "1"}, {tokSym, ")"},
				{tokIdent, "WHERE"}, {tokIdent, "k"}, {tokSym, "!="},
				{tokSym, "$"}, {tokNum, "2"}, {tokSym, "@"},
			},
		},
		{
			name:  "unterminated",
			query: "x = 'open /* never",
			want:  []token{{tokIdent, "x"}, {tokSym, "="}, {tokStr, "open /* never"}},
		},
		{
			name:  "unterminated block comment",
			query: "a /* tail",
			want:  []token{{tokIdent, "a"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenize(tc.query)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokenize(%q)\n got  %v\n want %v", tc.query, got, tc.want)
			}
		})
	}
}
