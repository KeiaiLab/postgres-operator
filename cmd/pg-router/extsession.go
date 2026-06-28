/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// extsession.go 는 *per-query extended protocol* 라우팅을 구현한다 — Parse/Bind/Describe/
// Execute/Sync 파이프라인을 세션이 끊기지 않은 채 *매 쿼리* 그 키의 샤드로 라우팅한다.
//
// 핵심 아이디어(vtgate 계열):
//   - 클라이언트의 extended 메시지를 Sync 까지 *버퍼링* 해 한 배치로 모은다.
//   - 라우팅 키는 Bind 의 파라미터(또는 인라인 리터럴)에 있으므로, Parse 만 온 단계에서는
//     *ParseComplete 를 합성* 해 ack 하고 실 Parse 는 미룬다(샤드 미정).
//   - Bind 가 오면 키로 샤드를 정하고, 그 샤드 백엔드에 stmt 가 아직 없으면 저장해 둔 Parse 를
//     *주입(prepare-on-first-use)* 한 뒤 Bind/Execute 를 보낸다. 주입한 Parse 의 ParseComplete 는
//     클라이언트가 요청하지 않았으므로 응답에서 *걸러낸다*.
//   - describe-first 드라이버(lib/pq·JDBC)의 `Parse→Describe→Sync`(Bind 전 메타데이터 조회)는
//     스키마가 샤드 공통이므로 임의(default) 샤드로 describe 를 대행한다.
//
// 지원: pgbench -M extended/prepared, lib/pq, pgx, JDBC 의 파라미터화·prepared 재사용.
// 미지원(후속): extended scatter(키 없는 파이프라인 fan-out), cross-shard 배치, Flush(H)
// 기반 파이프라이닝, named stmt 의 동일 이름 재Parse(Close 선행 필요).
package main

import (
	"bytes"
	"errors"
	"net"

	"github.com/keiailab/postgres-operator/internal/router"
)

// PG ParseComplete('1')·CloseComplete('3') 백엔드 메시지 타입.
const (
	msgParseComplete = '1'
	msgCloseComplete = '3'
)

// pstmt 는 세션에 등록된 prepared statement 메타다.
type pstmt struct {
	name     string
	sql      string
	paramIdx int       // `col=$N` 의 N (1-based). 0 이면 인라인 리터럴/키 없음.
	parseMsg pgMessage // 샤드 prepare-on-first-use 용 원본 Parse 메시지.
}

// handleExtendedBatch 는 Sync 로 끝나는 extended 메시지 배치(s.extBuf)를 처리한다.
func (s *session) handleExtendedBatch() bool {
	batch := s.extBuf
	s.extBuf = nil
	if len(batch) == 0 {
		return true
	}
	// 1) Parse 등록(라우팅 키 위치 계산).
	for _, m := range batch {
		if m.Type == 'P' {
			s.registerParse(m)
		}
	}
	// 2) 분류: Bind 있음 → 실행, 없고 Describe 있음 → describe 대행, 둘 다 없음 → ack 합성.
	var firstBind *pgMessage
	hasDescribe := false
	for i := range batch {
		switch batch[i].Type {
		case 'B':
			if firstBind == nil {
				firstBind = &batch[i]
			}
		case 'D':
			hasDescribe = true
		}
	}
	switch {
	case firstBind != nil:
		return s.runExtendedExec(batch, *firstBind)
	case hasDescribe:
		return s.runDescribeOnly(batch)
	default:
		return s.synthExtendedAcks(batch)
	}
}

// registerParse 는 Parse 를 세션 stmt 맵에 등록하고, 같은 이름의 백엔드 prepared 상태를
// 무효화한다(재Parse 대응).
func (s *session) registerParse(m pgMessage) {
	name := parseStmtName(m)
	sql, ok := parseSQL(m)
	if !ok {
		return
	}
	st := &pstmt{name: name, sql: sql, parseMsg: m}
	if idx, ok := router.ExtractParamRef(sql, s.qr.shardColumn()); ok {
		st.paramIdx = idx
	}
	s.stmts[name] = st
	for _, set := range s.backendStmts { // 재Parse: 백엔드별 캐시 무효화.
		delete(set, name)
	}
}

// runExtendedExec 는 Bind 가 있는 배치를 키의 샤드로 라우팅·실행한다.
func (s *session) runExtendedExec(batch []pgMessage, bind pgMessage) bool {
	stmtName := bindStmtName(bind)
	st := s.stmts[stmtName]
	if st == nil {
		writePgError(s.client, "26000", "prepared statement not found: "+stmtName)
		return s.sendReadyIdle()
	}

	// 라우팅: 파라미터화면 Bind 값으로, 인라인 리터럴이면 SQL 로.
	var (
		d   router.RouteDecision
		err error
	)
	if st.paramIdx > 0 {
		params, ok := bindParams(bind)
		if !ok || st.paramIdx-1 >= len(params) || params[st.paramIdx-1] == nil {
			writePgError(s.client, "08006", "could not extract routing parameter from Bind")
			return s.sendReadyIdle()
		}
		d, err = s.qr.routeKey(string(params[st.paramIdx-1]), router.IsReadOnlyQuery(st.sql))
	} else {
		d, err = s.qr.routeSQL(st.sql)
	}
	if errors.Is(err, router.ErrWriteBlocked) {
		writePgError(s.client, "25006", err.Error()) // cutover write-block.
		return s.sendReadyIdle()
	}
	if err != nil || d.Scatter {
		writePgError(s.client, "08006", "extended routing failed (no routing key; extended scatter unsupported)")
		return s.sendReadyIdle()
	}

	conn, err := s.backendForRouted(d)
	if err != nil {
		writePgError(s.client, "08006", "backend: "+err.Error())
		return false
	}
	set := s.preparedSet(conn)

	// Parse(클라이언트가 보낸 것) 와 그 외(Bind/Describe/Execute/Close)를 분리.
	var parses, others []pgMessage
	for _, m := range batch {
		switch m.Type {
		case 'P':
			parses = append(parses, m)
			set[parseStmtName(m)] = true
		case 'S': // 우리가 끝에 직접 Sync.
		default:
			others = append(others, m)
		}
	}
	// Bind 가 참조하는 stmt 가 이 백엔드에 없으면 Parse 주입(prepare-on-first-use).
	injected := 0
	if !set[stmtName] {
		parses = append([]pgMessage{st.parseMsg}, parses...)
		set[stmtName] = true
		injected = 1
	}

	// Parse 들을 먼저, 그 다음 Bind/Describe/Execute, 마지막에 Sync.
	for _, m := range parses {
		if err := writeMessage(conn, m.Type, m.Payload); err != nil {
			return false
		}
	}
	for _, m := range others {
		if err := writeMessage(conn, m.Type, m.Payload); err != nil {
			return false
		}
	}
	if err := writeMessage(conn, 'S', nil); err != nil {
		return false
	}
	logRoute('B', d)
	return s.relayExtended(conn, injected)
}

// runDescribeOnly 는 Bind 없는 `Parse?→Describe→Sync`(describe-first) 배치를 임의(default)
// 샤드로 대행한다 — 스키마는 샤드 공통. 트랜잭션 중이면 pin 된 백엔드를 쓴다.
func (s *session) runDescribeOnly(batch []pgMessage) bool {
	conn, err := s.describeBackend()
	if err != nil {
		writePgError(s.client, "08006", "describe-round: "+err.Error())
		return s.sendReadyIdle()
	}
	set := s.preparedSet(conn)
	for _, m := range batch { // Parse/Describe/Sync 전달.
		if m.Type == 'P' {
			set[parseStmtName(m)] = true
		}
		if err := writeMessage(conn, m.Type, m.Payload); err != nil {
			return false
		}
	}
	return s.relayExtended(conn, 0) // 클라이언트가 보낸 Parse 의 ParseComplete 는 모두 전달.
}

// synthExtendedAcks 는 Bind/Describe 없는 `Parse`/`Close` 전용 배치의 ack 를 합성한다 —
// 실 Parse 는 샤드 미정이라 미루고 ParseComplete/CloseComplete 만 즉시 돌려준다(prepared
// statement 캐싱 드라이버 대응: 미리 Parse 해두고 나중에 Bind→Execute).
func (s *session) synthExtendedAcks(batch []pgMessage) bool {
	for _, m := range batch {
		switch m.Type {
		case 'P':
			if err := writeMessage(s.client, msgParseComplete, nil); err != nil {
				return false
			}
		case 'C': // Close(statement/portal).
			s.handleClose(m)
			if err := writeMessage(s.client, msgCloseComplete, nil); err != nil {
				return false
			}
		}
	}
	return s.sendReadyIdle()
}

// backendForRouted 는 라우팅 결정의 백엔드를 반환하되 트랜잭션 pin 을 존중한다.
func (s *session) backendForRouted(d router.RouteDecision) (net.Conn, error) {
	if s.inTx && s.txBackend != nil {
		return s.txBackend, nil
	}
	conn, err := s.backendFor(d.Backend)
	if err != nil {
		return nil, err
	}
	if s.inTx && s.txBackend == nil { // tx 시작 후 첫 키 쿼리: BEGIN 을 이 샤드로 보내 pin.
		if err := writeMessage(conn, s.pendingBegin.Type, s.pendingBegin.Payload); err != nil {
			return nil, err
		}
		if err := drainResponse(conn); err != nil {
			return nil, err
		}
		s.txBackend = conn
	}
	return conn, nil
}

// describeBackend 는 describe 대행용 백엔드를 반환한다(tx pin 우선, 아니면 임의 샤드).
func (s *session) describeBackend() (net.Conn, error) {
	if s.inTx && s.txBackend != nil {
		return s.txBackend, nil
	}
	_, backend, err := s.qr.anyShard()
	if err != nil {
		return nil, err
	}
	return s.backendFor(backend)
}

// preparedSet 은 한 백엔드 연결의 prepared stmt 이름 집합을 lazy 반환한다.
func (s *session) preparedSet(conn net.Conn) map[string]bool {
	set := s.backendStmts[conn]
	if set == nil {
		set = map[string]bool{}
		s.backendStmts[conn] = set
	}
	return set
}

// relayExtended 는 backend 응답을 ReadyForQuery 까지 클라이언트로 relay 하되, *주입한 Parse*
// 의 ParseComplete 를 앞에서부터 swallowParseComplete 개 걸러낸다(클라이언트 미요청분).
func (s *session) relayExtended(conn net.Conn, swallowParseComplete int) bool {
	swallowed := 0
	for {
		m, err := readMessage(conn)
		if err != nil {
			return false
		}
		if m.Type == msgParseComplete && swallowed < swallowParseComplete {
			swallowed++
			continue
		}
		if err := writeMessage(s.client, m.Type, m.Payload); err != nil {
			return false
		}
		if m.Type == 'Z' { // ReadyForQuery — 배치 완료.
			return true
		}
	}
}

// handleClose 는 Close(statement) 를 세션 stmt 맵에서 제거한다(백엔드의 lingering prepared 는
// 연결 종료 시 정리되므로 무해 — 동일 이름 재사용은 드라이버가 회피).
func (s *session) handleClose(m pgMessage) {
	if len(m.Payload) < 1 {
		return
	}
	if m.Payload[0] != 'S' { // 'P'(portal)는 세션 추적 불필요.
		return
	}
	name := string(bytes.TrimRight(m.Payload[1:], "\x00"))
	delete(s.stmts, name)
}

// sendReadyIdle 은 ReadyForQuery 를 보낸다(트랜잭션 중이면 'T', 아니면 'I').
func (s *session) sendReadyIdle() bool {
	status := byte('I')
	if s.inTx {
		status = 'T'
	}
	return writeMessage(s.client, 'Z', []byte{status}) == nil
}

// parseStmtName 은 Parse('P') payload 의 첫 cstring(statement 이름)을 반환한다.
func parseStmtName(m pgMessage) string {
	i := bytes.IndexByte(m.Payload, 0)
	if i < 0 {
		return ""
	}
	return string(m.Payload[:i])
}

// bindStmtName 은 Bind('B') payload 의 둘째 cstring(참조 statement 이름)을 반환한다.
func bindStmtName(m pgMessage) string {
	i := bytes.IndexByte(m.Payload, 0) // portal 끝.
	if i < 0 {
		return ""
	}
	rest := m.Payload[i+1:]
	j := bytes.IndexByte(rest, 0)
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}
