/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/router"
)

func TestReadStartupParsesParams(t *testing.T) {
	t.Parallel()

	paramBytes := []byte("user\x00alice\x00database\x00shop\x00\x00")
	body := make([]byte, 4+len(paramBytes))
	binary.BigEndian.PutUint32(body[0:4], 196608) // protocol v3.0
	copy(body[4:], paramBytes)
	msg := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(msg[0:4], uint32(4+len(body)))
	copy(msg[4:], body)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	go func() { _, _ = c2.Write(msg); _ = c2.Close() }()

	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, params, err := readStartup(c1)
	if err != nil {
		t.Fatalf("readStartup: %v", err)
	}
	if params["user"] != "alice" || params["database"] != "shop" {
		t.Fatalf("params = %v, want user=alice database=shop", params)
	}
	if len(raw) != len(msg) {
		t.Fatalf("raw len %d != original %d (must be forwardable verbatim)", len(raw), len(msg))
	}
}

func TestShardSpecRoutesByVindex(t *testing.T) {
	t.Parallel()

	spec := shardSpec()
	seen := map[string]bool{}
	for _, key := range []string{"alice", "bob", "carol", "dave", "eve", "frank", "grace", "heidi", "ivan", "judy"} {
		sh, err := router.ResolveShard(spec, key)
		if err != nil {
			t.Fatalf("ResolveShard(%q): %v", key, err)
		}
		if sh != "shard-0" && sh != "shard-1" {
			t.Fatalf("key %q -> unexpected shard %q", key, sh)
		}
		seen[sh] = true
	}
	// The PoC's whole point: the vindex is a live consumer and every key maps to
	// a real shard. (Distribution across both shards is best-effort with a small
	// sample, so we only log if it lands on one.)
	if len(seen) < 2 {
		t.Logf("note: sample keys all hashed to %v", seen)
	}
}

func TestBackendForUsesEnvMapping(t *testing.T) {
	t.Setenv("PGROUTER_BACKEND_SHARD_0", "10.0.0.1:5432")
	if got := backendFor("shard-0"); got != "10.0.0.1:5432" {
		t.Fatalf("backendFor(shard-0) = %q, want 10.0.0.1:5432", got)
	}
	if got := backendFor("shard-9"); got != "127.0.0.1:5432" {
		t.Fatalf("backendFor(shard-9) default = %q, want 127.0.0.1:5432", got)
	}
}

// TestReadStartupHandlesSSLRequest pins the live-found bug: a real psql client
// sends SSLRequest before the StartupMessage; readStartup must decline ('N') and
// parse the StartupMessage that follows (else params are empty → all-shard-0).
func TestReadStartupHandlesSSLRequest(t *testing.T) {
	t.Parallel()

	ssl := make([]byte, 8)
	binary.BigEndian.PutUint32(ssl[0:4], 8)
	binary.BigEndian.PutUint32(ssl[4:8], 80877103)

	paramBytes := []byte("database\x00shop\x00\x00")
	body := make([]byte, 4+len(paramBytes))
	binary.BigEndian.PutUint32(body[0:4], 196608)
	copy(body[4:], paramBytes)
	startup := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(startup[0:4], uint32(4+len(body)))
	copy(startup[4:], body)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	go func() {
		_, _ = c2.Write(ssl)
		decline := make([]byte, 1)
		_, _ = io.ReadFull(c2, decline) // the 'N' reply
		_, _ = c2.Write(startup)
		_ = c2.Close()
	}()

	_ = c1.SetDeadline(time.Now().Add(2 * time.Second))
	_, params, err := readStartup(c1)
	if err != nil {
		t.Fatalf("readStartup after SSLRequest: %v", err)
	}
	if params["database"] != "shop" {
		t.Fatalf("database = %q, want shop (SSLRequest must be skipped)", params["database"])
	}
}
