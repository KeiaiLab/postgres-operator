/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// scattermode.go 는 *라우팅 키 없는 쿼리*(예: `SELECT * FROM t`)를 모든 샤드에 fan-out
// 하고 결과를 병합해 클라이언트에 돌려준다 (scatter-gather, simple Query).
//
// 동작: 각 샤드에 동일 쿼리를 보내 RowDescription·DataRow·CommandComplete 를 수집한 뒤,
// 첫 샤드의 RowDescription 1개 + 모든 샤드의 DataRow 전부 + 합산 CommandComplete +
// ReadyForQuery 를 클라이언트에 보낸다 (UNION ALL 의미).
//
// 제약: 행 concat 만 한다 — `SELECT count(*)` 같은 *집계* 는 샤드별 부분 결과를 그대로
// 합치므로(샤드당 1행) 재집계가 필요하다(후속, planner 가 sum/merge). ORDER BY 전역 정렬,
// LIMIT pushdown, 병렬 fan-out(현재 순차)도 후속.
package main

import (
	"fmt"
	"net"
	"sync"
)

// shardResult 는 한 샤드의 scatter 응답이다.
type shardResult struct {
	rowDesc *pgMessage
	rows    []pgMessage
	errMsg  *pgMessage // 백엔드 ErrorResponse
	err     error      // 전송/연결 오류
}

// scatterQuery 는 simple Query('Q')를 모든 샤드에 *병렬* fan-out 하고 병합 결과를 보낸다.
// 병렬이라 전체 지연이 max(샤드) 에 가깝다(순차 합산 아님) — 분산 읽기 확장의 핵심.
func scatterQuery(client net.Conn, qr queryRouter, query pgMessage, raw []byte, dialer *backendDialer, password string) {
	shards, err := qr.allShards()
	if err != nil || len(shards) == 0 {
		writePgError(client, "08006", "scatter: no shards available")
		return
	}

	results := make([]shardResult, len(shards))
	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func(i int, sb shardBackend) {
			defer wg.Done()
			results[i] = scatterOne(sb, query, raw, dialer, password)
		}(i, shards[i])
	}
	wg.Wait()

	var rowDesc *pgMessage
	var dataRows []pgMessage
	for i := range results {
		r := results[i]
		if r.err != nil {
			writePgError(client, "08006", fmt.Sprintf("scatter: shard %s: %v", shards[i].shard, r.err))
			return
		}
		if r.errMsg != nil { // 한 샤드라도 에러면 그대로 전달(fail-fast).
			_ = writeMessage(client, 'E', r.errMsg.Payload)
			_ = writeMessage(client, 'Z', []byte{'I'})
			return
		}
		if rowDesc == nil {
			rowDesc = r.rowDesc
		}
		dataRows = append(dataRows, r.rows...)
	}

	// 병합 결과 송신: RowDescription(1) + DataRow(전부) + CommandComplete + ReadyForQuery.
	if rowDesc != nil {
		if err := writeMessage(client, 'T', rowDesc.Payload); err != nil {
			return
		}
	}
	for _, dr := range dataRows {
		if err := writeMessage(client, 'D', dr.Payload); err != nil {
			return
		}
	}
	_ = writeMessage(client, 'C', cstring(fmt.Sprintf("SELECT %d", len(dataRows))))
	_ = writeMessage(client, 'Z', []byte{'I'})
}

// scatterOne 은 한 샤드에 연결·인증·쿼리하고 결과를 수집한다 (goroutine 에서 호출).
func scatterOne(sb shardBackend, query pgMessage, raw []byte, dialer *backendDialer, password string) shardResult {
	conn, err := dialer.Dial(sb.backend)
	if err != nil {
		return shardResult{err: err}
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(raw); err != nil {
		return shardResult{err: err}
	}
	if err := authenticateAndDrain(conn, password); err != nil {
		return shardResult{err: err}
	}
	bc := newBufConn(conn) // 핸드셰이크 후 읽기 버퍼로 감싼다.
	if err := writeMessage(bc, 'Q', query.Payload); err != nil {
		return shardResult{err: err}
	}
	rd, rows, errMsg, err := readQueryResult(bc)
	return shardResult{rowDesc: rd, rows: rows, errMsg: errMsg, err: err}
}

// readQueryResult 는 한 백엔드의 simple-query 응답을 ReadyForQuery 까지 읽어 RowDescription·
// DataRow·ErrorResponse 를 수집한다. CommandComplete·기타는 무시.
func readQueryResult(conn net.Conn) (rowDesc *pgMessage, rows []pgMessage, errMsg *pgMessage, err error) {
	for {
		m, err := readMessage(conn)
		if err != nil {
			return nil, nil, nil, err
		}
		switch m.Type {
		case 'T': // RowDescription
			if rowDesc == nil {
				rd := m
				rowDesc = &rd
			}
		case 'D': // DataRow
			rows = append(rows, m)
		case 'E': // ErrorResponse
			em := m
			errMsg = &em
		case 'Z': // ReadyForQuery — 이 샤드 완료.
			return rowDesc, rows, errMsg, nil
		}
	}
}
