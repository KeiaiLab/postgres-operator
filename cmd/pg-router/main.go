/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Command pg-router is a PoC PostgreSQL wire-protocol v3 proxy that routes a
// client connection to a shard backend using the in-tree vindex
// (internal/router.ResolveShard). It revives the G3 router package (restored from
// before its #124 dead-code removal) as a *live consumer*, so that code is no
// longer dead (ROADMAP G3 / RFC 0004).
//
// Scope (PoC): single-shard fast-path. The routing key is taken from the startup
// "database" (else "user") parameter; the connection is then proxied to the
// resolved shard backend. Full SQL-parse routing and multi-shard scatter-gather
// forwarding (via internal/router.ScatterGather) are future work (G5).
//
// Config (env): PGROUTER_LISTEN (default :5432), PGROUTER_CLUSTER,
// PGROUTER_BACKEND_SHARD_0 / PGROUTER_BACKEND_SHARD_1 (host:port).
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

func main() {
	addr := env("PGROUTER_LISTEN", ":5432")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("pg-router: listen %s: %v", addr, err)
	}
	log.Printf("pg-router PoC listening on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("pg-router: accept: %v", err)
			continue
		}
		go handleConn(conn)
	}
}

func handleConn(client net.Conn) {
	defer func() { _ = client.Close() }()

	raw, params, err := readStartup(client)
	if err != nil {
		log.Printf("pg-router: startup read: %v", err)
		return
	}
	key := params["database"]
	if key == "" {
		key = params["user"]
	}
	shardID, err := router.ResolveShard(shardSpec(), key)
	if err != nil {
		log.Printf("pg-router: resolve shard for key %q: %v", key, err)
		return
	}
	backend := backendFor(shardID)
	server, err := net.Dial("tcp", backend)
	if err != nil {
		log.Printf("pg-router: dial backend %s (shard %s): %v", backend, shardID, err)
		return
	}
	defer func() { _ = server.Close() }()
	log.Printf("pg-router: routed key=%q -> shard=%s backend=%s", key, shardID, backend)

	// Forward the original startup message, then proxy both directions.
	if _, err := server.Write(raw); err != nil {
		log.Printf("pg-router: forward startup: %v", err)
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}

// readStartup reads a PostgreSQL v3 startup message and returns its raw bytes
// (for forwarding) plus the parsed parameters.
func readStartup(conn net.Conn) ([]byte, map[string]string, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, nil, err
	}
	length := binary.BigEndian.Uint32(hdr)
	if length < 8 || length > 1<<20 {
		return nil, nil, fmt.Errorf("invalid startup length %d", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, nil, err
	}
	// SSLRequest (80877103) / GSSENCRequest (80877104) precede the real
	// StartupMessage. Real psql clients send SSLRequest first; the PoC speaks
	// plaintext only, so decline with 'N' and read the StartupMessage that
	// follows. Without this the request was mis-parsed as a (param-less) startup
	// and every connection routed to shard-0 (live-found, pg-e2e 2026-06-04).
	if code := binary.BigEndian.Uint32(body[0:4]); code == 80877103 || code == 80877104 {
		if _, err := conn.Write([]byte{'N'}); err != nil {
			return nil, nil, err
		}
		return readStartup(conn)
	}
	raw := append(hdr, body...)
	// body[0:4] = protocol version; params follow as key\0value\0...\0\0
	params := map[string]string{}
	for parts := strings.Split(string(body[4:]), "\x00"); len(parts) >= 2; parts = parts[2:] {
		if parts[0] == "" {
			break
		}
		params[parts[0]] = parts[1]
	}
	return raw, params, nil
}

// shardSpec is the PoC routing table (a 2-shard hash vindex). In production this
// is sourced from ShardRange CRDs reconciled by the operator.
func shardSpec() v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Cluster:  env("PGROUTER_CLUSTER", "quickstart"),
		Keyspace: "default",
		Vindex:   v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

func backendFor(shardID string) string {
	envKey := "PGROUTER_BACKEND_" + strings.ToUpper(strings.ReplaceAll(shardID, "-", "_"))
	return env(envKey, "127.0.0.1:5432")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
