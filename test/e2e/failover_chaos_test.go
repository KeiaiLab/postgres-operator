//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Replica rejoin chaos drill e2e (D.1.2).
// 시나리오: HA cluster (replicas≥1) → primary 파괴 → replica 자동 promotion →
// 이전 primary 가 신규 standby 로 rejoin (pg_rewind 또는 fresh basebackup).

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/keiailab/postgres-operator/test/utils"
)

const (
	chaosNamespace = "pg-failover-chaos-e2e"
	chaosCRName    = "pg-chaos-test"
	// 라이브 운영 사실: instance manager 가 게시하는 Pod annotation key (statusapi.AnnotationKey).
	// instance-role *label* 은 PG Pod 에 부착되지 않으므로 role 판정은 본 annotation 으로 한다
	// (failover_e2e_test.go p2 와 동일 패턴 — status.shards[0].primary.pod + annotation.role).
	chaosInstanceAnno = "postgres.keiailab.io/instance-status"
)

var _ = Describe("Failover chaos drill (D.1.2)", Ordered, Label("p1"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", chaosNamespace))
		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "18"
  shards:
    initialCount: 1
    replicas: 2
    storage:
      size: 1Gi
`, chaosCRName, chaosNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		// Ready 대기.
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
				chaosCRName, "-n", chaosNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
			return out
		}, 5*time.Minute, 10*time.Second).Should(Equal("True"))
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", chaosNamespace, "--wait=false"))
	})

	Context("Primary kill chaos → 자동 failover", func() {
		var oldPrimary string

		It("초기 primary 식별", func() {
			// 라이브 사실: primary 는 status.shards[0].primary.pod 로 게시된다
			// (instance-role label 미부착 — failover_e2e_test.go p2 와 동일).
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					chaosCRName, "-n", chaosNamespace,
					"-o", "jsonpath={.status.shards[0].primary.pod}"))
				oldPrimary = strings.TrimSpace(out)
				return oldPrimary
			}, 2*time.Minute, 2*time.Second).ShouldNot(BeEmpty(),
				"초기 primary 가 status.shards[0].primary.pod 에 기록")
		})

		It("Primary force delete (chaos)", func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod",
				oldPrimary, "-n", chaosNamespace, "--force",
				"--grace-period=0"))
		})

		It("replica 가 새 primary 로 promotion (RTO < 60s)", func() {
			// 새 primary 가 등장 (oldPrimary 와 다름) + ready=true 가 될 때까지 대기.
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					chaosCRName, "-n", chaosNamespace,
					"-o", "jsonpath={.status.shards[0].primary.pod}={.status.shards[0].primary.ready}"))
				line := strings.TrimSpace(out)
				// "<newPod>=true" 형태 — oldPrimary 가 아니고 ready=true 여야 통과.
				if line == "" || strings.HasPrefix(line, oldPrimary+"=") || !strings.HasSuffix(line, "=true") {
					return ""
				}
				return line
			}, 60*time.Second, 2*time.Second).ShouldNot(BeEmpty(),
				"새 primary 60초 이내 promotion (RTO ≤ 60s SLO)")
		})

		It("이전 primary 가 standby 로 rejoin", func() {
			// 라이브 사실: role 은 instance-status annotation 의 JSON role 필드로 판정.
			// oldPrimary Pod (StatefulSet ordinal-stable) 가 재기동 후 role=replica 여야 함.
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "pod",
					oldPrimary, "-n", chaosNamespace,
					"-o", fmt.Sprintf("jsonpath={.metadata.annotations.%s}",
						strings.ReplaceAll(chaosInstanceAnno, ".", `\.`))))
				g.Expect(err).NotTo(HaveOccurred())
				raw := strings.TrimSpace(out)
				g.Expect(raw).NotTo(BeEmpty(), "이전 primary instance-status annotation 부재 (재기동 전?)")

				var payload map[string]any
				g.Expect(json.Unmarshal([]byte(raw), &payload)).To(Succeed(),
					"instance-status annotation 이 유효 JSON 아님: %q", raw)
				g.Expect(payload["role"]).To(Equal("replica"),
					"이전 primary 가 replica 역할로 rejoin, got role=%v", payload["role"])
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("Cluster Ready=True 복귀", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					chaosCRName, "-n", chaosNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
				return out
			}, 3*time.Minute, 5*time.Second).Should(Equal("True"))
		})
	})
})
