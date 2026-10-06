/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"strconv"
	"testing"
)

// FuzzParseHashBound 는 ShardRange 의 사용자 입력 bound 해석이 임의 문자열에
// panic 하지 않고, 해석된 값이 hex·10진 재직렬화 후 같은 값으로 돌아오는지 본다.
func FuzzParseHashBound(f *testing.F) {
	for _, seed := range []string{
		"0", "0x00000000", "0x7fffffff", "0xffffffff", "ffffffff",
		"4294967295", "4294967296", "0x100000000", "-1", "", "0x", "zz",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		v, err := parseHashBound(s)
		if err != nil {
			return
		}

		// 재직렬화 왕복 — 예: 0xffffffff → "0xffffffff" / "4294967295" → 0xffffffff.
		hex := "0x" + strconv.FormatUint(uint64(v), 16)
		dec := strconv.FormatUint(uint64(v), 10)
		for _, in := range []string{hex, dec} {
			got, err := parseHashBound(in)
			if err != nil || got != v {
				t.Fatalf("%q → %#x, round-trip %q → %#x (err %v)", s, v, in, got, err)
			}
		}
	})
}
