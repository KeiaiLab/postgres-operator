//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// 설치 층 — 저장·CRD·data 래퍼 차트·비밀·-rw Service·쓰기 탐침과 사전 탐침(P1·P2·선풀).

const (
	testdataDir         = "test/e2e/loopdrill/testdata"
	releaseName         = "platform-data-postgres-operator"
	drillOperatorDeploy = releaseName + "-controller-manager"
	probeNamespace      = "loopdrill-probe"
	probeConfigMap      = "p1"
	probeKey            = "VALUE"
	amd64Machine        = "x86_64"
	installBound        = 20 * time.Minute // 설치·수렴 상한 — amd64 에뮬레이션 아래 initdb·basebackup
	probeBound          = 3 * time.Minute
	resetBound          = 10 * time.Minute
	imageShipBuild      = "image-ship" // 이 맥 빌더(buildkit 미러 = Harbor)
	localOperator       = "loopdrill/operator"
	localPG             = "loopdrill/pg"
	operatorBuildFS     = "Dockerfile.keiailab"
	pgBuildFS           = "Dockerfile.pg-drill"

	// pgDrillBase 는 pg-drill 인스턴스 매니저를 굽는 기점 = 운영 beta.11(KEIAILAB.md) — 설계 I26 무변.
	pgDrillBase = "c7608427b7c059df900ac1003c30316711ae4d17"

	// 노드 — kind-config 의 역할 라벨로 적격 워커를 골라 운영 적격 라벨(etcd)을 API 로 붙인다.
	// kubelet 은 node-role.kubernetes.io/* 를 스스로 달지 못한다(NodeRestriction).
	nodeRoleLabel   = "loopdrill.keiailab.io/role"
	nodeRolePG      = "pg" // 적격 워커 셋
	nodeRoleW4      = "w4" // 무라벨 W4 — e21·kured 역
	eligibleLabel   = "node-role.kubernetes.io/etcd"
	controlPlaneKey = "node-role.kubernetes.io/control-plane"

	// 차트 — 벤더 CRD 자리 · RED 전용 덮어쓰기 · 비밀.
	vendorCRDDir      = "charts/postgres-operator/crds"
	redValuesFile     = "red-values-kind.yaml"
	superuserSecret   = "cnpg-default-superuser" // data SE0 · D1 superuserSecretRef
	superuserName     = "postgres"
	secretPasswordKey = "password"
	passwordBytes     = 24

	// -rw Service · 쓰기 탐침(testdata — 운영 L1 과 같은 파일).
	rwServiceFile    = "rw-service.yaml"
	rwService        = "postgres-prod-shard-0-rw"
	writeProbeFile   = "write-probe.yaml"
	writeProbeSQL    = "write-probe.sql"
	writeProbePod    = "postgres-write-probe"
	writeProbeSecret = "postgres-write-probe"
	writeProbeRole   = "write_probe"
	probeIntervalEnv = "PROBE_INTERVAL_SECONDS"

	// probeStartBound 는 탐침 파드 첫 기동 상한 — infra 노드가 Harbor 에서 pg 이미지를 처음 당긴다.
	probeStartBound = 5 * time.Minute
	warmupBound     = probeStartBound + time.Duration(loopdrill.WriteProbeWarmup)*loopdrill.WriteProbeInterval
)

// pgDrillSources 는 기점에서 pg-drill 컨텍스트로 푸는 경로(Dockerfile.pg-drill 의 COPY 대상).
var pgDrillSources = []string{"go.mod", "go.sum", "api", "cmd", "internal"}

func testdataPath(name string) string {
	return filepath.Join(testdataDir, name)
}

// installInfra 는 스냅숏·Cilium CNP·ExternalSecret CRD, NFS 서버·CSI·스냅숏 컨트롤러·class, 적격 노드 라벨을 올린다(멱등).
func installInfra() error {
	crds := []string{"storage/snapshot-crds.yaml", "cilium-cnp-crd.yaml", "externalsecret-crd.yaml"}
	for _, f := range crds {
		if _, err := kubectl("apply", "-f", testdataPath(f)); err != nil {
			return err
		}
	}
	if _, err := kubectlLong("wait", "--for=condition=Established", "--timeout=2m",
		"crd/volumesnapshotclasses.snapshot.storage.k8s.io", "crd/ciliumnetworkpolicies.cilium.io",
		"crd/externalsecrets.external-secrets.io"); err != nil {
		return err
	}
	for _, f := range []string{"storage/nfs-server.yaml", "storage/csi-driver-nfs.yaml", "storage/snapshot-controller.yaml", "storage/classes.yaml"} {
		if _, err := kubectl("apply", "-f", testdataPath(f)); err != nil {
			return err
		}
	}
	rollouts := [][]string{
		{"-n", "loopdrill-nfs", "deploy/nfs-server"},
		{"-n", "kube-system", "deploy/csi-nfs-controller"},
		{"-n", "kube-system", "ds/csi-nfs-node"},
		{"-n", "kube-system", "deploy/snapshot-controller"},
	}
	for _, r := range rollouts {
		if _, err := kubectlLong(append([]string{"rollout", "status", "--timeout=10m"}, r...)...); err != nil {
			return err
		}
	}
	_, err := kubectl("label", "nodes", "-l", nodeRoleLabel+"="+nodeRolePG, eligibleLabel+"=true", "--overwrite")
	return err
}

// nodeSets 는 적격 노드(control-plane ∨ etcd 라벨)와 W4 — J11 배치 판정의 분류.
func nodeSets() (eligible, avoid []string, err error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := kubectlJSON(&list, "get", "nodes"); err != nil {
		return nil, nil, err
	}
	for _, n := range list.Items {
		labels := n.Metadata.Labels
		_, cp := labels[controlPlaneKey]
		_, etcd := labels[eligibleLabel]
		switch {
		case cp || etcd:
			eligible = append(eligible, n.Metadata.Name)
		case labels[nodeRoleLabel] == nodeRoleW4:
			avoid = append(avoid, n.Metadata.Name)
		}
	}
	return eligible, avoid, nil
}

// nodesWithRole 은 kind-config 역할 라벨이 role 인 노드 이름.
func nodesWithRole(role string) ([]string, error) {
	out, err := kubectl("get", "nodes", "-l", nodeRoleLabel+"="+role, "-o", "jsonpath={.items[*].metadata.name}")
	return strings.Fields(out), err
}

// release 는 한 번의 helm 설치 입력.
type release struct {
	ChartDir string
	Operator imageRef // 노드에 실은 태그
	PG       imageRef // 노드에 실은 태그
	APICIDR  string
	Overlays []string // testdata 의 추가 values(RED 전용 anti-affinity)
}

// installRelease 는 Flux CreateReplace 처럼 벤더 CRD 를 먼저 바꾸고, 운영 values 위에 kind 덮어쓰기와 런타임 값을
// 얹어 설치·업그레이드한다(helm 은 upgrade 때 crds/ 를 건드리지 않는다).
func installRelease(r release, stateDir string) error {
	if err := replaceCRDs(r.ChartDir); err != nil {
		return err
	}
	runtime, err := runtimeValues(r)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "runtime-values.yaml")
	if err := os.WriteFile(path, runtime, 0o600); err != nil {
		return err
	}
	args := []string{"upgrade", "--install", releaseName, r.ChartDir, "-n", dataNamespace, "--create-namespace",
		"-f", testdataPath("wrapper-values-kind.yaml")}
	for _, o := range r.Overlays {
		args = append(args, "-f", testdataPath(o))
	}
	_, err = helm(append(args, "-f", path)...)
	return err
}

// replaceCRDs 는 차트 벤더 CRD 마다 replace, 없으면 create 다(Flux crds: CreateReplace).
func replaceCRDs(chartDir string) error {
	files, err := filepath.Glob(filepath.Join(chartDir, vendorCRDDir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		out, err := kubectl("replace", "-f", f)
		if err == nil {
			continue
		}
		if !strings.Contains(out, "NotFound") && !strings.Contains(out, "not found") {
			return err
		}
		if _, err := kubectl("create", "-f", f); err != nil {
			return err
		}
	}
	return nil
}

// runtimeValues 는 이미지 참조·apiserver CIDR 을 덮는다. 카탈로그는 차트 values 의 항목을 이어받아 이미지만 바꾼다.
func runtimeValues(r release) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(r.ChartDir, "values.yaml"))
	if err != nil {
		return nil, err
	}
	var chart struct {
		ImageCatalog map[string]any `json:"imageCatalog"`
	}
	if err := yaml.Unmarshal(raw, &chart); err != nil {
		return nil, err
	}
	images, ok := chart.ImageCatalog["images"].([]any)
	if !ok || len(images) == 0 {
		return nil, errors.New("차트 values 에 imageCatalog.images 없음")
	}
	for _, entry := range images {
		if m, ok := entry.(map[string]any); ok {
			m["image"] = r.PG.String()
		}
	}
	values := map[string]any{
		"postgres-operator": map[string]any{"image": map[string]any{"repository": r.Operator.Repo, "tag": r.Operator.Tag}},
		"networkPolicy":     map[string]any{"kubeAPIServer": map[string]any{"cidr": r.APICIDR}},
		"imageCatalog":      chart.ImageCatalog,
	}
	return yaml.Marshal(values)
}

// resetRelease 는 이전 시나리오의 흔적을 걷는다 — 멈춘 오퍼레이터를 기다리지 않게 finalizer 부터 뗀다.
// Released PV 는 남긴다(J5 는 시나리오 안의 증가만 센다).
func resetRelease() error {
	dropClusterFinalizers()
	if out, err := helm("uninstall", releaseName, "-n", dataNamespace, "--wait", "--timeout", "5m"); err != nil && !strings.Contains(out, "not found") {
		return err
	}
	// 지워지던 오퍼레이터가 그 사이 finalizer 를 다시 달았을 수 있다
	dropClusterFinalizers()
	if out, err := kubectlLong("delete", "namespace", dataNamespace, "--wait=true", "--timeout="+resetBound.String()); err != nil && !strings.Contains(out, "NotFound") {
		return err
	}
	return nil
}

func dropClusterFinalizers() {
	_, _ = kubectl("patch", "postgrescluster", clusterCR, "-n", dataNamespace, "--type=merge",
		"-p", `{"metadata":{"finalizers":null}}`)
}

// clusterReplicas 는 CR 의 spec.shards.replicas.
func clusterReplicas() (int, error) {
	out, err := kubectl("get", "postgrescluster", clusterCR, "-n", dataNamespace, "-o", "jsonpath={.spec.shards.replicas}")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// waitConverged 는 수렴(converged)할 때까지 기다린다.
func waitConverged(members int, bound time.Duration) (observation, error) {
	deadline := time.Now().Add(bound)
	for {
		obs, err := probeCluster()
		if err == nil && obs.converged(members) {
			return obs, nil
		}
		if time.Now().After(deadline) {
			return obs, fmt.Errorf("%s 안에 수렴하지 않았다(마지막 오류 %v)", bound, err)
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
}

// ── 비밀 · -rw Service · 쓰기 탐침 ──

// secrets 는 실행 중 만든 비밀번호다 — 커밋·판정문·로그에 싣지 않고 stdin 으로만 넘긴다.
// 클러스터에 이미 있으면(KEEP=1 재개) 그 값을 이어 쓴다 — 멤버·탐침이 이미 그 값으로 붙어 있다.
type secrets struct {
	superuser string
	probe     string
}

// ensureNamespace 는 data 네임스페이스를 만든다(helm 전 — 비밀이 차트보다 먼저 선다).
func ensureNamespace() error {
	if out, err := kubectl("create", "namespace", dataNamespace); err != nil && !strings.Contains(out, "AlreadyExists") {
		return err
	}
	return nil
}

// ensureSecret 은 Secret 의 password 를 읽거나, 없으면 무작위로 만든다(basic-auth · username = user).
func ensureSecret(name, user string) (string, error) {
	out, err := kubectl("get", "secret", name, "-n", dataNamespace, "-o", "jsonpath={.data."+secretPasswordKey+"}")
	if err == nil && strings.TrimSpace(out) != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
		return string(raw), err
	}
	if err != nil && !strings.Contains(out, "NotFound") {
		return "", err
	}
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	manifest := fmt.Sprintf("apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\n"+
		"type: kubernetes.io/basic-auth\nstringData:\n  username: %s\n  %s: %s\n",
		name, dataNamespace, user, secretPasswordKey, password)
	_, err = kubectlIn(manifest, "apply", "-f", "-")
	return password, err
}

// randomPassword 는 영숫자 비밀번호다(SQL·passfile 이스케이프가 필요 없다).
func randomPassword() (string, error) {
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.NewReplacer("+", "a", "/", "b", "=", "").Replace(base64.StdEncoding.EncodeToString(raw)), nil
}

// ensureSecrets 는 superuser(D1 superuserSecretRef — 사전 차트는 안 읽는다)·탐침 비밀을 세운다.
func ensureSecrets() (secrets, error) {
	if err := ensureNamespace(); err != nil {
		return secrets{}, err
	}
	superuser, err := ensureSecret(superuserSecret, superuserName)
	if err != nil {
		return secrets{}, err
	}
	probe, err := ensureSecret(writeProbeSecret, writeProbeRole)
	return secrets{superuser: superuser, probe: probe}, err
}

// ensureWriteProbe 는 -rw Service · 탐침 역할·스키마(primary 로컬, 비밀번호는 stdin) · 탐침 파드를 세우고
// 연속 WriteProbeWarmup 개 ack 까지 기다린다. 준비가 안 되면 false(판정 J10·J15 가 붉게 남긴다).
func ensureWriteProbe(primary string, s secrets) (bool, error) {
	if err := probeIntervalMatches(); err != nil {
		return false, err
	}
	if _, err := kubectl("apply", "-f", testdataPath(rwServiceFile)); err != nil {
		return false, err
	}
	sql, err := os.ReadFile(testdataPath(writeProbeSQL))
	if err != nil {
		return false, err
	}
	setup := `IFS= read -r PROBE_PASSWORD && export PROBE_PASSWORD && ` +
		`exec "$POSTGRES_BIN_DIR/psql" "$POSTGRES_LOCAL_DSN" -Xq -v ON_ERROR_STOP=1`
	if _, err := kubectlIn(s.probe+"\n"+string(sql), "exec", "-i", "-n", dataNamespace, primary, "-c", pgContainer, "--",
		"sh", "-c", setup); err != nil {
		return false, err
	}
	if _, err := kubectl("apply", "-f", testdataPath(writeProbeFile)); err != nil {
		return false, err
	}
	deadline := time.Now().Add(warmupBound)
	for time.Now().Before(deadline) {
		if acks, err := probeAcks(); err == nil && loopdrill.WarmedUp(acks, loopdrill.WriteProbeWarmup) {
			return true, nil
		}
		time.Sleep(loopdrill.WriteProbeInterval)
	}
	return false, nil
}

// probeIntervalMatches 는 탐침 매니페스트 간격이 writeProbeInterval 과 같은지 본다 — J10 표본 하한이 그 간격을 쓴다.
func probeIntervalMatches() error {
	raw, err := os.ReadFile(testdataPath(writeProbeFile))
	if err != nil {
		return err
	}
	var pod struct {
		Spec struct {
			Containers []struct {
				Env []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &pod); err != nil {
		return err
	}
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			if e.Name != probeIntervalEnv {
				continue
			}
			secs, err := strconv.Atoi(e.Value)
			if err == nil && time.Duration(secs)*time.Second == loopdrill.WriteProbeInterval {
				return nil
			}
			return fmt.Errorf("%s %s = %q 가 writeProbeInterval %s 와 다르다",
				writeProbeFile, probeIntervalEnv, e.Value, loopdrill.WriteProbeInterval)
		}
	}
	return fmt.Errorf("%s 에 %s 없음", writeProbeFile, probeIntervalEnv)
}

// ── 사전 탐침 ──

// ProbeResult 는 P1·P2·선풀 기록.
type ProbeResult struct {
	NewPodLatest  bool     `json:"newPodLatest"`       // P1 — 새 파드가 CM 최신 값을 받는다(필수)
	RestartLatest bool     `json:"restartLatest"`      // P1 — 컨테이너 재시작도 새 값을 받는다(기록)
	Amd64         bool     `json:"amd64"`              // P2 — 노드가 linux/amd64 이미지를 돌린다
	Unpulled      []string `json:"unpulled,omitempty"` // 선풀 — 적격 노드 status.images 에 없는 시험 이미지
}

func runProbes() (ProbeResult, error) {
	var result ProbeResult
	if out, err := kubectl("create", "namespace", probeNamespace); err != nil && !strings.Contains(out, "AlreadyExists") {
		return result, err
	}
	defer func() { _, _ = kubectlLong("delete", "namespace", probeNamespace, "--wait=false") }()
	if err := setProbeValue("1"); err != nil {
		return result, err
	}
	if _, err := kubectl("apply", "-f", testdataPath("probes.yaml")); err != nil {
		return result, err
	}

	amd64, err := probeAmd64()
	if err != nil {
		return result, err
	}
	result.Amd64 = amd64

	result.NewPodLatest, result.RestartLatest, err = probeConfigMapEnv()
	return result, err
}

// prepulled 는 적격 노드마다 status.images 에 시험 오퍼레이터·pg 이미지가 있는가(설계 §3.13 선풀).
func prepulled(images ...imageRef) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Images []struct {
					Names []string `json:"names"`
				} `json:"images"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := kubectlJSON(&list, "get", "nodes", "-l", eligibleLabel); err != nil {
		return nil, err
	}
	nodes := map[string][]string{}
	for _, n := range list.Items {
		for _, img := range n.Status.Images {
			nodes[n.Metadata.Name] = append(nodes[n.Metadata.Name], names(img.Names)...)
		}
	}
	var want []string
	for _, img := range images {
		want = append(want, img.String())
	}
	return loopdrill.Unpulled(nodes, want), nil
}

// names 는 status.images 이름에 docker.io 줄임을 푼 이름을 더한다(kind 는 docker.io/<repo> 로 싣는다).
func names(raw []string) []string {
	out := append([]string(nil), raw...)
	for _, n := range raw {
		if short, ok := strings.CutPrefix(n, "docker.io/"); ok {
			out = append(out, short)
		}
	}
	return out
}

func setProbeValue(value string) error {
	patch := fmt.Sprintf(`{"data":{%q:%q}}`, probeKey, value)
	if _, err := kubectl("patch", "configmap", probeConfigMap, "-n", probeNamespace, "--type=merge", "-p", patch); err == nil {
		return nil
	}
	_, err := kubectl("create", "configmap", probeConfigMap, "-n", probeNamespace, "--from-literal="+probeKey+"="+value)
	return err
}

// probeAmd64 는 P2 — amd64 단일 매니페스트 파드가 x86_64 를 찍고 끝나는가.
func probeAmd64() (bool, error) {
	deadline := time.Now().Add(probeBound)
	for time.Now().Before(deadline) {
		phase, _ := kubectl("get", "pod", "p2", "-n", probeNamespace, "-o", "jsonpath={.status.phase}")
		switch strings.TrimSpace(phase) {
		case "Succeeded":
			out, err := kubectl("logs", "p2", "-n", probeNamespace)
			return strings.TrimSpace(out) == amd64Machine, err
		case "Failed":
			return false, nil
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
	return false, errors.New("P2 파드가 끝나지 않았다(노드 안 Harbor 당기기 확인)")
}

// probeConfigMapEnv 는 P1 — CM 을 바꾼 뒤 새 파드와 재시작한 컨테이너가 받는 env.
func probeConfigMapEnv() (newPod, restart bool, err error) {
	if err := waitProbePod(); err != nil {
		return false, false, err
	}
	if err := setProbeValue("2"); err != nil {
		return false, false, err
	}
	if _, err := kubectlLong("delete", "pod", "p1", "-n", probeNamespace, "--wait=true"); err != nil {
		return false, false, err
	}
	if _, err := kubectl("apply", "-f", testdataPath("probes.yaml")); err != nil {
		return false, false, err
	}
	if err := waitProbePod(); err != nil {
		return false, false, err
	}
	value, err := probeEnv()
	if err != nil {
		return false, false, err
	}
	newPod = value == "2"

	// 같은 파드의 컨테이너 재시작(PID 1 이 TERM 을 받아 끝난다)
	if err := setProbeValue("3"); err != nil {
		return newPod, false, err
	}
	_, _ = kubectl("exec", "p1", "-n", probeNamespace, "--", "kill", "1")
	deadline := time.Now().Add(probeBound)
	for time.Now().Before(deadline) {
		restarts, _ := kubectl("get", "pod", "p1", "-n", probeNamespace, "-o", "jsonpath={.status.containerStatuses[0].restartCount}")
		if strings.TrimSpace(restarts) != "0" && waitProbePod() == nil {
			value, err := probeEnv()
			return newPod, value == "3", err
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
	return newPod, false, errors.New("P1 컨테이너가 재시작하지 않았다")
}

func waitProbePod() error {
	_, err := kubectlLong("wait", "--for=condition=Ready", "pod/p1", "-n", probeNamespace, "--timeout="+probeBound.String())
	return err
}

func probeEnv() (string, error) {
	out, err := kubectl("exec", "p1", "-n", probeNamespace, "--", "printenv", probeKey)
	return strings.TrimSpace(out), err
}

// ── 시험 이미지 ──

// prepareImages 는 P2 에 따라 시험 이미지를 노드에 싣고 pg-drill 이미지 ID 를 낸다.
// 오퍼레이터: 참 = 발행 digest 그대로(amd64 매니페스트) · 거짓 = 같은 sha 를 이 맥에서 노드 아키텍처로 굽는다(push 0).
// pg: 늘 pg-drill — 오퍼레이터와 같은 플랫폼으로 굽는다(참이면 amd64 에뮬레이션 그대로).
func prepareImages(operator imageRef, fidelity Fidelity) (imageRef, imageRef, string, error) {
	platform := nodePlatformArm
	if fidelity == FidelityPublished {
		platform = nodePlatformAmd
	}
	pg, pgID, err := buildPGDrill(platform)
	if err != nil {
		return imageRef{}, imageRef{}, "", err
	}
	if err := loadLocalImage(pg, platform); err != nil {
		return imageRef{}, imageRef{}, "", err
	}
	if fidelity == FidelityPublished {
		op, err := loadImage(operator, nodePlatformAmd)
		return op, pg, pgID, err
	}

	sha, err := run(commandTimeout, "git", "rev-parse", "--short=8", "HEAD")
	if err != nil {
		return imageRef{}, imageRef{}, "", err
	}
	op := imageRef{Repo: localOperator, Tag: strings.TrimSpace(sha)}
	if err := buildLocal(operatorBuildFS, ".", nodePlatformArm, op); err != nil {
		return imageRef{}, imageRef{}, "", err
	}
	return op, pg, pgID, loadLocalImage(op, nodePlatformArm)
}

// buildPGDrill 은 기점 트리를 임시 컨텍스트로 풀어 pg-drill 을 굽는다 — 작업 트리의 인스턴스 매니저는 싣지 않는다.
func buildPGDrill(platform string) (imageRef, string, error) {
	ctxDir, err := os.MkdirTemp("", "loopdrill-pg-drill-")
	if err != nil {
		return imageRef{}, "", err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()

	// 기점 go.mod·go.sum·api·cmd·internal 만 푼다
	archive := filepath.Join(ctxDir, "base.tar")
	args := append([]string{"archive", "-o", archive, pgDrillBase}, pgDrillSources...)
	if _, err := run(commandTimeout, "git", args...); err != nil {
		return imageRef{}, "", err
	}
	if _, err := run(commandTimeout, "tar", "-xf", archive, "-C", ctxDir); err != nil {
		return imageRef{}, "", err
	}
	if err := os.Remove(archive); err != nil {
		return imageRef{}, "", err
	}

	dockerfile, err := filepath.Abs(testdataPath(pgBuildFS))
	if err != nil {
		return imageRef{}, "", err
	}
	tag := imageRef{Repo: localPG, Tag: shortHash(pgDrillBase) + "-" + path.Base(platform)}
	if err := buildLocal(dockerfile, ctxDir, platform, tag); err != nil {
		return imageRef{}, "", err
	}
	id, err := docker("image", "inspect", "--format", "{{.Id}}", tag.String())
	return tag, strings.TrimSpace(id), err
}

// buildLocal 은 이 맥 빌드 슬롯을 쥔 채 platform 으로 굽는다.
func buildLocal(dockerfile, contextDir, platform string, tag imageRef) error {
	release, err := holdBuildSlot(time.Now().Add(installBound))
	if err != nil {
		return err
	}
	defer release()
	_, err = docker("buildx", "build", "--builder", imageShipBuild, "--platform", platform,
		"-f", dockerfile, "-t", tag.String(), "--load", contextDir)
	return err
}

// imageRevision 은 이미지 revision 라벨(org.opencontainers.image.revision) — 판정문 S 결속(설계 §3.12).
func imageRevision(ref imageRef) string {
	format := `{{index .Config.Labels "org.opencontainers.image.revision"}}`
	out, err := docker("image", "inspect", "--format", format, ref.String())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ── C1 확인(설계 §3.13 — CRD 필드 · CR 값 · ClusterRole · VAP · Deployment 0/D · STS generation) ──

const (
	postgresClusterCRD = "postgresclusters.postgres.keiailab.io"
	snapshotGroup      = "snapshot.storage.k8s.io"
	verbDelete         = "delete"
	verbAll            = "*"
	operationDelete    = "DELETE"
)

// verifyC1 은 C1 착지 뒤(오퍼레이터 0) 렌더가 설계대로인지 보고 어긋난 항목을 낸다 — 비면 통과.
func verifyC1(stsGen int64, operator imageRef) []string {
	checks := []struct {
		what string
		ok   func() bool
	}{
		{"CRD 필드(superuserSecretRef · postgresql.walKeepSize · postgresql.hostAuthCIDRs)", crdHasFields},
		{"CR superuserSecretRef · postgresql.walKeepSize", crHasFields},
		{"ClusterRole(nodes 읽기 · volumesnapshots create·delete · PVC delete 0)", roleShaped},
		{"VAP(volumesnapshots DELETE) · 바인딩", vapBound},
		{"Deployment 0 · 시험 이미지", func() bool { return operatorAtZero(operator) }},
		{"STS generation 불변", func() bool { return stsGeneration() == stsGen }},
	}
	var faults []string
	for _, c := range checks {
		if !c.ok() {
			faults = append(faults, c.what)
		}
	}
	return faults
}

type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
}

func crdHasFields() bool {
	var crd struct {
		Spec struct {
			Versions []struct {
				Served bool `json:"served"`
				Schema struct {
					OpenAPIV3Schema schemaNode `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if kubectlJSON(&crd, "get", "crd", postgresClusterCRD) != nil {
		return false
	}
	for _, v := range crd.Spec.Versions {
		spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
		pg := spec.Properties["postgresql"].Properties
		_, ref := spec.Properties["superuserSecretRef"]
		_, wal := pg["walKeepSize"]
		_, cidrs := pg["hostAuthCIDRs"]
		if v.Served && ref && wal && cidrs {
			return true
		}
	}
	return false
}

func crHasFields() bool {
	var cr struct {
		Spec struct {
			SuperuserSecretRef struct {
				Name string `json:"name"`
			} `json:"superuserSecretRef"`
			PostgreSQL struct {
				WalKeepSize any `json:"walKeepSize"`
			} `json:"postgresql"`
		} `json:"spec"`
	}
	if kubectlJSON(&cr, "get", "postgrescluster", clusterCR, "-n", dataNamespace) != nil {
		return false
	}
	return cr.Spec.SuperuserSecretRef.Name != "" && cr.Spec.PostgreSQL.WalKeepSize != nil
}

type policyRule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// allows 는 규칙이 group 의 resource 에 verb 를 주는가.
func (r policyRule) allows(group, resource, verb string) bool {
	return (slices.Contains(r.APIGroups, group) || slices.Contains(r.APIGroups, verbAll)) &&
		(slices.Contains(r.Resources, resource) || slices.Contains(r.Resources, verbAll)) &&
		(slices.Contains(r.Verbs, verb) || slices.Contains(r.Verbs, verbAll))
}

func roleShaped() bool {
	var role struct {
		Rules []policyRule `json:"rules"`
	}
	if kubectlJSON(&role, "get", "clusterrole", managerRole) != nil {
		return false
	}
	grants := func(group, resource string, verbs ...string) bool {
		for _, verb := range verbs {
			if !slices.ContainsFunc(role.Rules, func(r policyRule) bool { return r.allows(group, resource, verb) }) {
				return false
			}
		}
		return true
	}
	return grants("", "nodes", "get", "list", "watch") &&
		grants(snapshotGroup, "volumesnapshots", "create", verbDelete) &&
		!grants("", "persistentvolumeclaims", verbDelete)
}

type vapBinding struct {
	Spec struct {
		PolicyName string `json:"policyName"`
	} `json:"spec"`
}

func vapBound() bool {
	var policies struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				MatchConstraints struct {
					ResourceRules []struct {
						Resources  []string `json:"resources"`
						Operations []string `json:"operations"`
					} `json:"resourceRules"`
				} `json:"matchConstraints"`
			} `json:"spec"`
		} `json:"items"`
	}
	var bindings struct {
		Items []vapBinding `json:"items"`
	}
	if kubectlJSON(&policies, "get", "validatingadmissionpolicies") != nil {
		return false
	}
	if kubectlJSON(&bindings, "get", "validatingadmissionpolicybindings") != nil {
		return false
	}
	for _, p := range policies.Items {
		guards := false
		for _, r := range p.Spec.MatchConstraints.ResourceRules {
			deletes := slices.Contains(r.Operations, operationDelete)
			guards = guards || (slices.Contains(r.Resources, "volumesnapshots") && deletes)
		}
		bound := slices.ContainsFunc(bindings.Items, func(b vapBinding) bool { return b.Spec.PolicyName == p.Metadata.Name })
		if guards && bound {
			return true
		}
	}
	return false
}

func operatorAtZero(operator imageRef) bool {
	var dep struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if kubectlJSON(&dep, "get", "deployment", drillOperatorDeploy, "-n", dataNamespace) != nil {
		return false
	}
	image := ""
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == managerContainer {
			image = c.Image
		}
	}
	return dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0 && image == operator.String()
}

func stsGeneration() int64 {
	var sts stsJSON
	if kubectlJSON(&sts, "get", "sts", shardSTS, "-n", dataNamespace) != nil {
		return 0
	}
	return sts.Metadata.Generation
}
