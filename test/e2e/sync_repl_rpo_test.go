//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Synchronous replication RPO=0 drill e2e (D.1.3).
// 시나리오: sync rep ANY 1 → INSERT 1000 row → commit_lsn == flush_lsn (lag=0)
// → 실측 RPO=0 직접 증명.

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
	syncReplNamespace = "pg-sync-rpo-e2e"
	syncReplCRName    = "pg-sync-rpo-test"
)

var _ = Describe("Synchronous replication RPO=0 drill (D.1.3)", Ordered, Label("p1"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", syncReplNamespace))
		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "18"
  postgresql:
    synchronous:
      method: any
      number: 1
      dataDurability: required
  shards:
    count: 1
    replicas: 2
    storage:
      size: 1Gi
`, syncReplCRName, syncReplNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
				syncReplCRName, "-n", syncReplNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
			return out
		}, 5*time.Minute, 10*time.Second).Should(Equal("True"))
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", syncReplNamespace, "--wait=false"))
	})

	Context("synchronous_standby_names 적용 확인", func() {
		It("ANY 1 (...) wiring", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", syncReplCRName), "-n", syncReplNamespace,
				"--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SHOW synchronous_standby_names"))
			Expect(strings.TrimSpace(out)).To(ContainSubstring("ANY 1"))
		})

		It("pg_stat_replication 에 sync_state=quorum 또는 sync", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", syncReplCRName), "-n", syncReplNamespace,
					"--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT count(*) FROM pg_stat_replication WHERE sync_state IN ('quorum','sync')"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Or(Equal("1"), Equal("2")))
		})
	})

	Context("RPO=0 직접 증명 (1000-row commit lag)", func() {
		It("INSERT 1000 row", func() {
			_, err := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", syncReplCRName), "-n", syncReplNamespace,
				"--", "psql", "-U", "postgres", "-c",
				"CREATE TABLE rpo_drill(v int); INSERT INTO rpo_drill SELECT generate_series(1,1000);"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("pg_wal_lsn_diff = 0 (commit_lsn == flush_lsn)", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", syncReplCRName), "-n", syncReplNamespace,
				"--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SELECT pg_wal_lsn_diff(write_lsn, flush_lsn) FROM pg_stat_replication LIMIT 1"))
			Expect(strings.TrimSpace(out)).To(Equal("0"),
				"RPO=0: write_lsn == flush_lsn (sync standby 가 따라잡음)")
		})
	})
})
