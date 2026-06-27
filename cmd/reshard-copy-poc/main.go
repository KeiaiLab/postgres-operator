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
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

// reshardSpec is the 2-shard murmur3 hash vindex matching cmd/pg-router shardSpec.
func reshardSpec(col string) v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: col, Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

func main() {
	src := os.Getenv("PGROUTER_SOURCE_DSN")
	tgt := os.Getenv("PGROUTER_TARGET_DSN")
	table := os.Getenv("PGROUTER_COPY_TABLE")
	targetShard := os.Getenv("PGROUTER_RESHARD_TARGET_SHARD")
	if src == "" || tgt == "" || table == "" {
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_SOURCE_DSN/TARGET_DSN/COPY_TABLE required")
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
	spec := reshardSpec(col)
	fmt.Printf("reshard-copy-poc: InitialCopy (range) table=%q vindex=%s target=%s source→target\n", table, col, targetShard)
	copied, scanned, err := router.CopyShardRange(ctx, src, tgt, table, spec, targetShard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d/%d before error)\n", err, copied, scanned)
		os.Exit(1)
	}
	fmt.Printf("reshard-copy-poc: copied %d/%d row(s) (only %s keys) source→target (rollback=drop target)\n",
		copied, scanned, targetShard)

	if os.Getenv("PGROUTER_RESHARD_DELETE_AFTER") == "" {
		return
	}
	// Cutover cleanup: delete moved rows from source (run only after routing switch).
	fmt.Printf("reshard-copy-poc: Cutover cleanup — deleting %s keys from source\n", targetShard)
	deleted, err := router.DeleteShardRange(ctx, src, table, spec, targetShard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: delete: %v (deleted %d before error)\n", err, deleted)
		os.Exit(1)
	}
	fmt.Printf("reshard-copy-poc: deleted %d row(s) from source (split complete: %s now owns its range)\n",
		deleted, targetShard)
}
