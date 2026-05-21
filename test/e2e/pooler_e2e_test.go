//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
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
	poolerNamespace  = "pg-pooler-e2e"
	poolerCRName     = "pg-pooler-test"
	poolerClusterFor = "quickstart"
)

var _ = Describe("Pooler PgBouncer live (D.5.4 + D.5.5)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", poolerNamespace))
		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: Pooler
metadata:
  name: %s
  namespace: %s
spec:
  cluster: %s
  instances: 2
  type: rw
  pgbouncer:
    poolMode: transaction
    parameters:
      max_client_conn: "200"
      default_pool_size: "25"
`, poolerCRName, poolerNamespace, poolerClusterFor)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", poolerNamespace, "--wait=false"))
	})

	Context("PgBouncer Deployment + Service reconcile", func() {
		It("Deployment 2/2 Ready", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "deploy",
					poolerCRName+"-pgbouncer", "-n", poolerNamespace,
					"-o", "jsonpath={.status.readyReplicas}"))
				return strings.TrimSpace(out)
			}, 3*time.Minute, 5*time.Second).Should(Equal("2"))
		})

		It("Service psql SELECT 1 PASS (D.5.4)", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "run", "psql-test",
					"-n", poolerNamespace, "--rm", "-i", "--restart=Never",
					"--image=ghcr.io/keiailab/pg:18", "--",
					"psql", fmt.Sprintf("postgresql://postgres@%s-pgbouncer/postgres", poolerCRName),
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
			pod := poolerCRName + "-pgbouncer"
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("deploy/%s", pod), "-n", poolerNamespace,
				"-c", "exporter", "--",
				"sh", "-c", "wget -qO- localhost:9127/metrics | grep -c pgbouncer_pools"))
			cnt := strings.TrimSpace(out)
			Expect(cnt).NotTo(Equal("0"), "pgbouncer_pools metric 노출")
		})
	})
})
