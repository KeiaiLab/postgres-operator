/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"slices"
	"testing"
)

func TestReachable(t *testing.T) {
	cases := []struct {
		name                      string
		primary                   string
		edges                     map[string]map[string]string
		wantConnected, wantStream []string
	}{
		{"직결", "p-0", map[string]map[string]string{"p-0": {"p-1": "streaming", "p-2": "streaming"}},
			[]string{"p-1", "p-2"}, []string{"p-1", "p-2"}},
		{"연쇄 — switchover 뒤 옛 primary 를 따라가는 standby", "p-1", map[string]map[string]string{
			"p-1": {"p-0": "streaming"}, "p-0": {"p-2": "streaming"}},
			[]string{"p-0", "p-2"}, []string{"p-0", "p-2"}},
		{"catchup 은 연결 · streaming 아님", "p-0", map[string]map[string]string{"p-0": {"p-1": "catchup", "p-2": "streaming"}},
			[]string{"p-1", "p-2"}, []string{"p-2"}},
		{"catchup 뒤 연쇄는 streaming 으로 못 닿는다", "p-0", map[string]map[string]string{
			"p-0": {"p-1": "catchup"}, "p-1": {"p-2": "streaming"}},
			[]string{"p-1", "p-2"}, nil},
		{"모르는 상태 · 딴 뿌리 · 고리", "p-0", map[string]map[string]string{
			"p-0": {"p-1": "startup", "p-2": "streaming"}, "p-2": {"p-0": "streaming"}, "p-9": {"p-1": "streaming"}},
			[]string{"p-2"}, []string{"p-2"}},
		{"없음", "p-0", nil, nil, nil},
	}
	for _, c := range cases {
		connected, streaming := Reachable(c.primary, c.edges)
		if !slices.Equal(connected, c.wantConnected) || !slices.Equal(streaming, c.wantStream) {
			t.Fatalf("%s: 연결 %v · streaming %v(기대 %v · %v)", c.name, connected, streaming, c.wantConnected, c.wantStream)
		}
	}
}
