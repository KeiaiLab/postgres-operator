/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"math"
	"testing"
)

func TestClampInt32(t *testing.T) {
	cases := map[int64]int32{
		0:                 0,
		-7:                -7,
		math.MaxInt32:     math.MaxInt32,
		math.MaxInt32 + 1: math.MaxInt32,
		math.MinInt32 - 1: math.MinInt32,
		math.MaxInt64:     math.MaxInt32,
	}
	for in, want := range cases {
		if got := clampInt32(in); got != want {
			t.Errorf("clampInt32(%d) = %d, want %d", in, got, want)
		}
	}
}
