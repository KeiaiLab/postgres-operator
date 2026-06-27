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
	"bytes"
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

// writeMessage 는 typed v3 메시지 1개를 쓴다 (길이는 자기 자신 포함 Int32). 헤더+payload 를
// 한 버퍼로 합쳐 *단일* Write 로 보낸다 — 메시지당 syscall 2→1 (라우터 per-query 오버헤드
// 감소). 버퍼링/flush 없이 즉시 전송하므로 deadlock 위험 없음.
func writeMessage(w io.Writer, typ byte, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(4+len(payload)))
	copy(buf[5:], payload)
	_, err := w.Write(buf)
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

// parseSQL 은 'P'(Parse, extended protocol) 메시지에서 쿼리 텍스트를 뽑는다. payload =
// statement-name(cstring) + query(cstring) + Int16 param 수 + .... 첫 cstring(이름)을
// 건너뛰고 둘째 cstring(쿼리)을 반환한다.
//
// 주의: parameterized 쿼리(`WHERE id = $1`)는 실제 값이 후속 Bind 메시지에 있으므로
// Parse 만으로는 라우팅 키를 못 얻는다(extractor 가 리터럴 없음 → scatter). Bind 까지
// 상관(correlate)하는 완전 extended 라우팅은 후속.
func parseSQL(m pgMessage) (string, bool) {
	if m.Type != 'P' {
		return "", false
	}
	i := bytes.IndexByte(m.Payload, 0)
	if i < 0 {
		return "", false
	}
	rest := m.Payload[i+1:]
	j := bytes.IndexByte(rest, 0)
	if j < 0 {
		return "", false
	}
	return string(rest[:j]), true
}

// bindParams 는 'B'(Bind) 메시지에서 파라미터 값들을 추출한다 (NULL 은 nil). payload =
// portal(cstring) + statement(cstring) + Int16 format수 + [Int16]×format + Int16
// param수 + (Int32 len + bytes)×param + .... *text 포맷 가정* — 라우팅 키(텍스트)는
// 드라이버가 text 로 보내는 게 보통. binary 포맷 값은 raw 바이트라 라우팅이 어긋날 수
// 있음(드문 케이스).
func bindParams(m pgMessage) ([][]byte, bool) {
	if m.Type != 'B' {
		return nil, false
	}
	p := m.Payload
	pos, ok := skipCString(p, 0) // portal
	if !ok {
		return nil, false
	}
	pos, ok = skipCString(p, pos) // statement
	if !ok {
		return nil, false
	}
	if pos+2 > len(p) {
		return nil, false
	}
	numFmt := int(binary.BigEndian.Uint16(p[pos:]))
	pos += 2 + numFmt*2 // format codes skip
	if pos+2 > len(p) {
		return nil, false
	}
	numParams := int(binary.BigEndian.Uint16(p[pos:]))
	pos += 2
	out := make([][]byte, 0, numParams)
	for k := 0; k < numParams; k++ {
		if pos+4 > len(p) {
			return nil, false
		}
		plen := int32(binary.BigEndian.Uint32(p[pos:]))
		pos += 4
		if plen < 0 { // NULL
			out = append(out, nil)
			continue
		}
		if pos+int(plen) > len(p) {
			return nil, false
		}
		out = append(out, p[pos:pos+int(plen)])
		pos += int(plen)
	}
	return out, true
}

// skipCString 은 pos 부터 null 종단 문자열을 건너뛴 다음 위치를 반환한다.
func skipCString(p []byte, pos int) (int, bool) {
	if pos > len(p) {
		return 0, false
	}
	rel := bytes.IndexByte(p[pos:], 0)
	if rel < 0 {
		return 0, false
	}
	return pos + rel + 1, true
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
