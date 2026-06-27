/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// querymode.go 는 *쿼리 인지 라우팅*(E)의 라우팅 엔진 결선부다 — 연결 단위(startup param)
// 라우팅과 달리, 클라이언트의 *매* 쿼리에서 샤딩 키를 뽑아 샤드를 정한다(per-query).
//
// 핸드셰이크 후 흐름은 persession.go(simple Query)·extsession.go(extended protocol)가
// 세션 단위로 처리하고, 본 파일은 그 둘이 쓰는 queryRouter(토폴로지+extractor+resolver →
// RouteDecision) 와 라우팅 로그를 제공한다.
//   - simple Query('Q'): 인라인 리터럴(`WHERE id='x'`) → routeSQL.
//   - extended: parameterized(`WHERE id=$1`)는 Bind 파라미터로 routeKey, 인라인 리터럴은
//     routeSQL. → pgx/psycopg/lib-pq/JDBC 등 실 드라이버 + prepared statement 동작.
//   - 키 없음 → scatter(simple) / 에러(extended).
//
// 백엔드 인증(trust/cleartext/scram)은 라우터가 대행한다(scram.go).
package main

import (
	"context"
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

// anyShard 는 describe-round 대행용 임의(결정적) 샤드 이름 + 그 backend 를 반환한다.
// 스키마(파라미터/컬럼 타입)는 모든 샤드 공통이므로 어느 샤드로 describe 해도 무방하다.
func (qr queryRouter) anyShard() (shard, backend string, err error) {
	topo, err := qr.provider.Current(context.Background())
	if err != nil {
		return "", "", err
	}
	shard, err = topo.AnyShard()
	if err != nil {
		return "", "", err
	}
	backend, err = qr.write(shard)
	return shard, backend, err
}

// shardBackend 는 한 샤드와 그 backend 주소다.
type shardBackend struct {
	shard   string
	backend string
}

// allShards 는 현재 토폴로지의 모든 distinct 샤드 + backend 를 반환한다 (scatter fan-out).
func (qr queryRouter) allShards() ([]shardBackend, error) {
	topo, err := qr.provider.Current(context.Background())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []shardBackend
	for _, r := range topo.Spec.Ranges {
		if r.Shard == "" || seen[r.Shard] {
			continue
		}
		seen[r.Shard] = true
		backend, err := qr.write(r.Shard) // 읽기 replica 분산은 후속.
		if err != nil {
			return nil, err
		}
		out = append(out, shardBackend{shard: r.Shard, backend: backend})
	}
	return out, nil
}

// handleQueryMode 는 쿼리 인지 라우팅으로 한 연결을 처리한다. backendPassword 는
// 백엔드 인증 대행(scram/cleartext)용 — "" 면 trust 백엔드만 동작.
func handleQueryMode(client net.Conn, qr queryRouter, dialer *backendDialer, serverVersion, backendPassword string) {
	defer func() { _ = client.Close() }()

	raw, _, err := readStartup(client)
	if err != nil {
		return
	}
	if err := sendTrustHandshake(client, serverVersion); err != nil {
		return
	}
	// per-query 라우팅 세션: 매 simple Query 를 키의 샤드로 라우팅(연결 고정 해소).
	runPerQuerySession(client, qr, dialer, backendPassword, raw)
}

func logRoute(typ byte, d router.RouteDecision) {
	log.Printf("pg-router: routed (%c) shard=%s backend=%s read=%v", typ, d.Shard, d.Backend, d.Read)
}
