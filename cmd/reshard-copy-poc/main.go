/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Command reshard-copy-poc is a G3 online-resharding live PoC. It performs the
// *reversible* data-movement step of ShardSplitJob via internal/router:
//
//   - Full copy (default): router.CopyTable (source table → target, all rows).
//   - Range copy (PGROUTER_RESHARD_TARGET_SHARD set): router.CopyShardRange — only
//     the rows whose vindex key resolves to the target shard (the *real* split:
//     move just the moving sub-range, same vindex as routing).
//   - Cutover cleanup (PGROUTER_RESHARD_DELETE_AFTER=1): after the copy and the
//     routing switch, router.DeleteShardRange removes the moved rows from the
//     source. Run ONLY after routing is switched (else data loss).
//
// The built-in vindex (murmur3 hash, 2-shard split at 0x80000000, column from
// PGROUTER_VINDEX_TYPE / PGROUTER_VINDEX_COLUMN, default "hash" / "id") matches
// the ShardRange topology so the copy and the router agree on which keys belong
// where.
//
// Config: PGROUTER_SOURCE_DSN, PGROUTER_TARGET_DSN, PGROUTER_COPY_TABLE,
// PGROUTER_RESHARD_TARGET_SHARD, PGROUTER_VINDEX_TYPE, PGROUTER_VINDEX_COLUMN,
// PGROUTER_RESHARD_DELETE_AFTER.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

// reshardSpec builds the post-split vindex spec. The ranges come from
// PGROUTER_RANGES ("shard:lo:hi,shard:lo:hi") when set (controller passes the
// target topology), else the default 2-shard murmur3 split (standalone use).
// vtype/fn are from PGROUTER_VINDEX_TYPE / PGROUTER_VINDEX_FUNCTION. Standalone
// use defaults to hash + murmur3.
func reshardSpec(vtype, col, fn, rangesEnv string) v1alpha1.ShardRangeSpec {
	if vtype == "" {
		vtype = string(v1alpha1.VindexTypeHash)
	}
	if fn == "" && (vtype == string(v1alpha1.VindexTypeHash) || vtype == string(v1alpha1.VindexTypeConsistentHash)) {
		fn = "murmur3"
	}
	ranges := parseRanges(rangesEnv)
	if len(ranges) == 0 {
		ranges = []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		}
	}
	return v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexType(vtype), Column: col, Function: v1alpha1.VindexHashFunction(fn)},
		Ranges: ranges,
	}
}

// parseRanges parses "shard:lo:hi,shard:lo:hi" into ShardRangeEntry list.
func parseRanges(s string) []v1alpha1.ShardRangeEntry {
	var out []v1alpha1.ShardRangeEntry
	for _, part := range csv(s) {
		f := strings.Split(part, ":")
		if len(f) != 3 {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: bad range %q (want shard:lo:hi)\n", part)
			os.Exit(2)
		}
		out = append(out, v1alpha1.ShardRangeEntry{Shard: f[0], Lo: f[1], Hi: f[2]})
	}
	return out
}

// defaultCopyTimeout 은 RESHARD_COPY_TIMEOUT 미지정 시 복사 제한시간이다.
const defaultCopyTimeout = 15 * time.Minute

// pocConfig 는 main 이 읽는 env 설정이다.
type pocConfig struct {
	src, tgt, table, targetShard, mode string
	cdcMode                            bool
	// delete-only: cutover 후 source 에서 이동분 삭제만(복사 없음, Cleanup phase). target 불요.
	deleteOnly bool
}

func loadConfig() pocConfig {
	mode := os.Getenv("PGROUTER_RESHARD_MODE")
	return pocConfig{
		src:         os.Getenv("PGROUTER_SOURCE_DSN"),
		tgt:         os.Getenv("PGROUTER_TARGET_DSN"),
		table:       os.Getenv("PGROUTER_COPY_TABLE"),
		targetShard: os.Getenv("PGROUTER_RESHARD_TARGET_SHARD"),
		mode:        mode,
		cdcMode:     mode == "cdc-setup" || mode == "cdc-finalize" || mode == "cdc-abort",
		deleteOnly:  os.Getenv("PGROUTER_RESHARD_DELETE_ONLY") != "",
	}
}

// problem 은 설정이 모드에 맞지 않으면 사용법 오류 메시지를, 맞으면 "" 를 반환한다.
func (c pocConfig) problem() string {
	switch {
	case c.src == "":
		return "reshard-copy-poc: PGROUTER_SOURCE_DSN required"
	case c.cdcMode && c.targetShard == "":
		return "reshard-copy-poc: CDC requires PGROUTER_RESHARD_TARGET_SHARD"
	case c.cdcMode && c.tgt == "":
		return "reshard-copy-poc: CDC requires PGROUTER_TARGET_DSN"
	case c.deleteOnly && c.targetShard == "":
		return "reshard-copy-poc: DELETE_ONLY requires PGROUTER_RESHARD_TARGET_SHARD"
	case !c.deleteOnly && c.tgt == "":
		return "reshard-copy-poc: PGROUTER_TARGET_DSN required (unless DELETE_ONLY)"
	case !c.cdcMode && c.targetShard == "" && c.table == "":
		return "reshard-copy-poc: full copy requires PGROUTER_COPY_TABLE"
	}
	return ""
}

func main() {
	cfg := loadConfig()
	if msg := cfg.problem(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), copyTimeout())
	defer cancel()

	// CDC(online) 모드: 논리복제 setup(스키마+pub+sub+lag대기) 또는 finalize(drain+drop+범위정리).
	if cfg.cdcMode {
		runCDC(ctx, cfg.mode, cfg.src, cfg.tgt, cfg.targetShard)
		return
	}

	// Full copy (no target shard) vs range-filtered copy (the real split).
	if cfg.targetShard == "" {
		runFullCopy(ctx, cfg)
		return
	}
	runRangeCopy(ctx, cfg)
}

// copyTimeout 은 복사 제한시간이다. offline(범위복사) 기본값이 60s 로 하드코딩돼 있어 조금만
// 큰 테이블이면 `context deadline exceeded` 로 InitialCopy 가 실패했다 (B-15, 4노드 라이브 실측
// 2026-07-14: orders 10만행 복사가 60s 를 넘겨 ShardSplitJob 이 Failed). 데이터 크기는
// 클러스터마다 다르므로 env 로 조정 가능하게 하고, 기본값도 현실적인 값으로 올린다.
func copyTimeout() time.Duration {
	v := os.Getenv("RESHARD_COPY_TIMEOUT")
	if v == "" {
		return defaultCopyTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	fmt.Fprintf(os.Stderr, "reshard-copy-poc: invalid RESHARD_COPY_TIMEOUT=%q (using %s)\n", v, defaultCopyTimeout)
	return defaultCopyTimeout
}

// runFullCopy 는 table 전체를 source→target 으로 복사한다.
func runFullCopy(ctx context.Context, cfg pocConfig) {
	fmt.Printf("reshard-copy-poc: InitialCopy (full) table=%q source→target\n", cfg.table)
	n, err := router.CopyTable(ctx, cfg.src, cfg.tgt, cfg.table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d before error)\n", err, n)
		os.Exit(1)
	}
	fmt.Printf("reshard-copy-poc: copied %d row(s) source→target (rollback=drop target)\n", n)
}

// runRangeCopy 는 targetShard 로 가는 키 범위만 옮긴다 — delete-only 면 source 정리만,
// 아니면 복사 후 PGROUTER_RESHARD_DELETE_AFTER 가 있으면 source 정리까지.
func runRangeCopy(ctx context.Context, cfg pocConfig) {
	col := os.Getenv("PGROUTER_VINDEX_COLUMN")
	if col == "" {
		col = "id"
	}
	spec := reshardSpec(os.Getenv("PGROUTER_VINDEX_TYPE"), col,
		os.Getenv("PGROUTER_VINDEX_FUNCTION"), os.Getenv("PGROUTER_RANGES"))
	tables := reshardTables(ctx, cfg)

	// Cleanup(delete-only): cutover 후 source 에서 targetShard 로 이동한 row 삭제만.
	if cfg.deleteOnly {
		for _, tbl := range tables {
			deleted := deleteMoved(ctx, cfg, tbl, spec)
			fmt.Printf("reshard-copy-poc: deleted %d row(s) of %q (%s keys) from source\n", deleted, tbl, cfg.targetShard)
		}
		fmt.Printf("reshard-copy-poc: cleanup complete — %s rows reclaimed from source across %d table(s)\n",
			cfg.targetShard, len(tables))
		return
	}

	for _, tbl := range tables {
		fmt.Printf("reshard-copy-poc: InitialCopy (range) table=%q vindex=%s target=%s source→target\n",
			tbl, col, cfg.targetShard)
		copied, scanned, err := router.CopyShardRange(ctx, cfg.src, cfg.tgt, tbl, spec, cfg.targetShard)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: %v (copied %d/%d before error)\n", err, copied, scanned)
			os.Exit(1)
		}
		fmt.Printf("reshard-copy-poc: copied %d/%d row(s) of %q (only %s keys) source→target\n",
			copied, scanned, tbl, cfg.targetShard)
	}

	if os.Getenv("PGROUTER_RESHARD_DELETE_AFTER") == "" {
		return
	}
	// Cutover cleanup: delete moved rows from source (run only after routing switch).
	for _, tbl := range tables {
		fmt.Printf("reshard-copy-poc: Cutover cleanup — deleting %s keys from %q in source\n", cfg.targetShard, tbl)
		deleted := deleteMoved(ctx, cfg, tbl, spec)
		fmt.Printf("reshard-copy-poc: deleted %d row(s) of %q from source\n", deleted, tbl)
	}
	fmt.Printf("reshard-copy-poc: split complete — %s now owns its range across %d table(s)\n",
		cfg.targetShard, len(tables))
}

// reshardTables 는 옮길 테이블을 정한다: COPY_TABLE 지정 시 그 하나, 아니면 source 의 모든
// user 테이블에서 reference 테이블(PGROUTER_REFERENCE_TABLES, 전 샤드 복제라 이동 대상 아님)을
// 뺀 전부.
func reshardTables(ctx context.Context, cfg pocConfig) []string {
	if cfg.table != "" {
		return []string{cfg.table}
	}
	all, err := router.ListUserTables(ctx, cfg.src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: list tables: %v\n", err)
		os.Exit(1)
	}
	tables := router.FilterTables(all, csv(os.Getenv("PGROUTER_REFERENCE_TABLES")))
	fmt.Printf("reshard-copy-poc: discovered %d table(s) to reshard: %v\n", len(tables), tables)
	return tables
}

// deleteMoved 는 source 의 tbl 에서 targetShard 로 이동한 row 를 지우고 삭제 수를 반환한다.
func deleteMoved(ctx context.Context, cfg pocConfig, tbl string, spec v1alpha1.ShardRangeSpec) int {
	deleted, err := router.DeleteShardRange(ctx, cfg.src, tbl, spec, cfg.targetShard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: delete: %v (deleted %d before error)\n", err, deleted)
		os.Exit(1)
	}
	return deleted
}

// runCDC 는 online resharding 의 논리복제 단계를 수행한다.
//   - cdc-setup: target 스키마 보장 + source publication + target subscription(copy_data=true)
//     생성 후 lag ≤ CDC_MAX_LAG 까지 대기(라이브 쓰기 따라잡기, write-block 없음).
//   - cdc-finalize: 최종 drain(lag→0, write-block 하에 호출 전제) + subscription drop +
//     DeleteForeignRange(범위 밖 정리) + publication drop.
//   - cdc-abort: 실패/중단 경로에서 subscription + publication 만 멱등 정리.
func runCDC(ctx context.Context, mode, src, tgt, targetShard string) {
	if targetShard == "" {
		fmt.Fprintln(os.Stderr, "reshard-copy-poc: CDC requires PGROUTER_RESHARD_TARGET_SHARD")
		os.Exit(2)
	}
	pub := "rsd_pub_" + sanitize(targetShard)
	sub := "rsd_sub_" + sanitize(targetShard)
	connInfo := src
	if v := os.Getenv("PGROUTER_SOURCE_CONNINFO"); v != "" {
		connInfo = v
	}
	maxLag := int64(16 << 20)
	if v := os.Getenv("PGROUTER_CDC_MAX_LAG"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			maxLag = n
		}
	}

	switch mode {
	case "cdc-setup":
		tables := cdcTables(ctx, src)
		fmt.Printf("reshard-copy-poc: cdc-setup target=%s tables=%v\n", targetShard, tables)
		must(router.EnsureSchema(ctx, src, tgt, tables), "ensure schema")
		must(router.CreatePublication(ctx, src, pub, tables), "create publication")
		must(router.CreateSubscription(ctx, tgt, connInfo, sub, pub, true), "create subscription")
		// 초기 스냅샷(tablesync) 완료를 *먼저* 게이트한다 — WAL lag 만으론 초기 COPY 미완을
		// 놓쳐 cutover/source-delete 가 빈 target 위에서 진행되어 데이터가 유실된다(B-21).
		waitTablesync(ctx, tgt, sub, len(tables))
		waitLag(ctx, src, sub, maxLag)
		fmt.Printf("reshard-copy-poc: cdc-setup 완료 — subscription %s 활성, 초기복사 완료 + lag ≤ %d\n", sub, maxLag)
	case "cdc-finalize":
		col := os.Getenv("PGROUTER_VINDEX_COLUMN")
		if col == "" {
			col = "id"
		}
		spec := reshardSpec(os.Getenv("PGROUTER_VINDEX_TYPE"), col,
			os.Getenv("PGROUTER_VINDEX_FUNCTION"), os.Getenv("PGROUTER_RANGES"))
		tables := cdcTables(ctx, src)
		fmt.Printf("reshard-copy-poc: cdc-finalize target=%s (write-block 하 최종 drain)\n", targetShard)
		waitLag(ctx, src, sub, 0) // write-block 하 → 새 쓰기 없음 → lag→0 수렴.
		must(router.DropSubscription(ctx, tgt, sub), "drop subscription")
		total := 0
		for _, t := range tables {
			n, err := router.DeleteForeignRange(ctx, tgt, t, spec, targetShard)
			must(err, "delete foreign range "+t)
			total += n
		}
		// 범위 정리 후 인덱스/PK + 제약(CHECK·FK) 복제(데이터 확정 후 — bulk 효율).
		for _, t := range tables {
			_, err := router.ReplicateIndexes(ctx, src, tgt, t)
			must(err, "replicate indexes "+t)
			_, err = router.ReplicateConstraints(ctx, src, tgt, t)
			must(err, "replicate constraints "+t)
		}
		must(router.DropPublication(ctx, src, pub), "drop publication")
		fmt.Printf("reshard-copy-poc: cdc-finalize 완료 — 범위 밖 %d row 삭제, %s 자기 범위만 보유\n", total, targetShard)
	case "cdc-abort":
		fmt.Printf("reshard-copy-poc: cdc-abort target=%s\n", targetShard)
		// 1) 정상 drop 시도(원격 slot 까지 정리). source 불통 등으로 실패하면 force
		//    fallback 으로 target subscription 을 확실히 제거한다(§6.7 abort 누수 차단 —
		//    source-down 에도 AbortCleanup 이 완료되도록).
		if err := router.DropSubscription(ctx, tgt, sub); err != nil {
			fmt.Printf("reshard-copy-poc: cdc-abort 정상 drop 실패(%v) → force fallback(slot detach)\n", err)
			must(router.ForceDropSubscription(ctx, tgt, sub), "force drop subscription")
		}
		// 2) publication drop 은 best-effort — source 불통이면 어차피 불가하고, orphan
		//    slot 은 source 복구 후 정리한다(target 정리 우선).
		if err := router.DropPublication(ctx, src, pub); err != nil {
			fmt.Printf("reshard-copy-poc: cdc-abort publication drop best-effort 실패(%v) — "+
				"source 불통 가정, orphan slot 는 source 복구 후 정리\n", err)
		}
		fmt.Printf("reshard-copy-poc: cdc-abort 완료 — subscription %s 정리(fallback 포함)\n", sub)
	}
}

// cdcTables 는 COPY_TABLE 지정 시 그 하나, 아니면 source user 테이블에서 reference 제외 전부.
func cdcTables(ctx context.Context, src string) []string {
	if t := os.Getenv("PGROUTER_COPY_TABLE"); t != "" {
		return []string{t}
	}
	all, err := router.ListUserTables(ctx, src)
	must(err, "list tables")
	return router.FilterTables(all, csv(os.Getenv("PGROUTER_REFERENCE_TABLES")))
}

// waitTablesync 는 target subscription 의 초기 테이블 복사(pg_subscription_rel.srsubstate)가
// wantTables 전부 'r'|'s' 될 때까지 폴링한다(ctx 만료 시 실패 → phase Failed → 데이터 유실 대신
// 명시적 실패). B-21: waitLag(WAL lag) 만으론 초기 스냅샷 미완을 놓쳐 유실이 발생했다.
func waitTablesync(ctx context.Context, tgt, sub string, wantTables int) {
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: tablesync wait timeout (sub=%s)\n", sub)
			os.Exit(1)
		default:
		}
		pending, err := router.TablesyncPending(ctx, tgt, sub, wantTables)
		must(err, "tablesync")
		if pending == 0 {
			return
		}
		time.Sleep(time.Second)
	}
}

// waitLag 는 subscription 슬롯 lag 가 maxLag 이하가 될 때까지 폴링한다(ctx 만료 시 실패).
func waitLag(ctx context.Context, src, sub string, maxLag int64) {
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "reshard-copy-poc: lag wait timeout (sub=%s)\n", sub)
			os.Exit(1)
		default:
		}
		lag, err := router.SubscriptionLagBytes(ctx, src, sub)
		must(err, "lag")
		if lag >= 0 && lag <= maxLag {
			return
		}
		time.Sleep(time.Second)
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "reshard-copy-poc: %s: %v\n", what, err)
		os.Exit(1)
	}
}

// csv 는 콤마 구분 문자열을 trim 해 분리한다(빈 값 제거).
func csv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
