/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"
	"time"
)

// TestShouldPromoteAfterDebounce_RequiresSustainedFailure pins the #220 live-drill
// fix: a primary failure must persist for failoverDebounceThreshold before the
// operator promotes, so a single-reconcile status flicker (the spurious-promotion
// root cause that, via fenceNonTargetMembers, fenced a healthy member) is filtered.
func TestShouldPromoteAfterDebounce_RequiresSustainedFailure(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("must not promote on first failure detection (debounce window starts)")
	}
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(failoverDebounceThreshold-time.Second)) {
		t.Fatal("must not promote while within the debounce window")
	}
	if !r.shouldPromoteAfterDebounce(key, true, true, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("must promote once the failure has persisted past the threshold")
	}
}

// TestShouldPromoteAfterDebounce_ContinuesAfterCanStartDrops mirrors the live
// failover sequence: the window starts while the cluster was Ready (canStart=true),
// and subsequent reconciles continue it even though the failure has since dropped
// the phase out of Ready (canStart=false). The original integration bug was that
// the phase==Ready gate was re-evaluated every reconcile, so the window never
// accumulated past the first detection.
func TestShouldPromoteAfterDebounce_ContinuesAfterCanStartDrops(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	// Reconcile 1: cluster was Ready, failure just detected → start window.
	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("must not promote on first detection")
	}
	// Reconcile 2: phase has dropped (canStart=false now), still within window.
	if r.shouldPromoteAfterDebounce(key, true, false, t0.Add(statusPollInterval)) {
		t.Fatal("must not promote mid-window")
	}
	// Reconcile 3: phase still not Ready (canStart=false), window now sustained.
	if !r.shouldPromoteAfterDebounce(key, true, false, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("must promote after sustained failure even though canStart is now false")
	}
}

// TestShouldPromoteAfterDebounce_StartGatedByCanStart verifies a failure observed
// while the cluster was never Ready (e.g. initial bootstrap churn) never starts a
// window, so no promotion happens no matter how long the failure persists.
func TestShouldPromoteAfterDebounce_StartGatedByCanStart(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, false, t0) {
		t.Fatal("window must not start when canStart=false")
	}
	if r.shouldPromoteAfterDebounce(key, true, false, t0.Add(2*failoverDebounceThreshold)) {
		t.Fatal("with no window started, must never promote regardless of elapsed time")
	}
	// Once the cluster reaches Ready (canStart=true), the window starts.
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(2*failoverDebounceThreshold+time.Second)) {
		t.Fatal("first detection with canStart=true starts the window, not promote")
	}
}

// TestShouldPromoteAfterDebounce_TransientFlickerFiltered verifies a failure that
// recovers within the window never triggers a promotion, and that a later genuine
// failure restarts the window from scratch (old elapsed time must not carry over).
func TestShouldPromoteAfterDebounce_TransientFlickerFiltered(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("flicker: must not promote on first detection")
	}
	if r.shouldPromoteAfterDebounce(key, false, true, t0.Add(2*time.Second)) {
		t.Fatal("recovery must never promote and must clear the window")
	}
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(3*time.Second)) {
		t.Fatal("post-recovery failure must restart the window, not promote immediately")
	}
	if !r.shouldPromoteAfterDebounce(key, true, true, t0.Add(3*time.Second+failoverDebounceThreshold+time.Millisecond)) {
		t.Fatal("the restarted window must promote once it too is sustained")
	}
}

// TestShouldPromoteAfterDebounce_IndependentKeys ensures per-cluster windows do
// not interfere with one another.
func TestShouldPromoteAfterDebounce_IndependentKeys(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	t0 := time.Unix(1_900_000_000, 0).UTC()

	r.shouldPromoteAfterDebounce("ns/a", true, true, t0)
	if r.shouldPromoteAfterDebounce("ns/b", true, true, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("cluster b must start its own window, not inherit cluster a's elapsed time")
	}
}
