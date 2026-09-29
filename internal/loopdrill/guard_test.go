/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// kubeconfigFor 는 cluster 서버들과 현재 context 로 kubeconfig 를 만든다(첫 cluster 가 context 대상).
func kubeconfigFor(current string, servers ...string) []byte {
	var raw strings.Builder
	raw.WriteString("apiVersion: v1\nkind: Config\ncurrent-context: " + current + "\nclusters:\n")
	for i, s := range servers {
		fmt.Fprintf(&raw, "- name: c%d\n  cluster:\n    server: %s\n    certificate-authority-data: AAAA\n", i, s)
	}
	raw.WriteString("contexts:\n- name: kind-loopdrill\n  context:\n    cluster: c0\n    user: u0\n")
	raw.WriteString("users:\n- name: u0\n  user:\n    client-certificate-data: AAAA\n")
	return []byte(raw.String())
}

func TestCheckKubeconfig(t *testing.T) {
	green := map[string][]byte{
		"kind 127.0.0.1": kubeconfigFor("kind-loopdrill", "https://127.0.0.1:43215"),
		"localhost":      kubeconfigFor("kind-loopdrill", "https://localhost:6443"),
		"IPv6 루프백":       kubeconfigFor("kind-loopdrill", "https://[::1]:6443"),
	}
	for name, raw := range green {
		if err := CheckKubeconfig(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// 운영으로 바뀐 가짜 kubeconfig — 훈련장이 멈춰야 한다
	red := map[string][]byte{
		"운영 서버":         kubeconfigFor("kind-loopdrill", "https://kube.keiailab.com:6443"),
		"운영 사설 IP":      kubeconfigFor("kind-loopdrill", "https://10.31.10.21:6443"),
		"루프백 + 운영 섞임":   kubeconfigFor("kind-loopdrill", "https://127.0.0.1:43215", "https://kube.keiailab.com:6443"),
		"http":          kubeconfigFor("kind-loopdrill", "http://127.0.0.1:43215"),
		"현재 context 없음": kubeconfigFor("", "https://127.0.0.1:43215"),
		"현재 context 모름": kubeconfigFor("keiailab", "https://127.0.0.1:43215"),
		"cluster 없음":    []byte("apiVersion: v1\nkind: Config\ncurrent-context: kind-loopdrill\n"),
		"context 대상 없음": []byte("current-context: x\nclusters:\n- name: a\n  cluster:\n    server: https://127.0.0.1:1\ncontexts:\n- name: x\n  context:\n    cluster: b\n"),
		"깨진 YAML":       []byte("clusters: [\n"),
		"서버 URL 깨짐":     kubeconfigFor("kind-loopdrill", "https://%zz"),
		"서버 비어 있음":      kubeconfigFor("kind-loopdrill", ""),
		"루프백 흉내 도메인":    kubeconfigFor("kind-loopdrill", "https://127.0.0.1.nip.io:6443"),
	}
	for name, raw := range red {
		if err := CheckKubeconfig(raw); err == nil {
			t.Fatalf("%s: 오류 없음", name)
		}
	}
}

func TestUnpulled(t *testing.T) {
	nodes := map[string][]string{
		"w1": {"op:a", "pg:b"},
		"w2": {"pg:b"},
		"w3": {},
	}
	got := Unpulled(nodes, []string{"op:a", "pg:b"})
	want := []string{"w2: op:a", "w3: op:a", "w3: pg:b"}
	if !slices.Equal(got, want) {
		t.Fatalf("선풀 안 됨 %v(기대 %v)", got, want)
	}
	if Unpulled(map[string][]string{"w1": {"op:a"}}, []string{"op:a"}) != nil {
		t.Fatal("다 있는데 모자람")
	}
}
