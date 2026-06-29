//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// PostgresUser live smoke + password rotation SQL round-trip e2e (D.5.7).

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
	pgUserNamespace = "pg-user-e2e"
	pgUserCRName    = "pg-user-test"
	pgUserSecret    = "pg-user-test-pwd"
	pgClusterForU   = "quickstart"
)

var _ = Describe("PostgresUser live smoke + rotation (D.5.7)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		ensurePostgresClusterReady(pgUserNamespace, pgClusterForU)

		// Secret 사전 생성 (초기 password).
		_, _ = utils.Run(exec.Command("kubectl", "create", "secret", "generic",
			pgUserSecret, "-n", pgUserNamespace,
			"--from-literal=username=app_role",
			"--from-literal=password=initial_pwd_v1"))

		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresUser
metadata:
  name: %s
  namespace: %s
spec:
  cluster:
    name: %s
  name: app_role
  ensure: present
  login: true
  createdb: false
  createrole: false
  passwordSecretRef:
    name: %s
  userReclaimPolicy: delete
  inRoles:
    - postgres
`, pgUserCRName, pgUserNamespace, pgClusterForU, pgUserSecret)
		applyManifest(manifest)
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", pgUserNamespace, "--wait=false"))
	})

	Context("초기 role 생성", func() {
		It("status.applied=true 도달", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgresuser",
					pgUserCRName, "-n", pgUserNamespace,
					"-o", "jsonpath={.status.applied}"))
				return out
			}, 2*time.Minute, 5*time.Second).Should(Equal("true"))
		})

		It("PG pg_roles 에 app_role 존재", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
					"-c", "postgres", "--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT 1 FROM pg_roles WHERE rolname='app_role'"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Equal("1"))
		})

		It("초기 password 로 connect SELECT 1 PASS", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
				"-c", "postgres", "--", "psql", pgUserAppRoleConninfo("initial_pwd_v1"),
				"-t", "-A", "-c", "SELECT 1"))
			Expect(strings.TrimSpace(out)).To(Equal("1"))
		})
	})

	Context("Password rotation SQL round-trip", func() {
		It("Secret password update → 갱신된 password 로 connect PASS", func() {
			// 1. Secret 갱신.
			out, err := utils.Run(exec.Command("kubectl", "patch", "secret", pgUserSecret,
				"-n", pgUserNamespace, "--type=json", "-p",
				`[{"op":"replace","path":"/data/password","value":"`+
					base64Encode("rotated_pwd_v2")+`"}]`))
			Expect(err).NotTo(HaveOccurred(), "patch secret: %s", out)

			// 2. Controller 가 ALTER ROLE 실행 후 status.passwordSecretResourceVersion 갱신.
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
					"-c", "postgres", "--", "psql", pgUserAppRoleConninfo("rotated_pwd_v2"),
					"-t", "-A", "-c", "SELECT 1"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Equal("1"),
				"갱신된 password 로 connect 가능")
		})

		It("이전 password 는 거부", func() {
			out, _ := utils.Run(exec.Command("kubectl", "exec",
				fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
				"-c", "postgres", "--", "psql", pgUserAppRoleConninfo("initial_pwd_v1"),
				"-t", "-A", "-c", "SELECT 1"))
			Expect(out).To(ContainSubstring("authentication failed"),
				"이전 password 인증 거부")
		})
	})

	Context("CR 삭제 시 DROP ROLE", func() {
		It("PG pg_roles 에서 app_role 제거", func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "postgresuser",
				pgUserCRName, "-n", pgUserNamespace, "--wait=true", "--timeout=60s"))

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
					"-c", "postgres", "--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT count(*) FROM pg_roles WHERE rolname='app_role'"))
				return strings.TrimSpace(out)
			}, 1*time.Minute, 5*time.Second).Should(Equal("0"))
		})
	})
})

// base64Encode 는 kubectl patch JSON 의 .data.<key> 에 사용되는 base64 인코딩.
func base64Encode(s string) string {
	// k8s.io/apimachinery 의 base64 import 회피 — stdlib 만 사용.
	return base64stdEncode(s)
}

func pgUserAppRoleConninfo(password string) string {
	out, _ := utils.Run(exec.Command("kubectl", "get", "pod",
		fmt.Sprintf("%s-shard-0-0", pgClusterForU), "-n", pgUserNamespace,
		"-o", "jsonpath={.status.podIP}"))
	podIP := strings.TrimSpace(out)
	Expect(podIP).NotTo(BeEmpty(), "PostgresUser e2e pod IP must be available")
	return fmt.Sprintf("host=%s user=app_role password=%s dbname=postgres", podIP, password)
}

func base64stdEncode(s string) string {
	const t = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	in := []byte(s)
	out := make([]byte, 0, ((len(in)+2)/3)*4)
	for i := 0; i < len(in); i += 3 {
		var b1, b2, b3 byte
		b1 = in[i]
		if i+1 < len(in) {
			b2 = in[i+1]
		}
		if i+2 < len(in) {
			b3 = in[i+2]
		}
		out = append(out, t[b1>>2], t[((b1&0x3)<<4)|(b2>>4)])
		if i+1 < len(in) {
			out = append(out, t[((b2&0xf)<<2)|(b3>>6)])
		} else {
			out = append(out, '=')
		}
		if i+2 < len(in) {
			out = append(out, t[b3&0x3f])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}
