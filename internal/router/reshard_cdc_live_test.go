/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// TestCDCLive 는 CDC 증분 catch-up 빌딩블록을 *라이브 PG*(wal_level=logical)로 검증한다.
// env 미설정 시 skip(일반 go test 무영향). 검증: subscription(copy_data=true)이 초기 행 +
// *구독 이후 들어온 라이브 쓰기* 를 모두 target 에 복제하고, DeleteForeignRange 가 범위 밖
// row 만 제거하는지.
//
// 실행:
//
//	RESHARD_LIVE_SOURCE="host=pg-src ..." RESHARD_LIVE_TARGET="host=pg-tgt ..."
//	RESHARD_LIVE_CONNINFO="host=pg-src ..."(target→source 접속) go test -run TestCDCLive
func TestCDCLive(t *testing.T) {
	sourceDSN := os.Getenv("RESHARD_LIVE_SOURCE")
	targetDSN := os.Getenv("RESHARD_LIVE_TARGET")
	connInfo := os.Getenv("RESHARD_LIVE_CONNINFO")
	if sourceDSN == "" || targetDSN == "" || connInfo == "" {
		t.Skip("RESHARD_LIVE_SOURCE/TARGET/CONNINFO 미설정 — 라이브 CDC 테스트 skip")
	}
	ctx := context.Background()
	spec := specWithCol("id") // shard-0 [0,7fff], shard-1 [8000,ffff] murmur3.

	src := mustOpenT(t, sourceDSN)
	defer src.Close()
	tgt := mustOpenT(t, targetDSN)
	defer tgt.Close()

	// source: kv 초기 1..50.
	exec(t, src, `DROP TABLE IF EXISTS kv`)
	exec(t, src, `CREATE TABLE kv(id int PRIMARY KEY, val int)`)
	exec(t, src, `INSERT INTO kv SELECT g, g*10 FROM generate_series(1,50) g`)
	exec(t, tgt, `DROP TABLE IF EXISTS kv`)
	exec(t, tgt, `CREATE TABLE kv(id int PRIMARY KEY, val int)`)

	// cleanup any prior pub/sub.
	_ = DropSubscription(ctx, targetDSN, "sub_cdc")
	_ = DropPublication(ctx, sourceDSN, "pub_cdc")
	defer func() {
		_ = DropSubscription(ctx, targetDSN, "sub_cdc")
		_ = DropPublication(ctx, sourceDSN, "pub_cdc")
	}()

	if err := CreatePublication(ctx, sourceDSN, "pub_cdc", []string{"kv"}); err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}
	if err := CreateSubscription(ctx, targetDSN, connInfo, "sub_cdc", "pub_cdc", true); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// *구독 이후* 라이브 쓰기 — CDC 가 따라잡아야 함.
	exec(t, src, `INSERT INTO kv SELECT g, g*10 FROM generate_series(51,100) g`)
	exec(t, src, `UPDATE kv SET val = 999 WHERE id = 1`)

	// lag → 0 까지 대기(최대 30s).
	caughtUp := false
	for i := 0; i < 60; i++ {
		lag, err := SubscriptionLagBytes(ctx, sourceDSN, "sub_cdc")
		if err == nil && lag >= 0 && lag <= 0 {
			// 추가로 target row 수 확인(스트림 적용 확인).
			if countT(t, tgt, "SELECT count(*) FROM kv") == 100 && countT(t, tgt, "SELECT val FROM kv WHERE id=1") == 999 {
				caughtUp = true
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !caughtUp {
		t.Fatalf("CDC 미수렴: target count=%d, id1.val=%d (100/999 기대)",
			countT(t, tgt, "SELECT count(*) FROM kv"), countT(t, tgt, "SELECT val FROM kv WHERE id=1"))
	}
	t.Logf("CDC 수렴: target 에 초기 50 + 라이브 50 + UPDATE 반영 = 100 행")

	// cutover 정리: subscription 끊고 범위 밖(=shard-1) row 삭제 → target=shard-0 만.
	if err := DropSubscription(ctx, targetDSN, "sub_cdc"); err != nil {
		t.Fatalf("DropSubscription: %v", err)
	}
	deleted, err := DeleteForeignRange(ctx, targetDSN, "kv", spec, "shard-0")
	if err != nil {
		t.Fatalf("DeleteForeignRange: %v", err)
	}
	keep := countT(t, tgt, "SELECT count(*) FROM kv")
	t.Logf("DeleteForeignRange: %d 삭제(shard-1 키), target 잔존 %d(shard-0 키)", deleted, keep)
	if keep+deleted != 100 {
		t.Fatalf("키 보존 위반: keep(%d)+deleted(%d) != 100", keep, deleted)
	}
	// 잔존 row 가 전부 shard-0 인지 확인.
	ids := allIDs(t, tgt)
	for _, id := range ids {
		if s, _ := ResolveShard(spec, id); s != "shard-0" {
			t.Fatalf("target 에 범위 밖 키 잔존: id=%s → %s", id, s)
		}
	}
}

func mustOpenT(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open %q: %v", dsn, err)
	}
	return db
}

func exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func countT(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n sql.NullInt64
	if err := db.QueryRow(q).Scan(&n); err != nil {
		return -1
	}
	return int(n.Int64)
}

func allIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT id::text FROM kv")
	if err != nil {
		t.Fatalf("query ids: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		out = append(out, s)
	}
	return out
}
