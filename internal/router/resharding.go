// Package router — resharding.go 는 online resharding(ShardSplitJob) 의 키 범위
// 보존 불변식을 검증한다.
//
// ShardSplitJob 은 source shard 의 키 범위를 target shard 들로 split(또는 역으로
// merge)한다. 이 때 *데이터 보존 불변식* — source 가 커버하던 모든 키가 정확히 하나의
// target 으로 이동하며 손실(gap)/중복(overlap)이 0 — 이 깨지면 cutover 후 row 가
// 사라지거나 두 shard 에 중복된다. 본 file 은 7-step state machine(shardsplitjob_types.go)
// 의 Pending phase reconciler / validating webhook 이 재사용하는 *순수 검증 함수*다.
// 실 데이터 이동(SnapshotWAL/InitialCopy/CDCCatchup/Cutover)은 후속 reconciler.
package router

import (
	"errors"
	"fmt"
	"sort"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ErrSplitPlanGap 은 정렬된 range 사이에 키 공백이 있을 때 반환된다 (데이터 유실 위험).
var ErrSplitPlanGap = errors.New("router: split plan has a gap between ranges")

// ErrSplitPlanOverlap 은 range 가 겹칠 때 반환된다 (키 중복 — 어느 shard 로 갈지 모호).
var ErrSplitPlanOverlap = errors.New("router: split plan has overlapping ranges")

// ErrSplitPlanCoverage 는 source 합집합과 target 합집합의 경계가 다를 때 반환된다
// (resharding 후 source 가 커버하던 일부 키가 어떤 target 에도 속하지 않거나 그 반대).
var ErrSplitPlanCoverage = errors.New("router: source and target key coverage differ")

// ValidateSplitPlan 은 ShardSplitJob 의 source/target 범위가 데이터 보존 불변식을
// 만족하는지 검증한다: ① source 들은 무중첩·무공백 연속 ② target 들은 무중첩·무공백
// 연속 ③ source 합집합과 target 합집합의 [최소 lo, 최대 hi] 경계가 정확히 일치.
//
// Lo/Hi 는 hex 문자열(고정폭 0-padded, vindex.resolveRange 와 동일 lexical 순서)로
// 비교한다 — ShardRangeEntry 의 컨벤션(예: "0x00000000".."0x7fffffff").
func ValidateSplitPlan(sources, targets []v1alpha1.ShardRangeEntry) error {
	sLo, sHi, err := contiguousSpan(sources)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	tLo, tHi, err := contiguousSpan(targets)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if sLo != tLo || sHi != tHi {
		return fmt.Errorf("%w: source=[%s,%s] target=[%s,%s]", ErrSplitPlanCoverage, sLo, sHi, tLo, tHi)
	}
	return nil
}

// contiguousSpan 은 range 목록을 lo 기준 정렬한 뒤 무중첩·무공백(연속)임을 검증하고
// 전체 span 의 [최소 lo, 최대 hi] 를 반환한다. 빈 목록은 에러.
//
// "연속" 정의: 정렬된 range 에서 다음 range 의 lo 가 직전 range 의 hi 바로 다음이어야
// 한다. ShardRangeEntry 는 [lo, hi] *폐구간*(vindex 는 `>= lo && <= hi`)이므로 인접
// 경계는 hi 와 그 successor 가 같은 prefix 의 +1 — 본 PoC 는 단순화하여 "다음 lo 가
// 직전 hi 보다 크고, 사이에 다른 키가 없음"을 *문자열 successor* 로 검사하지 않고
// gap/overlap 만 lexical 비교로 잡는다(인접성은 호출자가 보장하는 hex 폭 고정 전제).
func contiguousSpan(ranges []v1alpha1.ShardRangeEntry) (lo, hi string, err error) {
	if len(ranges) == 0 {
		return "", "", errors.New("router: empty range list")
	}
	sorted := make([]v1alpha1.ShardRangeEntry, len(ranges))
	copy(sorted, ranges)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Lo < sorted[j].Lo })

	for i, r := range sorted {
		if r.Lo > r.Hi {
			return "", "", fmt.Errorf("router: range %q has lo > hi (%s > %s)", r.Shard, r.Lo, r.Hi)
		}
		if i == 0 {
			continue
		}
		prev := sorted[i-1]
		switch {
		case r.Lo <= prev.Hi:
			return "", "", fmt.Errorf("%w: %q[%s,%s] ∩ %q[%s,%s]",
				ErrSplitPlanOverlap, prev.Shard, prev.Lo, prev.Hi, r.Shard, r.Lo, r.Hi)
		case !adjacent(prev.Hi, r.Lo):
			return "", "", fmt.Errorf("%w: after %q.hi=%s before %q.lo=%s",
				ErrSplitPlanGap, prev.Shard, prev.Hi, r.Shard, r.Lo)
		}
	}
	return sorted[0].Lo, sorted[len(sorted)-1].Hi, nil
}

// adjacent 는 hi 와 그 다음 range 의 lo 가 인접한지(공백 없음) 본다. hex 문자열 폭이
// 고정(0-padded)이라는 전제에서, lo 가 hi 의 lexical successor 이면 인접이다. 단순
// PoC 구현: 동일 길이 hex 의 마지막 nibble 까지 비교하는 대신, "lo == hi 다음 정수"를
// big-int 없이 근사 — 폭 고정이므로 "hi < lo 이고 그 사이 키 부재"를 호출자 컨벤션
// (인접 range 는 hi+1 == lo)으로 가정하고, 여기서는 hi < lo 만 확인한다(gap 의 음성
// 경계는 ValidateSplitPlan coverage 가 추가로 잡는다).
func adjacent(hi, lo string) bool {
	// 폭 고정 hex 컨벤션에서 인접 range 는 lo 가 hi 보다 정확히 1 큼. 본 PoC 는
	// 문자열 successor 계산을 hexSuccessor 로 수행한다.
	return hexSuccessor(hi) == lo
}

// hexSuccessor 는 "0x"-prefixed 고정폭 hex 문자열의 +1 을 같은 폭으로 반환한다.
// overflow(전부 f) 시 입력을 그대로 반환(최댓값 — 다음 range 없음 전제).
func hexSuccessor(s string) string {
	const prefix = "0x"
	body := s
	hasPrefix := len(s) >= 2 && s[:2] == prefix
	if hasPrefix {
		body = s[2:]
	}
	out := []byte(body)
	for i := len(out) - 1; i >= 0; i-- {
		switch {
		case out[i] >= '0' && out[i] < '9', out[i] >= 'a' && out[i] < 'f':
			out[i]++
			return restore(hasPrefix, string(out))
		case out[i] == '9':
			out[i] = 'a'
			return restore(hasPrefix, string(out))
		case out[i] == 'f':
			out[i] = '0' // carry
		default:
			return s // 비표준 문자 — 그대로
		}
	}
	return s // 전부 f overflow
}

func restore(hasPrefix bool, body string) string {
	if hasPrefix {
		return "0x" + body
	}
	return body
}
