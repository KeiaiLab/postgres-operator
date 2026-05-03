/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package supervise

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsStandby_FileAbsent_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	if IsStandby(dir) {
		t.Fatalf("IsStandby(empty dir) = true, want false")
	}
}

func TestIsStandby_FileExists_ReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "standby.signal")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !IsStandby(dir) {
		t.Fatalf("IsStandby(dir with signal) = false, want true")
	}
}

func TestRemoveStandbySignal_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// 1차: 부재 상태 — nil 반환해야 idempotent.
	if err := RemoveStandbySignal(dir); err != nil {
		t.Fatalf("RemoveStandbySignal (absent #1): %v", err)
	}
	// 2차: 여전히 부재 — 마찬가지로 nil.
	if err := RemoveStandbySignal(dir); err != nil {
		t.Fatalf("RemoveStandbySignal (absent #2): %v", err)
	}
}

func TestCreateStandbySignal_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal #1: %v", err)
	}
	if err := CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal #2 (idempotent): %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "standby.signal"))
	if err != nil {
		t.Fatalf("Stat after Create: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file perms = %o, want 0600", mode)
	}
}
