/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import "math"

// clampInt32 는 int64 를 int32 범위로 포화시킨다 — k8s API 의 int32 필드에 담을 때
// wrap 대신 경계값을 쓴다(예: 1<<40 → MaxInt32).
func clampInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
