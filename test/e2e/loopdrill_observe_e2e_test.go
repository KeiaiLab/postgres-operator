//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// 관측 층 — 클러스터를 읽어 판정 재료(ClusterSnap)를 만든다.
// 쓰는 것은 표식·하트비트 행뿐이다(J8 · J4 의 status 변화 재료). 쓰기 탐침은 testdata/write-probe.yaml 이 한다.

const (
	dataNamespace  = "data"
	clusterCR      = "postgres-prod"
	shardName      = "shard-0"
	shardSTS       = "postgres-prod-shard-0"
	pgContainer    = "postgres"
	memberSelector = "app.kubernetes.io/instance=postgres-prod,app.kubernetes.io/component=shard"
	revisionLabel  = "controller-revision-hash"
	snapshotLabel  = "postgres.keiailab.io/member-snapshot=true"
	rebuildInfix   = "-rebuild-"
	auditLogPath   = "/var/log/kubernetes/audit.log"
	failedWatch    = "Failed to watch"

	// 오퍼레이터 계약(internal/controller) — 애노테이션·라벨·조건·이벤트 이름.
	instanceStatusKey   = "postgres.keiailab.io/instance-status"
	switchoverStateKey  = "postgres.keiailab.io/switchover-state"
	switchoverTargetKey = "postgres.keiailab.io/switchover-target"
	shardRuntimeKey     = "postgres.keiailab.io/shard-runtime"
	rebuildReasonLabel  = "postgres.keiailab.io/rebuild-reason"
	dataBearingLabel    = "postgres.keiailab.io/data-bearing"
	rolloutBlockedType  = "RolloutBlocked"
	switchoverStarted   = "SwitchoverStarted"
	switchoverCompleted = "SwitchoverCompleted"
	switchoverBehind    = "SwitchoverTargetBehind"
	memberHeld          = "MemberHeld"
	phaseStopping       = "Stopping"
	promotedMarker      = ".keiailab-promoted-primary"
	serviceNameLabel    = "kubernetes.io/service-name"
	stsControllerUser   = "system:serviceaccount:kube-system:statefulset-controller"

	// 하트비트 한 번 = 1KiB 행 64개 — pg_database_size 가 커져 CR status(sizeBytes)가 바뀐다.
	heartbeatPayloadBytes = 1024
	heartbeatRows         = 64

	blockedCondition = "MemberRebuildBlocked"
	blockedReason    = "BudgetExhausted"
	gaugeSuffix      = "status_write_timestamp_seconds"
	deniedSuffix     = "api_read_denied_total"
	metricsPort      = "8443"
	metricsTokenTTL  = 6 * time.Hour
	// metricsTokenRefresh 는 토큰을 다시 받는 나이 — GREEN 전체(13 시나리오)가 TTL 을 넘길 수 있다.
	metricsTokenRefresh = metricsTokenTTL / 2

	// 쓰기 탐침 접속 — 탐침 파드(비멤버)에서 -rw Service 로(J17).
	probeConnect = "host=" + rwService + "." + dataNamespace + ".svc port=5432 dbname=postgres connect_timeout=5"
)

// 풀기 이벤트(설계 §3.9) — 재구축이 파괴 단계에 들어갔다는 신호.
var releaseReasons = []string{"MemberRejoinReleased", "MemberReseedRequested"}

// 되감기 흔적 — 인스턴스 매니저(기점 c7608427)가 pg_rewind 로 옛 primary 를 standby 로 만들 때 남기는 글(J18).
var rewindMarkers = []string{"Restarted former primary prepared as standby", "pg_rewind"}

var memberPattern = regexp.MustCompile(clusterCR + `-shard-\d+-\d+`)

const (
	sqlRole = "SELECT CASE WHEN pg_is_in_recovery() THEN 'standby' ELSE 'primary' END, " +
		"CASE WHEN pg_is_in_recovery() THEN '' ELSE substr(pg_walfile_name(pg_current_wal_lsn()), 1, 8) END"
	sqlGraph  = "SELECT application_name, state FROM pg_stat_replication"
	sqlSchema = "CREATE SCHEMA IF NOT EXISTS loopdrill; " +
		"CREATE TABLE IF NOT EXISTS loopdrill.marker(id bigserial PRIMARY KEY, scenario text NOT NULL, at timestamptz NOT NULL DEFAULT now()); " +
		"CREATE TABLE IF NOT EXISTS loopdrill.heartbeat(id bigserial PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(), payload text NOT NULL)"
	// sqlHBA 는 SEC1 hbaJudgmentQuery 와 같은 식 — 적재 시각(초) > 파일 시각 · 비-local trust · 오류.
	sqlHBA = "SELECT date_trunc('second', pg_conf_load_time()) > " +
		"(pg_stat_file(current_setting('hba_file'))).modification, " +
		"count(*) FILTER (WHERE type <> 'local' AND auth_method = 'trust'), " +
		"count(*) FILTER (WHERE error IS NOT NULL) FROM pg_hba_file_rules"
	// sqlSettings 는 S0 운영 누적물 — wal_keep_size 출처 · log_connections · recovery · 마커 · auto.conf primary_conninfo 줄 수.
	sqlSettings = "SELECT coalesce((SELECT sourcefile FROM pg_settings WHERE name = 'wal_keep_size'), ''), " +
		"current_setting('log_connections'), pg_is_in_recovery(), " +
		"(pg_stat_file('" + promotedMarker + "', true)).size IS NOT NULL, " +
		"(SELECT count(*) FROM regexp_split_to_table(pg_read_file('postgresql.auto.conf'), E'\\n') AS l " +
		"WHERE l ~ '^\\s*primary_conninfo\\s*=')"
	sqlCheckpoint = "SELECT pg_is_in_recovery(), checkpoint_lsn FROM pg_control_checkpoint()"
	roleStandby   = "standby"
	rolePrimary   = "primary"
	pgTrue        = "t"
)

func sqlHeartbeat() string {
	return fmt.Sprintf("INSERT INTO loopdrill.heartbeat(payload) SELECT repeat('x', %d) FROM generate_series(1, %d)",
		heartbeatPayloadBytes, heartbeatRows)
}

// ── 멤버 관측 ──

// member 는 샤드 파드 하나.
type member struct {
	Pod         string
	UID         string
	Node        string
	ContainerID string
	Restarts    int32
	Ready       bool
	Terminating bool
	Created     time.Time
	Revision    string
	Position    int64             // instance-status walLsnBytes + lagBytes(-1 = 모름)
	Role        string            // primary | standby | ""(exec 실패)
	Timeline    int64             // primary 만
	Replication map[string]string // 이 멤버가 보내는 복제: application_name → state(연쇄 포함)
}

// observation 은 한 표본 — STS 와 멤버.
type observation struct {
	At                 time.Time
	Generation         int64
	ObservedGeneration int64
	UpdateRevision     string
	UpdateStrategy     string
	Replicas           int32
	ReadyReplicas      int32
	Members            []member
}

type stsJSON struct {
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas       int32 `json:"replicas"`
		UpdateStrategy struct {
			Type string `json:"type"`
		} `json:"updateStrategy"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64  `json:"observedGeneration"`
		ReadyReplicas      int32  `json:"readyReplicas"`
		UpdateRevision     string `json:"updateRevision"`
	} `json:"status"`
}

type podJSON struct {
	Metadata struct {
		Name              string            `json:"name"`
		UID               string            `json:"uid"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type containerStatus struct {
	Name         string `json:"name"`
	ContainerID  string `json:"containerID"`
	RestartCount int32  `json:"restartCount"`
	ImageID      string `json:"imageID"`
	State        struct {
		Running *struct{} `json:"running"`
	} `json:"state"`
}

func (p podJSON) ready() bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func (p podJSON) pgContainer() (id string, restarts int32, running bool) {
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == pgContainer {
			return c.ContainerID, c.RestartCount, c.State.Running != nil
		}
	}
	return "", 0, false
}

// position 은 instance-status 애노테이션의 수신 위치(walLsnBytes + lagBytes) — 오퍼레이터 switchover 대상 규칙과 같은 식.
func (p podJSON) position() int64 {
	var st struct {
		WALLSNBytes int64 `json:"walLsnBytes"`
		LagBytes    int64 `json:"lagBytes"`
	}
	if json.Unmarshal([]byte(p.Metadata.Annotations[instanceStatusKey]), &st) != nil {
		return -1
	}
	if st.WALLSNBytes <= 0 || st.LagBytes < 0 {
		return -1
	}
	return st.WALLSNBytes + st.LagBytes
}

// probeCluster 는 STS·파드를 읽고 돌고 있는 멤버마다 역할·타임라인·복제를 묻는다.
func probeCluster() (observation, error) {
	obs := observation{At: time.Now()}
	var sts stsJSON
	if err := kubectlJSON(&sts, "get", "sts", shardSTS, "-n", dataNamespace); err != nil {
		return obs, err
	}
	obs.Generation = sts.Metadata.Generation
	obs.ObservedGeneration = sts.Status.ObservedGeneration
	obs.UpdateRevision = sts.Status.UpdateRevision
	obs.UpdateStrategy = sts.Spec.UpdateStrategy.Type
	obs.Replicas = sts.Spec.Replicas
	obs.ReadyReplicas = sts.Status.ReadyReplicas

	pods, err := memberPods()
	if err != nil {
		return obs, err
	}
	for _, p := range pods {
		m := member{
			Pod:         p.Metadata.Name,
			UID:         p.Metadata.UID,
			Node:        p.Spec.NodeName,
			Ready:       p.ready(),
			Terminating: p.Metadata.DeletionTimestamp != nil,
			Created:     p.Metadata.CreationTimestamp,
			Revision:    p.Metadata.Labels[revisionLabel],
			Position:    p.position(),
		}
		id, restarts, running := p.pgContainer()
		m.ContainerID, m.Restarts = id, restarts
		if running && !m.Terminating {
			describeMember(&m)
		}
		obs.Members = append(obs.Members, m)
	}
	slices.SortFunc(obs.Members, func(a, b member) int { return strings.Compare(a.Pod, b.Pod) })
	return obs, nil
}

func memberPods() ([]podJSON, error) {
	var pods struct {
		Items []podJSON `json:"items"`
	}
	err := kubectlJSON(&pods, "get", "pods", "-n", dataNamespace, "-l", memberSelector)
	return pods.Items, err
}

// describeMember 는 psql 한 번으로 역할·타임라인·복제(standby 의 연쇄 송신 포함)를 채운다(실패 = 모름).
func describeMember(m *member) {
	out, _ := psql(m.Pod, sqlRole, sqlGraph)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return
	}
	role, tl, ok := strings.Cut(strings.TrimSpace(lines[0]), "|")
	if !ok || (role != rolePrimary && role != roleStandby) {
		return
	}
	m.Role = role
	if n, err := strconv.ParseInt(tl, 16, 64); role == rolePrimary && err == nil {
		m.Timeline = n
	}
	m.Replication = map[string]string{}
	for _, line := range lines[1:] {
		name, state, found := strings.Cut(strings.TrimSpace(line), "|")
		if found && memberPattern.MatchString(name) {
			m.Replication[name] = state
		}
	}
}

// psql 은 멤버의 postgres 컨테이너에서 로컬 소켓으로 문장들을 돌린다.
func psql(pod string, statements ...string) (string, error) {
	args := []string{"exec", "-n", dataNamespace, pod, "-c", pgContainer, "--", "sh", "-c",
		`"$POSTGRES_BIN_DIR/psql" "$POSTGRES_LOCAL_DSN" -XAtq -F'|' -v ON_ERROR_STOP=1 "$@"`, "psql"}
	for _, s := range statements {
		args = append(args, "-c", s)
	}
	return kubectl(args...)
}

func (o observation) primaries() []member {
	var out []member
	for _, m := range o.Members {
		if m.Role == rolePrimary {
			out = append(out, m)
		}
	}
	return out
}

// primary 는 primary 가 정확히 하나일 때만 그것을 낸다(split-brain 은 없음으로 친다).
func (o observation) primary() (member, bool) {
	ps := o.primaries()
	if len(ps) != 1 {
		return member{}, false
	}
	return ps[0], true
}

func (o observation) standbys() []member {
	var out []member
	for _, m := range o.Members {
		if m.Role == roleStandby {
			out = append(out, m)
		}
	}
	return out
}

func (o observation) memberNamed(pod string) (member, bool) {
	i := slices.IndexFunc(o.Members, func(m member) bool { return m.Pod == pod })
	if i < 0 {
		return member{}, false
	}
	return o.Members[i], true
}

// edges 는 멤버마다의 복제 송신 — 송신 → 수신 → state.
func (o observation) edges() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, m := range o.Members {
		if len(m.Replication) > 0 {
			out[m.Pod] = m.Replication
		}
	}
	return out
}

// converged 는 롤아웃 끝(전 파드 해시 = updateRevision — OnDelete 는 currentRevision 이 안 움직인다) · 전 멤버 Ready ·
// primary 하나 · 나머지 전부 streaming(연쇄 포함).
func (o observation) converged(members int) bool {
	if o.Generation == 0 || o.ObservedGeneration != o.Generation {
		return false
	}
	if int(o.ReadyReplicas) != members || len(o.Members) != members {
		return false
	}
	g, ok := o.graphMark()
	if !ok {
		return false
	}
	for _, m := range o.Members {
		if !m.Ready || m.Terminating || m.Revision != o.UpdateRevision {
			return false
		}
		if m.Pod != g.Primary && !slices.Contains(g.Streaming, m.Pod) {
			return false
		}
	}
	return true
}

func (o observation) graphMark() (loopdrill.GraphMark, bool) {
	primary, ok := o.primary()
	if !ok {
		return loopdrill.GraphMark{}, false
	}
	connected, streaming := loopdrill.Reachable(primary.Pod, o.edges())
	return loopdrill.GraphMark{At: o.At, Primary: primary.Pod, Connected: connected, Streaming: streaming}, true
}

// positions 는 standby 의 수신 위치(instance-status) — switchover 대상 고르기(loopdrill.FreshestStandby).
func (o observation) positions() map[string]int64 {
	out := map[string]int64{}
	for _, m := range o.standbys() {
		if m.Position > 0 {
			out[m.Pod] = m.Position
		}
	}
	return out
}

// ── 표식·하트비트 행 ──

func ensureSchema(primary string) error {
	_, err := psql(primary, sqlSchema)
	return err
}

func insertMarker(primary string, scn loopdrill.Scenario) (int64, error) {
	out, err := psql(primary, fmt.Sprintf("INSERT INTO loopdrill.marker(scenario) VALUES ('%s') RETURNING id", scn))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(firstLine(out)), 10, 64)
}

// markerCount 는 그 멤버에서 표식 행이 보이는가.
func markerCount(pod string, id int64) (int, error) {
	out, err := psql(pod, fmt.Sprintf("SELECT count(*) FROM loopdrill.marker WHERE id = %d", id))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(firstLine(out)))
}

// confirmMarker 는 표식 행이 모든 standby 에 재생될 때까지 기다린다 — "유발 전 확인한 행".
func confirmMarker(standbys []member, id int64, deadline time.Time) error {
	for _, s := range standbys {
		for {
			if n, err := markerCount(s.Pod, id); err == nil && n == 1 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("표식 행 %d 이 %s 에 재생되지 않았다", id, s.Pod)
			}
			time.Sleep(loopdrill.StatusPollInterval)
		}
	}
	return nil
}

func heartbeat(primary string) error {
	_, err := psql(primary, sqlHeartbeat())
	return err
}

func firstLine(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return line
}

// ── 쓰기 탐침 · 접속 ──

// probeAcks 는 탐침 파드 로그(지금·이전 컨테이너)의 ack 이다.
func probeAcks() ([]loopdrill.ProbeAck, error) {
	out, err := kubectl("logs", "-n", dataNamespace, writeProbePod)
	if err != nil {
		return nil, err
	}
	if prev, err := kubectl("logs", "-n", dataNamespace, writeProbePod, "--previous"); err == nil {
		out = prev + "\n" + out
	}
	return loopdrill.ParseAcks(out), nil
}

// storedIDs 는 최종 primary 의 탐침 id 중 [lo, hi] 안에 있는 것(구간으로 접는다).
func storedIDs(primary string, lo, hi int64) ([]loopdrill.IDRange, error) {
	query := fmt.Sprintf("SELECT id FROM %s.acks WHERE id BETWEEN %d AND %d ORDER BY id", writeProbeRole, lo, hi)
	out, err := psql(primary, query)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for line := range strings.FieldsSeq(out) {
		if id, err := strconv.ParseInt(line, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return loopdrill.IDRanges(ids), nil
}

// connectAs 는 탐침 파드(비멤버)에서 -rw Service 로 접속해 본다 — 비밀번호는 stdin 으로(argv 0).
type connectMode string

const (
	connectNoPassword connectMode = "no-password" // 비밀번호 없는 superuser — J17 거부 · 사전 trust 양성 대조
	connectProbe      connectMode = "probe"       // 탐침 역할(파드 env PGPASSWORD)
	connectSuperuser  connectMode = "superuser"   // Secret 비밀번호 superuser(stdin)
)

func connectAs(mode connectMode, superuserPassword string) loopdrill.ConnOutcome {
	query := `"SELECT 1"`
	var out string
	var err error
	switch mode {
	case connectNoPassword:
		out, err = kubectl("exec", "-n", dataNamespace, writeProbePod, "--", "sh", "-c",
			`unset PGPASSWORD; exec psql -w "`+probeConnect+` user=`+superuserName+`" -XAtqc `+query)
	case connectProbe:
		out, err = kubectl("exec", "-n", dataNamespace, writeProbePod, "--", "sh", "-c",
			`exec psql -w "`+probeConnect+` user=`+writeProbeRole+`" -XAtqc `+query)
	case connectSuperuser:
		script := `IFS= read -r PGPASSWORD && export PGPASSWORD && ` +
			`exec psql -w "` + probeConnect + ` user=` + superuserName + `" -XAtqc ` + query
		args := []string{"exec", "-i", "-n", dataNamespace, writeProbePod, "--", "sh", "-c", script}
		out, err = kubectlIn(superuserPassword+"\n", args...)
	}
	return loopdrill.ClassifyConn(out, err)
}

// hbaJudgment 는 멤버 하나의 적재 기준 hba 판정이다(실패 = 모름 −1).
func hbaJudgment(pod string) loopdrill.HBAMember {
	unknown := loopdrill.HBAMember{Pod: pod, Trust: -1, Errors: -1}
	out, err := psql(pod, sqlHBA)
	if err != nil {
		return unknown
	}
	fields := strings.Split(firstLine(out), "|")
	if len(fields) != 3 {
		return unknown
	}
	trust, errTrust := strconv.Atoi(fields[1])
	errs, errErrs := strconv.Atoi(fields[2])
	if errTrust != nil || errErrs != nil {
		return unknown
	}
	return loopdrill.HBAMember{Pod: pod, Fresh: fields[0] == pgTrue, Trust: trust, Errors: errs}
}

// memberSettings 는 멤버 하나의 운영 누적물 정리 상태다(실패 = 줄 수 −1).
func memberSettings(pod string) loopdrill.MemberSettings {
	s := loopdrill.MemberSettings{Pod: pod, ConninfoLines: -1}
	out, err := psql(pod, sqlSettings)
	if err != nil {
		return s
	}
	fields := strings.Split(firstLine(out), "|")
	if len(fields) != 5 {
		return s
	}
	s.WalKeepSource, s.LogConnections = fields[0], fields[1]
	s.Primary, s.Marker = fields[2] != pgTrue, fields[3] == pgTrue
	if n, err := strconv.Atoi(fields[4]); err == nil {
		s.ConninfoLines = n
	}
	return s
}

// checkpointLSN 은 in recovery 인 멤버의 checkpoint_lsn — 제자리 재기동 직후면 정지 체크포인트다(J18).
func checkpointLSN(pod string) (string, bool) {
	out, err := psql(pod, sqlCheckpoint)
	if err != nil {
		return "", false
	}
	recovery, lsn, ok := strings.Cut(firstLine(out), "|")
	return lsn, ok && recovery == pgTrue
}

// rewindLines 는 since 이후 멤버 postgres 컨테이너 로그(지금·이전)의 되감기 흔적 줄 수(J18).
func rewindLines(pod string, since time.Time) int {
	sinceArg := "--since-time=" + since.UTC().Format(time.RFC3339)
	count := 0
	for _, extra := range [][]string{nil, {"--previous"}} {
		out, err := kubectl(append([]string{"logs", "-n", dataNamespace, pod, "-c", pgContainer, sinceArg}, extra...)...)
		if err != nil {
			continue
		}
		for _, marker := range rewindMarkers {
			count += strings.Count(out, marker)
		}
	}
	return count
}

// rwEndpoints 는 -rw Service 의 ready 주소 수(J19).
func rwEndpoints() (int, error) {
	var list struct {
		Items []struct {
			Endpoints []struct {
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	selector := serviceNameLabel + "=" + rwService
	if err := kubectlJSON(&list, "get", "endpointslices", "-n", dataNamespace, "-l", selector); err != nil {
		return 0, err
	}
	count := 0
	for _, slice := range list.Items {
		for _, e := range slice.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				count++
			}
		}
	}
	return count, nil
}

// ── CR · 스냅숏 · 이벤트 ──

type clusterJSON struct {
	Metadata struct {
		Annotations   map[string]string `json:"annotations"`
		ManagedFields []struct {
			Subresource string    `json:"subresource"`
			Time        time.Time `json:"time"`
		} `json:"managedFields"`
	} `json:"metadata"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

// clusterState 는 CR 한 번 읽기의 판정 재료.
type clusterState struct {
	lastWrite      time.Time
	budgetBlocked  bool
	rolloutBlocked bool
	switchover     switchoverJSON // 진행 중 switchover(없으면 Phase "")
	forkLSN        string         // shard-runtime promotionLSN
	annotations    map[string]string
}

// switchoverJSON 은 오퍼레이터 switchover-state 애노테이션(JSON v1)의 드라이버가 읽는 부분.
type switchoverJSON struct {
	OldPod      string    `json:"oldPod"`
	OldUID      string    `json:"oldUID"`
	OldRestarts int32     `json:"oldRestarts"`
	Target      string    `json:"target"`
	Phase       string    `json:"phase"`
	Since       time.Time `json:"since"`
}

// readCluster 는 CR status 마지막 쓰기 시각·조건·switchover 상태·분기점을 읽는다.
func readCluster() (clusterState, error) {
	var cr clusterJSON
	if err := kubectlJSON(&cr, "get", "postgrescluster", clusterCR, "-n", dataNamespace, "--show-managed-fields"); err != nil {
		return clusterState{}, err
	}
	st := clusterState{annotations: cr.Metadata.Annotations}
	for _, f := range cr.Metadata.ManagedFields {
		if f.Subresource == "status" && f.Time.After(st.lastWrite) {
			st.lastWrite = f.Time
		}
	}
	for _, c := range cr.Status.Conditions {
		switch {
		case c.Type == blockedCondition && c.Status == "True" && c.Reason == blockedReason:
			st.budgetBlocked = true
		case c.Type == rolloutBlockedType && c.Status == "True":
			st.rolloutBlocked = true
		}
	}
	_ = json.Unmarshal([]byte(cr.Metadata.Annotations[switchoverStateKey]), &st.switchover)
	var runtime struct {
		Shards map[string]struct {
			PromotionLSN string `json:"promotionLSN"`
		} `json:"shards"`
	}
	if json.Unmarshal([]byte(cr.Metadata.Annotations[shardRuntimeKey]), &runtime) == nil {
		st.forkLSN = runtime.Shards[shardName].PromotionLSN
	}
	return st, nil
}

type snapshotJSON struct {
	Metadata struct {
		Name              string            `json:"name"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
	} `json:"metadata"`
	Status *struct {
		ReadyToUse *bool `json:"readyToUse"`
	} `json:"status"`
}

func listSnapshots() ([]snapshotJSON, error) {
	var list struct {
		Items []snapshotJSON `json:"items"`
	}
	err := kubectlJSON(&list, "get", "volumesnapshots", "-n", dataNamespace, "-l", snapshotLabel)
	return list.Items, err
}

func (s snapshotJSON) ready() bool {
	return s.Status != nil && s.Status.ReadyToUse != nil && *s.Status.ReadyToUse
}

func snapshotMember(name string) string {
	member, _, _ := strings.Cut(name, rebuildInfix)
	return member
}

// snapshotMark 는 스냅숏 하나의 판정 표본(ReadyAt 은 recorder 가 처음 본 시각).
func snapshotMark(s snapshotJSON) loopdrill.SnapshotMark {
	return loopdrill.SnapshotMark{
		Name:        s.Metadata.Name,
		Member:      snapshotMember(s.Metadata.Name),
		Reason:      s.Metadata.Labels[rebuildReasonLabel],
		DataBearing: s.Metadata.Labels[dataBearingLabel],
	}
}

// vapProbe 는 data-bearing 스냅숏을 오퍼레이터 SA 로 server dry-run 삭제해 본다 — 정책이 없어도 지우지 않는다(J16).
func vapProbe(snapshot, serviceAccount string) loopdrill.VAPOutcome {
	if snapshot == "" {
		return loopdrill.VAPNoTarget
	}
	out, err := kubectl("delete", "volumesnapshot", snapshot, "-n", dataNamespace, "--dry-run=server",
		"--as=system:serviceaccount:"+dataNamespace+":"+serviceAccount)
	return loopdrill.ClassifyVAP(out, err)
}

type eventJSON struct {
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	EventTime      time.Time `json:"eventTime"`
	LastTimestamp  time.Time `json:"lastTimestamp"`
	FirstTimestamp time.Time `json:"firstTimestamp"`
	Metadata       struct {
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
}

func (e eventJSON) at() time.Time {
	for _, t := range []time.Time{e.EventTime, e.LastTimestamp, e.FirstTimestamp} {
		if !t.IsZero() {
			return t
		}
	}
	return e.Metadata.CreationTimestamp
}

func clusterEvents(since time.Time) ([]loopdrill.EventMark, error) {
	var list struct {
		Items []eventJSON `json:"items"`
	}
	selector := "involvedObject.kind=PostgresCluster,involvedObject.name=" + clusterCR
	if err := kubectlJSON(&list, "get", "events", "-n", dataNamespace, "--field-selector", selector); err != nil {
		return nil, err
	}
	var out []loopdrill.EventMark
	for _, e := range list.Items {
		if e.at().Before(since) {
			continue
		}
		out = append(out, loopdrill.EventMark{At: e.at(), Type: e.Type, Reason: e.Reason, Message: e.Message, Count: 1})
	}
	slices.SortFunc(out, func(a, b loopdrill.EventMark) int { return a.At.Compare(b.At) })
	return out, nil
}

// foldEvents 는 판정문 증거용으로 같은 이벤트를 접는다(재구축 셈은 접기 전 목록으로 한다).
func foldEvents(events []loopdrill.EventMark) []loopdrill.EventMark {
	var out []loopdrill.EventMark
	index := map[loopdrill.EventMark]int{}
	for _, e := range events {
		key := loopdrill.EventMark{Type: e.Type, Reason: e.Reason, Message: e.Message}
		if i, seen := index[key]; seen {
			out[i].Count++
			continue
		}
		index[key] = len(out)
		out = append(out, e)
	}
	return out
}

// firstEvent 는 reason 의 첫 이벤트 시각이다(없으면 0).
func firstEvent(events []loopdrill.EventMark, reason string) time.Time {
	for _, e := range events {
		if e.Reason == reason {
			return e.At
		}
	}
	return time.Time{}
}

// heldReasons 는 MemberHeld 이벤트를 재구축 사유로 센다(S0 — 계획 밖 Rogue·FencedPrimary 0).
func heldReasons(events []loopdrill.EventMark) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		if e.Reason != memberHeld {
			continue
		}
		if reason := loopdrill.HeldReason(e.Message); reason != "" {
			out[reason]++
		}
	}
	return out
}

// rebuildMarks 는 풀기 이벤트마다 멤버와 그 앞의 마지막 멤버 스냅숏을 잇는다.
func rebuildMarks(events []loopdrill.EventMark, snapshots []snapshotJSON) []loopdrill.RebuildMark {
	var out []loopdrill.RebuildMark
	for _, e := range events {
		if !slices.Contains(releaseReasons, e.Reason) {
			continue
		}
		mark := loopdrill.RebuildMark{Member: memberPattern.FindString(e.Message), Reason: e.Reason, ReleasedAt: e.At}
		var latest time.Time
		for _, s := range snapshots {
			if snapshotMember(s.Metadata.Name) != mark.Member || s.Metadata.CreationTimestamp.After(e.At) {
				continue
			}
			if s.Metadata.CreationTimestamp.After(latest) || mark.Snapshot == "" {
				latest, mark.Snapshot = s.Metadata.CreationTimestamp, s.Metadata.Name
			}
		}
		out = append(out, mark)
	}
	return out
}

// ── PVC · PV · STS 이력 ──

type pvcList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
	} `json:"items"`
}

func pvcUIDs() (map[string]string, error) {
	var list pvcList
	if err := kubectlJSON(&list, "get", "pvc", "-n", dataNamespace, "-l", memberSelector); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range list.Items {
		out[p.Metadata.Name] = p.Metadata.UID
	}
	return out, nil
}

func releasedPVs() (int, error) {
	var list struct {
		Items []struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := kubectlJSON(&list, "get", "pv"); err != nil {
		return 0, err
	}
	count := 0
	for _, pv := range list.Items {
		if pv.Status.Phase == "Released" {
			count++
		}
	}
	return count, nil
}

func stsRevisions() ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name            string `json:"name"`
				OwnerReferences []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := kubectlJSON(&list, "get", "controllerrevisions", "-n", dataNamespace); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range list.Items {
		for _, o := range r.Metadata.OwnerReferences {
			if o.Kind == "StatefulSet" && o.Name == shardSTS {
				out = append(out, r.Metadata.Name)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// ── 오퍼레이터 로그 · 감사 ──

// operatorRef 는 설치된 오퍼레이터의 Deployment·SA·파드 셀렉터.
type operatorRef struct {
	Deployment     string
	ServiceAccount string
	Selector       string
}

func readOperator(deployment string) (operatorRef, error) {
	var dep struct {
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
			Template struct {
				Spec struct {
					ServiceAccountName string `json:"serviceAccountName"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := kubectlJSON(&dep, "get", "deployment", deployment, "-n", dataNamespace); err != nil {
		return operatorRef{}, err
	}
	var parts []string
	for k, v := range dep.Spec.Selector.MatchLabels {
		parts = append(parts, k+"="+v)
	}
	slices.Sort(parts)
	return operatorRef{
		Deployment:     deployment,
		ServiceAccount: dep.Spec.Template.Spec.ServiceAccountName,
		Selector:       strings.Join(parts, ","),
	}, nil
}

// user 는 감사 로그의 오퍼레이터 SA 사용자 이름이다.
func (o operatorRef) user() string {
	return "system:serviceaccount:" + dataNamespace + ":" + o.ServiceAccount
}

func (o operatorRef) pods() ([]string, error) {
	out, err := kubectl("get", "pods", "-n", dataNamespace, "-l", o.Selector, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// running 은 오퍼레이터 파드마다 노드·컨테이너 ID·실제 돈 imageID 다(S10 · 판정문 입력).
func (o operatorRef) running() ([]podJSON, error) {
	var list struct {
		Items []podJSON `json:"items"`
	}
	err := kubectlJSON(&list, "get", "pods", "-n", dataNamespace, "-l", o.Selector)
	return list.Items, err
}

// failedWatchLines 는 since 이후 오퍼레이터 로그(현재·이전 컨테이너)의 'Failed to watch' 줄 수.
func (o operatorRef) failedWatchLines(since time.Time) (int, error) {
	pods, err := o.pods()
	if err != nil {
		return 0, err
	}
	sinceArg := "--since-time=" + since.UTC().Format(time.RFC3339)
	count := 0
	for _, pod := range pods {
		out, err := kubectl("logs", "-n", dataNamespace, pod, "--all-containers", sinceArg)
		if err != nil {
			return 0, err
		}
		count += strings.Count(out, failedWatch)
		if prev, err := kubectl("logs", "-n", dataNamespace, pod, "--all-containers", "--previous", sinceArg); err == nil {
			count += strings.Count(prev, failedWatch)
		}
	}
	return count, nil
}

type auditEvent struct {
	Stage     string `json:"stage"`
	Verb      string `json:"verb"`
	ObjectRef struct {
		Resource  string `json:"resource"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"objectRef"`
	User struct {
		Username string              `json:"username"`
		Extra    map[string][]string `json:"extra"`
	} `json:"user"`
	RequestReceivedTimestamp time.Time `json:"requestReceivedTimestamp"`
}

const (
	auditStageComplete = "ResponseComplete"
	auditPodNameKey    = "authentication.kubernetes.io/pod-name"
)

// podDelete 는 감사 로그의 멤버 pod delete 하나(J12 교체 · J13 statefulset-controller).
type podDelete struct {
	Pod  string
	User string
	At   time.Time
}

// auditLog 는 since 이후 data 네임스페이스의 삭제 기록이다.
type auditLog struct {
	pvcDeletes int         // 오퍼레이터 SA 의 PVC delete(허용·거부)
	oldDeletes int         // oldPods(S0 옛 오퍼레이터)가 낸 pod delete
	stsDeletes int         // statefulset-controller 의 pod delete
	podDeletes []podDelete // 멤버 pod delete 전부(누가 · 언제)
}

// readAudit 는 control-plane 감사 로그를 읽는다.
func readAudit(serviceAccount string, since time.Time, oldPods []string) (auditLog, error) {
	out, err := nodeExec(controlPlaneNode(), "cat", auditLogPath)
	if err != nil {
		return auditLog{}, err
	}
	operator := "system:serviceaccount:" + dataNamespace + ":" + serviceAccount
	var log auditLog
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev auditEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		if ev.Stage != auditStageComplete || ev.ObjectRef.Namespace != dataNamespace {
			continue
		}
		if ev.RequestReceivedTimestamp.Before(since) || !strings.HasPrefix(ev.Verb, "delete") {
			continue
		}
		log.add(ev, operator, oldPods)
	}
	return log, scanner.Err()
}

func (l *auditLog) add(ev auditEvent, operator string, oldPods []string) {
	switch ev.ObjectRef.Resource {
	case "persistentvolumeclaims":
		if ev.User.Username == operator {
			l.pvcDeletes++
		}
	case "pods":
		if slices.ContainsFunc(ev.User.Extra[auditPodNameKey], func(p string) bool { return slices.Contains(oldPods, p) }) {
			l.oldDeletes++
		}
		if !memberPattern.MatchString(ev.ObjectRef.Name) {
			return
		}
		if ev.User.Username == stsControllerUser {
			l.stsDeletes++
		}
		del := podDelete{Pod: ev.ObjectRef.Name, User: ev.User.Username, At: ev.RequestReceivedTimestamp}
		l.podDeletes = append(l.podDeletes, del)
	}
}

// ── GREEN J4 게이지 ──

// gaugeScraper 는 오퍼레이터 /metrics 를 port-forward 로 긁는다(토큰은 메모리에만).
type gaugeScraper struct {
	operator operatorRef
	token    string
	tokenAt  time.Time
	client   *http.Client
}

func newGaugeScraper(op operatorRef) (*gaugeScraper, error) {
	if _, err := kubectl("apply", "-f", testdataPath("metrics-reader.yaml")); err != nil {
		return nil, err
	}
	// 목적지는 127.0.0.1 의 kubectl port-forward 뿐이다(터널 자체는 apiserver 로 검증된 TLS).
	// 끝의 인증서는 controller-runtime 이 메모리에서 만든 자체 서명이라 믿을 CA 가 없다.
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // 루프백 전용
	g := &gaugeScraper{operator: op, client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
	return g, g.refreshToken()
}

// refreshToken 은 메트릭 토큰을 새로 받는다.
func (g *gaugeScraper) refreshToken() error {
	ttl := "--duration=" + metricsTokenTTL.String()
	token, err := kubectl("create", "token", "loopdrill-metrics", "-n", dataNamespace, ttl)
	if err != nil {
		return errors.New("메트릭 토큰 발급 실패")
	}
	g.token, g.tokenAt = strings.TrimSpace(token), time.Now()
	return nil
}

// metricRead 는 한 번 긁은 값 — status 쓰기 게이지(리더 최댓값)와 api_read_denied_total 합.
type metricRead struct {
	gauge  float64
	denied float64
}

// read 는 리더 파드들의 게이지 최댓값과 거부 계수 합(비리더는 게이지가 없다 — 설계 §7 위험 15).
func (g *gaugeScraper) read() (metricRead, error) {
	if time.Since(g.tokenAt) > metricsTokenRefresh {
		if err := g.refreshToken(); err != nil {
			return metricRead{}, err
		}
	}
	pods, err := g.operator.pods()
	if err != nil {
		return metricRead{}, err
	}
	var result metricRead
	found := false
	for _, pod := range pods {
		value, ok, err := g.readPod(pod)
		if err != nil || !ok {
			continue
		}
		result.gauge, found = max(result.gauge, value.gauge), true
		result.denied += value.denied
	}
	if !found {
		return metricRead{}, errors.New("status 쓰기 게이지 없음")
	}
	return result, nil
}

func (g *gaugeScraper) readPod(pod string) (metricRead, bool, error) {
	port, err := freePort()
	if err != nil {
		return metricRead{}, false, err
	}
	forward := exec.Command("kubectl", "--kubeconfig="+drillKubeconfig,
		"port-forward", "-n", dataNamespace, "pod/"+pod, port+":"+metricsPort)
	if err := forward.Start(); err != nil {
		return metricRead{}, false, err
	}
	defer func() { _ = forward.Process.Kill(); _ = forward.Wait() }()
	time.Sleep(time.Second)

	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+port+"/metrics", nil)
	if err != nil {
		return metricRead{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	resp, err := g.client.Do(req)
	if err != nil {
		return metricRead{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return metricRead{}, false, err
	}
	var read metricRead
	found := false
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		value, ok := sampleValue(line)
		switch {
		case !ok:
		case strings.Contains(line, gaugeSuffix) && strings.Contains(line, `name="`+clusterCR+`"`):
			read.gauge, found = value, true
		case strings.Contains(line, deniedSuffix):
			read.denied += value
		}
	}
	return read, found, nil
}

// sampleValue 는 Prometheus 텍스트 표본 줄의 값이다.
func sampleValue(line string) (float64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	return v, err == nil
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}
