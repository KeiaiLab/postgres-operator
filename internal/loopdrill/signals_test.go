/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"testing"
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
