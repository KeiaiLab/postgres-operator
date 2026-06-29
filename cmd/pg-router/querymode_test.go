/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"net"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/router"
)

func testQR() queryRouter {
	provider := router.StaticTopologyProvider{T: router.Topology{Spec: shardSpec()}} // vindex column "id"
	write := func(s string) (string, error) { return s + ":5432", nil }
	return newQueryRouter(provider, write, nil)
}

// TestQueryRouter_routeSQL 은 인라인 리터럴 SQL 라우팅을 검증한다.
func TestQueryRouter_routeSQL(t *testing.T) {
	qr := testQR()
	for _, q := range []string{
		"INSERT INTO t (id, v) VALUES ('alice', 1)",
		"SELECT v FROM t WHERE id = 'bob'",
	} {
		d, err := qr.routeSQL(q)
		if err != nil || d.Shard == "" || d.Backend != d.Shard+":5432" {
			t.Fatalf("routeSQL(%q) = %+v err=%v", q, d, err)
		}
	}
	// 키 없음 → Scatter.
	if d, err := qr.routeSQL("SELECT * FROM t"); err == nil || !d.Scatter {
		t.Fatalf("no-key should scatter, got %+v err=%v", d, err)
	}
}

// TestQueryRouter_routeKey 는 *값 직접* 라우팅(extended Bind 파라미터)을 검증한다 —
// 같은 키는 routeSQL 과 같은 샤드로 가야 한다.
func TestQueryRouter_routeKey(t *testing.T) {
	qr := testQR()
	for _, key := range []string{"alice", "bob", "carol"} {
		bySQL, _ := qr.routeSQL("SELECT v FROM t WHERE id = '" + key + "'")
		byKey, err := qr.routeKey(key, false)
		if err != nil {
			t.Fatalf("routeKey(%q): %v", key, err)
		}
		if byKey.Shard != bySQL.Shard {
			t.Fatalf("key %q: routeKey shard=%s != routeSQL shard=%s", key, byKey.Shard, bySQL.Shard)
		}
	}
}

func TestSession_KeylessWriteDoesNotScatter(t *testing.T) {
	client, routerSide := net.Pipe()
	defer client.Close()
	defer routerSide.Close()

	s := &session{client: routerSide, qr: testQR()}
	done := make(chan bool, 1)
	go func() {
		done <- s.handleSimpleQuery(pgMessage{Type: 'Q', Payload: cstring("UPDATE t SET v=1")})
	}()

	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	errMsg, err := readMessage(client)
	if err != nil {
		t.Fatalf("read error response: %v", err)
	}
	if errMsg.Type != 'E' {
		t.Fatalf("first response type = %q, want E", errMsg.Type)
	}
	ready, err := readMessage(client)
	if err != nil {
		t.Fatalf("read ready response: %v", err)
	}
	if ready.Type != 'Z' || string(ready.Payload) != "I" {
		t.Fatalf("ready response = type %q payload %q, want Z/I", ready.Type, string(ready.Payload))
	}

	select {
	case keep := <-done:
		if !keep {
			t.Fatal("handleSimpleQuery should keep the session open after query error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleSimpleQuery did not return")
	}
}
