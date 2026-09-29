/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 렌더한 PG 설정의 소유다(설계 2026-09-25 §3.17, ADR-0035). ALTER SYSTEM 이 postgresql.auto.conf 에 쓴 값은
// 렌더한 postgresql.conf 를 이긴다 — 렌더 키가 auto.conf 에 남으면 spec 이 조용히 무시된다.
//
//	렌더 키 ∩ auto.conf 항목 (멤버마다 — auto.conf 는 PGDATA 에 있고 복제되지 않는다)
//	  렌더 conf 파일이 그 키를 렌더 값으로 보이나(pg_file_settings)  아니오 → 기다린다(다음 reconcile)
//	  auto 값 ≤ 렌더 값 (수는 GUC 기본 단위로, 그 밖은 같을 때만)     예   → ALTER SYSTEM RESET + reload
//	                                                               아니오 → 남긴다 · Warning · AutoConfOverride=True
//
// 멤버는 설정 해시·파드 UID 가 바뀌면, 그 밖엔 autoConfRecheck 마다 다시 본다. exec 실패는 다음 reconcile 이 다시 본다.

const (
	// autoConfRecheck 는 결론 난 멤버를 다시 보는 간격이다 — 나중에 손으로 한 ALTER SYSTEM 을 잡는다.
	autoConfRecheck = reseedCooldown

	// autoConfFile 은 ALTER SYSTEM 이 쓰는 PGDATA 안 파일이다.
	autoConfFile = "postgresql.auto.conf"

	// autoConfOriginConf·autoConfOriginAuto 는 판정 줄의 출처다 — 렌더 conf 파일 · auto.conf.
	autoConfOriginConf = "conf"
	autoConfOriginAuto = "auto"
	// autoConfFields 는 판정 줄의 열 수다: 출처|이름|값 hex|vartype|unit.
	autoConfFields = 5

	// autoConfResetReason 은 RESET 한 멤버·키의 이벤트 사유다(남길 때는 Warning ReasonAutoConfOverrideKept).
	autoConfResetReason = "AutoConfOverrideReset"
)

// autoConfExempt 는 렌더돼도 RESET 하지 않는 설정이다 — 부트스트랩·복제 그래프 재지정·복구가 auto.conf 에 쓴다.
var autoConfExempt = []string{"primary_conninfo", "primary_slot_name", "restore_command"}

// settingNamePattern 은 ALTER SYSTEM 에 이름 그대로 싣는 GUC 이름이다.
var settingNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// magnitudePattern 은 설정 값의 수와 단위다(예: 5GB · 1.5GB · 5120 · 2min). 음수는 받지 않는다(−1 = 제한 없음 같은 표지).
var magnitudePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]*)$`)

// timeUnits 는 PG 시간 단위의 µs 배수다(메모리 단위는 settingUnits).
var timeUnits = map[string]float64{"us": 1, "ms": 1e3, "s": 1e6, "min": 60e6, "h": 3600e6, "d": 86400e6}

// errAutoConfUnparsed 는 설정 파일 문법 오류다 — 파일 값을 믿을 수 없어 기다린다.
var errAutoConfUnparsed = errors.New("config file has syntax errors")

// unitFamily 는 단위 갈래다 — 다른 갈래끼리는 비교하지 않는다.
type unitFamily int

const (
	unitPlain  unitFamily = iota // 단위 없는 수
	unitMemory                   // 바이트
	unitTime                     // µs
)

// autoConfVerdict 는 렌더 키 하나의 판정이다.
type autoConfVerdict int

const (
	autoConfWait  autoConfVerdict = iota // 렌더 conf 파일이 아직 렌더 값을 보이지 않는다(kubelet 투영 전)
	autoConfReset                        // auto 값 ≤ 렌더 값 — RESET 해도 값이 줄지 않는다
	autoConfKeep                         // auto 값이 더 크거나 비교할 수 없다 — 남긴다
)

// autoConfEntry 는 한 멤버의 렌더 키 판정 재료다 — pg_file_settings 는 적재 상태가 아니라 지금 파일을 읽는다.
type autoConfEntry struct {
	auto    string // postgresql.auto.conf 의 마지막 값
	file    string // 렌더 conf 파일(투영)의 마지막 값
	inFile  bool   // 렌더 conf 파일에 그 키가 있다
	vartype string // pg_settings.vartype — 비면 모르는 설정(같을 때만 RESET)
	unit    string // pg_settings.unit — 단위 없는 값의 기본 단위
}

// autoConfMark 는 멤버 한 곳의 마지막 판정이다(메모리 — 오퍼레이터가 다시 뜨면 한 번씩 다시 본다).
type autoConfMark struct {
	uid    types.UID
	hash   string    // 판정한 설정 CM 해시
	at     time.Time // 결론 난 판정 시각 — 0(아주 오래전)이면 다음 reconcile 이 다시 본다
	kept   []string  // 남긴 항목("key=auto (rendered x)") — condition · Warning 한 번
	reload bool      // RESET 뒤 reload 가 실패했다 — 다음 reconcile 이 다시 보낸다
}

// due 는 다시 볼 때인지다 — 파드·설정이 바뀌었거나, autoConfRecheck 가 지났다(결론이 없으면 at 이 0 이라 늘 지났다).
func (m autoConfMark) due(uid types.UID, hash string, now time.Time) bool {
	return m.uid != uid || m.hash != hash || now.Sub(m.at) >= autoConfRecheck
}

// renderedKeys 는 렌더 conf 의 설정 이름이다(정렬) — 복제 연결 설정은 빠진다.
func renderedKeys(conf string) []string {
	settings := confSettings(conf)
	keys := make([]string, 0, len(settings))
	for key := range settings {
		if slices.Contains(autoConfExempt, key) || !settingNamePattern.MatchString(key) {
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// reconcileAutoConf 는 멤버마다 렌더 키의 auto.conf 항목을 RESET 하거나 남기고 condition AutoConfOverride 를 싣는다.
// 돌지 않는 멤버는 건너뛴다(죽은 노드 exec 는 시한까지 붙잡힌다 — 기억은 둔다). 사라진 멤버는 잊는다.
func (r *PostgresClusterReconciler) reconcileAutoConf(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pods []corev1.Pod,
	now time.Time,
) error {
	key := cluster.Namespace + "/" + cluster.Name
	prev := r.autoConfMarks(key)
	marks := make(map[string]autoConfMark, len(pods))
	var failed []error
	for i := range pods {
		pod := &pods[i]
		mark, err := r.ownAutoConf(ctx, cluster, pod, prev[pod.Name], now)
		marks[pod.Name] = mark
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", pod.Name, err))
		}
	}
	r.storeAutoConf(key, marks)
	setAutoConfCondition(cluster, marks)
	return errors.Join(failed...)
}

// ownAutoConf 는 멤버 한 곳의 판정이다 — 새 기억을 돌려준다.
func (r *PostgresClusterReconciler) ownAutoConf(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pod *corev1.Pod,
	mark autoConfMark,
	now time.Time,
) (autoConfMark, error) {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return mark, nil
	}
	// RESET 은 됐는데 reload 가 실패했다 — 파일에는 할 일이 없어 보이니 reload 부터 다시 보낸다.
	if mark.reload {
		if err := r.reloadPostgresConfig(ctx, pod.Namespace, pod.Name); err != nil {
			return mark, err
		}
		mark.reload = false
	}

	// 판정 기준 = 멤버가 마운트한 설정 CM(kubelet 이 파일로 투영하는 것).
	name := configMapMounted(&pod.Spec)
	if name == "" {
		return mark, nil
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: name}, &cm); err != nil {
		return mark, err
	}
	conf := cm.Data[path.Base(pgConfigFile)]
	keys := renderedKeys(conf)
	for _, k := range keys {
		MetricAutoConfResets.WithLabelValues(cluster.Namespace, cluster.Name, k).Add(0)
	}
	hash := postgresConfigHash(cm.Data)
	if !mark.due(pod.UID, hash, now) {
		return mark, nil
	}
	next := autoConfMark{uid: pod.UID, hash: hash, at: now}
	if len(keys) == 0 {
		return next, nil
	}

	entries, err := r.autoConfEntries(ctx, pod, keys)
	if errors.Is(err, errAutoConfUnparsed) {
		return mark, nil
	}
	if err != nil {
		return mark, err
	}

	// 렌더 키마다 판정 — 투영 전 키가 있으면 결론이 없다(다음 reconcile 이 다시 본다).
	rendered := confSettings(conf)
	var reset []string
	for _, k := range keys {
		entry, ok := entries[k]
		if !ok {
			continue
		}
		switch judgeAutoConf(entry, rendered[k]) {
		case autoConfWait:
			next.at = time.Time{}
		case autoConfReset:
			reset = append(reset, k)
		case autoConfKeep:
			next.kept = append(next.kept, fmt.Sprintf("%s=%s (rendered %s)", k, entry.auto, rendered[k]))
		}
	}
	if len(next.kept) > 0 && !slices.Equal(next.kept, mark.kept) {
		commonsevents.EmitWarningf(r.Recorder, cluster, ReasonAutoConfOverrideKept,
			"%s: postgresql.auto.conf keeps %s — larger than rendered or not comparable, not reset",
			pod.Name, strings.Join(next.kept, ", "))
	}
	if len(reset) == 0 {
		return next, nil
	}
	return r.resetAutoConf(ctx, cluster, pod, reset, rendered, next)
}

// autoConfQuery 는 렌더 키의 두 파일 내용이다(seqno 순 — 렌더 conf 뒤 auto.conf). 이름 없는 줄은 문법 오류다. 값은
// hex 다 — archive_command 같은 값에 '|' 가 있다.
func autoConfQuery(keys []string) string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, sqlLiteral(key))
	}
	return "SELECT CASE WHEN f.sourcefile = current_setting('config_file') THEN '" + autoConfOriginConf +
		"' ELSE '" + autoConfOriginAuto + "' END, coalesce(f.name, ''), " +
		"encode(convert_to(coalesce(f.setting, ''), 'UTF8'), 'hex'), coalesce(s.vartype, ''), coalesce(s.unit, '') " +
		"FROM pg_file_settings f LEFT JOIN pg_settings s ON s.name = f.name " +
		"WHERE (f.name IN (" + strings.Join(names, ", ") + ") OR (f.name IS NULL AND f.error IS NOT NULL)) " +
		"AND f.sourcefile IN (current_setting('config_file'), current_setting('data_directory') || '/" + autoConfFile + "') " +
		"ORDER BY f.seqno"
}

// autoConfEntries 는 auto.conf 에 있는 렌더 키의 판정 재료다(로컬 소켓 exec 한 번). 설정 파일 문법 오류면
// errAutoConfUnparsed 다.
func (r *PostgresClusterReconciler) autoConfEntries(
	ctx context.Context,
	pod *corev1.Pod,
	keys []string,
) (map[string]autoConfEntry, error) {
	out, err := r.memberSQL(ctx, pod, autoConfQuery(keys))
	if err != nil {
		return nil, err
	}

	entries := map[string]autoConfEntry{}
	inAuto := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, psqlFieldSeparator)
		if len(fields) != autoConfFields {
			return nil, fmt.Errorf("settings row %q unreadable", line)
		}
		if fields[1] == "" {
			return nil, errAutoConfUnparsed
		}
		value, err := hex.DecodeString(fields[2])
		if err != nil {
			return nil, fmt.Errorf("settings row %q unreadable: %w", line, err)
		}

		e := entries[fields[1]]
		e.vartype, e.unit = fields[3], fields[4]
		switch fields[0] {
		case autoConfOriginConf:
			e.file, e.inFile = string(value), true
		case autoConfOriginAuto:
			e.auto, inAuto[fields[1]] = string(value), true
		default:
			return nil, fmt.Errorf("settings row %q has an unknown origin", line)
		}
		entries[fields[1]] = e
	}
	maps.DeleteFunc(entries, func(name string, _ autoConfEntry) bool { return !inAuto[name] })
	return entries, nil
}

// judgeAutoConf 는 auto.conf 값을 렌더 값과 가른다 — 투영 확인이 먼저다(투영 전 RESET 은 옛 파일 값으로 떨어진다).
func judgeAutoConf(e autoConfEntry, rendered string) autoConfVerdict {
	if !e.inFile || e.file != rendered {
		return autoConfWait
	}
	if !autoConfAtMost(e, rendered) {
		return autoConfKeep
	}
	return autoConfReset
}

// autoConfAtMost 는 auto 값 ≤ 렌더 값인지다 — 같은 글이면 참, 수(integer·real)는 GUC 기본 단위로 비교한다
// (wal_keep_size '10240' = 10240MB — pg_size_bytes 는 바이트로 읽는다). 그 밖·읽지 못하는 값은 거짓이다.
func autoConfAtMost(e autoConfEntry, rendered string) bool {
	if e.auto == rendered {
		return true
	}
	if e.vartype != "integer" && e.vartype != "real" {
		return false
	}
	auto, autoOK := settingMagnitude(e.auto, e.unit)
	want, wantOK := settingMagnitude(rendered, e.unit)
	return autoOK && wantOK && auto <= want
}

// settingMagnitude 는 설정 값의 크기다 — 단위가 없으면 기본 단위(unit)다. 메모리는 바이트, 시간은 µs 로 낸다.
func settingMagnitude(raw, unit string) (float64, bool) {
	base, family, ok := baseScale(unit)
	m := magnitudePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if !ok || m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	if m[2] == "" {
		return n * base, true
	}
	scale, suffix, known := unitScale(m[2])
	if !known || suffix != family {
		return 0, false
	}
	return n * scale, true
}

// baseScale 은 pg_settings.unit 의 배수와 갈래다 — "8kB" = 8192 바이트 · "s" = 10⁶ µs · "" = 수.
func baseScale(unit string) (float64, unitFamily, bool) {
	if unit == "" {
		return 1, unitPlain, true
	}
	name := strings.TrimLeft(unit, "0123456789")
	count := 1.0
	if digits := unit[:len(unit)-len(name)]; digits != "" {
		n, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, unitPlain, false
		}
		count = n
	}
	scale, family, ok := unitScale(name)
	return count * scale, family, ok
}

// unitScale 은 단위 하나의 배수와 갈래다.
func unitScale(unit string) (float64, unitFamily, bool) {
	if scale, ok := settingUnits[unit]; ok {
		return float64(scale), unitMemory, true
	}
	if scale, ok := timeUnits[unit]; ok {
		return scale, unitTime, true
	}
	return 0, unitPlain, false
}

// resetAutoConf 는 키들을 auto.conf 에서 지우고(ALTER SYSTEM RESET — 스탠바이도 된다) reload 한다. RESET 이 실패하면
// 결론 없이 돌아가 다음 reconcile 이 다시 보고, reload 가 실패하면 다음 reconcile 이 reload 를 다시 보낸다.
func (r *PostgresClusterReconciler) resetAutoConf(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	pod *corev1.Pod,
	keys []string,
	rendered map[string]string,
	next autoConfMark,
) (autoConfMark, error) {
	var failed error
	done := 0
	for _, key := range keys {
		if _, err := r.memberSQL(ctx, pod, "ALTER SYSTEM RESET "+key); err != nil {
			failed, next.at = err, time.Time{}
			break
		}
		done++
		MetricAutoConfResets.WithLabelValues(cluster.Namespace, cluster.Name, key).Inc()
		commonsevents.Emitf(r.Recorder, cluster, autoConfResetReason,
			"%s: reset %s in postgresql.auto.conf — the rendered %s owns it", pod.Name, key, rendered[key])
	}
	if done == 0 {
		return next, failed
	}
	if err := r.reloadPostgresConfig(ctx, pod.Namespace, pod.Name); err != nil {
		next.reload = true
		return next, errors.Join(failed, err)
	}
	return next, failed
}

// autoConfMarks 는 클러스터(namespace/name)의 멤버 판정 기억이다 — 통째로 바뀌기만 해 잠금 밖에서 읽어도 된다.
func (r *PostgresClusterReconciler) autoConfMarks(key string) map[string]autoConfMark {
	r.autoConfMu.Lock()
	defer r.autoConfMu.Unlock()
	return r.autoConf[key]
}

// storeAutoConf 는 이번 멤버들의 기억으로 바꾼다.
func (r *PostgresClusterReconciler) storeAutoConf(key string, marks map[string]autoConfMark) {
	r.autoConfMu.Lock()
	defer r.autoConfMu.Unlock()
	if r.autoConf == nil {
		r.autoConf = map[string]map[string]autoConfMark{}
	}
	r.autoConf[key] = marks
}

// setAutoConfCondition 은 남긴 auto.conf 항목을 condition AutoConfOverride 로 보인다(KSM → O1 경보).
func setAutoConfCondition(cluster *postgresv1alpha1.PostgresCluster, marks map[string]autoConfMark) {
	count := 0
	for _, mark := range marks {
		count += len(mark.kept)
	}
	kept := make([]string, 0, count)
	for _, pod := range slices.Sorted(maps.Keys(marks)) {
		for _, item := range marks[pod].kept {
			kept = append(kept, pod+": "+item)
		}
	}
	if len(kept) == 0 {
		setCondition(&cluster.Status.Conditions, ConditionAutoConfOverride, metav1.ConditionFalse, ReasonAutoConfOwned,
			"no running member overrides a rendered setting in postgresql.auto.conf", cluster.Generation)
		return
	}
	setCondition(&cluster.Status.Conditions, ConditionAutoConfOverride, metav1.ConditionTrue, ReasonAutoConfOverrideKept,
		"postgresql.auto.conf overrides rendered settings: "+strings.Join(kept, "; "), cluster.Generation)
}
