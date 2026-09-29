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
)

// 오퍼레이터가 남기는 신호를 읽는 순수 해석 — 드라이버가 이 결과로 판정 재료를 채운다.

var (
	// behindPattern 은 SwitchoverTargetBehind 메시지의 수신 최대 파드("... furthest ahead (<pod> at <lsn>)").
	behindPattern = regexp.MustCompile(`furthest ahead \((\S+) at `)

	// heldPattern 은 MemberHeld 메시지의 재구축 사유("shard <s>: <pod> held for rebuild (<reason>)").
	heldPattern = regexp.MustCompile(`held for rebuild \((\w+)\)`)
)

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
