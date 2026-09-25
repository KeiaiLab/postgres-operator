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
// 쓰는 것은 표식·하트비트 행뿐이다(J8 · J4 의 status 변화 재료).

const (
	dataNamespace  = "data"
	clusterCR      = "postgres-prod"
	shardSTS       = "postgres-prod-shard-0"
	pgContainer    = "postgres"
	memberSelector = "app.kubernetes.io/instance=postgres-prod,app.kubernetes.io/component=shard"
	revisionLabel  = "controller-revision-hash"
	snapshotLabel  = "postgres.keiailab.io/member-snapshot=true"
	rebuildInfix   = "-rebuild-"
	auditLogPath   = "/var/log/kubernetes/audit.log"
	failedWatch    = "Failed to watch"

	// 하트비트 한 번 = 1KiB 행 64개 — pg_database_size 가 커져 CR status(sizeBytes)가 바뀐다.
	heartbeatPayloadBytes = 1024
	heartbeatRows         = 64

	blockedCondition = "MemberRebuildBlocked"
	blockedReason    = "BudgetExhausted"
	gaugeSuffix      = "status_write_timestamp_seconds"
	metricsPort      = "8443"
	metricsTokenTTL  = "6h"
)

// 복제 상태 — 연결로 치는 것(설계 §3.4: backup = basebackup 중).
var connectedStates = []string{"streaming", "catchup", "backup"}

// 풀기 이벤트(설계 §3.9) — 재구축이 파괴 단계에 들어갔다는 신호.
var releaseReasons = []string{"MemberRejoinReleased", "MemberReseedRequested"}

var memberPattern = regexp.MustCompile(clusterCR + `-shard-\d+-\d+`)

const (
	sqlRole = "SELECT CASE WHEN pg_is_in_recovery() THEN 'standby' ELSE 'primary' END, " +
		"CASE WHEN pg_is_in_recovery() THEN '' ELSE substr(pg_walfile_name(pg_current_wal_lsn()), 1, 8) END"
	sqlGraph  = "SELECT application_name, state FROM pg_stat_replication"
	sqlSchema = "CREATE SCHEMA IF NOT EXISTS loopdrill; " +
		"CREATE TABLE IF NOT EXISTS loopdrill.marker(id bigserial PRIMARY KEY, scenario text NOT NULL, at timestamptz NOT NULL DEFAULT now()); " +
		"CREATE TABLE IF NOT EXISTS loopdrill.heartbeat(id bigserial PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(), payload text NOT NULL)"
	roleStandby = "standby"
	rolePrimary = "primary"
)

func sqlHeartbeat() string {
	return fmt.Sprintf("INSERT INTO loopdrill.heartbeat(payload) SELECT repeat('x', %d) FROM generate_series(1, %d)",
		heartbeatPayloadBytes, heartbeatRows)
}

// ── 멤버 관측 ──

// member 는 샤드 파드 하나.
type member struct {
	Pod         string
	Node        string
	ContainerID string
	Ready       bool
	Terminating bool
	Created     time.Time
	Revision    string
	Role        string            // primary | standby | ""(exec 실패)
	Timeline    int64             // primary 만
	Replication map[string]string // primary 만: application_name → state
}

// observation 은 한 표본 — STS 와 멤버.
type observation struct {
	At                 time.Time
	Generation         int64
	ObservedGeneration int64
	UpdateRevision     string
	CurrentRevision    string
	Replicas           int32
	ReadyReplicas      int32
	Members            []member
}

type stsJSON struct {
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas int32 `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64  `json:"observedGeneration"`
		ReadyReplicas      int32  `json:"readyReplicas"`
		UpdateRevision     string `json:"updateRevision"`
		CurrentRevision    string `json:"currentRevision"`
	} `json:"status"`
}

type podJSON struct {
	Metadata struct {
		Name              string            `json:"name"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
		Labels            map[string]string `json:"labels"`
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
		ContainerStatuses []struct {
			Name        string `json:"name"`
			ContainerID string `json:"containerID"`
			State       struct {
				Running *struct{} `json:"running"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (p podJSON) ready() bool {
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func (p podJSON) pgContainer() (id string, running bool) {
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == pgContainer {
			return c.ContainerID, c.State.Running != nil
		}
	}
	return "", false
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
	obs.CurrentRevision = sts.Status.CurrentRevision
	obs.Replicas = sts.Spec.Replicas
	obs.ReadyReplicas = sts.Status.ReadyReplicas

	var pods struct {
		Items []podJSON `json:"items"`
	}
	if err := kubectlJSON(&pods, "get", "pods", "-n", dataNamespace, "-l", memberSelector); err != nil {
		return obs, err
	}
	for _, p := range pods.Items {
		m := member{
			Pod:         p.Metadata.Name,
			Node:        p.Spec.NodeName,
			Ready:       p.ready(),
			Terminating: p.Metadata.DeletionTimestamp != nil,
			Created:     p.Metadata.CreationTimestamp,
			Revision:    p.Metadata.Labels[revisionLabel],
		}
		id, running := p.pgContainer()
		m.ContainerID = id
		if running && !m.Terminating {
			describeMember(&m)
		}
		obs.Members = append(obs.Members, m)
	}
	slices.SortFunc(obs.Members, func(a, b member) int { return strings.Compare(a.Pod, b.Pod) })
	return obs, nil
}

// describeMember 는 psql 한 번으로 역할·타임라인·복제 그래프를 채운다(실패 = 모름).
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
	if role != rolePrimary {
		return
	}
	if n, err := strconv.ParseInt(tl, 16, 64); err == nil {
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

// converged 는 롤아웃 끝 · 전 멤버 Ready · primary 하나 · 나머지 전부 streaming.
func (o observation) converged(members int) bool {
	if o.Generation == 0 || o.ObservedGeneration != o.Generation || o.UpdateRevision != o.CurrentRevision {
		return false
	}
	if int(o.ReadyReplicas) != members || len(o.Members) != members {
		return false
	}
	primary, ok := o.primary()
	if !ok {
		return false
	}
	for _, m := range o.Members {
		if !m.Ready || m.Terminating || m.Revision != o.UpdateRevision {
			return false
		}
		if m.Pod != primary.Pod && primary.Replication[m.Pod] != "streaming" {
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
	mark := loopdrill.GraphMark{At: o.At, Primary: primary.Pod}
	for name, state := range primary.Replication {
		if slices.Contains(connectedStates, state) {
			mark.Connected = append(mark.Connected, name)
		}
	}
	slices.Sort(mark.Connected)
	return mark, true
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

// ── CR · 스냅숏 · 이벤트 ──

type clusterJSON struct {
	Metadata struct {
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

// readCluster 는 CR status 마지막 쓰기 시각과 예산 소진 조건을 읽는다.
func readCluster() (lastWrite time.Time, budgetBlocked bool, err error) {
	var cr clusterJSON
	if err := kubectlJSON(&cr, "get", "postgrescluster", clusterCR, "-n", dataNamespace, "--show-managed-fields"); err != nil {
		return time.Time{}, false, err
	}
	for _, f := range cr.Metadata.ManagedFields {
		if f.Subresource == "status" && f.Time.After(lastWrite) {
			lastWrite = f.Time
		}
	}
	for _, c := range cr.Status.Conditions {
		if c.Type == blockedCondition && c.Status == "True" && c.Reason == blockedReason {
			budgetBlocked = true
		}
	}
	return lastWrite, budgetBlocked, nil
}

type snapshotJSON struct {
	Metadata struct {
		Name              string    `json:"name"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
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

func snapshotMember(name string) string {
	member, _, _ := strings.Cut(name, rebuildInfix)
	return member
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

func (o operatorRef) pods() ([]string, error) {
	out, err := kubectl("get", "pods", "-n", dataNamespace, "-l", o.Selector, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
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

// auditDeletes 는 since 이후 오퍼레이터 SA 의 data PVC delete 수와, oldPods 가 낸 pod delete 수(허용·거부 모두).
func auditDeletes(serviceAccount string, since time.Time, oldPods []string) (pvcDeletes, podDeletes int, err error) {
	out, err := nodeExec(controlPlaneNode(), "cat", auditLogPath)
	if err != nil {
		return 0, 0, err
	}
	user := "system:serviceaccount:" + dataNamespace + ":" + serviceAccount
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev auditEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		if ev.Stage != auditStageComplete || ev.User.Username != user || ev.ObjectRef.Namespace != dataNamespace {
			continue
		}
		if ev.RequestReceivedTimestamp.Before(since) || !strings.HasPrefix(ev.Verb, "delete") {
			continue
		}
		switch ev.ObjectRef.Resource {
		case "persistentvolumeclaims":
			pvcDeletes++
		case "pods":
			if slices.ContainsFunc(ev.User.Extra[auditPodNameKey], func(p string) bool { return slices.Contains(oldPods, p) }) {
				podDeletes++
			}
		}
	}
	return pvcDeletes, podDeletes, scanner.Err()
}

// ── GREEN J4 게이지 ──

// gaugeScraper 는 오퍼레이터 /metrics 를 port-forward 로 긁는다(토큰은 메모리에만).
type gaugeScraper struct {
	operator operatorRef
	token    string
	client   *http.Client
}

func newGaugeScraper(op operatorRef) (*gaugeScraper, error) {
	if _, err := kubectl("apply", "-f", testdataPath("metrics-reader.yaml")); err != nil {
		return nil, err
	}
	token, err := kubectl("create", "token", "loopdrill-metrics", "-n", dataNamespace, "--duration="+metricsTokenTTL)
	if err != nil {
		return nil, errors.New("메트릭 토큰 발급 실패")
	}
	// 목적지는 127.0.0.1 의 kubectl port-forward 뿐이다(터널 자체는 apiserver 로 검증된 TLS).
	// 끝의 인증서는 controller-runtime 이 메모리에서 만든 자체 서명이라 믿을 CA 가 없다.
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // 루프백 전용
	return &gaugeScraper{operator: op, token: strings.TrimSpace(token), client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}, nil
}

// read 는 리더 파드들의 게이지 최댓값(비리더는 게이지가 없다 — 설계 §7 위험 16).
func (g *gaugeScraper) read() (float64, error) {
	pods, err := g.operator.pods()
	if err != nil {
		return 0, err
	}
	top, found := 0.0, false
	for _, pod := range pods {
		value, ok, err := g.readPod(pod)
		if err != nil || !ok {
			continue
		}
		top, found = max(top, value), true
	}
	if !found {
		return 0, errors.New("status 쓰기 게이지 없음")
	}
	return top, nil
}

func (g *gaugeScraper) readPod(pod string) (float64, bool, error) {
	port, err := freePort()
	if err != nil {
		return 0, false, err
	}
	forward := exec.Command("kubectl", "port-forward", "-n", dataNamespace, "pod/"+pod, port+":"+metricsPort)
	if err := forward.Start(); err != nil {
		return 0, false, err
	}
	defer func() { _ = forward.Process.Kill(); _ = forward.Wait() }()
	time.Sleep(time.Second)

	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1:"+port+"/metrics", nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") || !strings.Contains(line, gaugeSuffix) || !strings.Contains(line, `name="`+clusterCR+`"`) {
			continue
		}
		fields := strings.Fields(line)
		if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			return v, true, nil
		}
	}
	return 0, false, nil
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}
