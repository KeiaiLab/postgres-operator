/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
	"github.com/keiailab/postgres-operator/internal/instance/supervise"
)

func TestBuildElectionIdentity_UsesPodUID(t *testing.T) {
	got := buildElectionIdentity("demo-shard-0-0", "uid-123")
	if got != "demo-shard-0-0/uid-123" {
		t.Fatalf("identity = %q, want podName/podUID", got)
	}
}

func TestParsePodOrdinalOrDie_StatefulSetName(t *testing.T) {
	got := parsePodOrdinalOrDie("demo-shard-0-12")
	if got != 12 {
		t.Fatalf("ordinal = %d, want 12", got)
	}
}

func TestPrepareRestartedPrimaryAsStandby_UsesCurrentPrimaryEndpointForAnyOrdinal(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, supervise.RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	prepared, err := prepareRestartedPrimaryAsStandby(
		dir,
		"demo-shard-0-2.demo-shard-0-headless.ns1.svc.cluster.local:5432",
		"",
		"demo-shard-0-0",
		2,
		discardLogger(),
	)
	if err != nil {
		t.Fatalf("prepareRestartedPrimaryAsStandby: %v", err)
	}
	if !prepared {
		t.Fatal("prepared = false, want true for marked HA member regardless of pod ordinal")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "postgresql.auto.conf"))
	if err != nil {
		t.Fatalf("read postgresql.auto.conf: %v", err)
	}
	if !strings.Contains(string(raw), "host=demo-shard-0-2.demo-shard-0-headless.ns1.svc.cluster.local port=5432") {
		t.Fatalf("primary_conninfo must use current primary endpoint, got:\n%s", raw)
	}
}

func TestPatchRejoinFailureStatus_PublishesAnnotation(t *testing.T) {
	client := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-0", Namespace: "default"},
	})

	err := patchRejoinFailureStatus(
		context.Background(),
		client,
		"default",
		"demo-shard-0-0",
		"demo-shard-0-0.demo-shard-0-headless.default.svc.cluster.local:5432",
		"primary.svc:5432",
		"pg_rewind failed",
	)
	if err != nil {
		t.Fatalf("patchRejoinFailureStatus: %v", err)
	}
	pod, err := client.CoreV1().Pods("default").Get(context.Background(), "demo-shard-0-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	raw := pod.Annotations[statusapi.AnnotationKey]
	if raw == "" {
		t.Fatal("instance-status annotation missing")
	}
	var st statusapi.Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.Role != statusapi.RoleReplica || st.Ready {
		t.Fatalf("status role/ready = %s/%v, want replica/false", st.Role, st.Ready)
	}
	if st.Reason != "RejoinPreparationFailed" {
		t.Fatalf("reason = %q", st.Reason)
	}
	if !strings.Contains(st.Message, "primary.svc:5432") || !strings.Contains(st.Message, "pg_rewind failed") {
		t.Fatalf("message = %q, want endpoint and error", st.Message)
	}
	if st.Endpoint == "" || st.LastUpdate.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("status endpoint/time not populated: %+v", st)
	}
}

func TestHandleStoppedLeading_SkipsFenceAndDemoteForSingleMember(t *testing.T) {
	fencer := fencing.NewMock()
	sup := supervise.NewMock()

	// promotedAtLeastOnce=true so the single-member short-circuit is the
	// only reason fencing is skipped (proves the memberCount<=1 guard works).
	handleStoppedLeading(
		fencer, sup, t.TempDir(), "demo-shard-0-0", "demo-shard-0-primary",
		1, true, discardLogger(),
	)

	mark, _, _ := fencer.Calls()
	if mark != 0 {
		t.Fatalf("MarkFenced calls = %d, want 0 for single-member cluster", mark)
	}
	if sup.StopCalls != 0 {
		t.Fatalf("Stop calls = %d, want 0 for single-member cluster", sup.StopCalls)
	}
}

// TestHandleStoppedLeading_NeverFencesOrDemotes is the T30 contract:
// handleStoppedLeading is *always* a no-op on the fencing + demote side
// effects, irrespective of memberCount or promotedAtLeastOnce. Failover
// is the operator's job through executeClusterPromotion exec — never
// the instance manager's.
func TestHandleStoppedLeading_NeverFencesOrDemotes(t *testing.T) {
	fencer := fencing.NewMock()
	sup := supervise.NewMock()
	if err := sup.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Exercise the worst-case input (multi-member + already-promoted) to
	// prove the no-op behaviour, then a second time with the bootstrap
	// pre-promote case to prove no regression.
	for _, promoted := range []bool{true, false} {
		handleStoppedLeading(
			fencer, sup, t.TempDir(), "demo-shard-0-0", "demo-shard-0-primary",
			2, promoted, discardLogger(),
		)
	}

	mark, _, _ := fencer.Calls()
	if mark != 0 {
		t.Fatalf("MarkFenced calls = %d, want 0 (T30 no-op)", mark)
	}
	if sup.StopCalls != 0 {
		t.Fatalf("Stop calls = %d, want 0 (T30 no-op)", sup.StopCalls)
	}
}

// TestHandleStoppedLeading_SkipsFenceWhenNeverPromoted is a regression
// guard for the PG18 HA kind smoke iter#1 bootstrap crashloop: a pod that
// acquired the lease but exited *before* a successful pg_promote (initdb
// not finished, waitSupReady timed out, …) must not fence its own PVC.
// Otherwise every subsequent boot refuses to promote and the cluster
// never converges.
func TestHandleStoppedLeading_SkipsFenceWhenNeverPromoted(t *testing.T) {
	fencer := fencing.NewMock()
	sup := supervise.NewMock()

	handleStoppedLeading(
		fencer, sup, t.TempDir(), "demo-shard-0-0", "demo-shard-0-primary",
		2, false, discardLogger(),
	)

	mark, _, _ := fencer.Calls()
	if mark != 0 {
		t.Fatalf("MarkFenced calls = %d, want 0 (never promoted)", mark)
	}
	if sup.StopCalls != 0 {
		t.Fatalf("Stop calls = %d, want 0 (never promoted)", sup.StopCalls)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
