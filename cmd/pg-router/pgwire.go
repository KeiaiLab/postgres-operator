/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// pgwire.go 는 PostgreSQL v3 wire-protocol 의 *메시지 프레이밍*이다 — 쿼리 인지
// 라우팅(E, 프로토콜 종단)의 토대. startup 단계 이후의 typed 메시지(1바이트 타입 +
// Int32 길이 + payload)를 읽고 쓰며, 'Q'(simple Query)에서 SQL 을 뽑고, trust 모드
// 핸드셰이크(클라이언트가 인증된 것으로 믿게 하는 최소 응답)를 보낸다.
//
// 종단(termination) 전체는 클라이언트 인증을 라우터가 떠안고 백엔드로 별도 인증·재생
// 하는 큰 작업이며 라이브 PG 검증이 필요하다. 본 파일은 그중 *순수 프레이밍* 만 담아
// net.Pipe 로 단위 검증 가능하게 한다.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// pgMessage 는 startup 이후의 framed v3 메시지다.
type pgMessage struct {
	Type    byte
	Payload []byte
}

// readMessage 는 typed v3 메시지 1개를 읽는다.
func readMessage(r io.Reader) (pgMessage, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return pgMessage{}, err
	}
	length := binary.BigEndian.Uint32(hdr[1:5])
	if length < 4 || length > 1<<24 {
		return pgMessage{}, fmt.Errorf("pgwire: invalid message length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return pgMessage{}, err
	}
	return pgMessage{Type: hdr[0], Payload: payload}, nil
}

// writeMessage 는 typed v3 메시지 1개를 쓴다 (길이는 자기 자신 포함 Int32).
func writeMessage(w io.Writer, typ byte, payload []byte) error {
	hdr := []byte{typ, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(hdr[1:5], uint32(4+len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// querySQL 은 'Q'(simple Query) 메시지에서 SQL 텍스트를 뽑는다 (null 종단 제거).
func querySQL(m pgMessage) (string, bool) {
	if m.Type != 'Q' {
		return "", false
	}
	p := m.Payload
	if n := len(p); n > 0 && p[n-1] == 0 {
		p = p[:n-1]
	}
	return string(p), true
}

// cstring 은 null 종단 문자열 payload 를 만든다.
func cstring(s string) []byte {
	return append([]byte(s), 0)
}

// sendTrustHandshake 는 startup 직후 클라이언트에게 *인증 성공*으로 보이게 하는 최소
// 시퀀스를 보낸다: AuthenticationOk → 몇 개 ParameterStatus → BackendKeyData →
// ReadyForQuery(idle). trust 모드 — 비밀번호 검증 없음(개발/PoC). 실제 백엔드 인증은
// 라우터가 백엔드 연결 시 별도로 수행한다.
func sendTrustHandshake(w io.Writer, serverVersion string) error {
	// AuthenticationOk: 'R' + Int32(0)
	if err := writeMessage(w, 'R', []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	// ParameterStatus: 'S' + key\0value\0
	for _, kv := range [][2]string{
		{"server_version", serverVersion},
		{"client_encoding", "UTF8"},
		{"DateStyle", "ISO, MDY"},
	} {
		if err := writeMessage(w, 'S', append(cstring(kv[0]), cstring(kv[1])...)); err != nil {
			return err
		}
	}
	// BackendKeyData: 'K' + Int32 pid + Int32 secret
	if err := writeMessage(w, 'K', []byte{0, 0, 0, 1, 0, 0, 0, 1}); err != nil {
		return err
	}
	// ReadyForQuery: 'Z' + 'I'(idle)
	return writeMessage(w, 'Z', []byte{'I'})
}
