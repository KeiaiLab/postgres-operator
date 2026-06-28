/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Command reshard-copy-poc is a G3 online-resharding live PoC. It performs the
// *reversible* data-movement step of ShardSplitJob via internal/router:
//
//   - Full copy (default): router.CopyTable (source table → target, all rows).
//   - Range copy (PGROUTER_RESHARD_TARGET_SHARD set): router.CopyShardRange — only
//     the rows whose vindex key resolves to the target shard (the *real* split:
//     move just the moving sub-range, same vindex as routing).
//   - Cutover cleanup (PGROUTER_RESHARD_DELETE_AFTER=1): after the copy and the
//     routing switch, router.DeleteShardRange removes the moved rows from the
//     source. Run ONLY after routing is switched (else data loss).
//
// The built-in vindex (murmur3 hash, 2-shard split at 0x80000000, column from
// PGROUTER_VINDEX_TYPE / PGROUTER_VINDEX_COLUMN, default "hash" / "id") matches
// the ShardRange topology so the copy and the router agree on which keys belong
// where.
//
// Config: PGROUTER_SOURCE_DSN, PGROUTER_TARGET_DSN, PGROUTER_COPY_TABLE,
// PGROUTER_RESHARD_TARGET_SHARD, PGROUTER_VINDEX_TYPE, PGROUTER_VINDEX_COLUMN,
// PGROUTER_RESHARD_DELETE_AFTER.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

// reshardSpec builds the post-split vindex spec. The ranges come from
// PGROUTER_RANGES ("shard:lo:hi,shard:lo:hi") when set (controller passes the
// target topology), else the default 2-shard murmur3 split (standalone use).
// vtype/fn are from PGROUTER_VINDEX_TYPE / PGROUTER_VINDEX_FUNCTION. Standalone
// use defaults to hash + murmur3.
func reshardSpec(vtype, col, fn, rangesEnv string) v1alpha1.ShardRangeSpec {
	if vtype == "" {
		vtype = string(v1alpha1.VindexTypeHash)
	}
	if fn == "" && (vtype == string(v1alpha1.VindexTypeHash) || vtype == string(v1alpha1.VindexTypeConsistentHash)) {
		fn = "murmur3"
	}
	ranges := parseRanges(rangesEnv)
	if len(ranges) == 0 {
		ranges = []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		}
	}
	return v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexType(vtype), Column: col, Function: v1alpha1.VindexHashFunction(fn)},
		Ranges: ranges,
	}
}

// parseRanges parses "shard:lo:hi,shard:lo:hi" into ShardRangeEntry list.
func parseRanges(s string) []v1alpha1.ShardRangeEntry {
	var out []v1alpha1.ShardRangeEntry
	for _, part := range csv(s) {
		f := strings.Split(part, ":")
		if len(f) != 3 {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: bad range %q (want shard:lo:hi)\n", part)
			os.Exit(2)
		}
		out = append(out, v1alpha1.ShardRangeEntry{Shard: f[0], Lo: f[1], Hi: f[2]})
	}
	return out
}

func main() {
	src := os.Getenv("PGROUTER_SOURCE_DSN")
	tgt := os.Getenv("PGROUTER_TARGET_DSN")
	table := os.Getenv("PGROUTER_COPY_TABLE")
	targetShard := os.Getenv("PGROUTER_RESHARD_TARGET_SHARD")
	mode := os.Getenv("PGROUTER_RESHARD_MODE")
	cdcMode := mode == "cdc-setup" || mode == "cdc-finalize" || mode == "cdc-abort"
	// delete-only: cutover 후 source 에서 이동분 삭제만(복사 없음, Cleanup phase). target 불요.
	deleteOnly := os.Getenv("PGROUTER_RESHARD_DELETE_ONLY") != ""
	switch {
	case src == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_SOURCE_DSN required")
		os.Exit(2)
	case cdcMode && targetShard == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: CDC requires PGROUTER_RESHARD_TARGET_SHARD")
		os.Exit(2)
	case cdcMode && tgt == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: CDC requires PGROUTER_TARGET_DSN")
		os.Exit(2)
	case deleteOnly && targetShard == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: DELETE_ONLY requires PGROUTER_RESHARD_TARGET_SHARD")
		os.Exit(2)
	case !deleteOnly && tgt == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_TARGET_DSN required (unless DELETE_ONLY)")
		os.Exit(2)
	case !cdcMode && targetShard == "" && table == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: full copy requires PGROUTER_COPY_TABLE")
		os.Exit(2)
	}
	timeout := 60 * time.Second
	if cdcMode {
		timeout = 15 * time.Minute // CDC bulk 복사/drain 은 길 수 있다.
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// CDC(online) 모드: 논리복제 setup(스키마+pub+sub+lag대기) 또는 finalize(drain+drop+범위정리).
	if cdcMode {
		runCDC(ctx, mode, src, tgt, targetShard)
		return
	}

	// Full copy (no target shard) vs range-filtered copy (the real split).
	if targetShard == "" {
		fmt.Printf("reshard-copy-poc: InitialCopy (full) table=%q source→target\n", table)
		n, err := router.CopyTable(ctx, src, tgt, table)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d before error)\n", err, n)
			os.Exit(1)
		}
		fmt.Printf("reshard-copy-poc: copied %d row(s) source→target (rollback=drop target)\n", n)
		return
	}

	col := os.Getenv("PGROUTER_VINDEX_COLUMN")
	if col == "" {
		col = "id"
	}
	spec := reshardSpec(os.Getenv("PGROUTER_VINDEX_TYPE"), col, os.Getenv("PGROUTER_VINDEX_FUNCTION"), os.Getenv("PGROUTER_RANGES"))

	// 옮길 테이블 결정: COPY_TABLE 지정 시 그 하나, 아니면 source 의 모든 user 테이블에서
	// reference 테이블(PGROUTER_REFERENCE_TABLES, 전 샤드 복제라 이동 대상 아님)을 뺀 전부.
	var tables []string
	if table != "" {
		tables = []string{table}
	} else {
		all, err := router.ListUserTables(ctx, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: list tables: %v\n", err)
			os.Exit(1)
		}
		tables = router.FilterTables(all, csv(os.Getenv("PGROUTER_REFERENCE_TABLES")))
		fmt.Printf("reshard-copy-poc: discovered %d table(s) to reshard: %v\n", len(tables), tables)
	}

	// Cleanup(delete-only): cutover 후 source 에서 targetShard 로 이동한 row 삭제만.
	if deleteOnly {
		for _, tbl := range tables {
			deleted, err := router.DeleteShardRange(ctx, src, tbl, spec, targetShard)
			if err != nil {
				fmt.Fprintf(os.Stderr, "reshard-copy-poc: delete: %v (deleted %d before error)\n", err, deleted)
				os.Exit(1)
			}
			fmt.Printf("reshard-copy-poc: deleted %d row(s) of %q (%s keys) from source\n", deleted, tbl, targetShard)
		}
		fmt.Printf("reshard-copy-poc: cleanup complete — %s rows reclaimed from source across %d table(s)\n", targetShard, len(tables))
		return
	}

	for _, tbl := range tables {
		fmt.Printf("reshard-copy-poc: InitialCopy (range) table=%q vindex=%s target=%s source→target\n", tbl, col, targetShard)
		copied, scanned, err := router.CopyShardRange(ctx, src, tgt, tbl, spec, targetShard)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d/%d before error)\n", err, copied, scanned)
			os.Exit(1)
		}
		fmt.Printf("reshard-copy-poc: copied %d/%d row(s) of %q (only %s keys) source→target\n",
			copied, scanned, tbl, targetShard)
	}

	if os.Getenv("PGROUTER_RESHARD_DELETE_AFTER") == "" {
		return
	}
	// Cutover cleanup: delete moved rows from source (run only after routing switch).
	for _, tbl := range tables {
		fmt.Printf("reshard-copy-poc: Cutover cleanup — deleting %s keys from %q in source\n", targetShard, tbl)
		deleted, err := router.DeleteShardRange(ctx, src, tbl, spec, targetShard)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: delete: %v (deleted %d before error)\n", err, deleted)
			os.Exit(1)
		}
		fmt.Printf("reshard-copy-poc: deleted %d row(s) of %q from source\n", deleted, tbl)
	}
	fmt.Printf("reshard-copy-poc: split complete — %s now owns its range across %d table(s)\n", targetShard, len(tables))
}

// runCDC 는 online resharding 의 논리복제 단계를 수행한다.
//   - cdc-setup: target 스키마 보장 + source publication + target subscription(copy_data=true)
//     생성 후 lag ≤ CDC_MAX_LAG 까지 대기(라이브 쓰기 따라잡기, write-block 없음).
//   - cdc-finalize: 최종 drain(lag→0, write-block 하에 호출 전제) + subscription drop +
//     DeleteForeignRange(범위 밖 정리) + publication drop.
//   - cdc-abort: 실패/중단 경로에서 subscription + publication 만 멱등 정리.
func runCDC(ctx context.Context, mode, src, tgt, targetShard string) {
	if targetShard == "" {
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: CDC requires PGROUTER_RESHARD_TARGET_SHARD")
		os.Exit(2)
	}
	pub := "rsd_pub_" + sanitize(targetShard)
	sub := "rsd_sub_" + sanitize(targetShard)
	connInfo := src
	if v := os.Getenv("PGROUTER_SOURCE_CONNINFO"); v != "" {
		connInfo = v
	}
	maxLag := int64(16 << 20)
	if v := os.Getenv("PGROUTER_CDC_MAX_LAG"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			maxLag = n
		}
	}

	switch mode {
	case "cdc-setup":
		tables := cdcTables(ctx, src)
		fmt.Printf("reshard-copy-poc: cdc-setup target=%s tables=%v\n", targetShard, tables)
		must(router.EnsureSchema(ctx, src, tgt, tables), "ensure schema")
		must(router.CreatePublication(ctx, src, pub, tables), "create publication")
		must(router.CreateSubscription(ctx, tgt, connInfo, sub, pub, true), "create subscription")
		waitLag(ctx, src, sub, maxLag)
		fmt.Printf("reshard-copy-poc: cdc-setup 완료 — subscription %s 활성, lag ≤ %d\n", sub, maxLag)
	case "cdc-finalize":
		col := os.Getenv("PGROUTER_VINDEX_COLUMN")
		if col == "" {
			col = "id"
		}
		spec := reshardSpec(os.Getenv("PGROUTER_VINDEX_TYPE"), col, os.Getenv("PGROUTER_VINDEX_FUNCTION"), os.Getenv("PGROUTER_RANGES"))
		tables := cdcTables(ctx, src)
		fmt.Printf("reshard-copy-poc: cdc-finalize target=%s (write-block 하 최종 drain)\n", targetShard)
		waitLag(ctx, src, sub, 0) // write-block 하 → 새 쓰기 없음 → lag→0 수렴.
		must(router.DropSubscription(ctx, tgt, sub), "drop subscription")
		total := 0
		for _, t := range tables {
			n, err := router.DeleteForeignRange(ctx, tgt, t, spec, targetShard)
			must(err, "delete foreign range "+t)
			total += n
		}
		// 범위 정리 후 인덱스/PK + 제약(CHECK·FK) 복제(데이터 확정 후 — bulk 효율).
		for _, t := range tables {
			_, err := router.ReplicateIndexes(ctx, src, tgt, t)
			must(err, "replicate indexes "+t)
			_, err = router.ReplicateConstraints(ctx, src, tgt, t)
			must(err, "replicate constraints "+t)
		}
		must(router.DropPublication(ctx, src, pub), "drop publication")
		fmt.Printf("reshard-copy-poc: cdc-finalize 완료 — 범위 밖 %d row 삭제, %s 자기 범위만 보유\n", total, targetShard)
	case "cdc-abort":
		fmt.Printf("reshard-copy-poc: cdc-abort target=%s\n", targetShard)
		must(router.DropSubscription(ctx, tgt, sub), "drop subscription")
		must(router.DropPublication(ctx, src, pub), "drop publication")
		fmt.Printf("reshard-copy-poc: cdc-abort 완료 — subscription %s / publication %s 정리\n", sub, pub)
	}
}

// cdcTables 는 COPY_TABLE 지정 시 그 하나, 아니면 source user 테이블에서 reference 제외 전부.
func cdcTables(ctx context.Context, src string) []string {
	if t := os.Getenv("PGROUTER_COPY_TABLE"); t != "" {
		return []string{t}
	}
	all, err := router.ListUserTables(ctx, src)
	must(err, "list tables")
	return router.FilterTables(all, csv(os.Getenv("PGROUTER_REFERENCE_TABLES")))
}

// waitLag 는 subscription 슬롯 lag 가 maxLag 이하가 될 때까지 폴링한다(ctx 만료 시 실패).
func waitLag(ctx context.Context, src, sub string, maxLag int64) {
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: lag wait timeout (sub=%s)\n", sub)
			os.Exit(1)
		default:
		}
		lag, err := router.SubscriptionLagBytes(ctx, src, sub)
		must(err, "lag")
		if lag >= 0 && lag <= maxLag {
			return
		}
		time.Sleep(time.Second)
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: %s: %v\n", what, err)
		os.Exit(1)
	}
}

// csv 는 콤마 구분 문자열을 trim 해 분리한다(빈 값 제거).
func csv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
