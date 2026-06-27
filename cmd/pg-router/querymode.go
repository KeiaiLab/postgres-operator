/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// querymode.go 는 *쿼리 인지 라우팅*(E)을 결선한다 — 연결 단위(startup param) 라우팅과
// 달리, 클라이언트의 *첫 쿼리*에서 샤딩 키를 뽑아 QueryRouter 로 샤드를 정한다.
//
// 동작: startup 수신 → trust 핸드셰이크(라우터가 클라이언트를 인증된 것으로 수용) →
// 첫 'Q'(simple Query) 수신 → QueryRouter.Route(sql) → 해당 샤드 backend 에 연결 →
// startup + 첫 Query 전달 후 양방향 proxy.
//
// *제약(PoC, trust 백엔드 한정)*: 라우터가 클라이언트 비밀번호를 검증하지 않고(trust),
// 백엔드에는 클라이언트의 startup 을 그대로 전달한다 — 백엔드가 trust auth 일 때만
// 동작한다. 일반(비밀번호) 백엔드 인증을 라우터가 대행하는 완전한 종단은 별 트랙(라이브
// PG 검증 필요). 또 simple-query 첫 문장만 라우팅하며 extended protocol(Parse/Bind)·
// 멀티샤드 scatter 는 후속.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/keiailab/postgres-operator/internal/router"
)

// routeDecisionFunc 는 SQL → 라우팅 결정. QueryRouter + 현재 토폴로지를 묶는다.
type routeDecisionFunc func(sql string) (router.RouteDecision, error)

// buildQueryRouterFunc 는 provider(현재 토폴로지) + 백엔드 resolver 로 라우팅 함수를
// 만든다. write=primary, read=replica(nil 이면 write 사용). extractor 는 기본 전략.
func buildQueryRouterFunc(provider router.TopologyProvider, write, read router.BackendResolver) routeDecisionFunc {
	extractor, _ := router.NewRouteKeyExtractor("")
	return func(sql string) (router.RouteDecision, error) {
		topo, err := provider.Current(context.Background())
		if err != nil {
			return router.RouteDecision{}, err
		}
		qr := router.QueryRouter{Topology: topo, Extractor: extractor, Write: write, Read: read}
		return qr.Route(sql)
	}
}

// handleQueryMode 는 쿼리 인지 라우팅으로 한 연결을 처리한다.
func handleQueryMode(client net.Conn, route routeDecisionFunc, dialer *backendDialer, serverVersion string) {
	defer func() { _ = client.Close() }()

	raw, _, err := readStartup(client)
	if err != nil {
		return
	}
	if err := sendTrustHandshake(client, serverVersion); err != nil {
		return
	}

	for {
		m, err := readMessage(client)
		if err != nil {
			return
		}
		switch m.Type {
		case 'X': // Terminate
			return
		case 'Q', 'P': // simple Query / Parse(extended) → 첫 SQL 로 라우팅
			sql, ok := querySQL(m)
			if !ok {
				sql, ok = parseSQL(m)
			}
			if !ok {
				writePgError(client, "08P01", "could not parse query for routing")
				return
			}
			d, err := route(sql)
			if err != nil {
				writePgError(client, "08006", "routing failed: "+err.Error())
				return
			}
			if d.Scatter {
				writePgError(client, "0A000", "multi-shard query not supported yet (single-shard fast-path only)")
				return
			}
			log.Printf("pg-router: routed (%c) shard=%s backend=%s read=%v", m.Type, d.Shard, d.Backend, d.Read)
			proxyToShard(client, raw, m, d, dialer)
			return
		default:
			writePgError(client, "0A000", fmt.Sprintf("message type %q not supported in query-routing PoC", m.Type))
			return
		}
	}
}

// proxyToShard 는 결정된 샤드 backend 에 연결해 startup + 첫 Query 를 전달하고 양방향
// proxy 한다.
func proxyToShard(client net.Conn, raw []byte, firstQuery pgMessage, d router.RouteDecision, dialer *backendDialer) {
	server, err := dialer.Dial(d.Backend)
	if err != nil {
		writePgError(client, "08006", fmt.Sprintf("cannot reach shard %s (%s): %v", d.Shard, d.Backend, err))
		return
	}
	defer func() { _ = server.Close() }()

	if _, err := server.Write(raw); err != nil { // backend startup (trust 백엔드 전제)
		return
	}
	// 백엔드의 인증/핸드셰이크(AuthOk·ParameterStatus·BackendKeyData·ReadyForQuery)를
	// *소비* 한다 — 클라이언트는 이미 우리 trust 핸드셰이크를 받았으므로 백엔드 핸드셰이크를
	// 그대로 흘려보내면 중복이 된다. 비-trust(비밀번호) 백엔드는 여기서 AuthenticationRequest
	// (type>0)를 보내며, 본 PoC 는 비번을 대행하지 않으므로 그 경우 실패한다(trust 한정).
	if err := drainUntilReady(server); err != nil {
		writePgError(client, "08006", "backend startup: "+err.Error())
		return
	}
	if err := writeMessage(server, firstQuery.Type, firstQuery.Payload); err != nil { // 첫 메시지 재생(Q/P)
		return
	}
	proxyBidi(client, server)
}

// drainUntilReady 는 백엔드의 startup 응답을 ReadyForQuery('Z')까지 읽어 버린다.
// ErrorResponse('E')면 에러 반환. AuthenticationRequest(R, Int32>0)는 비번 백엔드
// 신호 — 본 PoC 는 대행하지 않으므로 에러로 처리한다.
func drainUntilReady(server net.Conn) error {
	for {
		m, err := readMessage(server)
		if err != nil {
			return err
		}
		switch m.Type {
		case 'Z': // ReadyForQuery
			return nil
		case 'E': // ErrorResponse
			return fmt.Errorf("backend error: %s", string(m.Payload))
		case 'R': // AuthenticationRequest — type 0 = Ok (trust), >0 = 비번 필요
			if len(m.Payload) >= 4 && (m.Payload[0]|m.Payload[1]|m.Payload[2]|m.Payload[3]) != 0 {
				return fmt.Errorf("backend requires non-trust auth (not supported in query-mode PoC)")
			}
		}
	}
}

// proxyBidi 는 두 연결을 양방향으로 복사하고 한쪽이 끝나면 반환한다.
func proxyBidi(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
}
