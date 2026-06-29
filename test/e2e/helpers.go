//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/keiailab/postgres-operator/test/utils"
)

type pgRuntimeImageBuild struct {
	once sync.Once
	err  error
}

var pgRuntimeImageMu sync.Mutex
var pgRuntimeImageBuilds = map[string]*pgRuntimeImageBuild{}

type dockerImageLoad struct {
	once sync.Once
	err  error
}

var dockerImageMu sync.Mutex
var dockerImageLoads = map[string]*dockerImageLoad{}

func ensurePGRuntimeImageMajor(major string) {
	pgRuntimeImageMu.Lock()
	build, ok := pgRuntimeImageBuilds[major]
	if !ok {
		build = &pgRuntimeImageBuild{}
		pgRuntimeImageBuilds[major] = build
	}
	pgRuntimeImageMu.Unlock()

	build.once.Do(func() {
		build.err = buildAndLoadPGRuntimeImageMajor(major)
	})
	Expect(build.err).NotTo(HaveOccurred(), "PG runtime image %s must be available in kind", major)
}

func buildAndLoadPGRuntimeImageMajor(major string) error {
	localImage := fmt.Sprintf("local/pg:%s", major)
	remoteImage := fmt.Sprintf("ghcr.io/keiailab/pg:%s", major)

	_, _ = fmt.Fprintf(GinkgoWriter, "[e2e] building PG runtime image major=%s\n", major)
	buildPG := exec.Command("docker", "build",
		"-f", "Dockerfile.pg",
		"--build-arg", "PG_MAJOR="+major,
		"-t", localImage, ".")
	out, err := utils.Run(buildPG)
	if err != nil {
		return fmt.Errorf("build PG runtime image %s: %w\n%s", major, err, out)
	}

	tagPG := exec.Command("docker", "tag", localImage, remoteImage)
	out, err = utils.Run(tagPG)
	if err != nil {
		return fmt.Errorf("tag PG runtime image %s: %w\n%s", major, err, out)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "[e2e] loading PG runtime image %s into kind\n", remoteImage)
	if err := utils.LoadImageToKindClusterWithName(remoteImage); err != nil {
		return fmt.Errorf("load PG runtime image %s into kind: %w", remoteImage, err)
	}
	return nil
}

func ensureDockerImageLoaded(image string) {
	dockerImageMu.Lock()
	load, ok := dockerImageLoads[image]
	if !ok {
		load = &dockerImageLoad{}
		dockerImageLoads[image] = load
	}
	dockerImageMu.Unlock()

	load.once.Do(func() {
		load.err = pullAndLoadDockerImage(image)
	})
	Expect(load.err).NotTo(HaveOccurred(), "container image %s must be available in kind", image)
}

func pullAndLoadDockerImage(image string) error {
	_, _ = fmt.Fprintf(GinkgoWriter, "[e2e] pulling linux/amd64 container image %s\n", image)
	pull := exec.Command("docker", "pull", "--platform", "linux/amd64", image)
	out, err := utils.Run(pull)
	if err != nil {
		return fmt.Errorf("pull container image %s: %w\n%s", image, err, out)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "[e2e] loading container image %s into kind\n", image)
	if err := loadDockerImageToKindForPlatform(image, "linux/amd64"); err != nil {
		return fmt.Errorf("load container image %s into kind: %w", image, err)
	}
	return nil
}

func loadDockerImageToKindForPlatform(image string, platform string) error {
	clusterName := os.Getenv("KIND_CLUSTER")
	if clusterName == "" {
		clusterName = "kind"
	}

	nodesOut, err := utils.Run(exec.Command("kind", "get", "nodes", "--name", clusterName))
	if err != nil {
		return fmt.Errorf("list kind nodes for cluster %q: %w\n%s", clusterName, err, nodesOut)
	}
	nodes := strings.Fields(nodesOut)
	if len(nodes) == 0 {
		return fmt.Errorf("kind cluster %q has no nodes", clusterName)
	}

	for _, node := range nodes {
		if err := importDockerImageToKindNode(image, platform, node); err != nil {
			return err
		}
	}
	return nil
}

func importDockerImageToKindNode(image string, platform string, node string) error {
	saveCmd := exec.Command("docker", "save", image)
	importCmd := exec.Command("docker", "exec", "--privileged", "-i", node,
		"ctr", "--namespace=k8s.io", "images", "import",
		"--platform", platform,
		"--snapshotter=overlayfs",
		"-")

	reader, writer := io.Pipe()
	saveCmd.Stdout = writer
	importCmd.Stdin = reader

	var saveErr bytes.Buffer
	var importOut bytes.Buffer
	saveCmd.Stderr = &saveErr
	importCmd.Stdout = &importOut
	importCmd.Stderr = &importOut

	if err := importCmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("start ctr import on kind node %s: %w", node, err)
	}
	if err := saveCmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = importCmd.Process.Kill()
		_ = importCmd.Wait()
		return fmt.Errorf("start docker save for image %s: %w", image, err)
	}

	saveWaitErr := saveCmd.Wait()
	_ = writer.CloseWithError(saveWaitErr)
	importWaitErr := importCmd.Wait()
	_ = reader.Close()

	if saveWaitErr != nil {
		return fmt.Errorf("docker save image %s: %w\n%s", image, saveWaitErr, saveErr.String())
	}
	if importWaitErr != nil {
		return fmt.Errorf("ctr import image %s on kind node %s: %w\n%s", image, node, importWaitErr, importOut.String())
	}
	return nil
}

func ensurePostgresClusterReady(namespace, name string) {
	ensurePGRuntimeImageMajor("18")

	_, _ = utils.Run(exec.Command("kubectl", "create", "namespace", namespace))

	manifest := fmt.Sprintf(`apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "18"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 0
    storage:
      size: 1Gi
`, name, namespace)
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "apply PostgresCluster %s/%s: %s", namespace, name, out)

	Eventually(func() string {
		out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
			name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
		return strings.TrimSpace(out)
	}, 5*time.Minute, 10*time.Second).Should(Equal("True"),
		"PostgresCluster %s/%s must become Ready", namespace, name)
}

func applyManifest(manifest string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "apply manifest: %s", out)
}

func waitPostgresUserApplied(namespace, name string) {
	Eventually(func() string {
		out, _ := utils.Run(exec.Command("kubectl", "get", "postgresuser",
			name, "-n", namespace,
			"-o", "jsonpath={.status.applied}"))
		return strings.TrimSpace(out)
	}, 2*time.Minute, 5*time.Second).Should(Equal("true"),
		"PostgresUser %s/%s must reach status.applied=true", namespace, name)
}
