/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 데이터·보안·비상 판정 — J16 data-bearing · J17 hba · J18 제자리 switchover · J19 비상 정지 · S0 전환(설계 §3.5·§3.16·§3.18·§3.19).

// EmergencyOperators 는 S10 이 세워야 할 오퍼레이터 파드 수 — D1s 2복제(설계 §3.11).
const EmergencyOperators = 2

// ConninfoLineCap 은 S0 뒤 멤버 auto.conf 의 primary_conninfo 줄 상한(설계 §3.12 — 바꿔 쓰기).
const ConninfoLineCap = 2

// logConnectionsOff 는 log_connections 가 꺼진 값 — 사전 누적물 양성 대조가 이것이면 붉다.
const logConnectionsOff = "off"

// AutoConfFile 은 ALTER SYSTEM 이 쓰는 파일 — 렌더 키의 sourcefile 이면 렌더 conf 가 진 것이다(설계 §3.17).
const AutoConfFile = "postgresql.auto.conf"

// lsnWordBits 는 LSN "상위/하위" 16진 두 낱말의 하위 비트 수.
const lsnWordBits = 32

// VAPOutcome 은 data-bearing 스냅숏을 오퍼레이터 SA 로 지워 본(server dry-run) 결과다.
type VAPOutcome string

const (
	VAPDenied    VAPOutcome = "denied"    // ValidatingAdmissionPolicy 가 거부 — 보호가 선다
	VAPAllowed   VAPOutcome = "allowed"   // 허용 — 오퍼레이터가 data-bearing 스냅숏을 지울 수 있다
	VAPForbidden VAPOutcome = "rbac"      // RBAC 거부 — 정책까지 가지 못해 시험이 안 됐다
	VAPNoTarget  VAPOutcome = "no-target" // data-bearing 스냅숏 없음
	VAPUnknown   VAPOutcome = "unknown"
)

// ConnOutcome 은 접속 시도의 결과다 — 인증 거부와 망 실패를 가른다(망이 막혀 거부된 것을 hba 초록으로 읽지 않게).
type ConnOutcome string

const (
	ConnAccepted     ConnOutcome = "accepted"      // 접속 성공
	ConnAuthRejected ConnOutcome = "auth-rejected" // 서버가 인증에서 거부 — 망은 닿았다
	ConnUnreachable  ConnOutcome = "unreachable"   // 망·DNS·시한 — 인증까지 못 갔다
	ConnUnknown      ConnOutcome = "unknown"
)

// 접속 오류 글(libpq·psql) — 소문자로 대조한다.
var (
	authRejections = []string{
		"password authentication failed", "no password supplied", "fe_sendauth",
		"no pg_hba.conf entry", "pg_hba.conf rejects",
	}
	unreachables = []string{
		"could not connect", "connection refused", "timeout expired", "could not translate host name",
		"no route to host", "connection timed out", "network is unreachable",
	}
)

// HBAMember 는 멤버 하나의 적재 기준 hba 판정(SEC1 hbaJudgmentQuery 와 같은 식).
type HBAMember struct {
	Pod    string `json:"pod"`
	Fresh  bool   `json:"fresh"`  // 적재 시각(초) > hba 파일 수정 시각
	Trust  int    `json:"trust"`  // 비-local trust 줄 수(−1 = 모름)
	Errors int    `json:"errors"` // pg_hba_file_rules 오류 줄 수(−1 = 모름)
}

// HBARecord 는 J17 재료 — 사전 trust 양성 대조와 창 끝 판정.
type HBARecord struct {
	PreTrust   ConnOutcome `json:"preTrust,omitempty"`   // 사전 상태 비멤버의 비밀번호 없는 superuser(S0 before)
	Members    []HBAMember `json:"members,omitempty"`    // 창 끝 멤버별 판정
	NoPassword ConnOutcome `json:"noPassword,omitempty"` // 창 끝 비멤버 psql -w -U postgres
	Probe      ConnOutcome `json:"probe,omitempty"`      // 창 끝 탐침 역할(비밀번호)
	Superuser  ConnOutcome `json:"superuser,omitempty"`  // 창 끝 Secret 비밀번호 superuser
}

// SwitchoverRecord 는 제자리 switchover 한 번의 기록(J18).
type SwitchoverRecord struct {
	OldPrimary     string `json:"oldPrimary,omitempty"` // 멈춘 옛 primary(상태 애노테이션 oldPod)
	OldUID         string `json:"oldUID,omitempty"`     // 그 파드 UID(상태 애노테이션)
	OldRestarts    int32  `json:"oldRestarts"`          // 정지 직전 postgres 컨테이너 재시작 수(상태 애노테이션)
	BackUID        string `json:"backUID,omitempty"`    // 복귀를 본 파드 UID
	BackRestarts   int32  `json:"backRestarts"`         // 복귀 때 재시작 수
	BackInRecovery bool   `json:"backInRecovery"`       // 복귀 때 in recovery
	OldEnd         string `json:"oldEnd,omitempty"`     // 복귀 뒤 첫 checkpoint_lsn — 정지 체크포인트
	Fork           string `json:"fork,omitempty"`       // 새 타임라인 분기점(CR shard-runtime promotionLSN)
	RewindLines    int    `json:"rewindLines"`          // 옛 primary 로그의 되감기 줄
	Completed      bool   `json:"completed"`            // SwitchoverCompleted 관측
}

// EmergencyRecord 는 S10 비상 정지 기록(J19).
type EmergencyRecord struct {
	StoppingAt  time.Time `json:"stoppingAt,omitzero"`  // switchover-state Stopping 관측
	Operators   int       `json:"operators"`            // 그때 오퍼레이터 파드 수
	Frozen      int       `json:"frozen"`               // SIGSTOP 한 오퍼레이터 프로세스 수
	HoldExit    int       `json:"holdExit"`             // pg-stop hold --apply 종료 코드
	RecoverExit int       `json:"recoverExit"`          // pg-stop recover-primary --apply 종료 코드
	RecoverAt   time.Time `json:"recoverAt,omitzero"`   // recover-primary 시작
	EndpointsAt time.Time `json:"endpointsAt,omitzero"` // 그 뒤 -rw Endpoints 가 정확히 1 인 첫 시각
	WriteAt     time.Time `json:"writeAt,omitzero"`     // 그 뒤 첫 ack
}

// MemberSettings 는 멤버 하나의 운영 누적물 정리 상태(S0).
type MemberSettings struct {
	Pod            string `json:"pod"`
	Primary        bool   `json:"primary"`
	WalKeepSource  string `json:"walKeepSource"`  // pg_settings wal_keep_size sourcefile
	LogConnections string `json:"logConnections"` // SHOW log_connections
	ConninfoLines  int    `json:"conninfoLines"`  // auto.conf primary_conninfo 줄 수(−1 = 모름)
	Marker         bool   `json:"marker"`         // promoted 마커 파일
}

// J16 — data-bearing 스냅숏은 사라지지 않는다(GC·상한 무관) ∧ (S6b) 이 창의 Demoted 는 true · Stale 은 false ·
// 오퍼레이터 SA 삭제를 VAP 가 거부. 앞 시나리오의 스냅숏은 보존만 본다(S6 의 Stale 은 분기점을 넘어 true 일 수 있다).
func judgeDataBearing(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckDataBearing}
	present := map[string]bool{}
	for _, s := range after.Snapshots {
		present[s.Name] = !s.Gone
	}
	kept := 0
	for _, s := range slices.Concat(before.Snapshots, after.Snapshots) {
		if s.DataBearing != LabelTrue {
			continue
		}
		if !present[s.Name] {
			check.Detail = "data-bearing 스냅숏 " + s.Name + " 사라짐"
			return check
		}
		kept++
	}
	if !exp.DetachedData {
		check.Pass = true
		check.Detail = fmt.Sprintf("data-bearing 표본 %d 보존", kept)
		return check
	}

	// S6b — lead 모름 Demoted 는 true, 끊긴 쪽 Stale 은 false, 오퍼레이터 삭제는 VAP 가 거부
	demoted := 0
	for _, s := range fresh(before, after) {
		switch {
		case s.Reason == RebuildDemoted && s.DataBearing != LabelTrue:
			check.Detail = fmt.Sprintf("Demoted %s data-bearing=%q(lead 모름 = true)", s.Name, s.DataBearing)
			return check
		case s.Reason == RebuildStale && s.DataBearing != LabelFalse:
			check.Detail = fmt.Sprintf("Stale %s data-bearing=%q(끊긴 쪽 = false)", s.Name, s.DataBearing)
			return check
		}
		if s.Reason == RebuildDemoted {
			demoted++
		}
	}
	if demoted == 0 {
		check.Detail = "옛 primary Demoted 스냅숏 미관측"
		return check
	}
	if after.VAP != VAPDenied {
		check.Detail = fmt.Sprintf("오퍼레이터 SA 의 data-bearing 삭제 %q(기대 %s)", after.VAP, VAPDenied)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("data-bearing 보존 · Demoted %d = true · VAP 거부", demoted)
	return check
}

// fresh 는 이 창에서 새로 본 스냅숏이다(before 에 없던 이름).
func fresh(before, after ClusterSnap) []SnapshotMark {
	old := map[string]bool{}
	for _, s := range before.Snapshots {
		old[s.Name] = true
	}
	var out []SnapshotMark
	for _, s := range after.Snapshots {
		if !old[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// J17 — (S0) 사전 trust 접속 성공(양성 대조) ∧ 전 멤버 적재 기준 network trust 0 · 오류 0 ∧
// 비멤버 비밀번호 없는 superuser 는 인증 거부 ∧ 탐침·Secret 접속 성공.
func judgeHBA(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckHBA}
	if exp.TrustControl && before.HBA.PreTrust != ConnAccepted {
		check.Detail = fmt.Sprintf("사전 비멤버 trust 접속 %q(양성 대조 실패)", before.HBA.PreTrust)
		return check
	}
	h := after.HBA
	if len(h.Members) != exp.Members {
		check.Detail = fmt.Sprintf("hba 판정 멤버 %d(기대 %d)", len(h.Members), exp.Members)
		return check
	}
	for _, m := range h.Members {
		switch {
		case !m.Fresh:
			check.Detail = m.Pod + " hba 적재가 파일보다 이르다"
			return check
		case m.Trust != 0:
			check.Detail = fmt.Sprintf("%s network trust %d줄", m.Pod, m.Trust)
			return check
		case m.Errors != 0:
			check.Detail = fmt.Sprintf("%s hba 오류 %d줄", m.Pod, m.Errors)
			return check
		}
	}
	checks := []struct {
		what string
		got  ConnOutcome
		want ConnOutcome
	}{
		{"비밀번호 없는 비멤버 superuser", h.NoPassword, ConnAuthRejected},
		{"탐침 역할", h.Probe, ConnAccepted},
		{"Secret superuser", h.Superuser, ConnAccepted},
	}
	for _, c := range checks {
		if c.got != c.want {
			check.Detail = fmt.Sprintf("%s 접속 %q(기대 %s)", c.what, c.got, c.want)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("멤버 %d 적재 기준 network trust 0 · 비밀번호 없는 superuser 거부", len(h.Members))
	return check
}

// J18 — 옛 primary 가 같은 파드로 한 번 재시작해 in recovery ∧ 분기점 > 옛 primary 정지 체크포인트 ∧ 되감기 0 ∧
// 그 멤버의 새 스냅숏·재구축 0.
func judgeInPlace(before, after ClusterSnap, _ Expect) Check {
	check := Check{ID: CheckInPlace}
	s := after.Switchover
	switch {
	case s.OldPrimary == "":
		check.Detail = "switchover 미관측"
		return check
	case !s.Completed:
		check.Detail = "SwitchoverCompleted 미관측"
		return check
	case s.BackUID == "":
		check.Detail = "옛 primary 복귀 미관측"
		return check
	case s.BackUID != s.OldUID:
		check.Detail = "옛 primary 파드가 바뀜(삭제·재생성)"
		return check
	case s.BackRestarts != s.OldRestarts+1:
		check.Detail = fmt.Sprintf("옛 primary 재시작 %d→%d(기대 +1)", s.OldRestarts, s.BackRestarts)
		return check
	case !s.BackInRecovery:
		check.Detail = "옛 primary 가 standby 로 돌아오지 않음"
		return check
	case s.RewindLines != 0:
		check.Detail = fmt.Sprintf("옛 primary 되감기 %d줄", s.RewindLines)
		return check
	}
	end, errEnd := ParseLSN(s.OldEnd)
	fork, errFork := ParseLSN(s.Fork)
	if errEnd != nil || errFork != nil {
		check.Detail = fmt.Sprintf("LSN 미관측(옛 primary 끝 %q · 분기점 %q)", s.OldEnd, s.Fork)
		return check
	}
	if fork <= end {
		check.Detail = fmt.Sprintf("분기점 %s ≤ 옛 primary 정지 체크포인트 %s — 승자가 끝을 다 받지 못했다", s.Fork, s.OldEnd)
		return check
	}
	if touched(before, after, s.OldPrimary) {
		check.Detail = "옛 primary " + s.OldPrimary + " 스냅숏·재구축"
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("%s 제자리 재시작 1 · 분기점 %s > 끝 %s · 되감기 0", s.OldPrimary, s.Fork, s.OldEnd)
	return check
}

// touched 는 창 안에 그 멤버의 새 스냅숏이나 재구축이 있었나.
func touched(before, after ClusterSnap, member string) bool {
	for _, s := range fresh(before, after) {
		if s.Member == member {
			return true
		}
	}
	for _, r := range after.Rebuilds {
		if r.Member == member {
			return true
		}
	}
	return false
}

// J19 — Stopping 에서 오퍼레이터 전부(EmergencyOperators)를 세웠다 ∧ pg-stop hold·recover-primary 성공 ∧
// recover 뒤 switchoverBudget 안 -rw Endpoints 1 · 쓰기 성공 ∧ primary 둘 0 ∧ PVC UID 불변.
func judgeEmergency(before, after ClusterSnap, _ Expect) Check {
	check := Check{ID: CheckEmergency}
	e := after.Emergency
	deadline := e.RecoverAt.Add(SwitchoverBudget)
	switch {
	case e.StoppingAt.IsZero():
		check.Detail = "switchover Stopping 미관측"
	case e.Operators < EmergencyOperators || e.Frozen != e.Operators:
		check.Detail = fmt.Sprintf("오퍼레이터 %d 중 %d 세움(기대 %d 전부)", e.Operators, e.Frozen, EmergencyOperators)
	case e.HoldExit != 0 || e.RecoverExit != 0:
		check.Detail = fmt.Sprintf("pg-stop hold %d · recover-primary %d", e.HoldExit, e.RecoverExit)
	case e.RecoverAt.IsZero():
		check.Detail = "recover-primary 시각 미관측"
	case e.EndpointsAt.IsZero() || e.EndpointsAt.After(deadline):
		check.Detail = fmt.Sprintf("-rw Endpoints 1 이 %s 안에 안 섰다(%s)", SwitchoverBudget, stamp(e.EndpointsAt))
	case e.WriteAt.IsZero() || e.WriteAt.After(deadline):
		check.Detail = fmt.Sprintf("쓰기 성공이 %s 안에 없다(%s)", SwitchoverBudget, stamp(e.WriteAt))
	case after.MaxPrimaries > 1:
		check.Detail = fmt.Sprintf("primary %d개 동시 관측", after.MaxPrimaries)
	case !maps.Equal(before.PVCUIDs, after.PVCUIDs):
		check.Detail = "비상 정지 중 PVC UID 변화"
	default:
		check.Pass = true
		check.Detail = fmt.Sprintf("recover 뒤 Endpoints 1 %s · 쓰기 %s", e.EndpointsAt.Sub(e.RecoverAt).Round(time.Second), e.WriteAt.Sub(e.RecoverAt).Round(time.Second))
	}
	return check
}

// S0 — C1 확인 ∧ 전 파드 새 revision Ready ∧ 옛 오퍼레이터 pod 삭제 0 ∧ 계획 밖 Rogue·FencedPrimary 재구축 0 ∧
// 운영 누적물 정리(wal_keep_size 출처 = 렌더 conf · 복제본 마커 0 · log_connections 유지 · primary_conninfo ≤ 2줄).
// 타임라인 +1(switchover)은 J3 이 본다.
func judgeTransition(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckTransition}
	switch {
	case len(after.C1Faults) > 0:
		check.Detail = "C1 확인 실패: " + strings.Join(after.C1Faults, " · ")
		return check
	case after.PodsOnUpdate != exp.Members:
		check.Detail = fmt.Sprintf("새 revision Ready 파드 %d(기대 %d)", after.PodsOnUpdate, exp.Members)
		return check
	case after.AuditPodDeletes != 0:
		check.Detail = fmt.Sprintf("옛 오퍼레이터의 pod 삭제 %d건", after.AuditPodDeletes)
		return check
	}
	if n := after.HeldReasons[RebuildRogue] + after.HeldReasons[RebuildFencedPrimary]; n > 0 {
		check.Detail = fmt.Sprintf("계획 밖 Rogue·FencedPrimary 재구축 %d", n)
		return check
	}
	if detail := settingsFault(before.Settings, after.Settings, exp.Members); detail != "" {
		check.Detail = detail
		return check
	}
	check.Pass = true
	check.Detail = "전 파드 새 revision · 운영 누적물 정리 · 옛 오퍼레이터 삭제 0"
	return check
}

// settingsFault 는 운영 누적물 정리의 첫 결함 — 사전 log_connections 는 양성 대조(켜 둔 값)여야 한다.
func settingsFault(pre, post []MemberSettings, members int) string {
	logged := ""
	for _, m := range pre {
		if m.Primary {
			logged = m.LogConnections
		}
	}
	if logged == "" || logged == logConnectionsOff {
		return fmt.Sprintf("사전 log_connections %q(양성 대조 실패)", logged)
	}
	if len(post) != members {
		return fmt.Sprintf("설정 표본 멤버 %d(기대 %d)", len(post), members)
	}
	for _, m := range post {
		switch {
		case m.WalKeepSource == "" || strings.HasSuffix(m.WalKeepSource, AutoConfFile):
			return fmt.Sprintf("%s wal_keep_size 출처 %q(렌더 conf 아님)", m.Pod, m.WalKeepSource)
		case !m.Primary && m.Marker:
			return "복제본 " + m.Pod + " promoted 마커"
		case m.LogConnections != logged:
			return fmt.Sprintf("%s log_connections %q(사전 %q)", m.Pod, m.LogConnections, logged)
		case m.ConninfoLines < 0 || m.ConninfoLines > ConninfoLineCap:
			return fmt.Sprintf("%s primary_conninfo %d줄(상한 %d)", m.Pod, m.ConninfoLines, ConninfoLineCap)
		}
	}
	return ""
}

// ClassifyConn 은 psql 접속 결과를 가른다 — 오류가 없으면 성공, 인증 거부 글이면 거부, 망 글이면 닿지 못함.
func ClassifyConn(out string, err error) ConnOutcome {
	if err == nil {
		return ConnAccepted
	}
	text := strings.ToLower(out + " " + err.Error())
	if containsAny(text, authRejections) {
		return ConnAuthRejected
	}
	if containsAny(text, unreachables) {
		return ConnUnreachable
	}
	return ConnUnknown
}

func containsAny(text string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// ParseLSN 은 PostgreSQL LSN "상위/하위"(16진)를 바이트 위치로 읽는다.
func ParseLSN(lsn string) (uint64, error) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(lsn), "/")
	if !ok {
		return 0, errors.New("LSN 형식 아님: " + lsn)
	}
	h, err := strconv.ParseUint(hi, 16, lsnWordBits)
	if err != nil {
		return 0, err
	}
	l, err := strconv.ParseUint(lo, 16, lsnWordBits)
	if err != nil {
		return 0, err
	}
	return h<<lsnWordBits | l, nil
}
