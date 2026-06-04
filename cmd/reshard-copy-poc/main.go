/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command reshard-copy-poc is a G3 online-resharding InitialCopy live PoC. It
// copies a table from a source shard to a target shard via router.CopyTable —
// the *reversible* data-movement step of ShardSplitJob (rollback = drop target).
// The irreversible Cutover (write-block + routing switch) is intentionally out of
// scope here. Config: PGROUTER_SOURCE_DSN, PGROUTER_TARGET_DSN, PGROUTER_COPY_TABLE.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/keiailab/postgres-operator/internal/router"
)

func main() {
	src := os.Getenv("PGROUTER_SOURCE_DSN")
	tgt := os.Getenv("PGROUTER_TARGET_DSN")
	table := os.Getenv("PGROUTER_COPY_TABLE")
	if src == "" || tgt == "" || table == "" {
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: PGROUTER_SOURCE_DSN/TARGET_DSN/COPY_TABLE required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("reshard-copy-poc: InitialCopy table=%q source→target\n", table)
	n, err := router.CopyTable(ctx, src, tgt, table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d before error)\n", err, n)
		os.Exit(1)
	}
	fmt.Printf("reshard-copy-poc: copied %d row(s) source→target (rollback=drop target)\n", n)
}
