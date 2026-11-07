/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/router"
)

func TestScatterQuery_NoShardsSendsReadyForQuery(t *testing.T) {
	client, routerSide := net.Pipe()
	defer func() { _ = client.Close() }()     // teardown
	defer func() { _ = routerSide.Close() }() // teardown

	qr := queryRouter{
		provider: router.StaticTopologyProvider{T: router.Topology{}},
		write:    func(s string) (string, error) { return s + ":5432", nil },
	}
	done := make(chan struct{}, 1)
	go func() {
		scatterQuery(routerSide, qr, pgMessage{Type: 'Q', Payload: cstring("SELECT * FROM t")}, nil, nil, "")
		done <- struct{}{}
	}()

	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	errMsg, err := readMessage(client)
	if err != nil {
		t.Fatalf("read error response: %v", err)
	}
	if errMsg.Type != 'E' {
		t.Fatalf("first response type = %q, want E", errMsg.Type)
	}
	ready, err := readMessage(client)
	if err != nil {
		t.Fatalf("read ready response: %v", err)
	}
	if ready.Type != 'Z' || string(ready.Payload) != "I" {
		t.Fatalf("ready response = type %q payload %q, want Z/I", ready.Type, string(ready.Payload))
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scatterQuery did not return")
	}
}

// --- B-11 회귀 차단: scatter 결과 재결합 -----------------------------------------
//
// 트리거(4노드 라이브 2026-07-14): `SELECT count(*) FROM t` 가 샤드별 부분 count 2행
// (`3`,`1`)을 그대로 반환했다 — 라우터가 조용히 틀린 답을 줬다.

func TestMergeScatterRows_AggregateReMerge(t *testing.T) {
	rd := &pgMessage{Type: 'T', Payload: rowDescPayload("count")}
	partials := []pgMessage{
		{Type: 'D', Payload: encodeDataRow([]any{"3"})}, // shard-0 부분 count
		{Type: 'D', Payload: encodeDataRow([]any{"1"})}, // shard-1 부분 count
	}
	got := mergeScatterRows("SELECT count(*) FROM t", rd, partials)
	if len(got) != 1 {
		t.Fatalf("count(*) 재결합 결과 %d행, want 1행", len(got))
	}
	vals, ok := decodeDataRow(got[0].Payload)
	if !ok || len(vals) != 1 {
		t.Fatalf("DataRow 디코드 실패: %v", vals)
	}
	if fmt.Sprintf("%v", vals[0]) != "4" {
		t.Errorf("count(*) = %v, want 4 (3+1)", vals[0])
	}
}

func TestMergeScatterRows_GlobalOrderBy(t *testing.T) {
	rd := &pgMessage{Type: 'T', Payload: rowDescPayload("tenant_id")}
	// 샤드별로는 정렬돼 있으나(alice,carol,dave / bob) 이어붙이면 전역 순서가 깨진다.
	partials := []pgMessage{
		{Type: 'D', Payload: encodeDataRow([]any{"alice"})},
		{Type: 'D', Payload: encodeDataRow([]any{"carol"})},
		{Type: 'D', Payload: encodeDataRow([]any{"dave"})},
		{Type: 'D', Payload: encodeDataRow([]any{"bob"})},
	}
	got := mergeScatterRows("SELECT tenant_id FROM t ORDER BY tenant_id", rd, partials)
	out := make([]string, 0, len(got))
	for _, m := range got {
		vals, _ := decodeDataRow(m.Payload)
		out = append(out, fmt.Sprintf("%v", vals[0]))
	}
	want := []string{"alice", "bob", "carol", "dave"}
	if strings.Join(out, ",") != strings.Join(want, ",") {
		t.Errorf("전역 ORDER BY = %v, want %v", out, want)
	}
}

// 실제 클라이언트(psql)는 후행 세미콜론을 붙여 보낸다 — 트림하지 않으면 DetectAggregates 가
// 다중문으로 보고 재결합을 거부해 집계가 통째로 깨진다(라이브 실측 2026-07-14).
func TestQueryOf_TrimsTrailingSemicolon(t *testing.T) {
	got := queryOf(pgMessage{Type: 'Q', Payload: cstring("SELECT count(*) FROM t;")})
	if got != "SELECT count(*) FROM t" {
		t.Errorf("queryOf = %q, want 세미콜론 제거된 형태", got)
	}
	if aggs, ok := router.DetectAggregates(got); !ok || len(aggs) != 1 {
		t.Errorf("세미콜론 트림 후에도 집계 미감지: aggs=%v ok=%v", aggs, ok)
	}
}

func TestMergeScatterRows_NonAggregatePassthrough(t *testing.T) {
	rd := &pgMessage{Type: 'T', Payload: rowDescPayload("v")}
	partials := []pgMessage{
		{Type: 'D', Payload: encodeDataRow([]any{"a"})},
		{Type: 'D', Payload: encodeDataRow([]any{"b"})},
	}
	// 집계도 ORDER BY 도 아니면 UNION ALL 그대로(행 수 보존).
	if got := mergeScatterRows("SELECT v FROM t", rd, partials); len(got) != 2 {
		t.Errorf("passthrough 결과 %d행, want 2행", len(got))
	}
	// AVG 는 부분평균 재결합 불가 → 감지가 거부하고 concat 으로 degrade(틀린 답 금지).
	if got := mergeScatterRows("SELECT avg(x) FROM t", rd, partials); len(got) != 2 {
		t.Errorf("avg degrade 결과 %d행, want 2행(concat)", len(got))
	}
}

// rowDescPayload 는 컬럼명 1개짜리 RowDescription payload 를 만든다(테스트 헬퍼).
func rowDescPayload(name string) []byte {
	p := []byte{0, 1}
	p = append(p, name...)
	p = append(p, 0)
	p = append(p, make([]byte, 18)...) // tableOID..format 메타
	return p
}
