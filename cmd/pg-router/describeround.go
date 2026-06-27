/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// describeround.go 는 *extended protocol describe-first 드라이버*(lib/pq·JDBC 등)의
// 파라미터화 쿼리 라우팅을 처리한다.
//
// 문제: lib/pq 는 `Parse → Describe → Sync` 를 먼저 보내 파라미터/컬럼 타입을 조회한 뒤,
// 그 응답을 받고 나서야 `Bind → Execute → Sync` 를 보낸다. 라우팅 키는 Bind 의 파라미터
// 값에 있는데, 클라이언트는 describe 응답을 *기다리며 블록* 하므로 라우터가 Bind 까지
// 기다릴 수 없다(닭-달걀).
//
// 해법(vtgate 와 동일 계열): 스키마(파라미터/컬럼 타입)는 모든 샤드 공통이므로, 라우터가
// *임의(default) 샤드* 로 describe round 를 대행해 메타데이터를 클라이언트에 돌려준다.
// 이후 Bind 가 오면 파라미터 값으로 *실제 샤드* 를 정한다. 실제 샤드가 default 와 같으면
// 그 연결을 재사용하고, 다르면 실 샤드에 re-Parse 한 뒤 그 중복 ParseComplete 만 걸러낸다.
package main

import (
	"io"
	"net"

	"github.com/keiailab/postgres-operator/internal/router"
)

// PG backend 메시지 타입 (필터용).
const msgParseComplete = '1'

// describeRoundDelegate 는 describe round(Parse..Sync, describeRound)를 default 샤드로
// 대행한 뒤, 클라이언트의 Bind 파라미터로 실 샤드를 정해 실행한다.
func describeRoundDelegate(client net.Conn, qr queryRouter, describeRound []pgMessage, parse pgMessage, pidx int, sql string, raw []byte, dialer *backendDialer, password string) {
	// 1) default 샤드로 describe round 대행.
	_, defBackend, err := qr.anyShard()
	if err != nil {
		writePgError(client, "08006", "no shard for describe round: "+err.Error())
		return
	}
	dconn, err := dialer.Dial(defBackend)
	if err != nil {
		writePgError(client, "08006", "describe-round dial: "+err.Error())
		return
	}
	if _, err := dconn.Write(raw); err != nil {
		_ = dconn.Close()
		return
	}
	if err := authenticateAndDrain(dconn, password); err != nil {
		_ = dconn.Close()
		writePgError(client, "08006", "describe-round backend startup: "+err.Error())
		return
	}
	for _, m := range describeRound { // Parse, [Describe], Sync 전달.
		if err := writeMessage(dconn, m.Type, m.Payload); err != nil {
			_ = dconn.Close()
			return
		}
	}
	// describe 응답(ParseComplete·ParameterDescription·RowDescription·ReadyForQuery)을
	// 클라이언트로 전달.
	if err := forwardUntilReady(dconn, client); err != nil {
		_ = dconn.Close()
		return
	}

	// 2) 클라이언트의 Bind → 파라미터 값으로 실 샤드 결정.
	bind, err := readMessage(client)
	if err != nil {
		_ = dconn.Close()
		return
	}
	if bind.Type != 'B' {
		_ = dconn.Close()
		writePgError(client, "08P01", "expected Bind after describe round")
		return
	}
	params, ok := bindParams(bind)
	if !ok || pidx-1 >= len(params) || params[pidx-1] == nil {
		_ = dconn.Close()
		writePgError(client, "08006", "could not extract routing parameter from Bind")
		return
	}
	d, err := qr.routeKey(string(params[pidx-1]), router.IsReadOnlyQuery(sql))
	if err != nil || d.Scatter {
		_ = dconn.Close()
		writePgError(client, "08006", "routing failed for parameter")
		return
	}
	logRoute('B', d)

	// 3a) 실 샤드 == default: describe 한 연결을 재사용(이미 Parse 됨).
	if d.Backend == defBackend {
		defer func() { _ = dconn.Close() }()
		if err := writeMessage(dconn, bind.Type, bind.Payload); err != nil {
			return
		}
		proxyBidi(client, dconn) // 클라이언트의 Execute/Sync ↔ 결과.
		return
	}

	// 3b) 실 샤드 != default: 새 연결에 re-Parse + Bind, 중복 ParseComplete 만 필터.
	_ = dconn.Close()
	rconn, err := dialer.Dial(d.Backend)
	if err != nil {
		writePgError(client, "08006", "execute dial: "+err.Error())
		return
	}
	defer func() { _ = rconn.Close() }()
	if _, err := rconn.Write(raw); err != nil {
		return
	}
	if err := authenticateAndDrain(rconn, password); err != nil {
		writePgError(client, "08006", "execute backend startup: "+err.Error())
		return
	}
	if err := writeMessage(rconn, parse.Type, parse.Payload); err != nil { // re-Parse.
		return
	}
	if err := writeMessage(rconn, bind.Type, bind.Payload); err != nil { // Bind.
		return
	}
	proxyExecuteRound(client, rconn) // Execute/Sync ↔ 결과(앞선 ParseComplete 1개 필터).
}

// forwardUntilReady 는 src 의 메시지를 dst 로 전달하며 ReadyForQuery('Z')/ErrorResponse
// ('E')까지 진행한다.
func forwardUntilReady(src, dst net.Conn) error {
	for {
		m, err := readMessage(src)
		if err != nil {
			return err
		}
		if err := writeMessage(dst, m.Type, m.Payload); err != nil {
			return err
		}
		if m.Type == 'Z' || m.Type == 'E' {
			return nil
		}
	}
}

// proxyExecuteRound 는 client↔server 를 양방향 proxy 하되, server→client 방향에서 *맨 앞
// ParseComplete 1개* 를 걸러낸다(re-Parse 로 생긴 중복 — 클라이언트는 describe round 에서
// 이미 ParseComplete 를 받았다).
func proxyExecuteRound(client, server net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }() // Execute/Sync 등.
	go func() {
		swallowed := false
		for {
			m, err := readMessage(server)
			if err != nil {
				break
			}
			if !swallowed && m.Type == msgParseComplete {
				swallowed = true
				continue
			}
			if err := writeMessage(client, m.Type, m.Payload); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}
