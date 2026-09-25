//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// 설치 층 — 저장·CRD·data 래퍼 차트와 사전 탐침(P1·P2).

const (
	testdataDir     = "test/e2e/loopdrill/testdata"
	releaseName     = "platform-data-postgres-operator"
	operatorDeploy  = releaseName + "-controller-manager"
	probeNamespace  = "loopdrill-probe"
	probeConfigMap  = "p1"
	probeKey        = "VALUE"
	amd64Machine    = "x86_64"
	installBound    = 20 * time.Minute // 설치·수렴 상한 — amd64 에뮬레이션 아래 initdb·basebackup
	probeBound      = 3 * time.Minute
	resetBound      = 10 * time.Minute
	imageShipBuild  = "image-ship" // 이 맥 빌더(buildkit 미러 = Harbor)
	localOperator   = "loopdrill/operator"
	localPG         = "loopdrill/pg"
	operatorBuildFS = "Dockerfile.keiailab"
	pgBuildFS       = "Dockerfile.pg-drill"
)

func testdataPath(name string) string {
	return filepath.Join(testdataDir, name)
}

// installInfra 는 스냅숏 CRD·Cilium CNP CRD·NFS 서버·CSI·스냅숏 컨트롤러·class 를 올린다(멱등).
func installInfra() error {
	crds := []string{"storage/snapshot-crds.yaml", "cilium-cnp-crd.yaml"}
	for _, f := range crds {
		if _, err := kubectl("apply", "-f", testdataPath(f)); err != nil {
			return err
		}
	}
	if _, err := kubectlLong("wait", "--for=condition=Established", "--timeout=2m",
		"crd/volumesnapshotclasses.snapshot.storage.k8s.io", "crd/ciliumnetworkpolicies.cilium.io"); err != nil {
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
	return nil
}

// release 는 한 번의 helm 설치 입력.
type release struct {
	ChartDir string
	Operator imageRef // 노드에 실은 태그
	PG       imageRef // 노드에 실은 태그
	APICIDR  string
}

// installRelease 는 운영 values 위에 kind 덮어쓰기와 런타임 값을 얹어 설치·업그레이드한다.
func installRelease(r release, stateDir string) error {
	runtime, err := runtimeValues(r)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "runtime-values.yaml")
	if err := os.WriteFile(path, runtime, 0o600); err != nil {
		return err
	}
	_, err = helm("upgrade", "--install", releaseName, r.ChartDir, "-n", dataNamespace, "--create-namespace",
		"-f", testdataPath("wrapper-values-kind.yaml"), "-f", path)
	return err
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
		time.Sleep(StatusPollInterval)
	}
}

// ── 사전 탐침 ──

// ProbeResult 는 P1·P2 기록.
type ProbeResult struct {
	NewPodLatest  bool `json:"newPodLatest"`  // P1 — 새 파드가 CM 최신 값을 받는다(필수)
	RestartLatest bool `json:"restartLatest"` // P1 — 컨테이너 재시작도 새 값을 받는다(기록)
	Amd64         bool `json:"amd64"`         // P2 — 노드가 linux/amd64 이미지를 돌린다
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
		time.Sleep(StatusPollInterval)
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
		time.Sleep(StatusPollInterval)
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

// prepareImages 는 P2 에 따라 시험 이미지를 노드에 싣는다.
// 참 = 발행 digest 그대로(amd64 매니페스트) · 거짓 = 같은 sha 를 이 맥에서 노드 아키텍처로 굽는다(push 0).
func prepareImages(operator, pg imageRef, fidelity Fidelity) (imageRef, imageRef, error) {
	if fidelity == FidelityPublished {
		op, err := loadImage(operator, nodePlatformAmd)
		if err != nil {
			return imageRef{}, imageRef{}, err
		}
		pgLocal, err := loadImage(pg, nodePlatformAmd)
		return op, pgLocal, err
	}

	sha, err := run(commandTimeout, "git", "rev-parse", "--short=8", "HEAD")
	if err != nil {
		return imageRef{}, imageRef{}, err
	}
	op := imageRef{Repo: localOperator, Tag: strings.TrimSpace(sha)}
	pgLocal := imageRef{Repo: localPG, Tag: strings.TrimSpace(sha)}
	if err := buildLocal(operatorBuildFS, op); err != nil {
		return imageRef{}, imageRef{}, err
	}
	if err := buildLocal(testdataPath(pgBuildFS), pgLocal); err != nil {
		return imageRef{}, imageRef{}, err
	}
	if err := loadLocalImage(op); err != nil {
		return imageRef{}, imageRef{}, err
	}
	return op, pgLocal, loadLocalImage(pgLocal)
}

// buildLocal 은 이 맥 빌드 슬롯을 쥔 채 노드 아키텍처로 굽는다.
func buildLocal(dockerfile string, tag imageRef) error {
	release, err := holdBuildSlot(time.Now().Add(installBound))
	if err != nil {
		return err
	}
	defer release()
	_, err = docker("buildx", "build", "--builder", imageShipBuild, "--platform", nodePlatformArm,
		"-f", dockerfile, "-t", tag.String(), "--load", ".")
	return err
}
