/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestPgMessageRoundTrip 은 writeMessage → readMessage 가 타입/payload 를 보존함을 검증.
func TestPgMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMessage(&buf, 'Q', cstring("SELECT 1")); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	m, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if m.Type != 'Q' {
		t.Fatalf("type = %q, want 'Q'", m.Type)
	}
	sql, ok := querySQL(m)
	if !ok || sql != "SELECT 1" {
		t.Fatalf("querySQL = (%q,%v), want (SELECT 1,true)", sql, ok)
	}
}

// TestQuerySQL_NonQuery 는 'Q' 가 아닌 메시지는 거부함을 검증.
func TestQuerySQL_NonQuery(t *testing.T) {
	if _, ok := querySQL(pgMessage{Type: 'P', Payload: cstring("x")}); ok {
		t.Fatal("non-Query message should not yield SQL")
	}
}

// TestReadMessage_BadLength 는 비정상 길이를 에러로 처리함을 검증.
func TestReadMessage_BadLength(t *testing.T) {
	bad := []byte{'Q', 0, 0, 0, 0} // length=0 < 4
	if _, err := readMessage(bytes.NewReader(bad)); err == nil {
		t.Fatal("length<4 should error")
	}
}

// TestSendTrustHandshake 는 trust 핸드셰이크가 유효한 메시지 시퀀스(R,S,S,S,K,Z)를
// 내보내고 ReadyForQuery 가 idle 임을 검증.
func TestSendTrustHandshake(t *testing.T) {
	var buf bytes.Buffer
	if err := sendTrustHandshake(&buf, "18.3"); err != nil {
		t.Fatalf("sendTrustHandshake: %v", err)
	}
	var types []byte
	r := bytes.NewReader(buf.Bytes())
	for r.Len() > 0 {
		hdr := make([]byte, 5)
		if _, err := r.Read(hdr); err != nil {
			t.Fatalf("read hdr: %v", err)
		}
		length := binary.BigEndian.Uint32(hdr[1:5])
		body := make([]byte, length-4)
		if length > 4 {
			_, _ = r.Read(body)
		}
		types = append(types, hdr[0])
		if hdr[0] == 'Z' && (len(body) != 1 || body[0] != 'I') {
			t.Fatalf("ReadyForQuery body = %v, want ['I']", body)
		}
	}
	want := "RSSSKZ" // AuthOk, 3x ParameterStatus, BackendKeyData, ReadyForQuery
	if string(types) != want {
		t.Fatalf("message types = %q, want %q", string(types), want)
	}
}
