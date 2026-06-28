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

	// target 에 테이블이 없으면 source 의 컬럼 정의로 만든다(스키마 우선 복제 — resharding 은
	// 빈 새 shard 로 옮기므로 DDL 이 먼저 있어야 INSERT 가능).
	if err := ensureTargetTable(ctx, src, tgt, table); err != nil {
		return 0, 0, err
	}

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
	// 데이터 복사 후 인덱스/PK 복제(bulk load 효율 + uniqueness·조회 성능).
	if _, err := replicateIndexes(ctx, src, tgt, table); err != nil {
		return copied, scanned, err
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

// ListUserTables 는 dsn 의 public 스키마 base table 이름 목록을 반환한다(이름순). resharding
// 은 모든 샤딩 테이블을 옮겨야 하므로, copy 전에 source 의 테이블을 발견하는 데 쓴다.
func ListUserTables(ctx context.Context, dsn string) ([]string, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("router: open: %w", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema='public' AND table_type='BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("router: list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("router: scan table name: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// FilterTables 는 all 에서 exclude(대소문자 무시; reference 테이블 등)를 뺀 목록을 반환한다.
func FilterTables(all, exclude []string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		skip[strings.ToLower(e)] = true
	}
	var out []string
	for _, t := range all {
		if !skip[strings.ToLower(t)] {
			out = append(out, t)
		}
	}
	return out
}

// ReplicateIndexes 는 source table 의 인덱스(PK 백킹 unique index 포함)를 target 에 멱등
// 생성한다(IF NOT EXISTS). 데이터 복사·sync *후* 호출하는 게 효율적(bulk load 중 인덱스
// 유지비 회피). 생성한 인덱스 수 반환. 외래키/체크 제약은 후속.
func ReplicateIndexes(ctx context.Context, sourceDSN, targetDSN, table string) (int, error) {
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
	return replicateIndexes(ctx, src, tgt, table)
}

// replicateIndexes 는 열린 DB 핸들로 인덱스를 복제한다(CopyShardRange 내부 재사용).
func replicateIndexes(ctx context.Context, src, tgt *sql.DB, table string) (int, error) {
	rows, err := src.QueryContext(ctx,
		`SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename=$1`, table)
	if err != nil {
		return 0, fmt.Errorf("router: read indexes %s: %w", table, err)
	}
	var defs []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("router: scan indexdef %s: %w", table, err)
		}
		defs = append(defs, def)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("router: indexes %s: %w", table, err)
	}
	n := 0
	for _, def := range defs {
		if _, err := tgt.ExecContext(ctx, idxIfNotExists(def)); err != nil {
			return n, fmt.Errorf("router: create index on %s: %w", table, err)
		}
		n++
	}
	return n, nil
}

// idxIfNotExists 는 pg_indexes.indexdef 에 IF NOT EXISTS 를 주입해 멱등화한다.
func idxIfNotExists(def string) string {
	def = strings.Replace(def, "CREATE UNIQUE INDEX ", "CREATE UNIQUE INDEX IF NOT EXISTS ", 1)
	if !strings.Contains(def, "IF NOT EXISTS") {
		def = strings.Replace(def, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ", 1)
	}
	return def
}

// ensureTargetTable 은 target 에 table 이 없으면 source 의 컬럼 정의(format_type 로 충실한
// 타입)로 CREATE TABLE 한다. PK/인덱스/제약은 복제하지 않는다(PoC — 데이터 무결성은 copy 가
// vindex 로 보장; 인덱스는 cutover 후 별도 생성 트랙). 이미 있으면 no-op(IF NOT EXISTS).
func ensureTargetTable(ctx context.Context, src, tgt *sql.DB, table string) error {
	var ddl string
	err := src.QueryRowContext(ctx, `
		SELECT 'CREATE TABLE IF NOT EXISTS '||quote_ident($1)||' ('||
		       string_agg(quote_ident(a.attname)||' '||format_type(a.atttypid, a.atttypmod), ', ' ORDER BY a.attnum)||')'
		FROM pg_attribute a
		WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped`, table).Scan(&ddl)
	if err != nil {
		return fmt.Errorf("router: read source schema %s: %w", table, err)
	}
	if ddl == "" {
		return fmt.Errorf("router: source table %s has no columns", table)
	}
	if _, err := tgt.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("router: create target table %s: %w", table, err)
	}
	return nil
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
