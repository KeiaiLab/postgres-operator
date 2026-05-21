//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// PostgresDatabase CRD live smoke e2e (D.5.6).
// 시나리오: CRD apply → PG 안에 database 생성 + extension/schema/privilege 적용 →
// retain-policy=delete finalizer 검증.

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
	pgDatabaseNamespace = "pg-database-e2e"
	pgDatabaseCRName    = "pg-db-test"
	pgClusterForDB      = "quickstart"
)

var _ = Describe("PostgresDatabase live smoke (D.5.6)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", pgDatabaseNamespace))
		// 전제: PostgresCluster "quickstart" 가 동일 ns 에 Ready=True.
		// 별 BeforeSuite 가 quickstart 를 부트스트랩하거나, smoke.sh 가 선 실행.

		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresDatabase
metadata:
  name: %s
  namespace: %s
spec:
  cluster: %s
  name: app_db
  owner: app_owner
  ensure: present
  databaseReclaimPolicy: delete
  extensions:
    - name: pg_stat_statements
  schemas:
    - name: app
      owner: app_owner
  privileges:
    - schema: app
      grantee: app_reader
      privileges: [USAGE]
`, pgDatabaseCRName, pgDatabaseNamespace, pgClusterForDB)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "apply PostgresDatabase: %s", out)
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", pgDatabaseNamespace, "--wait=false"))
	})

	Context("CRD reconcile + status.applied", func() {
		It("status.applied=true 도달", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgresdatabase",
					pgDatabaseCRName, "-n", pgDatabaseNamespace,
					"-o", "jsonpath={.status.applied}"))
				return out
			}, 2*time.Minute, 5*time.Second).Should(Equal("true"))
		})

		It("PG 안에 app_db database 생성 확인", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgClusterForDB), "-n", pgDatabaseNamespace,
					"--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT 1 FROM pg_database WHERE datname='app_db'"))
				return strings.TrimSpace(out)
			}, 2*time.Minute, 5*time.Second).Should(Equal("1"))
		})

		It("app schema + pg_stat_statements extension 적용 확인", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pgClusterForDB), "-n", pgDatabaseNamespace,
				"--", "psql", "-U", "postgres", "-d", "app_db", "-t", "-A", "-c",
				"SELECT 1 FROM information_schema.schemata WHERE schema_name='app'"))
			Expect(strings.TrimSpace(out)).To(Equal("1"), "app schema must exist")

			out, _ = utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pgClusterForDB), "-n", pgDatabaseNamespace,
				"--", "psql", "-U", "postgres", "-d", "app_db", "-t", "-A", "-c",
				"SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements'"))
			Expect(strings.TrimSpace(out)).To(Equal("1"), "pg_stat_statements extension")
		})
	})

	Context("databaseReclaimPolicy=delete finalizer", func() {
		It("CR 삭제 시 PG database 도 DROP", func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "postgresdatabase",
				pgDatabaseCRName, "-n", pgDatabaseNamespace, "--wait=true",
				"--timeout=60s"))

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgClusterForDB), "-n", pgDatabaseNamespace,
					"--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT count(*) FROM pg_database WHERE datname='app_db'"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Equal("0"),
				"delete reclaim policy → app_db DROP")
		})
	})
})
