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

// TestParseSQL 은 'P'(Parse, extended) 메시지에서 쿼리 텍스트를 뽑음을 검증.
func TestParseSQL(t *testing.T) {
	var payload []byte
	payload = append(payload, cstring("")...)                             // 무명 statement
	payload = append(payload, cstring("SELECT v FROM t WHERE id='x'")...) // 쿼리
	payload = append(payload, 0, 0)                                       // Int16 param 수 = 0
	sql, ok := parseSQL(pgMessage{Type: 'P', Payload: payload})
	if !ok || sql != "SELECT v FROM t WHERE id='x'" {
		t.Fatalf("parseSQL = (%q,%v), want the query", sql, ok)
	}
	if _, ok := parseSQL(pgMessage{Type: 'Q', Payload: cstring("x")}); ok {
		t.Fatal("non-Parse should be rejected")
	}
}

// TestBindParams 는 'B'(Bind) 메시지에서 파라미터 값 추출(NULL 포함)을 검증.
func TestBindParams(t *testing.T) {
	var p []byte
	p = append(p, cstring("")...) // portal
	p = append(p, cstring("")...) // statement
	p = append(p, 0, 0)           // numFormatCodes = 0
	p = append(p, 0, 2)           // numParams = 2
	p = append(p, 0, 0, 0, 5)     // param0 len = 5
	p = append(p, []byte("alice")...)
	p = append(p, 0xff, 0xff, 0xff, 0xff) // param1 = NULL
	params, ok := bindParams(pgMessage{Type: 'B', Payload: p})
	if !ok || len(params) != 2 || string(params[0]) != "alice" || params[1] != nil {
		t.Fatalf("bindParams = %v ok=%v, want [alice, nil]", params, ok)
	}
	if _, ok := bindParams(pgMessage{Type: 'Q'}); ok {
		t.Fatal("non-Bind should be rejected")
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
