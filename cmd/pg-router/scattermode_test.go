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

func TestScatterQuery_NoShardsSendsReadyForQuery(t *testing.T) {
	client, routerSide := net.Pipe()
	defer client.Close()
	defer routerSide.Close()

	qr := queryRouter{
		provider: router.StaticTopologyProvider{T: router.Topology{}},
		write:    func(s string) (string, error) { return s + ":5432", nil },
	}
	done := make(chan struct{}, 1)
	go func() {
		scatterQuery(routerSide, qr, pgMessage{Type: 'Q', Payload: cstring("SELECT * FROM t")}, nil, nil, "")
		done <- struct{}{}
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
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scatterQuery did not return")
	}
}
