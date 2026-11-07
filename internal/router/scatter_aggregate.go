// Package router — scatter_aggregate.go 는 scatter-gather 의 *집계 재결합*(능력 사다리
// 3단계)이다. COUNT/SUM/MIN/MAX 를 GROUP BY 유무와 무관하게 shard 별 부분 결과에서
// 하나로 재합친다.
//
// 배경: `SELECT count(*) FROM t WHERE ...` 를 N shard 로 scatter 하면 각 shard 가
// *부분* count 를 반환한다. UNION ALL(MergeConcat)로 이어붙이면 N 행이 나와 틀린다 —
// 부분 결과를 aggregate 함수별로 재결합해야 정답 1행(또는 GROUP BY 그룹당 1행)이 된다:
//   - COUNT → 부분 count 들의 SUM   - SUM → 부분 sum 들의 SUM
//   - MIN → 부분 min 들의 MIN        - MAX → 부분 max 들의 MAX
//
// AVG 는 부분 평균만으로 재결합 불가(가중 필요) → 쿼리를 SUM/COUNT 로 rewrite 해야
// 하므로 여기 범위 밖(planner rewrite 후속). GROUP BY 는 non-aggregate(key) 컬럼으로
// 그룹핑해 그룹당 aggregate 를 결합한다.
package router

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// AggregateFunc 는 재결합 시 각 출력 컬럼의 결합 함수다. planner 가 SELECT 리스트를
// 분석해 컬럼별로 지정한다(AggNone = GROUP BY key / passthrough 컬럼).
type AggregateFunc int

const (
	// AggNone — group-by key 또는 비집계 컬럼. 그룹 식별에 쓰이고 값은 그대로 통과.
	AggNone AggregateFunc = iota
	// AggCount — 부분 count 들을 합산(SUM of counts).
	AggCount
	// AggSum — 부분 sum 들을 합산.
	AggSum
	// AggMin — 부분 min 들의 최소.
	AggMin
	// AggMax — 부분 max 들의 최대.
	AggMax
)

// mergeAggregate 는 flat row 들을 AggNone 컬럼(그룹 key) 기준으로 그룹핑하고 각 그룹의
// aggregate 컬럼을 함수별로 결합해 그룹당 1행을 만든다. 그룹 순서는 최초 등장 순(결정론).
// aggregates 가 컬럼 수보다 짧으면 부족분은 AggNone(passthrough)로 간주한다.
func (s *ScatterGather) mergeAggregate(order []ShardID, collected map[ShardID][]Row) []Row {
	flat := mergeConcat(order, collected)
	aggs := s.Aggregates

	type group struct {
		keyRow Row
		accs   []*aggAcc
	}
	groups := map[string]*group{}
	var groupOrder []string

	for _, row := range flat {
		key := aggGroupKey(row, aggs)
		g, ok := groups[key]
		if !ok {
			accs := make([]*aggAcc, len(aggs))
			for i := range aggs {
				accs[i] = &aggAcc{fn: aggs[i]}
			}
			g = &group{keyRow: row, accs: accs}
			groups[key] = g
			groupOrder = append(groupOrder, key)
		}
		for i := range aggs {
			if aggs[i] == AggNone {
				continue
			}
			g.accs[i].add(valueAt(row, i))
		}
	}

	out := make([]Row, 0, len(groupOrder))
	for _, key := range groupOrder {
		g := groups[key]
		vals := make([]any, len(aggs))
		for i := range aggs {
			if aggs[i] == AggNone {
				vals[i] = valueAt(g.keyRow, i)
			} else {
				vals[i] = g.accs[i].result()
			}
		}
		out = append(out, Row{Values: vals})
	}
	return out
}

// aggGroupKey 는 row 의 AggNone(그룹 key) 컬럼 값들을 결정론적 문자열로 이어붙인다.
// 집계 컬럼이 하나도 없거나 key 컬럼이 없으면(순수 스칼라 집계) 모든 row 가 단일 그룹.
func aggGroupKey(row Row, aggs []AggregateFunc) string {
	var b []byte
	for i := range aggs {
		if aggs[i] != AggNone {
			continue
		}
		b = append(b, toStr(valueAt(row, i))...)
		b = append(b, 0x1f) // unit separator — 값 경계 모호성 방지.
	}
	return string(b)
}

// aggAcc 는 한 컬럼의 aggregate 누산기다. COUNT/SUM 은 정수 유지(모두 정수면 int64,
// 실수 등장 시 float64 승격), MIN/MAX 는 실제 값을 compareValues 로 유지한다.
type aggAcc struct {
	fn AggregateFunc

	// 수치 누산(COUNT/SUM).
	i       int64
	f       float64
	isFloat bool
	hasNum  bool

	// MIN/MAX.
	best    any
	hasBest bool
}

func (a *aggAcc) add(v any) {
	switch a.fn {
	case AggCount, AggSum:
		// 부분 SUM 이 NULL(해당 shard 에 행 없음)일 수 있다 — 무시.
		if v == nil {
			return
		}
		if iv, ok := toInt64(v); ok && !a.isFloat {
			a.i += iv
			a.hasNum = true
			return
		}
		if fv, ok := toFloat(v); ok {
			if !a.isFloat {
				a.f = float64(a.i)
				a.isFloat = true
			}
			a.f += fv
			a.hasNum = true
		}
	case AggMin, AggMax:
		if v == nil { // SQL MIN/MAX 는 NULL 무시.
			return
		}
		if !a.hasBest {
			a.best = v
			a.hasBest = true
			return
		}
		c := compareValues(v, a.best)
		if (a.fn == AggMin && c < 0) || (a.fn == AggMax && c > 0) {
			a.best = v
		}
	}
}

func (a *aggAcc) result() any {
	switch a.fn {
	case AggCount:
		// COUNT 는 매칭 행이 없어도 0(NULL 아님).
		if !a.hasNum {
			return int64(0)
		}
		if a.isFloat {
			return a.f
		}
		return a.i
	case AggSum:
		// SUM 은 매칭 행이 없으면 NULL.
		if !a.hasNum {
			return nil
		}
		if a.isFloat {
			return a.f
		}
		return a.i
	case AggMin, AggMax:
		return a.best // 매칭 없으면 nil(NULL).
	}
	return nil
}

// toInt64 는 정수 계열 값을 int64 로 변환한다(정수 정밀도 유지용 — float 승격 회피).
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case string:
		// PG 텍스트 프로토콜은 모든 값을 문자열로 준다 — 부분 집계(count/sum)의 재결합에
		// 필수(B-11: 문자열 미파싱 시 count 가 0 이 됐다).
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil
	case []byte:
		i, err := strconv.ParseInt(strings.TrimSpace(string(n)), 10, 64)
		return i, err == nil
	}
	return 0, false
}

// --- 와이어 레벨 라우터(cmd/pg-router) 결선용 익스포트 래퍼 -----------------------
//
// 배경(B-11): 재결합 로직은 여기 있었지만 *실제 배포되는 라우터*(cmd/pg-router 의
// scatter 경로)가 이를 호출하지 않아, `SELECT count(*)` 이 샤드별 부분결과 N행을 그대로
// 클라이언트에 반환했다(4노드 라이브 실측 2026-07-14: count → 2행 `3`,`1`). 라우터는
// ScatterGather.Execute(ShardExecutor 추상) 대신 PG 와이어 메시지를 직접 다루므로,
// *이미 수집된 부분 결과*에 재결합만 적용할 진입점이 필요하다.

// MergeAggregatePartials 는 샤드별 부분 집계 행들을 컬럼별 함수(aggs)로 재결합해
// 그룹당 1행으로 만든다. aggs 는 DetectAggregates 의 반환값을 그대로 쓴다.
func MergeAggregatePartials(rows []Row, aggs []AggregateFunc) []Row {
	if len(aggs) == 0 {
		return rows
	}
	sg := &ScatterGather{Merge: MergeAggregate, Aggregates: aggs}
	const all ShardID = "scatter"
	return sg.mergeAggregate([]ShardID{all}, map[ShardID][]Row{all: rows})
}

// SortRowsByCol 은 병합된 행들을 col 번째 컬럼으로 안정 정렬한다(전역 ORDER BY merge).
// 각 샤드 결과는 PG 가 이미 정렬해 보내므로, flatten 후 재정렬하면 전역 순서가 된다.
func SortRowsByCol(rows []Row, col int, desc bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		c := cmpAtCol(rows[i], rows[j], col)
		if desc {
			return c > 0
		}
		return c < 0
	})
}
