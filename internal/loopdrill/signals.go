/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"time"
)

// 오퍼레이터가 남기는 신호를 읽는 순수 해석 — 드라이버가 이 결과로 판정 재료를 채운다.

var (
	// behindPattern 은 SwitchoverTargetBehind 메시지의 수신 최대 파드("... furthest ahead (<pod> at <lsn>)").
	behindPattern = regexp.MustCompile(`furthest ahead \((\S+) at `)

	// heldPattern 은 MemberHeld 메시지의 재구축 사유("shard <s>: <pod> held for rebuild (<reason>)").
	heldPattern = regexp.MustCompile(`held for rebuild \((\w+)\)`)

	// readyPattern 은 MemberSnapshotReady 메시지의 스냅숏("... snapshot <name> ready (data-bearing=…)").
	readyPattern = regexp.MustCompile(`snapshot (\S+) ready `)
)

// EventSnapshotReady 는 오퍼레이터가 readyToUse 를 직접 읽은 순간 내는 이벤트 사유(member_rebuild.go).
const EventSnapshotReady = "MemberSnapshotReady"

// VAP 거부와 RBAC 거부의 API 오류 글.
const (
	vapDeniedText   = "validatingadmissionpolicy"
	rbacDeniedText  = "cannot delete"
	notFoundText    = "notfound"
	notFoundAltText = "not found"
)

// FreshestStandby 는 수신 위치가 가장 앞선 standby — 동률은 낮은 서수(오퍼레이터 switchover 대상 규칙).
// positions 는 파드 → walLsnBytes + lagBytes(instance-status 애노테이션). 비었으면 "".
func FreshestStandby(positions map[string]int64) string {
	pods := make([]string, 0, len(positions))
	for pod := range positions {
		pods = append(pods, pod)
	}
	if len(pods) == 0 {
		return ""
	}
	return slices.MaxFunc(pods, func(a, b string) int {
		if c := cmp.Compare(positions[a], positions[b]); c != 0 {
			return c
		}
		return cmp.Compare(podOrdinal(b), podOrdinal(a))
	})
}

// BehindTarget 은 SwitchoverTargetBehind 메시지가 가리킨 수신 최대 파드다(없으면 "").
func BehindTarget(message string) string {
	m := behindPattern.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return m[1]
}

// HeldReason 은 MemberHeld 메시지의 재구축 사유다(없으면 "").
func HeldReason(message string) string {
	m := heldPattern.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return m[1]
}

// ClassifyVAP 는 오퍼레이터 SA 로 스냅숏을 server dry-run 삭제한 결과를 가른다.
func ClassifyVAP(out string, err error) VAPOutcome {
	if err == nil {
		return VAPAllowed
	}
	text := strings.ToLower(out + " " + err.Error())
	switch {
	case strings.Contains(text, vapDeniedText):
		return VAPDenied
	case strings.Contains(text, rbacDeniedText):
		return VAPForbidden
	case strings.Contains(text, notFoundText) || strings.Contains(text, notFoundAltText):
		return VAPNoTarget
	}
	return VAPUnknown
}

// ReadyFromEvents 는 스냅숏 표본의 ReadyAt 을 오퍼레이터의 MemberSnapshotReady 시각으로 당긴다.
// 오퍼레이터는 readyToUse 를 읽은 같은 reconcile 에서 해제하므로 recorder 의 느린 표본으로는 순서를 못 가른다.
// recorder 가 readyToUse 를 직접 본 적 없는 표본(ReadyAt 0)은 이벤트만으로 당기지 않는다.
func ReadyFromEvents(marks []SnapshotMark, events []EventMark) []SnapshotMark {
	// 스냅숏마다 오퍼레이터가 ready 를 읽은 첫 시각
	read := map[string]time.Time{}
	for _, e := range events {
		m := readyPattern.FindStringSubmatch(e.Message)
		if e.Reason != EventSnapshotReady || m == nil {
			continue
		}
		if at, seen := read[m[1]]; !seen || e.At.Before(at) {
			read[m[1]] = e.At
		}
	}

	out := slices.Clone(marks)
	for i, mark := range out {
		at, seen := read[mark.Name]
		if mark.ReadyAt.IsZero() || !seen || !at.Before(mark.ReadyAt) {
			continue
		}
		out[i].ReadyAt = at
	}
	return out
}

// CountMark 는 셈이 바뀐 시각이다 — 표본은 바뀔 때만 쌓여 값은 다음 표본까지 유지된다.
type CountMark struct {
	At    time.Time
	Count int
}

// FirstCountAt 은 from 이후 셈이 want 인 첫 시각이다(없으면 zero).
// from 에 이미 want 로 서 있으면 from — 바뀔 때만 쌓으니 from 뒤 표본이 없을 수 있다.
// 예: [{0s 1}] · from 57s → 57s (V1 5판 S10 -rw Endpoints).
func FirstCountAt(marks []CountMark, from time.Time, want int) time.Time {
	// from 에 서 있던 값 — from 이하 마지막 표본
	current, seen := 0, false
	for _, m := range marks {
		if m.At.After(from) {
			break
		}
		current, seen = m.Count, true
	}
	if seen && current == want {
		return from
	}

	for _, m := range marks {
		if m.At.After(from) && m.Count == want {
			return m.At
		}
	}
	return time.Time{}
}
