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

// buildInsert 는 `INSERT INTO <table> (c1,c2) VALUES ($1,$2)` 를 만든다.
func buildInsert(table string, cols []string) string {
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))
}
