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

// pgClusterE2E 시나리오 — RFC 0006 R1+R2 회귀 테스트.
//
// 본 Describe 블록은 cross-validation 시 발견된 3 개 production bug
// (commit chain df7a0ca → de63519) 의 자동 회귀를 보장한다:
//   - R1: instance manager 가 Pod annotation 으로 status 를 게시한다.
//   - R2: controller 가 Pod annotation 을 watch 하여 PostgresCluster.status.shards[*].primary.endpoint 에 *실제 Pod DNS* 를 반영한다 (placeholder 가 아니다).
//   - 추가: psql round-trip 으로 PG 가 실제로 가용함을 확인한다.
//
// Label("p1") = roadmap §10 Pillar-1 (HA 기본 라이프사이클).
// Ordered = BeforeAll/AfterAll 의존 (image build → CR apply → 검증).
//
// 본 Describe 는 "Manager" Describe 와 *별도 namespace* (pg-e2e) 를 사용해
// 기존 e2e suite (postgres-operator-system) 와 공존한다.
const (
	pgClusterNamespace   = "pg-e2e"
	pgClusterName        = "quickstart"
	pgSampleManifest     = "config/samples/postgres_v1alpha1_postgrescluster_dev.yaml"
	pgInstanceStatusAnno = "postgres.keiailab.io/instance-status"
	// 기본 PG image — matrix.go 가 ghcr.io/keiailab/pg:18 을 reference 한다.
	pgRuntimeImageLocal  = "local/pg:18"
	pgRuntimeImageRemote = "ghcr.io/keiailab/pg:18"
)

var _ = Describe("PostgresCluster", Ordered, Label("p1"), func() {
	BeforeAll(func() {
		// kind binary 부재 환경 (CI 가 아닌 곳) 에서는 graceful skip.
		if _, err := exec.LookPath("kind"); err != nil {
			Skip("kind binary not found in PATH — postgrescluster e2e 시나리오는 kind 의존")
		}
		if _, err := exec.LookPath("docker"); err != nil {
			Skip("docker binary not found in PATH — postgrescluster e2e 는 docker build 의존")
		}

		_, _ = fmt.Fprintf(GinkgoWriter, "[Step 2] Building PG runtime image (Dockerfile.pg, PG_MAJOR=18)\n")
		// PG_MAJOR=18 + provenance/sbom off → kind load 가능한 단일 manifest image.
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

		_, _ = fmt.Fprintf(GinkgoWriter, "[Step 2] Loading PG image into kind cluster\n")
		Expect(utils.LoadImageToKindClusterWithName(pgRuntimeImageRemote)).To(Succeed(),
			"Failed to load PG runtime image into kind")

		_, _ = fmt.Fprintf(GinkgoWriter, "[Step 4] Creating namespace + applying PostgresCluster CR\n")
		// namespace 가 이미 있어도 OK (멱등).
		_, _ = utils.Run(exec.Command("kubectl", "create", "namespace", pgClusterNamespace))

		// sample 의 namespace 는 default — `-n` 으로 override.
		applyCmd := exec.Command("kubectl", "apply",
			"-n", pgClusterNamespace,
			"-f", pgSampleManifest)
		_, err = utils.Run(applyCmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply PostgresCluster sample")
	})

	AfterAll(func() {
		// KEEP=1 시 cleanup skip — 로컬 디버그 시 클러스터 상태 보존.
		if os.Getenv("KEEP") == "1" {
			_, _ = fmt.Fprintf(GinkgoWriter, "KEEP=1 — skipping cleanup\n")
			return
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete",
			"-n", pgClusterNamespace,
			"-f", pgSampleManifest, "--ignore-not-found", "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", pgClusterNamespace,
			"--ignore-not-found", "--wait=false"))
	})

	AfterEach(func() {
		// 실패 시 디버깅 정보 수집 — Pod log + describe + events.
		report := CurrentSpecReport()
		if !report.Failed() {
			return
		}
		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] PostgresCluster CR YAML:\n")
		out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster", pgClusterName,
			"-n", pgClusterNamespace, "-o", "yaml"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)
		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] Pods:\n")
		out, _ = utils.Run(exec.Command("kubectl", "get", "pods", "-n", pgClusterNamespace, "-o", "wide"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)
		_, _ = fmt.Fprintf(GinkgoWriter, "[debug] Events:\n")
		out, _ = utils.Run(exec.Command("kubectl", "get", "events", "-n", pgClusterNamespace,
			"--sort-by=.lastTimestamp"))
		_, _ = fmt.Fprintln(GinkgoWriter, out)
	})

	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	It("creates a StatefulSet for shard-0", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "statefulset",
				"-n", pgClusterNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", pgClusterName),
				"-o", "jsonpath={.items[0].metadata.name}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "shard-0 StatefulSet not yet created")
		}).Should(Succeed())
	})

	It("brings the StatefulSet ReadyReplicas >= 1", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "statefulset",
				"-n", pgClusterNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", pgClusterName),
				"-o", "jsonpath={.items[0].status.readyReplicas}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("1"), "ReadyReplicas != 1")
		}).Should(Succeed())
	})

	It("makes the primary Pod Ready=True", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods",
				"-n", pgClusterNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", pgClusterName),
				"-o", "jsonpath={.items[0].status.conditions[?(@.type=='Ready')].status}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("True"), "Pod Ready != True")
		}).Should(Succeed())
	})

	// RFC 0006 R1 검증: instance manager 가 Pod annotation 에 status 를 게시한다.
	It("publishes valid instance-status annotation with role=primary (RFC 0006 R1)", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods",
				"-n", pgClusterNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", pgClusterName),
				"-o", fmt.Sprintf("jsonpath={.items[0].metadata.annotations.%s}",
					// jsonpath 는 dot 을 escape 해야 함.
					strings.ReplaceAll(pgInstanceStatusAnno, ".", `\.`)))
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			raw := strings.TrimSpace(out)
			g.Expect(raw).NotTo(BeEmpty(), "instance-status annotation absent")

			var payload map[string]any
			g.Expect(json.Unmarshal([]byte(raw), &payload)).To(Succeed(),
				"instance-status annotation is not valid JSON: %q", raw)
			g.Expect(payload["role"]).To(Equal("primary"),
				"instance-status.role expected 'primary', got %v", payload["role"])
		}).Should(Succeed())
	})

	// RFC 0006 R2 검증: controller 가 Pod annotation 을 watch 해 status.shards[0].primary.endpoint 를
	// *실제 Pod DNS* 로 반영한다 (placeholder 'pending' 등이 아니다).
	It("reflects real Pod DNS in PostgresCluster.status.shards[0].primary.endpoint (RFC 0006 R2)", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", pgClusterName,
				"-n", pgClusterNamespace,
				"-o", "jsonpath={.status.shards[0].primary.endpoint}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			endpoint := strings.TrimSpace(out)
			g.Expect(endpoint).NotTo(BeEmpty(), "primary.endpoint not yet populated")
			// placeholder 검출 — 'pending' / 'unknown' / TBD 등이 들어가면 fail.
			lower := strings.ToLower(endpoint)
			for _, bad := range []string{"pending", "unknown", "tbd", "placeholder"} {
				g.Expect(lower).NotTo(ContainSubstring(bad),
					"primary.endpoint contains placeholder %q: %s", bad, endpoint)
			}
			// 실제 Pod DNS 형태 검증 — 헤드리스 svc 안 stable Pod DNS 는 cluster-dns 형식
			// "<pod>.<svc>.<ns>.svc..." 또는 최소한 namespace 가 endpoint 에 포함됨.
			g.Expect(endpoint).To(ContainSubstring(pgClusterNamespace),
				"primary.endpoint missing namespace token: %s", endpoint)
		}).Should(Succeed())
	})

	// 통합 검증: psql round-trip — PG 가 실제로 query 를 처리한다.
	It("accepts SQL queries via local socket inside the Pod", func() {
		var podName string
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods",
				"-n", pgClusterNamespace,
				"-l", fmt.Sprintf("app.kubernetes.io/instance=%s", pgClusterName),
				"-o", "jsonpath={.items[0].metadata.name}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			podName = strings.TrimSpace(out)
			g.Expect(podName).NotTo(BeEmpty())
		}).Should(Succeed())

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "exec",
				"-n", pgClusterNamespace, podName,
				"-c", "postgres",
				"--", "psql", "-h", "/var/run/postgresql", "-U", "postgres",
				"-tAc", "SELECT 1")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "psql exec failed: %s", out)
			g.Expect(strings.TrimSpace(out)).To(Equal("1"), "psql 'SELECT 1' did not return 1")
		}, 3*time.Minute, 5*time.Second).Should(Succeed())
	})

	It("sets PostgresCluster.status.conditions[Ready]=True", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "postgrescluster", pgClusterName,
				"-n", pgClusterNamespace,
				"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.TrimSpace(out)).To(Equal("True"),
				"PostgresCluster Ready condition != True")
		}).Should(Succeed())
	})
})
