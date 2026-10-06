/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"strings"
	"testing"
)

// TestSourceShardPodDNS_OrdinalBounds 는 shard ordinal 이 int32 범위를 벗어나면
// 잘라서 엉뚱한 pod 를 가리키지 않고 에러를 내는지 검증한다.
func TestSourceShardPodDNS_OrdinalBounds(t *testing.T) {
	tests := []struct {
		shard   string
		wantErr bool
		wantPod string
	}{
		{shard: "shard-0", wantPod: "c-shard-0-0."},
		{shard: "shard-2147483647", wantPod: "c-shard-2147483647-0."},
		{shard: "shard-2147483648", wantErr: true},
		{shard: "shard-4294967296", wantErr: true},
		{shard: "shard-x", wantErr: true},
		{shard: "s-1", wantErr: true},
	}
	for _, tt := range tests {
		got, err := sourceShardPodDNS("c", "ns", tt.shard)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", tt.shard, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tt.shard, err)
			continue
		}
		if !strings.HasPrefix(got, tt.wantPod) {
			t.Errorf("%s: got %q, want prefix %q", tt.shard, got, tt.wantPod)
		}
	}
}
