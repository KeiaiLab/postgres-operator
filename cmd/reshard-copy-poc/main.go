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
// PGROUTER_VINDEX_COLUMN, default "id") matches cmd/pg-router shardSpec so the
// copy and the router agree on which keys belong where.
//
// Config: PGROUTER_SOURCE_DSN, PGROUTER_TARGET_DSN, PGROUTER_COPY_TABLE,
// PGROUTER_RESHARD_TARGET_SHARD, PGROUTER_VINDEX_COLUMN, PGROUTER_RESHARD_DELETE_AFTER.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

// reshardSpec builds the post-split vindex spec. The ranges come from
// PGROUTER_RANGES ("shard:lo:hi,shard:lo:hi") when set (controller passes the
// target topology), else the default 2-shard murmur3 split (standalone use).
// fn is the vindex function from PGROUTER_VINDEX_FUNCTION (default murmur3).
func reshardSpec(col, fn, rangesEnv string) v1alpha1.ShardRangeSpec {
	if fn == "" {
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
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: col, Function: v1alpha1.VindexHashFunction(fn)},
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
	// delete-only: cutover 후 source 에서 이동분 삭제만(복사 없음, Cleanup phase). target 불요.
	deleteOnly := os.Getenv("PGROUTER_RESHARD_DELETE_ONLY") != ""
	switch {
	case src == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_SOURCE_DSN required")
		os.Exit(2)
	case deleteOnly && targetShard == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: DELETE_ONLY requires PGROUTER_RESHARD_TARGET_SHARD")
		os.Exit(2)
	case !deleteOnly && tgt == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_TARGET_DSN required (unless DELETE_ONLY)")
		os.Exit(2)
	case targetShard == "" && table == "":
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: full copy requires PGROUTER_COPY_TABLE")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

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
	spec := reshardSpec(col, os.Getenv("PGROUTER_VINDEX_FUNCTION"), os.Getenv("PGROUTER_RANGES"))

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
