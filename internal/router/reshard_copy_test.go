/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package router

import (
	"context"
	"errors"
	"testing"
)

func TestBuildInsert(t *testing.T) {
	got := buildInsert("t", []string{"id", "v"})
	want := "INSERT INTO t (id, v) VALUES ($1, $2)"
	if got != want {
		t.Fatalf("buildInsert = %q, want %q", got, want)
	}
}

// TestCopyTable_RejectsInjection 은 테이블 이름이 식별자 화이트리스트를 위반하면
// DB 연결 전에 ErrInvalidTable 로 차단됨을 검증한다 (SQL injection 차단).
func TestCopyTable_RejectsInjection(t *testing.T) {
	for _, bad := range []string{"t; DROP TABLE u", "t v", "1t", "t'", ""} {
		if _, err := CopyTable(context.Background(), "", "", bad); !errors.Is(err, ErrInvalidTable) {
			t.Errorf("CopyTable(%q) err = %v, want ErrInvalidTable", bad, err)
		}
	}
}
