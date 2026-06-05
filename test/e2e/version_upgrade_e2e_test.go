//go:build e2e
// +build e2e

/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// version_upgrade_e2e_test.go — Phase 3 P3 의 multi-version e2e (iteration 20).
// PostgreSQL 17 → 18 rolling upgrade 회귀 가드.
//
// 사용자 요구 (2026-05-07): "postgresql 17, 18" 최소 마일스톤 2개 버전 호환 —
// internal/version/matrix.go 가 이미 16/17/18 stable 보유. 본 e2e 는 *runtime
// 동작 검증* — patch propagate + Pod rotation + Phase=Running 복귀.
//
// 가설 A/B/C 회귀 가드 적용.

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
	pgVersionUpgradeNamespace = "pg-version-upgrade-e2e"
	pgVersionUpgradeCRName    = "pg-upgrade-test"
)

var _ = Describe("PostgresCluster Version Upgrade Rolling (Phase 3 P3 / iteration 20)",
	Ordered, Label("p3"), func() {
		BeforeAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "create", "ns", pgVersionUpgradeNamespace))

			manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "17"
  shardingMode: none
  shards:
    initialCount: 1
    replicas: 0
    storage:
      size: 1Gi
`, pgVersionUpgradeCRName, pgVersionUpgradeNamespace)

			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "PostgresCluster CR apply (PG 17)")
		})

		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "postgrescluster",
				pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns",
				pgVersionUpgradeNamespace, "--ignore-not-found"))
		})

		Context("초기 부트스트랩 PG 17", func() {
			It("Ready condition + STS image=ghcr.io/keiailab/pg:17", func() {
				Eventually(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
						pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
					return out
				}, 5*time.Minute, 10*time.Second).Should(Equal("True"))

				stsImage, err := utils.Run(exec.Command("kubectl", "get", "sts",
					"-l", "app.kubernetes.io/instance="+pgVersionUpgradeCRName,
					"-n", pgVersionUpgradeNamespace,
					"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(stsImage).To(Equal("ghcr.io/keiailab/pg:17"),
					"STS image 가 internal/version/matrix.go 의 PG 17 entry 와 정확 일치")
			})
		})

		Context("Patch postgresVersion 17 → 18 (rolling upgrade)", func() {
			It("spec.postgresVersion=18 patch", func() {
				patch := `{"spec":{"postgresVersion":"18"}}`
				_, err := utils.Run(exec.Command("kubectl", "patch", "postgrescluster",
					pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace,
					"--type=merge", "-p", patch))
				Expect(err).NotTo(HaveOccurred(), "17 → 18 patch")
			})

			// 가설 A — STS image propagate.
			It("STS image 가 18 로 propagate", func() {
				Eventually(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
						"-l", "app.kubernetes.io/instance="+pgVersionUpgradeCRName,
						"-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}"))
					return out
				}, 90*time.Second, 5*time.Second).Should(
					ContainSubstring("ghcr.io/keiailab/pg:18"),
					"STS image 가 ghcr.io/keiailab/pg:18 (digest pin 포함) 로 propagate (가설 A)")
			})

			// 가설 C — Pod 재생성 (rolling).
			It("Pod (shard-0 primary) 가 18 image 로 재생성", func() {
				Eventually(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "pods",
						"-l", "app.kubernetes.io/instance="+pgVersionUpgradeCRName,
						"-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.items[0].spec.containers[0].image}"))
					return out
				}, 5*time.Minute, 10*time.Second).Should(
					ContainSubstring("ghcr.io/keiailab/pg:18"),
					"primary Pod 가 새 image (digest pin 포함) 로 재생성 (가설 C)")
			})

			// 가설 B — webhook / defaulter 가 spec 17 으로 reverting 안 함.
			It("CR spec.postgresVersion 18 보존", func() {
				Eventually(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
						pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.spec.postgresVersion}"))
					return out
				}, 30*time.Second, 5*time.Second).Should(
					Equal("18"),
					"CR spec.postgresVersion 이 17 으로 reverting 되지 않음 (가설 B)")
			})

			It("Ready condition=True 복귀", func() {
				Eventually(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
						pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
					return out
				}, 5*time.Minute, 10*time.Second).Should(Equal("True"))
			})
		})

		Context("Unsupported version reject (matrix 화이트리스트)", func() {
			It("postgresVersion=15 patch — IsSupported 거부 기대", func() {
				// internal/version/matrix.go 의 IsSupported 가 15 reject.
				// CRD validation 또는 controller condition error.
				patch := `{"spec":{"postgresVersion":"15"}}`
				_, _ = utils.Run(exec.Command("kubectl", "patch", "postgrescluster",
					pgVersionUpgradeCRName, "-n", pgVersionUpgradeNamespace,
					"--type=merge", "-p", patch))

				// CR patch 가 admission 통과해도 controller 가 reject — STS image
				// 변경 안 됨 (18 유지).
				Consistently(func() string {
					out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
						"-l", "app.kubernetes.io/instance="+pgVersionUpgradeCRName,
						"-n", pgVersionUpgradeNamespace,
						"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}"))
					return out
				}, 30*time.Second, 5*time.Second).Should(
					ContainSubstring("ghcr.io/keiailab/pg:18"),
					"15 patch 에도 STS image 18 (digest pin 포함) 유지 (controller IsSupported 거부)")
			})
		})
	})
