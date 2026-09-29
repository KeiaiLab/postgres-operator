/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"

	"sigs.k8s.io/yaml"
)

// 훈련장 오조준 차단(설계 FX-DR1b 안전 개정) — 드라이버의 kubectl·helm 은 kind 가 만든 kubeconfig 만 쓴다.
// 그 파일이 루프백(kind API 포트 매핑)이 아닌 서버를 가리키면 운영을 칠 수 있다 — 훈련 시작과 파괴 동작
// (docker stop/pause · SIGSTOP · 파드 삭제) 직전마다 이 판정을 본다.

// localhostName 은 루프백으로 치는 이름이다(IP 는 net.IP.IsLoopback).
const localhostName = "localhost"

// httpsScheme 는 kind API 서버의 스킴이다.
const httpsScheme = "https"

type kubeconfig struct {
	CurrentContext string         `json:"current-context"`
	Clusters       []namedCluster `json:"clusters"`
	Contexts       []namedContext `json:"contexts"`
}

type namedCluster struct {
	Name    string `json:"name"`
	Cluster struct {
		Server string `json:"server"`
	} `json:"cluster"`
}

type namedContext struct {
	Name    string `json:"name"`
	Context struct {
		Cluster string `json:"cluster"`
	} `json:"context"`
}

// CheckKubeconfig 는 kubeconfig 의 모든 cluster 서버가 루프백이고 현재 context 가 그중 하나인지 본다 — 아니면 오류.
func CheckKubeconfig(raw []byte) error {
	var cfg kubeconfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("kubeconfig 해석: %w", err)
	}
	if len(cfg.Clusters) == 0 {
		return errors.New("kubeconfig 에 cluster 없음")
	}
	for _, c := range cfg.Clusters {
		if err := loopbackServer(c.Cluster.Server); err != nil {
			return fmt.Errorf("cluster %q: %w", c.Name, err)
		}
	}

	// 현재 context 가 위 cluster 를 가리켜야 한다 — 비었거나 없는 이름이면 kubectl 이 다른 것을 고를 수 있다
	i := slices.IndexFunc(cfg.Contexts, func(c namedContext) bool { return c.Name == cfg.CurrentContext })
	if cfg.CurrentContext == "" || i < 0 {
		return fmt.Errorf("current-context %q 가 contexts 에 없다", cfg.CurrentContext)
	}
	target := cfg.Contexts[i].Context.Cluster
	if !slices.ContainsFunc(cfg.Clusters, func(c namedCluster) bool { return c.Name == target }) {
		return fmt.Errorf("context %q 의 cluster %q 가 clusters 에 없다", cfg.CurrentContext, target)
	}
	return nil
}

// loopbackServer 는 서버 URL 이 https 루프백인지 본다.
func loopbackServer(server string) error {
	u, err := url.Parse(server)
	if err != nil {
		return fmt.Errorf("서버 %q: %w", server, err)
	}
	if u.Scheme != httpsScheme {
		return fmt.Errorf("서버 %q 가 https 아님", server)
	}
	host := u.Hostname()
	if host == localhostName {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("서버 %q 가 kind 루프백 아님 — 운영일 수 있다", server)
}

// Unpulled 는 노드마다 status.images 에 없는 이미지다("노드: 이미지") — 선풀 판정(설계 §3.13 선풀).
func Unpulled(nodes map[string][]string, want []string) []string {
	var missing []string
	for _, node := range sortedNodes(nodes) {
		for _, image := range want {
			if !slices.Contains(nodes[node], image) {
				missing = append(missing, node+": "+image)
			}
		}
	}
	return missing
}

func sortedNodes(nodes map[string][]string) []string {
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
