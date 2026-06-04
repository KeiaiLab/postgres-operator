/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command scatter-poc is a G3 scatter-gather live PoC. It builds a
// router.ScatterGather backed by router.SQLShardExecutor (the live lib/pq
// consumer) from per-shard DSNs in the environment, fans the query out to every
// shard, merges the results, and prints them. This exercises the previously
// test-only ShardExecutor path against real PostgreSQL backends (RFC-0004 §2.2
// Scenario 2). Full SQL-parse routing is future work; here the query is taken
// verbatim and broadcast to all shards (UNION ALL semantics via MergeConcat).
//
// Config (env):
//
//	PGROUTER_SCATTER_QUERY        query to broadcast (default "SELECT 1")
//	PGROUTER_SHARD_<N>_DSN        DSN for shard-N (N=0,1,2,...; contiguous)
//	PGROUTER_SCATTER_POLICY       "fail-fast" (default) | "best-effort"
package main

import (
	"context"
	"fmt"
	"os"
	"time"

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

	policy := router.FailFast
	if os.Getenv("PGROUTER_SCATTER_POLICY") == "best-effort" {
		policy = router.BestEffort
	}

	sg := &router.ScatterGather{
		Shard:  &router.SQLShardExecutor{DSNs: dsns},
		Policy: policy,
		Merge:  router.MergeConcat,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("scatter-poc: query=%q shards=%v policy=%v\n", query, shards, policy)
	rows, err := sg.Execute(ctx, query, shards)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scatter-poc: execute: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("scatter-poc: %d row(s) gathered + merged:\n", len(rows))
	for _, r := range rows {
		fmt.Printf("  [%s] %v\n", r.Shard, r.Values)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
