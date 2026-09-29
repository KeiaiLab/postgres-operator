//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
	"github.com/keiailab/postgres-operator/test/utils"
)

// 드라이버 층 — kind·docker·kubectl·helm 명령과 이미지 싣기를 여기에만 둔다.
// 위층(설치·시나리오·관측)은 이 함수들로만 클러스터를 만진다. 명령은 test/utils.Run, 싣기는 helpers.go 로 돈다.
//
//	loopdrill_e2e_test.go ─▶ install / scenarios / observe ─▶ cluster(이 파일) ─▶ kind·docker·kubectl·helm
//
// 오조준 차단 — kubectl·helm·pg-stop 은 늘 훈련장 kubeconfig(drillKubeconfig)를 명시해 부른다. 비었으면 부르지 않는다.
// 훈련 시작과 파괴 동작 직전마다 guardTarget 이 그 파일의 서버가 kind 루프백인지 본다(loopdrill.CheckKubeconfig).

const (
	clusterName     = "loopdrill"
	commandTimeout  = 2 * time.Minute
	longTimeout     = 15 * time.Minute
	requestTimeout  = "15s" // kubectl 요청 시한 — S4(control-plane pause) 동안 매달리지 않는다
	infraNodeLabel  = "loopdrill.keiailab.io/role=infra"
	nodePlatformArm = "linux/arm64"
	nodePlatformAmd = "linux/amd64"
	drillTagPrefix  = "loopdrill-"
	shortDigestLen  = 8
	kindNetwork     = "kind"
	kindContext     = "kind-" + clusterName
	pgStopBinary    = "pg-stop" // EM0 이 맥 도구(설계 §3.19)
)

// drillKubeconfig 는 훈련장 전용 kubeconfig 경로 — prepareState 가 정한다. 비면 kubectl·helm 을 부르지 않는다.
var drillKubeconfig string

var (
	// errNoKubeconfig 는 훈련장 kubeconfig 없이 클러스터 명령을 부르려 했다는 뜻이다.
	errNoKubeconfig = errors.New("훈련장 kubeconfig 가 정해지지 않았다 — 클러스터 명령을 부르지 않는다")

	// errMistarget 은 훈련장 kubeconfig 가 kind 루프백이 아니라는 뜻이다 — 파괴 동작을 멈춘다.
	errMistarget = errors.New("훈련장 오조준 차단")
)

// guardTarget 은 훈련장 kubeconfig 가 kind 루프백만 가리키는지 본다 — 아니면 운영을 칠 수 있어 멈춘다.
func guardTarget() error {
	if drillKubeconfig == "" {
		return errNoKubeconfig
	}
	raw, err := os.ReadFile(drillKubeconfig)
	if err != nil {
		return fmt.Errorf("훈련장 kubeconfig: %w", err)
	}
	if err := loopdrill.CheckKubeconfig(raw); err != nil {
		return fmt.Errorf("%w: %w", errMistarget, err)
	}
	return nil
}

// run 은 명령 하나를 시한 안에 돌린다(출력 = stdout+stderr).
func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return utils.Run(exec.CommandContext(ctx, name, args...))
}

func kubectl(args ...string) (string, error) {
	if drillKubeconfig == "" {
		return "", errNoKubeconfig
	}
	return run(commandTimeout, "kubectl", append(kubectlBase(), args...)...)
}

// kubectlBase 는 모든 kubectl 호출의 머리 — 훈련장 kubeconfig 명시 · 요청 시한.
func kubectlBase() []string {
	return []string{"--kubeconfig=" + drillKubeconfig, "--request-timeout=" + requestTimeout}
}

// kubectlLong 은 wait·rollout 처럼 스스로 시한을 갖는 명령용.
func kubectlLong(args ...string) (string, error) {
	if drillKubeconfig == "" {
		return "", errNoKubeconfig
	}
	return run(longTimeout, "kubectl", append([]string{"--kubeconfig=" + drillKubeconfig}, args...)...)
}

// kubectlIn 은 stdin 을 넘긴다 — 비밀번호·SQL 을 argv(와 utils.Run 의 명령 줄 로그)에 싣지 않는다.
func kubectlIn(stdin string, args ...string) (string, error) {
	if drillKubeconfig == "" {
		return "", errNoKubeconfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append(kubectlBase(), args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	return utils.Run(cmd)
}

func kubectlJSON(v any, args ...string) error {
	out, err := kubectl(append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	return decodeJSON(out, v)
}

func helm(args ...string) (string, error) {
	if drillKubeconfig == "" {
		return "", errNoKubeconfig
	}
	return run(longTimeout, "helm", append([]string{"--kubeconfig", drillKubeconfig}, args...)...)
}

// pgStop 은 EM0 비상 정지 도구를 훈련장 kubeconfig·context 로 부르고 종료 코드를 낸다(3 = primary 둘 이상).
func pgStop(args ...string) (int, string, error) {
	if err := guardTarget(); err != nil {
		return -1, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), longTimeout)
	defer cancel()
	target := []string{"--context", kindContext, "--namespace", dataNamespace, "--cluster", clusterCR}
	cmd := exec.CommandContext(ctx, pgStopBinary, append(args, target...)...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+drillKubeconfig)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out), nil
	}
	if err != nil {
		return -1, string(out), err
	}
	return 0, string(out), nil
}

func docker(args ...string) (string, error) {
	return run(longTimeout, "docker", args...)
}

func kindBinary() string {
	if v := os.Getenv("KIND"); v != "" {
		return v
	}
	return "kind"
}

func kind(args ...string) (string, error) {
	return run(longTimeout, kindBinary(), args...)
}

// decodeJSON 은 명령 출력의 첫 JSON 값을 읽는다 — 앞에 붙은 경고 줄은 건너뛴다.
func decodeJSON(out string, v any) error {
	start := strings.IndexAny(out, "{[")
	if start < 0 {
		return fmt.Errorf("JSON 없음: %.200q", out)
	}
	return json.NewDecoder(strings.NewReader(out[start:])).Decode(v)
}

// ── 클러스터 수명 ──

func clusterExists() (bool, error) {
	out, err := kind("get", "clusters")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(out) {
		if name == clusterName {
			return true, nil
		}
	}
	return false, nil
}

func createCluster(configPath, kubeconfig string) error {
	_, err := kind("create", "cluster", "--name", clusterName, "--config", configPath, "--kubeconfig", kubeconfig, "--wait", "5m")
	return err
}

// exportKubeconfig 는 재사용(KEEP=1) 클러스터의 자격을 드라이버 전용 파일로 낸다 — 사용자 kubeconfig 무변.
func exportKubeconfig(kubeconfig string) error {
	_, err := kind("export", "kubeconfig", "--name", clusterName, "--kubeconfig", kubeconfig)
	return err
}

func deleteCluster(kubeconfig string) error {
	_, err := kind("delete", "cluster", "--name", clusterName, "--kubeconfig", kubeconfig)
	return err
}

// teardownCluster 는 NFS 손님(pg 워커)을 서버(infra 워커)보다 먼저 내린 뒤 지운다.
// 거꾸로면 손님 postgres 의 NFS CLOSE 가 사라진 서버를 영원히 기다려(D 상태) 노드 컨테이너가
// 지워지지 않는다 — 2026-09-25 실측(rpc_wait_bit_killable, kind delete 가 kill 이벤트를 못 받음).
func teardownCluster(kubeconfig string) error {
	if err := guardTarget(); err != nil {
		return err
	}
	_ = resetRelease()
	out, err := kind("get", "nodes", "--name", clusterName)
	if err != nil {
		return err
	}
	infra, _ := kubectl("get", "nodes", "-l", infraNodeLabel, "-o", "jsonpath={.items[*].metadata.name}")
	for _, node := range strings.Fields(out) {
		if node == controlPlaneNode() || node == infraNodeName(infra) {
			continue
		}
		_ = stopNode(node)
	}
	return deleteCluster(kubeconfig)
}

// infraNodeName 은 라벨로 찾은 infra 노드 — API 가 없으면 kind 가 kind-config 의 다섯째 워커에 붙이는 이름.
func infraNodeName(labelled string) string {
	if name := strings.TrimSpace(labelled); name != "" {
		return name
	}
	return clusterName + "-worker5"
}

func controlPlaneNode() string {
	return clusterName + "-control-plane"
}

// kindSubnetV4 는 kind docker 망의 IPv4 대역 — dataplane NetworkPolicy 의 apiserver ipBlock.
func kindSubnetV4() (string, error) {
	out, err := docker("network", "inspect", kindNetwork, "-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}")
	if err != nil {
		return "", err
	}
	for _, subnet := range strings.Fields(out) {
		if strings.Contains(subnet, ".") {
			return subnet, nil
		}
	}
	return "", fmt.Errorf("kind 망에 IPv4 대역 없음: %q", out)
}

// ── 노드 장애 ──

// cordonNode·uncordonNode 는 S0 사전 배치(0-0·0-1 은 W4, 0-2 는 적격 워커)를 만든다.
func cordonNode(node string) error {
	_, err := kubectl("cordon", node)
	return err
}

func uncordonNode(node string) error {
	_, err := kubectl("uncordon", node)
	return err
}

func pauseNode(node string) error {
	_, err := docker("pause", node)
	return err
}

func unpauseNode(node string) error {
	_, err := docker("unpause", node)
	return err
}

func stopNode(node string) error {
	_, err := docker("stop", node)
	return err
}

func startNode(node string) error {
	_, err := docker("start", node)
	return err
}

func nodeExec(node string, args ...string) (string, error) {
	return docker(append([]string{"exec", node}, args...)...)
}

// containerPID 는 파드 컨테이너의 PID 1 을 노드 PID 공간에서 찾는다(crictl).
func containerPID(node, containerID string) (string, error) {
	id := strings.TrimPrefix(containerID, "containerd://")
	out, err := nodeExec(node, "crictl", "inspect", "--output", "go-template", "--template", "{{.info.pid}}", id)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// signalTree 는 pid 와 그 자식들에게 신호를 보낸다 — postmaster 를 세우면 walreceiver 도 세워야 끊긴다.
func signalTree(node, signal, pid string) error {
	script := fmt.Sprintf("kill -%s %s $(pgrep -P %s) 2>/dev/null || kill -%s %s", signal, pid, pid, signal, pid)
	_, err := nodeExec(node, "sh", "-c", script)
	return err
}

// ── 이미지 ──

// imageRef 는 repo[:tag][@digest] 참조.
type imageRef struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest,omitempty"`
}

func (r imageRef) String() string {
	s := r.Repo
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

func parseImageRef(ref string) (imageRef, error) {
	name, digest, _ := strings.Cut(ref, "@")
	out := imageRef{Repo: name, Digest: digest}
	slash := strings.LastIndex(name, "/")
	if colon := strings.LastIndex(name, ":"); colon > slash {
		out.Repo, out.Tag = name[:colon], name[colon+1:]
	}
	if out.Repo == "" || (out.Tag == "" && out.Digest == "") {
		return imageRef{}, fmt.Errorf("이미지 참조는 repo:tag 또는 repo@digest 여야 한다: %q", ref)
	}
	return out, nil
}

type rawManifest struct {
	MediaType string `json:"mediaType"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// platformDigest 는 인덱스면 platform 자식의 digest, 단일 매니페스트면 참조의 digest 다.
func platformDigest(ref imageRef, platform string) (string, error) {
	out, err := docker("buildx", "imagetools", "inspect", "--raw", ref.String())
	if err != nil {
		return "", err
	}
	var raw rawManifest
	if err := decodeJSON(out, &raw); err != nil {
		return "", err
	}
	if len(raw.Manifests) == 0 {
		if ref.Digest == "" {
			return "", fmt.Errorf("단일 매니페스트는 digest 로 고정해야 한다: %s", ref)
		}
		return ref.Digest, nil
	}
	for _, m := range raw.Manifests {
		if m.Platform.OS+"/"+m.Platform.Architecture == platform {
			return m.Digest, nil
		}
	}
	return "", fmt.Errorf("%s 에 %s 매니페스트 없음", ref, platform)
}

// loadImage 는 platform 매니페스트를 당겨 내용 주소 태그로 전 노드에 싣는다.
// 노드는 digest 참조를 실은 이미지와 맞추지 못한다(repoDigest 가 import 이름으로 남는다) — 그래서 태그로 싣는다.
func loadImage(ref imageRef, platform string) (imageRef, error) {
	digest, err := platformDigest(ref, platform)
	if err != nil {
		return imageRef{}, err
	}
	pinned := imageRef{Repo: ref.Repo, Digest: digest}
	if _, err := docker("pull", "--platform", platform, pinned.String()); err != nil {
		return imageRef{}, err
	}
	local := imageRef{Repo: ref.Repo, Tag: drillTag(digest)}
	if _, err := docker("tag", pinned.String(), local.String()); err != nil {
		return imageRef{}, err
	}
	if err := loadDockerImageToKindForPlatform(local.String(), platform); err != nil {
		return imageRef{}, err
	}
	return local, nil
}

// loadLocalImage 는 이 맥에서 구운 이미지를 그 플랫폼으로 싣는다(helpers.go 의 ctr import).
func loadLocalImage(local imageRef, platform string) error {
	return loadDockerImageToKindForPlatform(local.String(), platform)
}

func drillTag(digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return drillTagPrefix + hex[:min(shortDigestLen, len(hex))]
}

// ── 이 맥 빌드 슬롯(전역 §3.8 — 무거운 빌드 동시 2) ──

const (
	buildSlots      = 2
	buildSlotPoll   = 15 * time.Second
	buildSlotSubdir = "Library/Application Support/image-ship/locks"
)

// holdBuildSlot 은 image-ship 과 같은 flock 슬롯 하나를 잡는다 — 비면 곧바로, 아니면 기다린다.
func holdBuildSlot(deadline time.Time) (release func(), err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, buildSlotSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for time.Now().Before(deadline) {
		for i := range buildSlots {
			if release, ok := tryBuildSlot(filepath.Join(dir, fmt.Sprintf("slot-%d.lock", i))); ok {
				return release, nil
			}
		}
		time.Sleep(buildSlotPoll)
	}
	return nil, errors.New("이 맥 빌드 슬롯을 시한 안에 못 잡았다")
}

func tryBuildSlot(path string) (func(), bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() { _ = f.Close() }, true
}
