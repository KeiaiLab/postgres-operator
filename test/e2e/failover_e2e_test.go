//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/keiailab/postgres-operator/test/utils"
)

// Failover 시나리오 — RFC 0006 R3 회귀 e2e.
//
// 본 Describe 블록은 R3 (commit chain e501ab4 → 9f416d0) 가 보장하는
// HA 라이프사이클의 자동 회귀 검증이다:
//   - It #1: ord-0 가 초기 primary 로 election.
//   - It #2: ord-1 이 standby 로 부팅 (instance-status.role=replica + standby.signal 존재).
//   - It #3: primary kill → 새 primary 자동 promote, RTO < 30s (RFC 0006 §7 beta 기준).
//   - It #4: 옛 primary 가 K8s 재기동 후 standby 로 rejoin (annotation role=replica + standby.signal),
//     새 primary 의 psql round-trip 정상.
//
// Label("p2") = roadmap §10 Pillar-2 (HA 자동 failover).
// 본 Describe 는 p1 suite 와 *별도 namespace* (pg-failover-e2e) + replicas=1 (Pod 2 개) 사용.
const (
	failoverNamespace      = "pg-failover-e2e"
	failoverClusterName    = "failover"
	failoverPrimaryPodName = "failover-shard-0-0"
	failoverStandbyPodName = "failover-shard-0-1"
	failoverInstanceAnno   = "postgres.keiailab.io/instance-status"
	// replicas=1 + initialCount=1 → primary 1 + async standby 1 = 총 2 Pod.
	// 인라인 manifest — sample 의 replicas=0 과 다르므로 재사용 불가.
	failoverManifest = `apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: failover
  namespace: pg-failover-e2e
spec:
  postgresVersion: "18"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 1
    storage:
      size: 10Gi
`
)

var _ = Describe("Failover", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		// kind/docker 부재 환경 (CI 가 아닌 곳) 에서는 graceful skip.
		if _, err := exec.LookPath("kind"); err != nil {
			Skip("kind binary not found in PATH — failover e2e 는 kind 의존")
		}
		if _, err := exec.LookPath("docker"); err != nil {
			Skip("docker binary not found in PATH — failover e2e 는 docker 의존")
		}

		// p1 suite 의존 차단 — 본 suite 단독 실행 (`make test-e2e-failover` from fresh kind)
		// 시 image 부재로 ErrImagePull 발생을 방지. p1 BeforeAll 와 동일 14-line 블록 복제.
		// (3-번째 e2e 파일 등장 시 test/utils 로 추출 — 현재는 Surgical 원칙 우선.)
		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] Building PG runtime image (Dockerfile.pg, PG_MAJOR=18)\n")
		buildPG := exec.Command("docker", "build",
			"-f", "Dockerfile.pg",
			"--build-arg", "PG_MAJOR=18",
			"-t", pgRuntimeImageLocal, ".")
		_, err := utils.Run(buildPG)
		Expect(err).NotTo(HaveOccurred(), "Failed to build PG runtime image")

		// matrix.go 가 ghcr.io/keiailab/pg:18 을 참조 → tag 동기화.
		tagPG := exec.Command("docker", "tag", pgRuntimeImageLocal, pgRuntimeImageRemote)
		_, err = utils.Run(tagPG)
		Expect(err).NotTo(HaveOccurred(), "Failed to tag PG image to ghcr.io/keiailab/pg:18")

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] Loading PG image into kind cluster\n")
		Expect(utils.LoadImageToKindClusterWithName(pgRuntimeImageRemote)).To(Succeed(),
			"Failed to load PG runtime image into kind")

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] creating namespace %s\n", failoverNamespace)
		// namespace 가 이미 있어도 OK (멱등).
		_, _ = utils.Run(exec.Command("kubectl", "create", "namespace", failoverNamespace))

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] applying CR (replicas=1, 2 Pods total)\n")
		applyCmd := exec.Command("kubectl", "apply", "-n", failoverNamespace, "-f", "-")
		applyCmd.Stdin = strings.NewReader(failoverManifest)
		out, err := utils.Run(applyCmd)
		Expect(err).NotTo(HaveOccurred(), "failed to apply failover CR: %s", out)

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] waiting both Pods Ready=True (timeout 5m)\n")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods",
				"-n", failoverNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", failoverClusterName),
				"-o", "jsonpath={range .items[*]}{.metadata.name}={.status.conditions[?(@.type=='Ready')].status};{end}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			line := strings.TrimSpace(out)
			// 두 Pod 모두 Ready=True 여야 함 — "<pod>=True;<pod>=True;".
			g.Expect(line).To(ContainSubstring(failoverPrimaryPodName+"=True"),
				"ord-0 not Ready: %s", line)
			g.Expect(line).To(ContainSubstring(failoverStandbyPodName+"=True"),
				"ord-1 not Ready: %s", line)
		}, 5*time.Minute, 3*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		// KEEP=1 시 cleanup skip — 로컬 디버그용.
		if os.Getenv("KEEP") == "1" {
			_, _ = fmt.Fprintf(GinkgoWriter, "KEEP=1 — skipping cleanup\n")
			return
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", failoverNamespace,
			"--ignore-not-found", "--wait=false"))
	})

	AfterEach(func() {
		// 실패 시 디버깅 정보 dump — CR yaml + Pods + events + 각 Pod log.
		report := CurrentSpecReport()
		if !report.Failed() {
			return
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] PostgresCluster CR YAML:\n")
		out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster", failoverClusterName,
			"-n", failoverNamespace, "-o", "yaml"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)

		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] Pods (-o wide):\n")
		out, _ = utils.Run(exec.Command("kubectl", "get", "pods", "-n", failoverNamespace, "-o", "wide"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)

		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] Events:\n")
		out, _ = utils.Run(exec.Command("kubectl", "get", "events", "-n", failoverNamespace,
			"--sort-by=.lastTimestamp"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)

		// 각 Pod 의 postgres container log tail.
		for _, podName := range []string{failoverPrimaryPodName, failoverStandbyPodName} {
			_, _ = fmt.Fprintf(GinkgoWriter, "[debug] logs %s -c postgres (--tail=50):\n", podName)
			out, _ = utils.Run(exec.Command("kubectl", "logs",
				"-n", failoverNamespace, podName, "-c", "postgres", "--tail=50"))
			_, _ = fmt.Fprintln(GinkgoWriter, out)
		}
	})

	SetDefaultEventuallyPollingInterval(2 * time.Second)

	// It #1 — 초기 primary 로 ord-0 election.
	It("elects ord-0 as initial primary", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", failoverClusterName,
				"-n", failoverNamespace,
				"-o", "jsonpath={.status.shards[0].primary.pod}={.status.shards[0].primary.ready}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			line := strings.TrimSpace(out)
			g.Expect(line).To(Equal(failoverPrimaryPodName+"=true"),
				"primary not yet ord-0 ready=true: %s", line)
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
	})

	// It #2 — ord-1 이 standby 로 부팅 (annotation role=replica + standby.signal).
	It("spawns ord-1 as standby with role=replica annotation", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pod", failoverStandbyPodName,
				"-n", failoverNamespace,
				"-o", fmt.Sprintf("jsonpath={.metadata.annotations.%s}",
					strings.ReplaceAll(failoverInstanceAnno, ".", `\.`)))
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			raw := strings.TrimSpace(out)
			g.Expect(raw).NotTo(BeEmpty(), "ord-1 instance-status annotation absent")

			var payload map[string]any
			g.Expect(json.Unmarshal([]byte(raw), &payload)).To(Succeed(),
				"instance-status not valid JSON: %q", raw)
			g.Expect(payload["role"]).To(Equal("replica"),
				"ord-1 role expected 'replica', got %v", payload["role"])
		}, 3*time.Minute, 2*time.Second).Should(Succeed())

		// PGDATA/standby.signal 파일 존재 확인 — TaskB 의 pg_basebackup bootstrap 결과.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "exec",
				"-n", failoverNamespace, failoverStandbyPodName,
				"-c", "postgres", "--",
				"ls", "/var/lib/postgresql/data/pgdata/standby.signal")
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "standby.signal not present in ord-1")
		}, 1*time.Minute, 2*time.Second).Should(Succeed())
	})

	// It #3 — primary kill → 새 primary RTO < 30s (RFC 0006 §7 beta 성공 기준).
	// 본 Expect 는 *측정 도구* — R3 가 실 K8s 에서 wired 될 때까지 fail 가능.
	It("promotes new primary within RTO 30s after primary kill", func() {
		// (1) 현재 primary Pod 명 capture.
		var oldPrimary string
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", failoverClusterName,
				"-n", failoverNamespace,
				"-o", "jsonpath={.status.shards[0].primary.pod}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			oldPrimary = strings.TrimSpace(out)
			g.Expect(oldPrimary).NotTo(BeEmpty(), "primary not yet recorded in status")
		}, 1*time.Minute, 2*time.Second).Should(Succeed())

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] killing primary %s\n", oldPrimary)
		// t0 = "operator/사용자가 kill 명령 발행한 시점" 기준 RTO. apiserver round-trip 포함.
		t0 := time.Now()

		// (2) primary force delete (grace 0).
		killCmd := exec.Command("kubectl", "delete", "pod", oldPrimary,
			"--grace-period=0", "--force", "--namespace", failoverNamespace)
		out, err := utils.Run(killCmd)
		Expect(err).NotTo(HaveOccurred(), "failed to delete old primary: %s", out)

		// (3) 새 primary 가 등장할 때까지 대기 (45s window — 30s RTO 측정 여유).
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", failoverClusterName,
				"-n", failoverNamespace,
				"-o", "jsonpath={.status.shards[0].primary.pod}={.status.shards[0].primary.ready}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			line := strings.TrimSpace(out)
			g.Expect(line).To(HaveSuffix("=true"),
				"new primary not ready: %s", line)
			g.Expect(line).NotTo(HavePrefix(oldPrimary+"="),
				"primary still %s (no failover): %s", oldPrimary, line)
		}, 45*time.Second, 1*time.Second).Should(Succeed())

		rto := time.Since(t0)
		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] RTO measured = %s\n", rto)

		// RFC 0006 §7 beta 성공 기준 — 본 Expect 가 RTO 측정 도구 역할.
		Expect(rto).To(BeNumerically("<", 30*time.Second),
			"RFC 0006 §7 beta success criterion: RTO < 30s, got %s", rto)
	})

	// It #4 — 옛 primary 가 standby 로 rejoin + 새 primary psql round-trip.
	It("old primary rejoins as standby after pod restart", func() {
		// (1) 새 primary Pod 명 확인.
		var newPrimary string
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", failoverClusterName,
				"-n", failoverNamespace,
				"-o", "jsonpath={.status.shards[0].primary.pod}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			newPrimary = strings.TrimSpace(out)
			g.Expect(newPrimary).NotTo(BeEmpty())
		}, 1*time.Minute, 2*time.Second).Should(Succeed())

		// (2) 옛 primary 는 ord-0 (StatefulSet ordinal-stable) — 재기동 후 standby 가 되어야 함.
		// new primary 가 ord-0 이면 옛 primary 는 ord-1, 그렇지 않으면 ord-0.
		oldPrimary := failoverPrimaryPodName
		if newPrimary == failoverPrimaryPodName {
			oldPrimary = failoverStandbyPodName
		}

		_, _ = fmt.Fprintf(GinkgoWriter, "[failover] expecting %s to rejoin as standby\n", oldPrimary)

		// (3) 옛 primary 재기동 후 annotation role=replica.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pod", oldPrimary,
				"-n", failoverNamespace,
				"-o", fmt.Sprintf("jsonpath={.metadata.annotations.%s}",
					strings.ReplaceAll(failoverInstanceAnno, ".", `\.`)))
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			raw := strings.TrimSpace(out)
			g.Expect(raw).NotTo(BeEmpty(), "old primary annotation absent (Pod not yet rebooted?)")

			var payload map[string]any
			g.Expect(json.Unmarshal([]byte(raw), &payload)).To(Succeed())
			g.Expect(payload["role"]).To(Equal("replica"),
				"old primary not rejoined as replica, role=%v", payload["role"])
		}, 3*time.Minute, 2*time.Second).Should(Succeed())

		// (4) standby.signal 파일 — TaskA OnStoppedLeading 의 결과.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "exec",
				"-n", failoverNamespace, oldPrimary,
				"-c", "postgres", "--",
				"ls", "/var/lib/postgresql/data/pgdata/standby.signal")
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "standby.signal not present in rejoined pod")
		}, 1*time.Minute, 2*time.Second).Should(Succeed())

		// (5) 새 primary 의 psql round-trip — 실제로 query 처리 가능함을 확인.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "exec",
				"-n", failoverNamespace, newPrimary,
				"-c", "postgres", "--",
				"psql", "-h", "/var/run/postgresql", "-U", "postgres",
				"-tAc", "SELECT 1")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "psql exec failed: %s", out)
			g.Expect(strings.TrimSpace(out)).To(Equal("1"),
				"psql 'SELECT 1' did not return 1: %s", out)
		}, 2*time.Minute, 3*time.Second).Should(Succeed())
	})
})
