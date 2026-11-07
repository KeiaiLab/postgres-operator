/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"slices"
	"testing"
	"time"
)

func TestPlacementJudge(t *testing.T) {
	runRed(t, CheckPlacement, []redCase{
		{"W4 에 멤버", ScenarioS0, func(_, a *ClusterSnap) { a.Placement.Nodes[podP2] = "w4" }},
		{"적격 라벨이 붙은 W4", ScenarioS0, func(_, a *ClusterSnap) {
			a.Placement.Eligible = append(a.Placement.Eligible, "w4")
			a.Placement.Nodes[podP2] = "w4"
		}},
		{"두 멤버 한 노드", ScenarioS0, func(_, a *ClusterSnap) { a.Placement.Nodes[podP2] = "w1" }},
		{"적격 밖 노드", ScenarioS1, func(_, a *ClusterSnap) { a.Placement.Nodes[podP1] = "infra" }},
		{"노드 미관측", ScenarioS1, func(_, a *ClusterSnap) { a.Placement.Nodes[podP1] = "" }},
		{"멤버 모자람", ScenarioS5, func(_, a *ClusterSnap) { delete(a.Placement.Nodes, podP2) }},
		{"배치 미관측", ScenarioS5, func(_, a *ClusterSnap) { a.Placement = PlacementRecord{} }},
	})
}

func TestOrderJudge(t *testing.T) {
	runRed(t, CheckOrder, []redCase{
		{"교체 모자람", ScenarioS8, func(_, a *ClusterSnap) {
			a.Rollout.Steps = []RolloutStep{a.Rollout.Steps[0], a.Rollout.Steps[2]}
		}},
		{"switchover 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.SwitchDone = time.Time{} }},
		{"statefulset-controller 가 지움", ScenarioS8, func(_, a *ClusterSnap) {
			a.Rollout.Steps[0].By = "system:serviceaccount:kube-system:statefulset-controller"
		}},
		{"둘을 한꺼번에", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.Steps[1].DeletedAt = at(3 * time.Minute) }},
		{"교체 뒤 복귀 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.Steps[0].ReadyAt = time.Time{} }},
		{"서수 낮은 것 먼저", ScenarioS8, func(_, a *ClusterSnap) {
			a.Rollout.Steps[0].Pod, a.Rollout.Steps[1].Pod = podP1, podP2
		}},
		{"끊긴 replica 가 나중", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.Steps[1].Detached = true }},
		{"대상 밖 streaming 아님", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.Steps[1].Streaming = false }},
		{"replica 교체가 switchover 뒤", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.SwitchStart = at(4*time.Minute + 30*time.Second) }},
		{"switchover 가 replica 복귀 전", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.SwitchStart = at(6 * time.Minute) }},
		{"마지막이 옛 primary 아님", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.OldPrimary = "p-9" }},
		{"옛 primary 를 replica 처럼 먼저 교체", ScenarioS8, func(_, a *ClusterSnap) {
			a.Rollout.Steps[0].Pod, a.Rollout.Steps[0].Detached = "p-0", true
		}},
		{"옛 primary 가 먼저", ScenarioS0, func(_, a *ClusterSnap) { a.Rollout.OldPrimary = "p-2" }},
		{"옛 primary 가 switchover 완료 전", ScenarioS0, func(_, a *ClusterSnap) { a.Rollout.SwitchDone = at(9*time.Minute + 30*time.Second) }},
	})

	// 끊긴 replica 는 서수와 무관하게 먼저다
	before, after := switchoverPair()
	after.Rollout.Steps[0].Pod, after.Rollout.Steps[1].Pod = podP1, podP2
	after.Rollout.Steps[0].Detached = true
	wantPass(t, judgeGreen(ScenarioS8, before, after), CheckOrder)
}

// kubelet 의 종료 확인 delete 는 교체가 아니다 — 오퍼레이터의 진짜 두 번째 교체는 센다.
func TestReplacements(t *testing.T) {
	const kubelet = "system:node:w2"
	before, after := switchoverPair()
	steps := after.Rollout.Steps

	// 교체마다 kubelet 확인 delete 가 1 s 뒤에 붙었다(kind 실측 — 교체 6)
	var doubled []RolloutStep
	for _, s := range steps {
		echo := s
		echo.DeletedAt, echo.By = s.DeletedAt.Add(time.Second), kubelet
		doubled = append(doubled, s, echo)
	}
	got := Replacements(doubled)
	if len(got) != len(steps) {
		t.Fatalf("교체 %d(기대 %d): %+v", len(got), len(steps), got)
	}
	after.Rollout.Steps = got
	wantPass(t, judgeGreen(ScenarioS8, before, after), CheckOrder)

	// 같은 파드를 오퍼레이터가 몇 분 사이로 두 번 지웠다 — 둘 다 교체다
	twice := append(slices.Clone(steps), RolloutStep{Pod: "p-0", DeletedAt: at(15 * time.Minute), By: operatorUser})
	if got := Replacements(twice); len(got) != len(steps)+1 {
		t.Fatalf("교체 %d(기대 %d)", len(got), len(steps)+1)
	}
	after.Rollout.Steps = Replacements(twice)
	wantReason(t, judgeGreen(ScenarioS8, before, after), CheckOrder, "교체 4")

	// 오퍼레이터 밖 사람의 delete 는 남아 주체 판정이 붉힌다
	stranger := slices.Clone(steps)
	stranger[0].By = "kubernetes-admin"
	after.Rollout.Steps = Replacements(stranger)
	wantReason(t, judgeGreen(ScenarioS8, before, after), CheckOrder, "오퍼레이터 아님")
}

// 복귀 시각은 API 의 Ready 전이 시각이다 — 표본 간격만큼 늦은 관측이 "하나씩"을 거짓으로 붉히지 않는다.
func TestReturnedAt(t *testing.T) {
	sampled := at(5 * time.Minute)
	cases := []struct {
		name                 string
		readySince, unlinked time.Time
		want                 time.Time
	}{
		{"Ready 전이 시각", at(4*time.Minute + 56*time.Second), at(4*time.Minute + 50*time.Second), at(4*time.Minute + 56*time.Second)},
		{"전이 기록 없음 — 표본", time.Time{}, at(4*time.Minute + 50*time.Second), sampled},
		{"전이 뒤에도 끊겨 보임 — 끊긴 마지막 표본", at(4*time.Minute + 40*time.Second), at(4*time.Minute + 50*time.Second), at(4*time.Minute + 50*time.Second)},
		{"표본보다 늦은 전이 — 표본", at(6 * time.Minute), time.Time{}, sampled},
	}
	for _, c := range cases {
		if got := ReturnedAt(c.readySince, c.unlinked, sampled); !got.Equal(c.want) {
			t.Fatalf("%s: %s(기대 %s)", c.name, got, c.want)
		}
	}
}

func TestOnDeleteJudge(t *testing.T) {
	runRed(t, CheckOnDelete, []redCase{
		{"RollingUpdate", ScenarioS1, func(_, a *ClusterSnap) { a.UpdateStrategy = "RollingUpdate" }},
		{"전략 미관측", ScenarioS5, func(_, a *ClusterSnap) { a.UpdateStrategy = "" }},
		{"statefulset-controller 삭제", ScenarioS0, func(_, a *ClusterSnap) { a.AuditStsDeletes = 1 }},
	})
}

func TestConvergeJudge(t *testing.T) {
	runRed(t, CheckConverge, []redCase{
		{"수렴 미관측", ScenarioS3, func(_, a *ClusterSnap) { a.ConvergedAt = time.Time{} }},
		{"풀림 미관측", ScenarioS3, func(_, a *ClusterSnap) { a.ReleasedAt = time.Time{} }},
		{"수렴 상한 초과", ScenarioS6, func(_, a *ClusterSnap) { a.ConvergedAt = a.ReleasedAt.Add(ConvergeBound + time.Second) }},
		{"롤아웃 상한 초과", ScenarioS0, func(_, a *ClusterSnap) {
			a.ConvergedAt = a.ReleasedAt.Add(ConvergeBound + RolloutBound(fixtureMembers) + time.Second)
		}},
		{"창 끝에도 막힘", ScenarioS5, func(_, a *ClusterSnap) { a.Rollout.BlockedAtEnd = true }},
		{"S8 막힘 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Rollout.BlockedSeen = false }},
		{"S8 롤아웃 미완", ScenarioS8, func(_, a *ClusterSnap) { a.PodsOnUpdate = fixtureMembers - 1 }},
	})
}

func TestPodOrdinal(t *testing.T) {
	cases := map[string]int{"postgres-prod-shard-0-2": 2, "p-10": 10, "nodash": -1, "p-x": -1}
	for pod, want := range cases {
		if got := podOrdinal(pod); got != want {
			t.Fatalf("podOrdinal(%q) = %d(기대 %d)", pod, got, want)
		}
	}
}
