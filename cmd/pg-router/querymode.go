/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// querymode.go 는 *쿼리 인지 라우팅*(E)을 결선한다 — 연결 단위(startup param) 라우팅과
// 달리, 클라이언트의 *첫 쿼리*에서 샤딩 키를 뽑아 샤드를 정한다.
//
// 지원 경로:
//   - simple Query('Q'): 인라인 리터럴(`WHERE id='x'`) 추출 → 라우팅.
//   - extended Parse('P') 인라인 리터럴: 동일.
//   - extended Parse + Bind: parameterized(`WHERE id=$1`)는 값이 Bind 에 있으므로,
//     Parse 에서 `col=$N` 위치를 찾고 Bind 까지 버퍼링해 N번째 파라미터 값으로 라우팅.
//     → pgx/psycopg 등 실 드라이버 동작.
//
// *제약(PoC, trust 백엔드 한정)*: 라우터가 클라이언트 비밀번호를 검증하지 않고(trust),
// 백엔드에 클라이언트 startup 을 그대로 전달 — trust 백엔드 전제. 일반(scram) 백엔드
// 인증 대행 + 멀티샤드 scatter 는 별 트랙(라이브 PG 검증 필요).
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/keiailab/postgres-operator/internal/router"
)

// queryRouter 는 현재 토폴로지 + extractor + 백엔드 resolver 로 쿼리/값을 라우팅한다.
type queryRouter struct {
	provider  router.TopologyProvider
	extractor router.RouteKeyExtractor
	write     router.BackendResolver // primary
	read      router.BackendResolver // replica (nil 이면 write)
}

func newQueryRouter(provider router.TopologyProvider, write, read router.BackendResolver) queryRouter {
	ext, _ := router.NewRouteKeyExtractor("")
	return queryRouter{provider: provider, extractor: ext, write: write, read: read}
}

// routeSQL 은 SQL(인라인 리터럴)에서 키를 뽑아 라우팅한다.
func (qr queryRouter) routeSQL(sql string) (router.RouteDecision, error) {
	topo, err := qr.provider.Current(context.Background())
	if err != nil {
		return router.RouteDecision{}, err
	}
	r := router.QueryRouter{Topology: topo, Extractor: qr.extractor, Write: qr.write, Read: qr.read}
	return r.Route(sql)
}

// routeKey 는 *이미 아는 샤딩 키 값*(extended Bind 파라미터)을 vindex 로 직접 라우팅한다.
func (qr queryRouter) routeKey(key string, read bool) (router.RouteDecision, error) {
	topo, err := qr.provider.Current(context.Background())
	if err != nil {
		return router.RouteDecision{}, err
	}
	shard, err := router.ResolveShard(topo.Spec, key)
	if err != nil {
		return router.RouteDecision{}, err
	}
	pick := qr.write
	if read && qr.read != nil {
		pick = qr.read
	}
	backend, err := pick(shard)
	if err != nil {
		return router.RouteDecision{}, err
	}
	return router.RouteDecision{Shard: shard, Backend: backend, Read: read}, nil
}

// shardColumn 은 현재 토폴로지의 vindex 컬럼명을 반환한다.
func (qr queryRouter) shardColumn() string {
	topo, err := qr.provider.Current(context.Background())
	if err != nil {
		return ""
	}
	return topo.Spec.Vindex.Column
}

// handleQueryMode 는 쿼리 인지 라우팅으로 한 연결을 처리한다.
func handleQueryMode(client net.Conn, qr queryRouter, dialer *backendDialer, serverVersion string) {
	defer func() { _ = client.Close() }()

	raw, _, err := readStartup(client)
	if err != nil {
		return
	}
	if err := sendTrustHandshake(client, serverVersion); err != nil {
		return
	}

	m, err := readMessage(client)
	if err != nil {
		return
	}
	switch m.Type {
	case 'X': // Terminate
		return
	case 'Q': // simple Query
		sql, ok := querySQL(m)
		if !ok {
			writePgError(client, "08P01", "could not parse query")
			return
		}
		routeAndProxy(client, qr, sql, raw, []pgMessage{m}, dialer)
	case 'P': // extended Parse
		handleParse(client, qr, m, raw, dialer)
	default:
		writePgError(client, "0A000", fmt.Sprintf("message type %q not supported in query-routing PoC", m.Type))
	}
}

// handleParse 는 Parse('P') 를 처리한다: 인라인 리터럴이면 즉시, parameterized 면 Bind
// 까지 버퍼링해 파라미터 값으로 라우팅한다.
func handleParse(client net.Conn, qr queryRouter, parse pgMessage, raw []byte, dialer *backendDialer) {
	sql, ok := parseSQL(parse)
	if !ok {
		writePgError(client, "08P01", "could not parse query")
		return
	}
	// 1) 인라인 리터럴이면 SQL 만으로 라우팅.
	if d, err := qr.routeSQL(sql); err == nil && !d.Scatter {
		logRoute('P', d)
		proxyToShard(client, raw, []pgMessage{parse}, d, dialer)
		return
	}
	// 2) parameterized(`col = $N`): Bind 까지 버퍼링해 N번째 파라미터로 라우팅.
	pidx, ok := router.ExtractParamRef(sql, qr.shardColumn())
	if !ok {
		writePgError(client, "08006", "could not determine shard (no routing key in query)")
		return
	}
	buffered := []pgMessage{parse}
	for {
		next, err := readMessage(client)
		if err != nil {
			return
		}
		buffered = append(buffered, next)
		switch next.Type {
		case 'B': // Bind — 파라미터 값으로 라우팅.
			params, ok := bindParams(next)
			if !ok || pidx-1 >= len(params) || params[pidx-1] == nil {
				writePgError(client, "08006", "could not extract routing parameter from Bind")
				return
			}
			d, err := qr.routeKey(string(params[pidx-1]), router.IsReadOnlyQuery(sql))
			if err != nil || d.Scatter {
				writePgError(client, "08006", "routing failed for parameter")
				return
			}
			logRoute('B', d)
			proxyToShard(client, raw, buffered, d, dialer)
			return
		case 'S': // Sync 가 Bind 보다 먼저 — 라우팅 불가.
			writePgError(client, "08006", "no Bind before Sync; cannot route parameterized query")
			return
		}
	}
}

func logRoute(typ byte, d router.RouteDecision) {
	log.Printf("pg-router: routed (%c) shard=%s backend=%s read=%v", typ, d.Shard, d.Backend, d.Read)
}

// routeAndProxy 는 simple Query 경로의 라우팅 + proxy.
func routeAndProxy(client net.Conn, qr queryRouter, sql string, raw []byte, msgs []pgMessage, dialer *backendDialer) {
	d, err := qr.routeSQL(sql)
	if err != nil {
		writePgError(client, "08006", "routing failed: "+err.Error())
		return
	}
	if d.Scatter {
		writePgError(client, "0A000", "multi-shard query not supported yet (single-shard fast-path only)")
		return
	}
	logRoute('Q', d)
	proxyToShard(client, raw, msgs, d, dialer)
}

// proxyToShard 는 결정된 샤드 backend 에 연결해 startup + 버퍼링된 메시지(들)를 전달하고
// 양방향 proxy 한다.
func proxyToShard(client net.Conn, raw []byte, msgs []pgMessage, d router.RouteDecision, dialer *backendDialer) {
	server, err := dialer.Dial(d.Backend)
	if err != nil {
		writePgError(client, "08006", fmt.Sprintf("cannot reach shard %s (%s): %v", d.Shard, d.Backend, err))
		return
	}
	defer func() { _ = server.Close() }()

	if _, err := server.Write(raw); err != nil { // backend startup (trust 전제)
		return
	}
	// 백엔드 핸드셰이크를 ReadyForQuery 까지 소비 (클라이언트는 우리 trust 핸드셰이크를
	// 이미 받음 — 중복 방지). 비-trust 백엔드는 여기서 실패한다.
	if err := drainUntilReady(server); err != nil {
		writePgError(client, "08006", "backend startup: "+err.Error())
		return
	}
	for _, m := range msgs { // 버퍼링된 클라이언트 메시지(Parse/Bind 등) 재생.
		if err := writeMessage(server, m.Type, m.Payload); err != nil {
			return
		}
	}
	proxyBidi(client, server)
}

// drainUntilReady 는 백엔드 startup 응답을 ReadyForQuery('Z')까지 읽어 버린다.
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
		case 'R': // AuthenticationRequest — type 0 = Ok(trust), >0 = 비번 필요
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
