/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command scatter-poc is a G3 multi-shard query live PoC. It builds
// router.SQLShardExecutor (the live lib/pq consumer) from per-shard DSNs and:
//
//   - if the query is a point query (router.ExtractRoutingKey finds a routing
//     key), routes it to the single owning shard via the vindex
//     (router.ResolveShard) — SQL-parse single-shard fast-path, avoiding an
//     unnecessary fan-out to the other shards;
//   - otherwise fans the query out to every shard and merges
//     (router.ScatterGather) — scatter-gather.
//
// This exercises both G3 paths (RFC-0004 §2.2) against real PostgreSQL backends.
// Full SQL parsing (complex predicates, prepared statements) is future work.
//
// Config (env):
//
//	PGROUTER_SCATTER_QUERY        query (default "SELECT 1")
//	PGROUTER_SHARD_<N>_DSN        DSN for shard-N (N=0,1,2,...; contiguous)
//	PGROUTER_SCATTER_POLICY       scatter fallback policy: "fail-fast" (default) | "best-effort"
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

func main() {
	query := env("PGROUTER_SCATTER_QUERY", "SELECT 1")

	dsns := map[router.ShardID]string{}
	var shards []router.ShardID
	for i := 0; ; i++ {
		dsn := os.Getenv(fmt.Sprintf("PGROUTER_SHARD_%d_DSN", i))
		if dsn == "" {
			break
		}
		sid := router.ShardID(fmt.Sprintf("shard-%d", i))
		dsns[sid] = dsn
		shards = append(shards, sid)
	}
	if len(shards) == 0 {
		fmt.Fprintln(os.Stderr, "scatter-poc: no PGROUTER_SHARD_<N>_DSN set")
		os.Exit(2)
	}

	exec := &router.SQLShardExecutor{DSNs: dsns}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// SQL-parse routing: point query 면 routing key 를 추출하여 vindex 로 단일 shard 에만
	// 보낸다 (scatter 회피). 그렇지 않으면 scatter-gather 로 fallback.
	if key, routed := router.ExtractRoutingKey(query); routed {
		shardID, err := router.ResolveShard(shardSpec(), key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scatter-poc: resolve shard for key %q: %v\n", key, err)
			os.Exit(1)
		}
		fmt.Printf("scatter-poc: SQL-parse routed key=%q -> single shard %s (scatter 회피)\n", key, shardID)
		rows, err := exec.ExecuteOne(ctx, router.ShardID(shardID), query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scatter-poc: execute: %v\n", err)
			os.Exit(1)
		}
		printRows(rows)
		return
	}

	policy := router.FailFast
	if os.Getenv("PGROUTER_SCATTER_POLICY") == "best-effort" {
		policy = router.BestEffort
	}
	sg := &router.ScatterGather{Shard: exec, Policy: policy, Merge: router.MergeConcat}
	fmt.Printf("scatter-poc: scatter query=%q shards=%v policy=%v\n", query, shards, policy)
	rows, err := sg.Execute(ctx, query, shards)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scatter-poc: execute: %v\n", err)
		os.Exit(1)
	}
	printRows(rows)
}

func printRows(rows []router.Row) {
	fmt.Printf("scatter-poc: %d row(s):\n", len(rows))
	for _, r := range rows {
		fmt.Printf("  [%s] %v\n", r.Shard, r.Values)
	}
}

// shardSpec 은 2-shard murmur3 hash vindex (cmd/pg-router 와 동일).
func shardSpec() v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Cluster:  "scatter-poc",
		Keyspace: "default",
		Vindex:   v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
