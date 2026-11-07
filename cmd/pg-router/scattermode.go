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
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/keiailab/postgres-operator/internal/router"
)

// decodeDataRow 는 DataRow('D') payload 를 컬럼 값 슬라이스로 푼다. NULL(-1) 은 nil.
// 값은 text format 문자열로 다룬다(라우터는 text 프로토콜로 백엔드와 통신).
func decodeDataRow(p []byte) ([]any, bool) {
	if len(p) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(p[0:2]))
	vals := make([]any, 0, n)
	off := 2
	for range n {
		if off+4 > len(p) {
			return nil, false
		}
		l := int(int32(binary.BigEndian.Uint32(p[off : off+4])))
		off += 4
		if l < 0 { // NULL
			vals = append(vals, nil)
			continue
		}
		if off+l > len(p) {
			return nil, false
		}
		vals = append(vals, string(p[off:off+l]))
		off += l
	}
	return vals, true
}

// encodeDataRow 는 컬럼 값 슬라이스를 DataRow payload 로 만든다 (nil → NULL).
func encodeDataRow(vals []any) []byte {
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out[0:2], uint16(len(vals)))
	for _, v := range vals {
		if v == nil {
			out = binary.BigEndian.AppendUint32(out, ^uint32(0)) // -1 = NULL
			continue
		}
		s := fmt.Sprintf("%v", v)
		out = binary.BigEndian.AppendUint32(out, uint32(len(s)))
		out = append(out, s...)
	}
	return out
}

// rowDescNames 는 RowDescription('T') payload 에서 컬럼명을 순서대로 뽑는다.
// 레이아웃: int16 fieldCount, 필드마다 [name C-string + 18 bytes 메타].
func rowDescNames(rd *pgMessage) []string {
	if rd == nil || len(rd.Payload) < 2 {
		return nil
	}
	p := rd.Payload
	n := int(binary.BigEndian.Uint16(p[0:2]))
	names := make([]string, 0, n)
	off := 2
	for range n {
		end := off
		for end < len(p) && p[end] != 0 {
			end++
		}
		if end >= len(p) {
			return names
		}
		names = append(names, string(p[off:end]))
		off = end + 1 + 18 // NUL + (tableOID4 + colAttr2 + typeOID4 + typeLen2 + typeMod4 + format2)
		if off > len(p) {
			return names
		}
	}
	return names
}

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
		scatterClientError(client, "08006", "scatter: no shards available")
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
			scatterClientError(client, "08006", fmt.Sprintf("scatter: shard %s: %v", shards[i].shard, r.err))
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

	// 부분 결과 재결합(B-11): 집계 쿼리면 컬럼별 함수로 재merge 하고, ORDER BY 가 있으면
	// 전역 정렬한다. 둘 다 아니면 UNION ALL(concat) 그대로.
	dataRows = mergeScatterRows(queryOf(query), rowDesc, dataRows)

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

// queryOf 는 simple Query('Q') 메시지의 SQL 텍스트를 꺼낸다(payload = C-string).
//
// 후행 세미콜론·공백을 제거한다 — DetectAggregates 는 top-level `;` 를 *다중문*으로 보고
// 재결합을 거부하는데, psql 등 실제 클라이언트는 `SELECT count(*) FROM t;` 처럼 세미콜론을
// 붙여 보낸다. 트림하지 않으면 집계 재merge 가 통째로 무력화된다(라이브 실측 2026-07-14:
// ORDER BY 는 고쳐졌는데 count(*) 만 계속 2행이었던 원인).
func queryOf(m pgMessage) string {
	q := strings.TrimRight(string(m.Payload), "\x00")
	return strings.TrimRight(strings.TrimSpace(q), "; \t\r\n")
}

// mergeScatterRows 는 샤드별 부분 결과(DataRow 와이어 메시지)를 쿼리 의미대로 재결합한다.
//
// B-11 (4노드 라이브 실측 2026-07-14): 이 결선이 없어서 `SELECT count(*) FROM t` 가
// 샤드별 부분 count 2행(`3`,`1`)을 그대로 반환했다 — 라우터가 *조용히 틀린 답*을 주는
// 상태였다. 재결합 로직 자체는 internal/router 에 이미 있었으나 라우터가 호출하지 않았다.
//
//   - 집계(COUNT/SUM/MIN/MAX): router.DetectAggregates 로 컬럼별 함수를 뽑아
//     MergeAggregatePartials 로 그룹당 1행 재결합. AVG·표현식·SELECT * 는 감지가
//     ok=false 를 주므로 안전하게 concat 으로 degrade(틀린 재결합보다 낫다).
//   - ORDER BY: 각 샤드는 이미 정렬된 결과를 주므로 flatten 후 해당 컬럼으로 재정렬하면
//     전역 순서가 된다.
func mergeScatterRows(query string, rowDesc *pgMessage, dataRows []pgMessage) []pgMessage {
	if len(dataRows) == 0 {
		return dataRows
	}
	aggs, isAgg := router.DetectAggregates(query)
	col, desc, isOrdered := orderByCol(query, rowDesc)
	if !isAgg && !isOrdered {
		return dataRows
	}

	rows := make([]router.Row, 0, len(dataRows))
	for _, dr := range dataRows {
		vals, ok := decodeDataRow(dr.Payload)
		if !ok {
			return dataRows // 디코드 불가 → 원본 유지(안전).
		}
		rows = append(rows, router.Row{Values: vals})
	}

	if isAgg {
		rows = router.MergeAggregatePartials(rows, aggs)
	} else {
		router.SortRowsByCol(rows, col, desc)
	}

	out := make([]pgMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, pgMessage{Type: 'D', Payload: encodeDataRow(r.Values)})
	}
	return out
}

// orderByCol 은 `ORDER BY <컬럼|위치> [ASC|DESC]` 를 출력 컬럼 index 로 해석한다.
// 컬럼명은 RowDescription 의 필드명과 대조하고, `ORDER BY 2` 같은 위치 표기도 받는다.
// 복합 정렬(다중 컬럼)·표현식은 보수적으로 미지원(ok=false → 재정렬 안 함).
func orderByCol(query string, rowDesc *pgMessage) (col int, desc bool, ok bool) {
	f := strings.Fields(strings.ToLower(strings.TrimRight(strings.TrimSpace(query), "; \t\n")))
	idx := -1
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "order" && f[i+1] == "by" {
			idx = i + 2
		}
	}
	if idx < 0 || idx >= len(f) {
		return 0, false, false
	}
	target := strings.TrimSuffix(f[idx], ",")
	if strings.Contains(target, ",") || strings.Contains(target, "(") {
		return 0, false, false // 다중 컬럼 / 표현식 → 미지원.
	}
	if idx+1 < len(f) {
		switch f[idx+1] {
		case "desc":
			desc = true
		case "asc":
		default:
			if !strings.HasPrefix(f[idx+1], "limit") {
				return 0, false, false // 인식 못 한 절 → 보수적 미지원.
			}
		}
	}
	if n, err := strconv.Atoi(target); err == nil { // `ORDER BY 1` (1-based 위치)
		if n < 1 {
			return 0, false, false
		}
		return n - 1, desc, true
	}
	for i, name := range rowDescNames(rowDesc) {
		if strings.EqualFold(name, target) {
			return i, desc, true
		}
	}
	return 0, false, false
}

func scatterClientError(client net.Conn, code, msg string) {
	writePgError(client, code, msg)
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
