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
			out, _ := utils.Run(exec.Command("kubectl", "get", "pods",
				"-n", chaosNamespace,
				"-l", fmt.Sprintf("postgres.keiailab.io/cluster=%s,postgres.keiailab.io/instance-role=primary", chaosCRName),
				"-o", "jsonpath={.items[0].metadata.name}"))
			oldPrimary = strings.TrimSpace(out)
			Expect(oldPrimary).NotTo(BeEmpty(), "초기 primary pod 식별")
		})

		It("Primary force delete (chaos)", func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod",
				oldPrimary, "-n", chaosNamespace, "--force",
				"--grace-period=0"))
		})

		It("replica 가 새 primary 로 promotion (RTO < 60s)", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "pods",
					"-n", chaosNamespace,
					"-l", fmt.Sprintf("postgres.keiailab.io/cluster=%s,postgres.keiailab.io/instance-role=primary", chaosCRName),
					"-o", "jsonpath={.items[0].metadata.name}"))
				name := strings.TrimSpace(out)
				if name == "" || name == oldPrimary {
					return ""
				}
				return name
			}, 60*time.Second, 2*time.Second).ShouldNot(BeEmpty(),
				"새 primary 60초 이내 promotion (RTO ≤ 60s SLO)")
		})

		It("이전 primary 가 standby 로 rejoin", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "pod",
					oldPrimary, "-n", chaosNamespace,
					"-o", "jsonpath={.metadata.labels.postgres\\.keiailab\\.io/instance-role}"))
				return strings.TrimSpace(out)
			}, 3*time.Minute, 5*time.Second).Should(Equal("replica"),
				"이전 primary 가 replica 역할로 rejoin")
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
