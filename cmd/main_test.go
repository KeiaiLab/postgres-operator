/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestManagerSkipsNodeCache 는 Node 가 캐시 밖(live GET)으로 읽히는지 본다.
// 캐시 경유면 첫 Get 이 Node informer 를 띄우고, nodes list 권한이 없는 SA 에서
// WaitForCacheSync 가 영구 대기해 컨트롤러 전체가 멈춘다(2026-09-24 정지).
func TestManagerSkipsNodeCache(t *testing.T) {
	opts := managerClientOptions()
	if opts.Cache == nil {
		t.Fatal("Cache 옵션 없음 — Node 가 캐시로 읽힌다")
	}

	for _, obj := range opts.Cache.DisableFor {
		if _, ok := obj.(*corev1.Node); ok {
			return
		}
	}
	t.Fatalf("Cache.DisableFor 에 *corev1.Node 없음: %v", opts.Cache.DisableFor)
}
