//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Pooler (PgBouncer) live e2e (D.5.4) + exporter live verify (D.5.5).

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
	poolerNamespace     = "pg-pooler-e2e"
	poolerCRName        = "pg-pooler-test"
	poolerWorkloadName  = poolerCRName + "-pooler"
	poolerClusterFor    = "quickstart"
	poolerBuiltinRole   = "keiailab_pooler_pgbouncer"
	poolerImage         = "ghcr.io/cloudnative-pg/pgbouncer:1.24.1"
	poolerExporterImage = "quay.io/prometheuscommunity/pgbouncer-exporter:v0.10.0"
)

var _ = Describe("Pooler PgBouncer live (D.5.4 + D.5.5)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		ensurePostgresClusterReady(poolerNamespace, poolerClusterFor)
		ensureDockerImageLoaded(poolerImage)
		ensureDockerImageLoaded(poolerExporterImage)

		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: Pooler
metadata:
  name: %s
  namespace: %s
spec:
  cluster:
    name: %s
  instances: 2
  type: rw
  pgbouncer:
    image: %s
    poolMode: transaction
    exporter:
      image: %s
      port: 9127
      args:
        - --pgBouncer.connectionString=postgres://%s@127.0.0.1:5432/pgbouncer?sslmode=disable
    parameters:
      auth_type: trust
      stats_users: %s
      max_client_conn: "200"
      default_pool_size: "25"
`, poolerCRName, poolerNamespace, poolerClusterFor, poolerImage, poolerExporterImage, poolerBuiltinRole, poolerBuiltinRole)
		applyManifest(manifest)
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", poolerNamespace, "--wait=false"))
	})

	Context("PgBouncer Deployment + Service reconcile", func() {
		It("Deployment 2/2 Ready", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "deploy",
					poolerWorkloadName, "-n", poolerNamespace,
					"-o", "jsonpath={.status.readyReplicas}"))
				return strings.TrimSpace(out)
			}, 3*time.Minute, 5*time.Second).Should(Equal("2"))
		})

		It("Service psql SELECT 1 PASS (D.5.4)", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "run", "psql-test",
					"-n", poolerNamespace, "--rm", "-i", "--restart=Never",
					"--image=ghcr.io/keiailab/pg:18", "--command", "--",
					"psql", fmt.Sprintf("postgresql://%s@%s/postgres", poolerBuiltinRole, poolerWorkloadName),
					"-t", "-A", "-c", "SELECT 1"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(ContainSubstring("1"))
		})

		It("PAUSE / RESUME 토글", func() {
			// PAUSE.
			_, err := utils.Run(exec.Command("kubectl", "patch", "pooler",
				poolerCRName, "-n", poolerNamespace, "--type=merge",
				"-p", `{"spec":{"paused":true}}`))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "pooler",
					poolerCRName, "-n", poolerNamespace,
					"-o", "jsonpath={.status.paused}"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Equal("true"))
			// RESUME.
			_, _ = utils.Run(exec.Command("kubectl", "patch", "pooler",
				poolerCRName, "-n", poolerNamespace, "--type=merge",
				"-p", `{"spec":{"paused":false}}`))
		})
	})

	Context("PgBouncer exporter live scrape (D.5.5)", func() {
		It("/metrics endpoint pgbouncer_pools 노출", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("deploy/%s", poolerWorkloadName), "-n", poolerNamespace,
				"-c", "pgbouncer-exporter", "--",
				"sh", "-c", "wget -qO- localhost:9127/metrics | grep -c pgbouncer_pools"))
			cnt := strings.TrimSpace(out)
			Expect(cnt).NotTo(Equal("0"), "pgbouncer_pools metric 노출")
		})
	})
})
