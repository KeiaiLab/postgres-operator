/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func specWithCol(col string) v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: col, Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

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

// TestCopyShardRange_RejectsInjection 은 테이블/vindex 컬럼 이름 둘 다 화이트리스트로
// 차단됨을 검증한다.
func TestCopyShardRange_RejectsInjection(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"t; DROP TABLE u", "t v", ""} {
		if _, _, err := CopyShardRange(ctx, "", "", bad, specWithCol("id"), "shard-1"); !errors.Is(err, ErrInvalidTable) {
			t.Errorf("CopyShardRange table=%q err = %v, want ErrInvalidTable", bad, err)
		}
		if _, _, err := CopyShardRange(ctx, "", "", "tbl", specWithCol(bad), "shard-1"); !errors.Is(err, ErrInvalidTable) {
			t.Errorf("CopyShardRange col=%q err = %v, want ErrInvalidTable", bad, err)
		}
		if _, err := DeleteShardRange(ctx, "", bad, specWithCol("id"), "shard-1"); !errors.Is(err, ErrInvalidTable) {
			t.Errorf("DeleteShardRange table=%q err = %v, want ErrInvalidTable", bad, err)
		}
	}
}

// TestCDC_RejectsInjection 은 publication/subscription/slot/테이블 이름이 화이트리스트로
// (DB 접속 전) 차단됨을 검증한다.
func TestCDC_RejectsInjection(t *testing.T) {
	ctx := context.Background()
	bad := "p; DROP TABLE u"
	if err := CreatePublication(ctx, "", bad, nil); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("CreatePublication(%q) err = %v, want ErrInvalidTable", bad, err)
	}
	if err := CreateSubscription(ctx, "", "host=x", bad, "pub", true); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("CreateSubscription bad sub err = %v, want ErrInvalidTable", err)
	}
	if _, err := SubscriptionLagBytes(ctx, "", bad); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("SubscriptionLagBytes(%q) err = %v, want ErrInvalidTable", bad, err)
	}
	if err := DropSubscription(ctx, "", bad); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("DropSubscription err = %v, want ErrInvalidTable", err)
	}
	if _, err := DeleteForeignRange(ctx, "", bad, specWithCol("id"), "shard-0"); !errors.Is(err, ErrInvalidTable) {
		t.Errorf("DeleteForeignRange bad table err = %v, want ErrInvalidTable", err)
	}
}

func TestKeyString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{[]byte("alice"), "alice"},
		{"bob", "bob"},
		{int64(5), "5"},
		{42, "42"},
	}
	for _, c := range cases {
		if got := keyString(c.in); got != c.want {
			t.Errorf("keyString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFilterTables(t *testing.T) {
	all := []string{"orders", "kv", "Country", "users"}
	got := FilterTables(all, []string{"country", "REGIONS"})
	want := []string{"orders", "kv", "users"} // country 제외(대소문자 무시), REGIONS 부재 무해.
	if len(got) != len(want) {
		t.Fatalf("FilterTables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FilterTables[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIndexOfFold(t *testing.T) {
	cols := []string{"ID", "Val"}
	if i := indexOfFold(cols, "id"); i != 0 {
		t.Errorf("indexOfFold id = %d, want 0", i)
	}
	if i := indexOfFold(cols, "VAL"); i != 1 {
		t.Errorf("indexOfFold VAL = %d, want 1", i)
	}
	if i := indexOfFold(cols, "nope"); i != -1 {
		t.Errorf("indexOfFold nope = %d, want -1", i)
	}
}
