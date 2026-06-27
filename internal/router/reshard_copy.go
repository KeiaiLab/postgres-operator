// Package router — reshard_copy.go 는 online resharding 의 InitialCopy phase
// (source shard → target shard 데이터 이동)를 구현한다.
//
// ShardSplitJob 7-step state machine(shardsplitjob_types.go) 의 InitialCopy 는
// source 가 가진 키 범위의 row 들을 새 target shard 로 복사하는 *비가역이 아닌*
// 단계다 — target 을 비우거나 삭제하면 rollback 되며(§6 L3 self-repair 안전망:
// snapshot=source 그대로 + rollback=target drop), source 는 건드리지 않는다.
// 비가역은 그 다음 Cutover(write-block + routing 전환)뿐이며 본 file 범위 밖이다.
//
// 본 PoC 는 테이블 단위 전체 복사다. murmur3 hash range 필터(source 의 일부 키만
// target 으로)는 vindex 평가를 app-level 에서 적용하는 후속 작업이며, batch COPY /
// logical replication CDC 도 후속이다.
package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	_ "github.com/lib/pq" // postgres driver

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// tableNamePattern 은 복사 대상 테이블 식별자 허용 문자 집합 (SQL injection 차단 —
// 테이블 이름은 placeholder 로 바인딩 불가하므로 화이트리스트 검증한다).
var tableNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ErrInvalidTable 은 테이블 이름이 식별자 화이트리스트를 위반할 때 반환된다.
var ErrInvalidTable = errors.New("router: invalid table identifier")

// CopyTable 은 source DSN 의 테이블 전체를 target DSN 으로 복사하고 복사된 row 수를
// 반환한다 (resharding InitialCopy PoC). source 는 read-only(SELECT)만, target 에만
// INSERT 한다 — rollback 은 target 테이블 truncate/drop.
func CopyTable(ctx context.Context, sourceDSN, targetDSN, table string) (int, error) {
	if !tableNamePattern.MatchString(table) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}
	src, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return 0, fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = src.Close() }()
	tgt, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return 0, fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = tgt.Close() }()

	rows, err := src.QueryContext(ctx, "SELECT * FROM "+table) //nolint:gosec // table는 화이트리스트 검증됨
	if err != nil {
		return 0, fmt.Errorf("router: source select %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("router: columns %s: %w", table, err)
	}
	insertSQL := buildInsert(table, cols)

	copied := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return copied, fmt.Errorf("router: scan %s: %w", table, err)
		}
		if _, err := tgt.ExecContext(ctx, insertSQL, vals...); err != nil {
			return copied, fmt.Errorf("router: target insert %s: %w", table, err)
		}
		copied++
	}
	if err := rows.Err(); err != nil {
		return copied, fmt.Errorf("router: rows %s: %w", table, err)
	}
	return copied, nil
}

// CopyShardRange 는 source 테이블에서 *vindex 키가 targetShard 로 해소되는 row 들만*
// target 으로 복사한다 (copied, scanned 반환). 이것이 진짜 resharding 데이터 이동이다 —
// split 은 새 shard 의 키 범위에 속하는 부분집합만 옮긴다. 라우팅과 *동일한 vindex*
// (ResolveShard)로 각 row 의 키를 평가하므로 cutover 후 라우팅과 데이터 위치가 일치한다.
// source 는 read-only(SELECT)만 — rollback 은 target 테이블 truncate/drop.
func CopyShardRange(ctx context.Context, sourceDSN, targetDSN, table string, spec v1alpha1.ShardRangeSpec, targetShard string) (copied, scanned int, err error) {
	if !tableNamePattern.MatchString(table) {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}
	keyCol := spec.Vindex.Column
	if !tableNamePattern.MatchString(keyCol) {
		return 0, 0, fmt.Errorf("%w: vindex column %q", ErrInvalidTable, keyCol)
	}
	src, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return 0, 0, fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = src.Close() }()
	tgt, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return 0, 0, fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = tgt.Close() }()

	rows, err := src.QueryContext(ctx, "SELECT * FROM "+table) //nolint:gosec // table는 화이트리스트 검증됨
	if err != nil {
		return 0, 0, fmt.Errorf("router: source select %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return 0, 0, fmt.Errorf("router: columns %s: %w", table, err)
	}
	keyIdx := indexOfFold(cols, keyCol)
	if keyIdx < 0 {
		return 0, 0, fmt.Errorf("router: vindex column %q not found in table %q", keyCol, table)
	}
	insertSQL := buildInsert(table, cols)

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return copied, scanned, fmt.Errorf("router: scan %s: %w", table, err)
		}
		scanned++
		shard, err := ResolveShard(spec, keyString(vals[keyIdx]))
		if err != nil {
			return copied, scanned, fmt.Errorf("router: resolve key: %w", err)
		}
		if shard != targetShard {
			continue // 이 키는 target shard 소속이 아님 — 건너뜀.
		}
		if _, err := tgt.ExecContext(ctx, insertSQL, vals...); err != nil {
			return copied, scanned, fmt.Errorf("router: target insert %s: %w", table, err)
		}
		copied++
	}
	if err := rows.Err(); err != nil {
		return copied, scanned, fmt.Errorf("router: rows %s: %w", table, err)
	}
	return copied, scanned, nil
}

// DeleteShardRange 는 cutover *완료 후* source 테이블에서 movedShard 로 이동한 키의 row 들을
// 삭제한다(이동 키가 더는 이 shard 소속이 아니므로 정리). CopyShardRange 로 target 에 안전히
// 복사되고 라우팅이 전환된 *뒤에만* 호출해야 한다 — 그 전엔 데이터 유실 위험. 삭제된 row 수
// 반환.
func DeleteShardRange(ctx context.Context, sourceDSN, table string, spec v1alpha1.ShardRangeSpec, movedShard string) (int, error) {
	if !tableNamePattern.MatchString(table) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}
	keyCol := spec.Vindex.Column
	if !tableNamePattern.MatchString(keyCol) {
		return 0, fmt.Errorf("%w: vindex column %q", ErrInvalidTable, keyCol)
	}
	src, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return 0, fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = src.Close() }()

	rows, err := src.QueryContext(ctx, "SELECT DISTINCT "+keyCol+" FROM "+table) //nolint:gosec // keyCol 화이트리스트 검증됨
	if err != nil {
		return 0, fmt.Errorf("router: distinct keys %s: %w", table, err)
	}
	var moving []any
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("router: scan key %s: %w", table, err)
		}
		shard, err := ResolveShard(spec, keyString(v))
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("router: resolve key: %w", err)
		}
		if shard == movedShard {
			moving = append(moving, v)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("router: rows %s: %w", table, err)
	}

	deleted := 0
	delSQL := "DELETE FROM " + table + " WHERE " + keyCol + " = $1" //nolint:gosec // 식별자 화이트리스트 검증됨
	for _, k := range moving {
		res, err := src.ExecContext(ctx, delSQL, k)
		if err != nil {
			return deleted, fmt.Errorf("router: delete key in %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}
	return deleted, nil
}

// keyString 은 row 값(any)을 vindex 키 문자열로 정규화한다 — lib/pq 는 text 를 []byte 로
// 돌려주므로 string 변환(fmt.Sprint 면 "[97 ...]" 가 됨). 라우터의 키 추출(문자열)과 일치.
func keyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// indexOfFold 는 cols 에서 name 과 대소문자 무시 일치하는 첫 인덱스(없으면 -1).
func indexOfFold(cols []string, name string) int {
	for i, c := range cols {
		if strings.EqualFold(c, name) {
			return i
		}
	}
	return -1
}

// buildInsert 는 `INSERT INTO <table> (c1,c2) VALUES ($1,$2)` 를 만든다.
func buildInsert(table string, cols []string) string {
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))
}
