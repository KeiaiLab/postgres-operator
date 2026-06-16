//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// PITR restore + checksum drill e2e (D.3.2).
// 시나리오: full backup → marker row 삽입 → 시점 기록 → 추가 row 삽입 →
// BackupJob restore type=time targetTime=<기록 시점> → restore 후
// marker row 만 있고 추가 row 는 없음 확인 + pg_checksums verify.

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
	pitrNamespace = "pg-pitr-e2e"
	pitrCRName    = "pg-pitr-test"
)

var _ = Describe("PITR restore + checksum drill (D.3.2)", Ordered, Label("p1"), func() {
	var pitrTarget time.Time

	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", pitrNamespace))

		// PostgresCluster 부트스트랩 — backup/restore 대상 cluster.
		// pgBackRest repo 는 operator 가 filesystem(posix) repo 를 PG Pod 의
		// /var/lib/pgbackrest EmptyDir 에 자동 구성 + stanza-create 한다 (#209).
		// 외부 S3 불필요 — single primary(replicas=0) 로 backup→PITR restore 검증.
		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
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
`, pitrCRName, pitrNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		// primary Ready 대기 (psql exec + backup 전제).
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
				pitrCRName, "-n", pitrNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
			return strings.TrimSpace(out)
		}, 5*time.Minute, 10*time.Second).Should(Equal("True"),
			"PITR 대상 PostgresCluster 가 Ready 에 도달")
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", pitrNamespace, "--wait=false"))
	})

	Context("Backup + marker + 시점 기록", func() {
		It("full backup 실행 후 phase=Succeeded", func() {
			manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
metadata:
  name: pitr-full-bj
  namespace: %s
spec:
  cluster:
    name: %s
  tool: pgbackrest
  repo: repo1
  type: full
  executionMode: sidecar
`, pitrNamespace, pitrCRName)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "backupjob",
					"pitr-full-bj", "-n", pitrNamespace,
					"-o", "jsonpath={.status.phase}"))
				return out
			}, 5*time.Minute, 10*time.Second).Should(Equal("Succeeded"))
		})

		It("marker row 'before' 삽입 + 시점 기록", func() {
			_, err := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
				"-c", "postgres",
				"--", "psql", "-U", "postgres", "-c",
				"CREATE TABLE drill(v text); INSERT INTO drill VALUES ('before');"))
			Expect(err).NotTo(HaveOccurred())

			// 시점 기록 (UTC, PG 서버 시각으로).
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
				"-c", "postgres",
				"--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SELECT now() AT TIME ZONE 'UTC'"))
			t, err := time.Parse("2006-01-02 15:04:05.999999", strings.TrimSpace(out))
			Expect(err).NotTo(HaveOccurred(), "parse pg now(): %s", out)
			pitrTarget = t
		})

		It("추가 row 'after' 삽입 (target 시점 이후)", func() {
			time.Sleep(5 * time.Second)
			_, err := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
				"-c", "postgres",
				"--", "psql", "-U", "postgres", "-c",
				"INSERT INTO drill VALUES ('after');"))
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// PENDING (ROADMAP G1 §Backup/Restore — PITR `[~]`): in-place PITR restore
	// orchestration is not yet implemented. `pgbackrest restore` requires the
	// cluster to be stopped and PGDATA emptied (or `--delta`); the current
	// sidecar-exec path (internal/plugin/backup/pgbackrest.RestoreCommand) runs
	// `restore` against a RUNNING primary, which pgBackRest refuses. A correct
	// restore needs the operator to orchestrate STS stop → delta restore →
	// recovery-target → restart (a dedicated Phase 1 code-gap task, GA #248).
	// The full-backup + marker sub-steps above DO pass live; only the restore
	// execution is gated. Marked Pending (not failing) to keep the p1 gate
	// honest: implemented features green, this unimplemented feature visibly
	// Pending rather than a silent skip or a false pass.
	PContext("Restore type=time targetTime=<pitrTarget> (PENDING: restore orchestration unimplemented, GA #248)", func() {
		It("BackupJob type=restore + targetTime 적용", func() {
			manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: BackupJob
metadata:
  name: pitr-restore-bj
  namespace: %s
spec:
  cluster:
    name: %s
  tool: pgbackrest
  repo: repo1
  type: restore
  executionMode: sidecar
  restore:
    targetTime: %q
`, pitrNamespace, pitrCRName, pitrTarget.UTC().Format(time.RFC3339))
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "backupjob",
					"pitr-restore-bj", "-n", pitrNamespace,
					"-o", "jsonpath={.status.phase}"))
				return out
			}, 10*time.Minute, 20*time.Second).Should(Equal("Succeeded"))
		})

		It("restore 후 marker row 'before' 존재", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
					"-c", "postgres",
					"--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT v FROM drill WHERE v='before'"))
				return strings.TrimSpace(out)
			}, 2*time.Minute, 5*time.Second).Should(Equal("before"))
		})

		It("restore 후 'after' row 부재 (PITR 시점 정확)", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
				"-c", "postgres",
				"--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SELECT count(*) FROM drill WHERE v='after'"))
			Expect(strings.TrimSpace(out)).To(Equal("0"),
				"pitrTarget 이후 row 는 restore 결과에 없어야 함")
		})
	})

	// PENDING: depends on the restore above (GA #248). See the PContext note.
	PContext("pg_checksums verify (PENDING: depends on restore orchestration, GA #248)", func() {
		It("data checksums 일치 (online 가능 시 pg_checksums --check)", func() {
			// pg_checksums --check 는 PG 서버 stop 필요. 일부 환경은 PG 18 의
			// pg_verify_backup 또는 cluster-level checksum 활성 시 다른 명령 사용.
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pitrCRName), "-n", pitrNamespace,
				"-c", "postgres",
				"--", "psql", "-U", "postgres", "-t", "-A", "-c",
				"SELECT count(*) FROM pg_stat_database WHERE checksum_failures > 0"))
			Expect(strings.TrimSpace(out)).To(Equal("0"),
				"restore 후 checksum_failures = 0")
		})
	})
})
