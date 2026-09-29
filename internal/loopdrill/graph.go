/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import "slices"

// 복제 그래프 — 멤버마다 pg_stat_replication(송신 → 수신, state)을 모아 primary 에서 닿는 멤버를 센다.
// 연쇄 standby(standby 가 보내는 WAL)도 연결이다(설계 §3.4) — switchover 뒤 한 standby 가 옛 primary(이제 standby)를
// 따라가는 것은 정상이고 J9 가 그것을 끊김으로 읽으면 안 된다.
//
//	primary ─streaming─▶ p-1 ─streaming─▶ p-2      Connected = {p-1, p-2} · Streaming = {p-1, p-2}
//	primary ─catchup──▶ p-1                         Connected = {p-1}      · Streaming = {}

// StateStreaming 은 따라잡은 복제 상태다.
const StateStreaming = "streaming"

// connectedStates 는 연결로 치는 상태 — backup 은 basebackup 중(진척 아님, 설계 §3.4).
var connectedStates = []string{StateStreaming, "catchup", "backup"}

// Reachable 은 primary 에서 간선을 따라 닿는 멤버 — edges[송신][수신] = state.
// connected 는 연결 상태 전부로, streaming 은 streaming 간선만으로 닿는 멤버다(둘 다 이름순, primary 제외).
func Reachable(primary string, edges map[string]map[string]string) (connected, streaming []string) {
	connected = walk(primary, edges, connectedStates)
	streaming = walk(primary, edges, []string{StateStreaming})
	return connected, streaming
}

// walk 는 states 간선만 따라 primary 에서 닿는 멤버다(너비 우선, 고리 안전).
func walk(primary string, edges map[string]map[string]string, states []string) []string {
	seen := map[string]bool{primary: true}
	queue := []string{primary}
	var reached []string
	for len(queue) > 0 {
		sender := queue[0]
		queue = queue[1:]
		for receiver, state := range edges[sender] {
			if seen[receiver] || !slices.Contains(states, state) {
				continue
			}
			seen[receiver] = true
			reached = append(reached, receiver)
			queue = append(queue, receiver)
		}
	}
	slices.Sort(reached)
	return reached
}
