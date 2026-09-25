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

	"github.com/keiailab/postgres-operator/test/utils"
)

// 드라이버 층 — kind·docker·kubectl·helm 명령과 이미지 싣기를 여기에만 둔다.
// 위층(설치·시나리오·관측)은 이 함수들로만 클러스터를 만진다. 명령은 test/utils.Run, 싣기는 helpers.go 로 돈다.
//
//	loopdrill_e2e_test.go ─▶ install / scenarios / observe ─▶ cluster(이 파일) ─▶ kind·docker·kubectl·helm

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
)

// run 은 명령 하나를 시한 안에 돌린다(출력 = stdout+stderr).
func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return utils.Run(exec.CommandContext(ctx, name, args...))
}

func kubectl(args ...string) (string, error) {
	return run(commandTimeout, "kubectl", append([]string{"--request-timeout=" + requestTimeout}, args...)...)
}

// kubectlLong 은 wait·rollout 처럼 스스로 시한을 갖는 명령용.
func kubectlLong(args ...string) (string, error) {
	return run(longTimeout, "kubectl", args...)
}

func kubectlJSON(v any, args ...string) error {
	out, err := kubectl(append(args, "-o", "json")...)
	if err != nil {
		return err
	}
	return decodeJSON(out, v)
}

func helm(args ...string) (string, error) {
	return run(longTimeout, "helm", args...)
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

// infraNodeName 은 라벨로 찾은 infra 노드 — API 가 없으면 kind 가 kind-config 의 넷째 워커에 붙이는 이름.
func infraNodeName(labelled string) string {
	if name := strings.TrimSpace(labelled); name != "" {
		return name
	}
	return clusterName + "-worker4"
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
