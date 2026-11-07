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

func TestFreshestStandby(t *testing.T) {
	cases := []struct {
		positions map[string]int64
		want      string
	}{
		{map[string]int64{"pg-shard-0-1": 900, "pg-shard-0-2": 1000}, "pg-shard-0-2"},
		{map[string]int64{"pg-shard-0-2": 1000, "pg-shard-0-1": 1000}, "pg-shard-0-1"},
		{map[string]int64{"pg-shard-0-2": 5}, "pg-shard-0-2"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := FreshestStandby(c.positions); got != c.want {
			t.Fatalf("FreshestStandby(%v) = %q(기대 %q)", c.positions, got, c.want)
		}
	}
}

func TestBehindTarget(t *testing.T) {
	msg := "shard shard-0: postgres-prod-shard-0-2 is not the streaming standby furthest ahead (postgres-prod-shard-0-1 at 0/3000148); switchover not started"
	if got := BehindTarget(msg); got != "postgres-prod-shard-0-1" {
		t.Fatalf("BehindTarget = %q", got)
	}
	if BehindTarget("SwitchoverStarted shard-0") != "" {
		t.Fatal("다른 메시지에서 대상")
	}
}

func TestHeldReason(t *testing.T) {
	cases := map[string]string{
		"shard shard-0: postgres-prod-shard-0-0 held for rebuild (Demoted)": RebuildDemoted,
		"shard shard-0: postgres-prod-shard-0-2 held for rebuild (Rogue)":   RebuildRogue,
		"shard shard-0: postgres-prod-shard-0-2 released to rejoin":         "",
	}
	for msg, want := range cases {
		if got := HeldReason(msg); got != want {
			t.Fatalf("HeldReason(%q) = %q(기대 %q)", msg, got, want)
		}
	}
}

func TestClassifyVAP(t *testing.T) {
	failed := errors.New("exit status 1")
	cases := []struct {
		out  string
		err  error
		want VAPOutcome
	}{
		{`volumesnapshot.snapshot.storage.k8s.io "p-0-rebuild-a" deleted (server dry run)`, nil, VAPAllowed},
		{`Error from server (Forbidden): volumesnapshots.snapshot.storage.k8s.io "p-0-rebuild-a" is forbidden: ValidatingAdmissionPolicy 'volumesnapshot-delete-guard' with binding 'volumesnapshot-delete-guard' denied request: data-bearing`, failed, VAPDenied},
		{`Error from server (Forbidden): volumesnapshots.snapshot.storage.k8s.io "p-0-rebuild-a" is forbidden: User "system:serviceaccount:data:op" cannot delete resource "volumesnapshots"`, failed, VAPForbidden},
		{`Error from server (NotFound): volumesnapshots.snapshot.storage.k8s.io "x" not found`, failed, VAPNoTarget},
		{"Unable to connect to the server", failed, VAPUnknown},
	}
	for _, c := range cases {
		if got := ClassifyVAP(c.out, c.err); got != c.want {
			t.Fatalf("ClassifyVAP(%q) = %s(기대 %s)", c.out, got, c.want)
		}
	}
}

// S3·S5a·S9 판정문 — 오퍼레이터는 01:11:05.481432 에 readyToUse 를 읽고 .481501 에 풀었는데,
// recorder 는 15 s 뒤 느린 표본에서야 ready 를 봐 J6 이 붉었다.
func TestReadyFromEvents(t *testing.T) {
	read := time.Date(2026, 11, 7, 1, 11, 5, 481432000, time.UTC)
	released := read.Add(69 * time.Microsecond)
	polled := read.Add(15 * time.Second)
	const name = "postgres-prod-shard-0-1-rebuild-to3xy0-0"
	ready := EventMark{At: read, Reason: EventSnapshotReady,
		Message: "shard shard-0: postgres-prod-shard-0-1 snapshot " + name + " ready (data-bearing=true)"}
	other := EventMark{At: read.Add(-time.Minute), Reason: "MemberHeld",
		Message: "shard shard-0: postgres-prod-shard-0-1 held for rebuild (Demoted) snapshot " + name + " ready "}

	cases := []struct {
		label  string
		mark   SnapshotMark
		events []EventMark
		want   time.Time
	}{
		{"이벤트가 표본보다 이르면 당긴다", SnapshotMark{Name: name, ReadyAt: polled}, []EventMark{other, ready}, read},
		{"recorder 가 ready 를 못 봤으면 그대로", SnapshotMark{Name: name}, []EventMark{ready}, time.Time{}},
		{"표본이 더 이르면 그대로", SnapshotMark{Name: name, ReadyAt: read.Add(-time.Second)}, []EventMark{ready}, read.Add(-time.Second)},
		{"다른 스냅숏 이벤트는 무시", SnapshotMark{Name: name + "x", ReadyAt: polled}, []EventMark{ready}, polled},
	}
	for _, c := range cases {
		got := ReadyFromEvents([]SnapshotMark{c.mark}, c.events)
		if !got[0].ReadyAt.Equal(c.want) {
			t.Errorf("%s: ReadyAt = %s(기대 %s)", c.label, got[0].ReadyAt, c.want)
		}
	}

	// J6 — 당긴 표본이면 풀기 전 readyToUse 로 선다
	after := ClusterSnap{
		Snapshots: ReadyFromEvents([]SnapshotMark{{Name: name, ReadyAt: polled}}, []EventMark{ready}),
		Rebuilds:  []RebuildMark{{Member: "postgres-prod-shard-0-1", ReleasedAt: released, Snapshot: name}},
	}
	if c := judgeSource(ClusterSnap{}, after, Expect{}); !c.Pass {
		t.Fatalf("J6 붉음: %s", c.Detail)
	}
}
