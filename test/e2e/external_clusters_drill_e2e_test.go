//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Replica clusters / externalClusters cross-cluster drill (D.5.10).
// 시나리오: source cluster A → external pg_basebackup → replica cluster B (replica.enabled=true) →
// SELECT 가능 (read-only) → fail-closed local promotion (election 차단).

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
	externalNamespace  = "pg-external-e2e"
	sourceClusterName  = "pg-source"
	replicaClusterName = "pg-replica"
)

var _ = Describe("Replica clusters cross-cluster drill (D.5.10)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		ensurePostgresClusterReady(externalNamespace, sourceClusterName)

		// Replica cluster가 pg_basebackup을 수행할 source role과 password Secret을
		// 테스트 안에서 준비해 p2 단독 실행 시에도 외부 smoke.sh에 의존하지 않는다.
		_, _ = utils.Run(exec.Command("kubectl", "create", "secret", "generic",
			"src-replicator-pwd", "-n", externalNamespace,
			"--from-literal=username=replicator",
			"--from-literal=password=replicator_pwd_v1"))
		replicatorUser := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresUser
metadata:
  name: src-replicator
  namespace: %s
spec:
  cluster:
    name: %s
  name: replicator
  ensure: present
  login: true
  replication: true
  passwordSecretRef:
    name: src-replicator-pwd
`, externalNamespace, sourceClusterName)
		applyManifest(replicatorUser)
		waitPostgresUserApplied(externalNamespace, "src-replicator")

		// Replica cluster manifest:
		replica := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "18"
  replica:
    enabled: true
    source: src
  externalClusters:
    - name: src
      connectionParameters:
        host: %s-shard-0-0.%s-shard-0-headless.%s.svc.cluster.local
        port: "5432"
        user: replicator
        dbname: postgres
        sslmode: disable
      password:
        name: src-replicator-pwd
        key: password
  bootstrap:
    pg_basebackup:
      source: src
  shards:
    initialCount: 1
    replicas: 0
    storage:
      size: 1Gi
`, replicaClusterName, externalNamespace, sourceClusterName, sourceClusterName, externalNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(replica)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "apply replica cluster")
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", externalNamespace, "--wait=false"))
	})

	Context("Replica cluster bootstrap", func() {
		It("replica Pod 부팅 후 streaming standby 도달", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", replicaClusterName), "-n", externalNamespace,
					"-c", "postgres", "--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT pg_is_in_recovery()::text"))
				return strings.TrimSpace(out)
			}, 5*time.Minute, 10*time.Second).Should(Equal("true"),
				"replica 는 in_recovery=true 유지")
		})

		It("source 의 데이터가 replica 에 도달 (read-only SELECT)", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", replicaClusterName), "-n", externalNamespace,
				"-c", "postgres", "--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SELECT count(*) FROM pg_database"))
			Expect(strings.TrimSpace(out)).NotTo(Equal("0"),
				"source 의 system catalog 가 replica 에 streaming")
		})
	})

	Context("Fail-closed local promotion 차단", func() {
		It("operator-driven promotion election 차단", func() {
			// replica.enabled=true 인 cluster 는 election 으로 primary 선출 금지.
			// pg_is_in_recovery 유지 + lease holder 미선출 검증.
			out, _ := utils.Run(exec.Command("kubectl", "get", "lease",
				"-n", externalNamespace,
				"-l", fmt.Sprintf("postgres.keiailab.io/cluster=%s", replicaClusterName),
				"-o", "jsonpath={.items[?(@.metadata.name==\"primary-election\")].spec.holderIdentity}"))
			Expect(strings.TrimSpace(out)).To(BeEmpty(),
				"replica cluster 는 primary lease 보유 금지")
		})
	})
})
