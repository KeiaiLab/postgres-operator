/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 배치·단계화 롤아웃 판정 — J11 배치 · J12 순서 · J13 OnDelete · J14 수렴 회복(설계 §3.14·§3.16).
//
//	옛 replica(끊긴 것 → 서수 높은 것) 하나씩 ─▶ switchover ─▶ 옛 primary 교체
//	각 삭제 직전: 대상 밖 replica 전부 streaming · 앞 교체가 새 revision 으로 Ready ∧ 연결

// OnDeleteStrategy 는 STS updateStrategy.type — STS 컨트롤러는 멤버를 재시작하지 않는다(설계 §3.16).
const OnDeleteStrategy = "OnDelete"

// PlacementRecord 는 창 끝 멤버 배치와 노드 분류(J11).
type PlacementRecord struct {
	Nodes    map[string]string `json:"nodes,omitempty"`    // 파드 → 노드
	Eligible []string          `json:"eligible,omitempty"` // 적격 노드(control-plane ∨ etcd 라벨)
	Avoid    []string          `json:"avoid,omitempty"`    // W4(kured 역) — 멤버가 서면 안 되는 노드
}

// RolloutStep 은 단계화 롤아웃의 멤버 교체 하나 — 감사 로그의 pod delete 와 표본으로 만든다.
type RolloutStep struct {
	Pod       string    `json:"pod"`
	DeletedAt time.Time `json:"deletedAt"`
	By        string    `json:"by"`               // 감사 로그 사용자
	Detached  bool      `json:"detached"`         // 지울 때 그래프에서 끊겼나
	Streaming bool      `json:"streaming"`        // 지울 때 대상 밖 replica 가 전부 streaming 이었나
	ReadyAt   time.Time `json:"readyAt,omitzero"` // 새 revision 으로 Ready ∧ 연결된 시각
}

// RolloutRecord 는 단계화 롤아웃 기록(J12 · J14).
type RolloutRecord struct {
	Operator     string        `json:"operator,omitempty"`   // 삭제 주체여야 할 오퍼레이터 SA 사용자
	OldPrimary   string        `json:"oldPrimary,omitempty"` // 롤아웃 전 primary
	Steps        []RolloutStep `json:"steps,omitempty"`
	SwitchStart  time.Time     `json:"switchStart,omitzero"` // SwitchoverStarted
	SwitchDone   time.Time     `json:"switchDone,omitzero"`  // SwitchoverCompleted
	BlockedSeen  bool          `json:"blockedSeen"`          // RolloutBlocked=True 를 봤나
	BlockedAtEnd bool          `json:"blockedAtEnd"`         // 창 끝에도 RolloutBlocked=True
}

// J11 — 멤버 = Members ∧ 노드가 서로 다름 ∧ 전부 적격 ∧ W4 0.
func judgePlacement(_, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckPlacement}
	pl := after.Placement
	if len(pl.Nodes) != exp.Members {
		check.Detail = fmt.Sprintf("멤버 %d(기대 %d)", len(pl.Nodes), exp.Members)
		return check
	}
	pods := sortedKeys(pl.Nodes)
	holder := map[string]string{}
	for _, pod := range pods {
		node := pl.Nodes[pod]
		switch {
		case node == "":
			check.Detail = pod + " 노드 미관측"
			return check
		case slices.Contains(pl.Avoid, node):
			check.Detail = fmt.Sprintf("%s 가 W4(%s)에", pod, node)
			return check
		case !slices.Contains(pl.Eligible, node):
			check.Detail = fmt.Sprintf("%s 가 적격 밖 노드 %s 에", pod, node)
			return check
		}
		if other, taken := holder[node]; taken {
			check.Detail = fmt.Sprintf("%s·%s 가 같은 노드 %s", other, pod, node)
			return check
		}
		holder[node] = pod
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("멤버 %d 가 서로 다른 적격 노드", len(pods))
	return check
}

// J12 — 교체 = Members ∧ 삭제 주체 오퍼레이터 ∧ 하나씩(앞 교체 복귀 뒤) ∧ 옛 replica(끊긴 것 → 서수 높은 것) →
// switchover → 옛 primary 순 ∧ replica 삭제 직전 대상 밖 streaming.
func judgeOrder(_, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckOrder}
	r := after.Rollout
	if len(r.Steps) != exp.Members {
		check.Detail = fmt.Sprintf("교체 %d(기대 %d)", len(r.Steps), exp.Members)
		return check
	}
	if r.SwitchStart.IsZero() || r.SwitchDone.IsZero() {
		check.Detail = "switchover 시작·완료 미관측"
		return check
	}
	steps := slices.Clone(r.Steps)
	slices.SortFunc(steps, func(a, b RolloutStep) int { return a.DeletedAt.Compare(b.DeletedAt) })

	// 모든 교체 — 주체 · 복귀 · 하나씩
	for i := range steps {
		if detail := stepFault(steps, i, r.Operator); detail != "" {
			check.Detail = detail
			return check
		}
	}

	// 옛 replica 단계 — 대상 밖 streaming · 끊긴 것 → 서수 높은 것 · 전부 복귀한 뒤 switchover
	replicas, last := steps[:len(steps)-1], steps[len(steps)-1]
	for _, s := range replicas {
		switch {
		case s.Pod == r.OldPrimary:
			check.Detail = "옛 primary " + s.Pod + " 가 replica 단계에서 교체"
			return check
		case !s.Streaming:
			check.Detail = s.Pod + " 삭제 직전 대상 밖 replica 가 streaming 아님"
			return check
		}
	}
	if !replicaOrder(replicas) {
		check.Detail = fmt.Sprintf("replica 순서 %v(끊긴 것 → 서수 높은 것)", podsOf(replicas))
		return check
	}
	if len(replicas) > 0 && r.SwitchStart.Before(replicas[len(replicas)-1].ReadyAt) {
		check.Detail = "switchover 가 마지막 replica 복귀 전"
		return check
	}

	// 옛 primary 단계 — switchover 완료 뒤
	if last.Pod != r.OldPrimary {
		check.Detail = fmt.Sprintf("마지막 교체 %s(옛 primary %s 아님)", last.Pod, r.OldPrimary)
		return check
	}
	if last.DeletedAt.Before(r.SwitchDone) {
		check.Detail = "옛 primary 교체가 switchover 완료 전"
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("%v → switchover → %s", podsOf(replicas), last.Pod)
	return check
}

// stepFault 는 교체 i 의 공통 결함 — 오퍼레이터 밖 삭제 · 복귀 미관측 · 앞 교체 복귀 전 삭제.
func stepFault(steps []RolloutStep, i int, operator string) string {
	s := steps[i]
	if s.By != operator {
		return fmt.Sprintf("%s 삭제 주체 %q(오퍼레이터 아님)", s.Pod, s.By)
	}
	if s.ReadyAt.IsZero() {
		return s.Pod + " 교체 뒤 Ready·연결 미관측"
	}
	if i > 0 && s.DeletedAt.Before(steps[i-1].ReadyAt) {
		return fmt.Sprintf("%s 삭제가 %s 복귀 전(하나씩 아님)", s.Pod, steps[i-1].Pod)
	}
	return ""
}

// replicaOrder 는 끊긴 replica 가 먼저이고, 나머지는 서수가 내려가는가.
func replicaOrder(steps []RolloutStep) bool {
	attached := false
	last := -1
	for _, s := range steps {
		if s.Detached {
			if attached {
				return false
			}
			continue
		}
		ordinal := podOrdinal(s.Pod)
		if attached && ordinal > last {
			return false
		}
		attached, last = true, ordinal
	}
	return true
}

// J13 — STS updateStrategy = OnDelete ∧ statefulset-controller 의 pod 삭제 0.
func judgeOnDelete(_, after ClusterSnap, _ Expect) Check {
	check := Check{ID: CheckOnDelete}
	if after.UpdateStrategy != OnDeleteStrategy {
		check.Detail = fmt.Sprintf("updateStrategy %q(기대 %s)", after.UpdateStrategy, OnDeleteStrategy)
		return check
	}
	if after.AuditStsDeletes != 0 {
		check.Detail = fmt.Sprintf("statefulset-controller 의 pod 삭제 %d건", after.AuditStsDeletes)
		return check
	}
	check.Pass = true
	check.Detail = OnDeleteStrategy + " · statefulset-controller 삭제 0"
	return check
}

// J14 — 풀림 뒤 ConvergeWithin 안에 수렴 ∧ 창 끝 RolloutBlocked 0 ∧ (S8) 막힘이 섰다가 풀려 롤아웃이 끝났다.
func judgeConverge(_, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckConverge}
	if after.ReleasedAt.IsZero() || after.ConvergedAt.IsZero() {
		check.Detail = fmt.Sprintf("풀림 %s · 수렴 %s", stamp(after.ReleasedAt), stamp(after.ConvergedAt))
		return check
	}
	took := after.ConvergedAt.Sub(after.ReleasedAt)
	if took > exp.ConvergeWithin {
		check.Detail = fmt.Sprintf("풀림 뒤 수렴까지 %s(상한 %s)", took.Round(time.Second), exp.ConvergeWithin)
		return check
	}
	if after.Rollout.BlockedAtEnd {
		check.Detail = "창 끝에도 RolloutBlocked"
		return check
	}
	if exp.RolloutBlock && !after.Rollout.BlockedSeen {
		check.Detail = "RolloutBlocked 미관측(양성 대조)"
		return check
	}
	if exp.RolloutBlock && after.PodsOnUpdate != exp.Members {
		check.Detail = fmt.Sprintf("새 revision Ready 파드 %d(기대 %d)", after.PodsOnUpdate, exp.Members)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("풀림 뒤 수렴까지 %s", took.Round(time.Second))
	return check
}

// podOrdinal 은 STS 파드 이름 끝의 서수다(없으면 -1).
func podOrdinal(pod string) int {
	i := strings.LastIndex(pod, "-")
	n, err := strconv.Atoi(pod[i+1:])
	if err != nil {
		return -1
	}
	return n
}

func podsOf(steps []RolloutStep) []string {
	pods := make([]string, 0, len(steps))
	for _, s := range steps {
		pods = append(pods, s.Pod)
	}
	return pods
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
