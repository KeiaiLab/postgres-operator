/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"math"
	"testing"
)

// TestToInt64_UnsignedOverflow 는 int64 를 넘는 부호 없는 값을 거부하는지 본다 —
// wrap 된 음수가 count/sum 에 섞이면 안 된다.
func TestToInt64_UnsignedOverflow(t *testing.T) {
	for _, v := range []any{uint64(math.MaxUint64), uint(math.MaxUint), uint64(math.MaxInt64) + 1} {
		if got, ok := toInt64(v); ok {
			t.Errorf("toInt64(%v) = %d, true; want rejected", v, got)
		}
	}
	for _, v := range []any{uint64(math.MaxInt64), uint(7)} {
		if _, ok := toInt64(v); !ok {
			t.Errorf("toInt64(%v) rejected an in-range value", v)
		}
	}
}
