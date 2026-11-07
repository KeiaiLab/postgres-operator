/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// 이 파일은 §6.7 이 남긴 두 미검증 online-resharding 경로를 *라이브 PG*(wal_level=logical)
// 로 검증한다 — TestCDCLive 와 동일한 env-guard(RESHARD_LIVE_*) idiom 이라 kind/make 불요,
// `docker run postgres:18 -c wal_level=logical` 2개 + env 로 재현 가능하다. env 미설정 시
// skip(일반 go test 무영향). helper(mustOpenT/exec/countT/allIDs)는 reshard_cdc_live_test.go
// 와 공유한다(동일 package).
//
// 실행:
//
//	RESHARD_LIVE_SOURCE="host=pg-src port=5432 user=postgres dbname=postgres sslmode=disable" \
//	RESHARD_LIVE_TARGET="host=pg-tgt ..." RESHARD_LIVE_CONNINFO="host=pg-src ..."(target→source) \
//	go test ./internal/router -run 'TestReshardPKlessTargetConcurrentLive|TestReshardAbortSourceDownLive'

func reshardLiveDSNs(t *testing.T) (sourceDSN, targetDSN, connInfo string) {
	t.Helper()
	sourceDSN = os.Getenv("RESHARD_LIVE_SOURCE")
	targetDSN = os.Getenv("RESHARD_LIVE_TARGET")
	connInfo = os.Getenv("RESHARD_LIVE_CONNINFO")
	if sourceDSN == "" || targetDSN == "" || connInfo == "" {
		t.Skip("RESHARD_LIVE_SOURCE/TARGET/CONNINFO 미설정 — 라이브 resharding 테스트 skip")
	}
	return sourceDSN, targetDSN, connInfo
}

// TestReshardPKlessTargetConcurrentLive 는 online resharding 중 *동시 쓰기* 상황에서
// PK 없는 target 으로의 UPDATE/DELETE 논리복제(seq-scan 경로)를 검증한다 — §6.7 의
// "PK-없는 target 동시쓰기 경로 미검증" 갭.
//
// 배경: CDCCatchup 은 target 에 PK 를 나중(cdc-finalize)에 추가하므로, catch-up 동안
// target 은 PK/replica-identity 없이 UPDATE/DELETE 를 적용해야 한다. 논리복제 subscriber
// 는 적절한 인덱스가 없으면 seq-scan 으로 old-tuple 을 찾아 적용한다(동작하나 미검증이던
// 경로). source 는 PK 를 가지므로 publisher 측 replica identity 는 충족된다.
func TestReshardPKlessTargetConcurrentLive(t *testing.T) {
	sourceDSN, targetDSN, connInfo := reshardLiveDSNs(t)
	ctx := context.Background()

	src := mustOpenT(t, sourceDSN)
	defer func() { _ = src.Close() }() // teardown
	tgt := mustOpenT(t, targetDSN)
	defer func() { _ = tgt.Close() }() // teardown

	// source: PK 보유(publisher replica identity 충족). 초기 1..100.
	exec(t, src, `DROP TABLE IF EXISTS kv`)
	exec(t, src, `CREATE TABLE kv(id int PRIMARY KEY, val int)`)
	exec(t, src, `INSERT INTO kv SELECT g, g FROM generate_series(1,100) g`)
	// target: PK 없음(cdc-finalize 전 상태) — subscriber 가 seq-scan 으로 적용해야 함.
	exec(t, tgt, `DROP TABLE IF EXISTS kv`)
	exec(t, tgt, `CREATE TABLE kv(id int, val int)`)

	_ = DropSubscription(ctx, targetDSN, "sub_pkless")
	_ = DropPublication(ctx, sourceDSN, "pub_pkless")
	defer func() {
		_ = DropSubscription(ctx, targetDSN, "sub_pkless")
		_ = DropPublication(ctx, sourceDSN, "pub_pkless")
	}()

	if err := CreatePublication(ctx, sourceDSN, "pub_pkless", []string{"kv"}); err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}
	if err := CreateSubscription(ctx, targetDSN, connInfo, "sub_pkless", "pub_pkless", true); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// 구독 이후 *동시* 쓰기 스트림: goroutine 이 UPDATE/DELETE 를 흘리는 동안 스트림
	// 적용을 검증한다. UPDATE: 짝수 id 를 val=-1 로. DELETE: id > 90 제거.
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			exec(t, src, fmt.Sprintf(`UPDATE kv SET val = -1 WHERE id = %d`, (i%45)*2+2))
			if i%5 == 0 {
				exec(t, src, fmt.Sprintf(`DELETE FROM kv WHERE id = %d`, 91+i%10))
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	wg.Wait()

	// 확정 상태: source 의 최종 상태를 target 이 정확히 반영해야 함(유실0, seq-scan 적용).
	wantCount := countT(t, src, `SELECT count(*) FROM kv`)
	wantNeg := countT(t, src, `SELECT count(*) FROM kv WHERE val = -1`)

	converged := false
	for range 60 {
		lag, err := SubscriptionLagBytes(ctx, sourceDSN, "sub_pkless")
		if err == nil && lag >= 0 && lag <= 0 &&
			countT(t, tgt, `SELECT count(*) FROM kv`) == wantCount &&
			countT(t, tgt, `SELECT count(*) FROM kv WHERE val = -1`) == wantNeg {
			converged = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !converged {
		t.Fatalf("PK 없는 target 미수렴: src(count=%d,neg=%d) vs tgt(count=%d,neg=%d)",
			wantCount, wantNeg, countT(t, tgt, `SELECT count(*) FROM kv`), countT(t, tgt, `SELECT count(*) FROM kv WHERE val=-1`))
	}
	t.Logf("PK 없는 target seq-scan 적용 수렴: count=%d, val=-1 rows=%d (동시 UPDATE/DELETE 반영)", wantCount, wantNeg)

	// cdc-finalize 상당: 이제 PK/인덱스를 복제하면 target 이 replica identity 를 갖는다.
	if _, err := ReplicateIndexes(ctx, sourceDSN, targetDSN, "kv"); err != nil {
		t.Fatalf("ReplicateIndexes: %v", err)
	}
	if countT(t, tgt, `SELECT count(*) FROM pg_indexes WHERE tablename='kv' AND indexname='kv_pkey'`) != 1 {
		t.Fatal("finalize 후 target 에 PK 인덱스(kv_pkey) 미복제")
	}
}

// TestReshardAbortSourceDownLive 는 online resharding abort 의 source-down fallback
// (ForceDropSubscription)이 target subscription 을 확실히 제거하는지 검증한다 — §6.7 의
// "abort 누수" + §6.8 의 "source-down 강제 제거 fallback" 갭.
//
// 메커니즘 검증: subscription 생성 → ForceDropSubscription(DISABLE → slot detach →
// DROP) → pg_subscription 에서 소멸 확인 + 재호출 멱등(no-op). ForceDropSubscription 은
// 원격 slot 을 detach 후 DROP 하므로 publisher 접속 없이 target 을 정리한다 — 이것이
// source 가 죽어도 abort cleanup 이 완료(AbortCleanup=True)되게 하는 핵심이다. 실제
// source-down 상황은 kind live drill 로 확인하고, 본 테스트는 그 기반 메커니즘을 잠근다.
func TestReshardAbortSourceDownLive(t *testing.T) {
	sourceDSN, targetDSN, connInfo := reshardLiveDSNs(t)
	ctx := context.Background()

	src := mustOpenT(t, sourceDSN)
	defer func() { _ = src.Close() }() // teardown
	tgt := mustOpenT(t, targetDSN)
	defer func() { _ = tgt.Close() }() // teardown

	exec(t, src, `DROP TABLE IF EXISTS kv`)
	exec(t, src, `CREATE TABLE kv(id int PRIMARY KEY, val int)`)
	exec(t, src, `INSERT INTO kv SELECT g, g FROM generate_series(1,10) g`)
	exec(t, tgt, `DROP TABLE IF EXISTS kv`)
	exec(t, tgt, `CREATE TABLE kv(id int PRIMARY KEY, val int)`)

	_ = ForceDropSubscription(ctx, targetDSN, "sub_abort")
	_ = DropPublication(ctx, sourceDSN, "pub_abort")
	defer func() {
		_ = ForceDropSubscription(ctx, targetDSN, "sub_abort")
		_ = DropPublication(ctx, sourceDSN, "pub_abort")
	}()

	if err := CreatePublication(ctx, sourceDSN, "pub_abort", []string{"kv"}); err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}
	if err := CreateSubscription(ctx, targetDSN, connInfo, "sub_abort", "pub_abort", true); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if countT(t, tgt, `SELECT count(*) FROM pg_subscription WHERE subname='sub_abort'`) != 1 {
		t.Fatal("subscription 생성 확인 실패")
	}

	// force fallback: publisher 접속 없이 target subscription 제거.
	if err := ForceDropSubscription(ctx, targetDSN, "sub_abort"); err != nil {
		t.Fatalf("ForceDropSubscription: %v", err)
	}
	if got := countT(t, tgt, `SELECT count(*) FROM pg_subscription WHERE subname='sub_abort'`); got != 0 {
		t.Fatalf("force drop 후 subscription 잔존: %d", got)
	}

	// 멱등: 부재 상태 재호출은 no-op(에러 없음).
	if err := ForceDropSubscription(ctx, targetDSN, "sub_abort"); err != nil {
		t.Fatalf("ForceDropSubscription 멱등 재호출 실패: %v", err)
	}
	t.Log("source-down fallback 메커니즘 검증: ForceDropSubscription 이 slot detach 후 target subscription 제거(멱등)")
}
