/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"testing"
	"time"
)

func TestDataBearingJudge(t *testing.T) {
	runRed(t, CheckDataBearing, []redCase{
		{"창 안 data-bearing 사라짐", ScenarioS1, func(_, a *ClusterSnap) { a.Snapshots[0].Gone = true }},
		{"창 전 data-bearing 사라짐", ScenarioS7, func(b, _ *ClusterSnap) {
			b.Snapshots = []SnapshotMark{{Name: "p-1-rebuild-z", Member: "p-1", Reason: RebuildDemoted, DataBearing: LabelTrue}}
		}},
		{"S6b Demoted 가 false", ScenarioS6b, func(_, a *ClusterSnap) { a.Snapshots[0].DataBearing = LabelFalse }},
		{"S6b Demoted 라벨 없음", ScenarioS6b, func(_, a *ClusterSnap) { a.Snapshots[0].DataBearing = "" }},
		{"S6b 끊긴 쪽 Stale 이 true", ScenarioS6b, func(_, a *ClusterSnap) {
			a.Snapshots = append(a.Snapshots, SnapshotMark{Name: "p-2-rebuild-b", Member: "p-2", Reason: RebuildStale, DataBearing: LabelTrue})
		}},
		{"S6b Demoted 미관측", ScenarioS6b, func(_, a *ClusterSnap) { a.Snapshots = nil; a.Rebuilds = nil }},
		{"S6b VAP 허용", ScenarioS6b, func(_, a *ClusterSnap) { a.VAP = VAPAllowed }},
		{"S6b RBAC 로 막혀 VAP 미시험", ScenarioS6b, func(_, a *ClusterSnap) { a.VAP = VAPForbidden }},
		{"S6b 대상 없음", ScenarioS6b, func(_, a *ClusterSnap) { a.VAP = VAPNoTarget }},
	})

	// 앞 시나리오의 스냅숏은 보존만 본다 — S6 의 Stale(분기점을 넘어 true)이 S6b 를 붉히지 않는다
	before, after := detachedPair()
	older := SnapshotMark{Name: "p-2-rebuild-s6", Member: "p-2", Reason: RebuildStale, DataBearing: LabelTrue}
	before.Snapshots = []SnapshotMark{older}
	after.Snapshots = append(after.Snapshots, older)
	wantPass(t, judgeGreen(ScenarioS6b, before, after), CheckDataBearing)

	// 비 data-bearing 은 GC 돼도 된다
	before, after = quietPair()
	after.Snapshots = []SnapshotMark{{Name: "p-1-rebuild-c", Member: "p-1", Reason: RebuildStale, DataBearing: LabelFalse, Gone: true}}
	wantPass(t, judgeGreen(ScenarioS7, before, after), CheckDataBearing)
}

func TestHBAJudge(t *testing.T) {
	runRed(t, CheckHBA, []redCase{
		{"사전 trust 접속 실패(양성 대조)", ScenarioS0, func(b, _ *ClusterSnap) { b.HBA.PreTrust = ConnAuthRejected }},
		{"사전 trust 미관측", ScenarioS0, func(b, _ *ClusterSnap) { b.HBA.PreTrust = "" }},
		{"network trust 남음", ScenarioS0, func(_, a *ClusterSnap) { a.HBA.Members[1].Trust = 2 }},
		{"적재가 파일보다 이르다", ScenarioS1, func(_, a *ClusterSnap) { a.HBA.Members[2].Fresh = false }},
		{"hba 오류", ScenarioS1, func(_, a *ClusterSnap) { a.HBA.Members[0].Errors = 1 }},
		{"판정 모름", ScenarioS1, func(_, a *ClusterSnap) { a.HBA.Members[0].Trust = -1 }},
		{"멤버 모자람", ScenarioS5, func(_, a *ClusterSnap) { a.HBA.Members = a.HBA.Members[:2] }},
		{"비밀번호 없이 접속", ScenarioS0, func(_, a *ClusterSnap) { a.HBA.NoPassword = ConnAccepted }},
		{"망이 막혀 거부로 오인", ScenarioS0, func(_, a *ClusterSnap) { a.HBA.NoPassword = ConnUnreachable }},
		{"탐침 접속 실패", ScenarioS0, func(_, a *ClusterSnap) { a.HBA.Probe = ConnAuthRejected }},
		{"Secret 접속 실패", ScenarioS0, func(_, a *ClusterSnap) { a.HBA.Superuser = ConnUnknown }},
	})

	// 사전 대조는 S0 만 — 다른 시나리오는 before 를 보지 않는다
	before, after := quietPair()
	before.HBA.PreTrust = ""
	wantPass(t, judgeGreen(ScenarioS5, before, after), CheckHBA)
}

func TestInPlaceJudge(t *testing.T) {
	runRed(t, CheckInPlace, []redCase{
		{"switchover 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover = SwitchoverRecord{} }},
		{"옛 primary 이름 모름", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.OldPrimary = "" }},
		{"복귀 · UID 둘 다 모름", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.OldUID, a.Switchover.BackUID = "", "" }},
		{"완료 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.Completed = false }},
		{"복귀 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.BackUID = "" }},
		{"파드 재생성", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.BackUID = "uid-new" }},
		{"재시작 0", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.BackRestarts = 0 }},
		{"재시작 2", ScenarioS0, func(_, a *ClusterSnap) { a.Switchover.BackRestarts = 2 }},
		{"primary 로 돌아옴", ScenarioS0, func(_, a *ClusterSnap) { a.Switchover.BackInRecovery = false }},
		{"되감기", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.RewindLines = 1 }},
		{"분기점 = 끝", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.Fork = a.Switchover.OldEnd }},
		{"분기점 < 끝", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.Fork = "0/2FFFFFF" }},
		{"끝 미관측", ScenarioS8, func(_, a *ClusterSnap) { a.Switchover.OldEnd = "" }},
		{"옛 primary 스냅숏", ScenarioS8, func(_, a *ClusterSnap) {
			a.Snapshots = []SnapshotMark{{Name: "p-0-rebuild-x", Member: "p-0", Reason: RebuildDemoted, DataBearing: LabelTrue}}
		}},
		{"옛 primary 재구축", ScenarioS0, func(_, a *ClusterSnap) {
			a.Rebuilds = []RebuildMark{{Member: "p-0", ReleasedAt: at(9 * time.Minute)}}
		}},
	})

	// 창 전 스냅숏은 이 switchover 의 재구축이 아니다
	before, after := switchoverPair()
	old := SnapshotMark{Name: "p-0-rebuild-old", Member: "p-0", Reason: RebuildDemoted, DataBearing: LabelTrue}
	before.Snapshots = []SnapshotMark{old}
	after.Snapshots = []SnapshotMark{old}
	wantPass(t, judgeGreen(ScenarioS8, before, after), CheckInPlace)
}

func TestEmergencyJudge(t *testing.T) {
	runRed(t, CheckEmergency, []redCase{
		{"Stopping 미관측", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.StoppingAt = time.Time{} }},
		{"하나만 세움", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.Frozen = 1 }},
		{"오퍼레이터 하나뿐", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.Operators, a.Emergency.Frozen = 1, 1 }},
		{"hold 실패", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.HoldExit = 1 }},
		{"primary 둘(exit 3)", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.RecoverExit = 3 }},
		{"recover 시각 미관측", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.RecoverAt = time.Time{} }},
		{"Endpoints 늦음", ScenarioS10, func(_, a *ClusterSnap) {
			a.Emergency.EndpointsAt = a.Emergency.RecoverAt.Add(SwitchoverBudget + time.Second)
		}},
		{"Endpoints 미관측", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.EndpointsAt = time.Time{} }},
		{"쓰기 늦음", ScenarioS10, func(_, a *ClusterSnap) {
			a.Emergency.WriteAt = a.Emergency.RecoverAt.Add(SwitchoverBudget + time.Second)
		}},
		{"쓰기 미관측", ScenarioS10, func(_, a *ClusterSnap) { a.Emergency.WriteAt = time.Time{} }},
		{"primary 둘", ScenarioS10, func(_, a *ClusterSnap) { a.MaxPrimaries = 2 }},
		{"PVC UID 변화", ScenarioS10, func(_, a *ClusterSnap) { a.PVCUIDs["data-p-0"] = "u9" }},
	})

	// recover 시각을 모르면 그 까닭으로 붉다(뒤 기한 판정이 대신 붉히지 않게)
	before, after := emergencyPair()
	after.Emergency.RecoverAt = time.Time{}
	wantReason(t, judgeGreen(ScenarioS10, before, after), CheckEmergency, "recover-primary 시각 미관측")
}

func TestTransitionJudge(t *testing.T) {
	runRed(t, CheckTransition, []redCase{
		{"C1 확인 실패", ScenarioS0, func(_, a *ClusterSnap) { a.C1Faults = []string{"VAP 없음"} }},
		{"새 revision 모자람", ScenarioS0, func(_, a *ClusterSnap) { a.PodsOnUpdate = 2 }},
		{"옛 오퍼레이터 pod 삭제", ScenarioS0, func(_, a *ClusterSnap) { a.AuditPodDeletes = 1 }},
		{"계획 밖 Rogue", ScenarioS0, func(_, a *ClusterSnap) { a.HeldReasons = map[string]int{RebuildRogue: 1} }},
		{"계획 밖 FencedPrimary", ScenarioS0, func(_, a *ClusterSnap) { a.HeldReasons = map[string]int{RebuildFencedPrimary: 1} }},
		{"wal_keep_size 가 auto.conf", ScenarioS0, func(_, a *ClusterSnap) {
			a.Settings[0].WalKeepSource = "/var/lib/postgresql/data/pgdata/" + AutoConfFile
		}},
		{"wal_keep_size 출처 모름", ScenarioS0, func(_, a *ClusterSnap) { a.Settings[2].WalKeepSource = "" }},
		{"복제본 마커", ScenarioS0, func(_, a *ClusterSnap) { a.Settings[2].Marker = true }},
		{"log_connections 꺼짐", ScenarioS0, func(_, a *ClusterSnap) { a.Settings[1].LogConnections = "off" }},
		{"사전 log_connections 미관측(양성 대조)", ScenarioS0, func(b, _ *ClusterSnap) { b.Settings = nil }},
		{"사전 log_connections off", ScenarioS0, func(b, _ *ClusterSnap) { b.Settings[0].LogConnections = "off" }},
		{"사전·끝 모두 off", ScenarioS0, func(b, a *ClusterSnap) {
			b.Settings[0].LogConnections = logConnectionsOff
			for i := range a.Settings {
				a.Settings[i].LogConnections = logConnectionsOff
			}
		}},
		{"primary_conninfo 3줄", ScenarioS0, func(_, a *ClusterSnap) { a.Settings[0].ConninfoLines = ConninfoLineCap + 1 }},
		{"primary_conninfo 모름", ScenarioS0, func(_, a *ClusterSnap) { a.Settings[0].ConninfoLines = -1 }},
		{"설정 표본 모자람", ScenarioS0, func(_, a *ClusterSnap) { a.Settings = a.Settings[:2] }},
	})
}

func TestClassifyConn(t *testing.T) {
	failed := errors.New("exit status 2")
	cases := []struct {
		out  string
		err  error
		want ConnOutcome
	}{
		{"1\n", nil, ConnAccepted},
		{`psql: error: connection to server at "10.96.0.9", port 5432 failed: FATAL:  password authentication failed for user "postgres"`, failed, ConnAuthRejected},
		{"psql: error: connection to server at ... failed: fe_sendauth: no password supplied", failed, ConnAuthRejected},
		{`FATAL:  no pg_hba.conf entry for host "10.42.1.5", user "postgres"`, failed, ConnAuthRejected},
		{`psql: error: could not translate host name "postgres-prod-shard-0-rw.data.svc" to address`, failed, ConnUnreachable},
		{"psql: error: connection to server ... failed: timeout expired", failed, ConnUnreachable},
		{"psql: error: connection to server ... failed: Connection refused", failed, ConnUnreachable},
		{"error: unable to upgrade connection: container not found", failed, ConnUnknown},
	}
	for _, c := range cases {
		if got := ClassifyConn(c.out, c.err); got != c.want {
			t.Fatalf("ClassifyConn(%q) = %s(기대 %s)", c.out, got, c.want)
		}
	}
}

func TestParseLSN(t *testing.T) {
	good := map[string]uint64{"0/3000060": 0x3000060, "1/0": 1 << lsnWordBits, " 16/B374D848 ": 0x16_B374_D848}
	for in, want := range good {
		got, err := ParseLSN(in)
		if err != nil || got != want {
			t.Fatalf("ParseLSN(%q) = %x %v(기대 %x)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "3000060", "G/0", "0/100000000", "1/2/3"} {
		if _, err := ParseLSN(bad); err == nil {
			t.Fatalf("ParseLSN(%q) 오류 없음", bad)
		}
	}
}
