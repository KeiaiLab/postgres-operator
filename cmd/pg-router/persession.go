/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// persession.go 는 *per-query 라우팅* 세션을 구현한다 — 연결 고정(첫 쿼리로 샤드 결정)을
// 풀어, 한 연결의 *매 simple Query* 를 그 키의 샤드로 라우팅한다(vtgate 모델). 샤드별 백엔드
// 연결을 세션 내에서 lazy 풀링·재사용한다.
//
// 지원: autocommit 키 라우팅, 키 없는 쿼리 scatter, *단일샤드* 명시적 트랜잭션(BEGIN 응답을
// 합성하고 첫 키 쿼리로 한 샤드에 pin — cross-shard 2PC 는 범위 밖). extended protocol
// (Parse/Bind/Describe/Execute/Sync)도 per-query 로 라우팅한다 — Sync 까지 버퍼링해 배치
// 단위로 키의 샤드에 보내고 샤드별 prepared statement 를 lazy 관리한다(extsession.go).
package main

import (
	"fmt"
	"net"
	"strings"
)

// session 은 한 클라이언트 연결의 per-query 라우팅 상태다.
type session struct {
	client   net.Conn
	qr       queryRouter
	dialer   *backendDialer
	password string
	raw      []byte // 클라이언트 startup (백엔드 인증용)

	backends     map[string]net.Conn // backend addr → 연결 (lazy 풀).
	inTx         bool                // 명시적 트랜잭션 중.
	pendingBegin pgMessage           // 아직 백엔드로 안 보낸 BEGIN (pin 시 전송).
	txBackend    net.Conn            // 트랜잭션이 pin 된 백엔드.

	// extended protocol(per-query) 상태.
	extBuf       []pgMessage                  // Sync 까지 버퍼링한 extended 메시지.
	stmts        map[string]*pstmt            // statement 이름 → prepared 메타.
	backendStmts map[net.Conn]map[string]bool // 백엔드별 Parse 된 stmt 이름 집합.
}

// runPerQuerySession 은 핸드셰이크 후 세션 루프를 돈다.
func runPerQuerySession(client net.Conn, qr queryRouter, dialer *backendDialer, password string, raw []byte) {
	s := &session{
		client: client, qr: qr, dialer: dialer, password: password, raw: raw,
		backends:     map[string]net.Conn{},
		stmts:        map[string]*pstmt{},
		backendStmts: map[net.Conn]map[string]bool{},
	}
	defer s.closeBackends()
	for {
		m, err := readMessage(client)
		if err != nil {
			return
		}
		switch m.Type {
		case 'X': // Terminate
			return
		case 'Q': // simple Query → per-query 라우팅.
			if !s.handleSimpleQuery(m) {
				return
			}
		case 'P', 'B', 'D', 'E', 'C', 'H': // extended → Sync 까지 버퍼링.
			s.extBuf = append(s.extBuf, m)
		case 'S': // Sync → 버퍼링된 extended 배치를 per-query 라우팅.
			s.extBuf = append(s.extBuf, m)
			if !s.handleExtendedBatch() {
				return
			}
		default:
			writePgError(client, "0A000", fmt.Sprintf("message type %q not supported in per-query mode", m.Type))
			return
		}
	}
}

// handleSimpleQuery 는 한 simple Query 를 라우팅·실행하고 결과를 relay 한다. 세션 계속이면
// true.
func (s *session) handleSimpleQuery(m pgMessage) bool {
	sql, ok := querySQL(m)
	if !ok {
		writePgError(s.client, "08P01", "could not parse query")
		return false
	}
	kw := firstKeyword(sql)

	// 트랜잭션 pin 됨: pin 된 백엔드로.
	if s.inTx && s.txBackend != nil {
		alive := s.execAndRelay(s.txBackend, m)
		if kw == "commit" || kw == "rollback" || kw == "end" {
			s.inTx = false
			s.txBackend = nil
		}
		return alive
	}

	// BEGIN: 응답을 합성하고 다음 키 쿼리로 pin (BEGIN 은 키가 없어 단독 라우팅 불가).
	if !s.inTx && (kw == "begin" || kw == "start") {
		if err := fabricateBegin(s.client); err != nil {
			return false
		}
		s.inTx = true
		s.pendingBegin = m
		s.txBackend = nil
		return true
	}

	// 라우팅.
	d, err := s.qr.routeSQL(sql)
	if d.Scatter { // 키 없음 → scatter (자체 연결).
		if s.inTx {
			writePgError(s.client, "0A000", "cannot scatter a keyless query inside a transaction")
			return true
		}
		scatterQuery(s.client, s.qr, m, s.raw, s.dialer, s.password)
		return true
	}
	if err != nil {
		writePgError(s.client, "08006", "routing failed: "+err.Error())
		return true
	}
	conn, err := s.backendFor(d.Backend)
	if err != nil {
		writePgError(s.client, "08006", "backend: "+err.Error())
		return false
	}
	// 트랜잭션 시작 직후 첫 키 쿼리: BEGIN 을 이 샤드로 보내(응답 폐기) pin.
	if s.inTx && s.txBackend == nil {
		if err := writeMessage(conn, s.pendingBegin.Type, s.pendingBegin.Payload); err != nil {
			return false
		}
		if err := drainResponse(conn); err != nil {
			writePgError(s.client, "08006", "tx begin: "+err.Error())
			return false
		}
		s.txBackend = conn
	}
	logRoute('Q', d)
	return s.execAndRelay(conn, m)
}

// backendFor 는 backend 연결을 lazy 풀링·재사용한다 (세션 내).
func (s *session) backendFor(backend string) (net.Conn, error) {
	if c, ok := s.backends[backend]; ok {
		return c, nil
	}
	c, err := s.dialer.Dial(backend)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(s.raw); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := authenticateAndDrain(c, s.password); err != nil {
		_ = c.Close()
		return nil, err
	}
	s.backends[backend] = c
	return c, nil
}

// execAndRelay 는 query 를 backend 로 보내고 응답을 ReadyForQuery 까지 클라이언트로 relay
// 한다. 연결 유지면 true.
func (s *session) execAndRelay(conn net.Conn, m pgMessage) bool {
	if err := writeMessage(conn, m.Type, m.Payload); err != nil {
		return false
	}
	for {
		rm, err := readMessage(conn)
		if err != nil {
			return false
		}
		if err := writeMessage(s.client, rm.Type, rm.Payload); err != nil {
			return false
		}
		if rm.Type == 'Z' { // ReadyForQuery — 이 쿼리 완료.
			return true
		}
	}
}

func (s *session) closeBackends() {
	for _, c := range s.backends {
		_ = c.Close()
	}
}

// drainResponse 는 backend 응답을 ReadyForQuery 까지 읽어 *폐기* 한다 (합성 BEGIN 의 실응답).
func drainResponse(conn net.Conn) error {
	for {
		rm, err := readMessage(conn)
		if err != nil {
			return err
		}
		if rm.Type == 'E' {
			return fmt.Errorf("backend error: %s", string(rm.Payload))
		}
		if rm.Type == 'Z' {
			return nil
		}
	}
}

// fabricateBegin 은 클라이언트에 BEGIN 응답(CommandComplete + ReadyForQuery in-transaction)을
// 합성해 보낸다 — 실 BEGIN 은 첫 키 쿼리의 샤드로 미뤄 보낸다.
func fabricateBegin(client net.Conn) error {
	if err := writeMessage(client, 'C', cstring("BEGIN")); err != nil {
		return err
	}
	return writeMessage(client, 'Z', []byte{'T'}) // 'T' = in transaction block.
}

// firstKeyword 는 SQL 의 첫 단어를 소문자로 반환한다 (선행 공백/괄호 무시).
func firstKeyword(sql string) string {
	sql = strings.TrimLeft(sql, " \t\r\n(")
	i := strings.IndexAny(sql, " \t\r\n;(")
	if i < 0 {
		i = len(sql)
	}
	return strings.ToLower(sql[:i])
}
