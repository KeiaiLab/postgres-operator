//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// HA election distributed lock (K8s Lease) live e2e (D.2.2).
// 시나리오: operator manager 다중 replica 배포 → 1개만 leader 보유 →
// leader Pod kill → 다른 manager 가 lease handoff 후 leader 가 됨.

package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/keiailab/postgres-operator/test/utils"
)

const (
	operatorNS     = "postgres-operator-system"
	leaseName      = "postgres-operator-failover-leader"
	operatorDeploy = "postgres-operator-controller-manager"
)

var _ = Describe("HA election distributed lock (D.2.2)", Ordered, Label("p1"), func() {
	BeforeAll(func() {
		// operator manager 를 2 replica 로 scale (HA 모드 검증).
		_, err := utils.Run(exec.Command("kubectl", "scale", "deployment",
			operatorDeploy, "-n", operatorNS, "--replicas=2"))
		Expect(err).NotTo(HaveOccurred(), "scale operator to 2 replicas")

		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "get", "deploy",
				operatorDeploy, "-n", operatorNS,
				"-o", "jsonpath={.status.readyReplicas}"))
			return strings.TrimSpace(out)
		}, 3*time.Minute, 5*time.Second).Should(Equal("2"))
	})

	AfterAll(func() {
		// 원복: 1 replica.
		_, _ = utils.Run(exec.Command("kubectl", "scale", "deployment",
			operatorDeploy, "-n", operatorNS, "--replicas=1"))
	})

	Context("single-leader 보장", func() {
		It("Lease 리소스 holderIdentity 1개", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "lease",
					leaseName, "-n", operatorNS,
					"-o", "jsonpath={.spec.holderIdentity}"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).ShouldNot(BeEmpty(),
				"holderIdentity 가 1개 Pod 로 설정")
		})

		It("두 Pod 중 1개만 leader 보유", func() {
			// 라이브 RCA (2026-06-16): operator 재배포/scale 직후 Lease.holderIdentity
			// 가 직전 run 의 사라진 Pod 를 ~LeaseDuration(15s) 동안 가리킬 수 있다
			// (옛 holder 만료 후 현재 Pod 가 재획득). holder 가 *현재* 2 Pod 중 정확히
			// 1개로 수렴할 때까지 대기(단발 체크 금지 — execution.md §1.4 deterministic).
			Eventually(func() int {
				out, _ := utils.Run(exec.Command("kubectl", "get", "lease",
					leaseName, "-n", operatorNS,
					"-o", "jsonpath={.spec.holderIdentity}"))
				holder := strings.TrimSpace(out)

				pods, _ := utils.Run(exec.Command("kubectl", "get", "pods",
					"-n", operatorNS, "-l", "app.kubernetes.io/name=postgres-operator",
					"-o", "jsonpath={.items[*].metadata.name}"))
				podList := strings.Fields(pods)
				if len(podList) != 2 {
					return -1
				}
				found := 0
				for _, p := range podList {
					if p == holder {
						found++
					}
				}
				return found
			}, 45*time.Second, 3*time.Second).Should(Equal(1),
				"holderIdentity 가 현재 2 Pod 중 정확히 1개로 수렴")
		})
	})

	Context("Leader handoff (chaos)", func() {
		It("leader Pod kill 후 lease handoff", func() {
			out, _ := utils.Run(exec.Command("kubectl", "get", "lease",
				leaseName, "-n", operatorNS,
				"-o", "jsonpath={.spec.holderIdentity}"))
			oldLeader := strings.TrimSpace(out)
			Expect(oldLeader).NotTo(BeEmpty())

			// leader Pod kill.
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod",
				oldLeader, "-n", operatorNS, "--force", "--grace-period=0"))

			// 새 leader 선출 (LeaseDuration default 15s).
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "lease",
					leaseName, "-n", operatorNS,
					"-o", "jsonpath={.spec.holderIdentity}"))
				newHolder := strings.TrimSpace(out)
				if newHolder == "" || newHolder == oldLeader {
					return ""
				}
				return newHolder
			}, 60*time.Second, 2*time.Second).ShouldNot(BeEmpty(),
				"새 leader 선출 (LeaseDuration default 15s 이내)")

			// 결국 deploy 가 2 replica 복귀.
			Eventually(func() string {
				o, _ := utils.Run(exec.Command("kubectl", "get", "deploy",
					operatorDeploy, "-n", operatorNS,
					"-o", "jsonpath={.status.readyReplicas}"))
				return strings.TrimSpace(o)
			}, 3*time.Minute, 5*time.Second).Should(Equal("2"))
		})

		It("Failover-only lease 가 controller-runtime lease 와 분리 검증", func() {
			// controller-runtime manager 의 leader-election lease 는 별 이름:
			// `<controller-id>` (default `postgres-operator.postgres.keiailab.io`).
			// 본 test 는 두 lease 가 *동일 Pod* 보유 OR 분리 모두 합법.
			// failover-lease 와 manager-lease 이름이 다른 것만 확인.
			out, _ := utils.Run(exec.Command("kubectl", "get", "leases",
				"-n", operatorNS, "-o", "jsonpath={.items[*].metadata.name}"))
			names := strings.Fields(out)
			hasFailover, hasOther := false, false
			for _, n := range names {
				if n == leaseName {
					hasFailover = true
				} else {
					hasOther = true
				}
			}
			Expect(hasFailover).To(BeTrue(), "failover lease 존재")
			Expect(hasOther).To(BeTrue(),
				"manager lease 도 분리 존재 (책임 분리 정합)")
		})
	})
})
