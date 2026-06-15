//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// ImageCatalog extension + digest + live rollout e2e (D.5.9).

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
	imageCatalogNamespace = "pg-imagecatalog-e2e"
	imageCatalogName      = "pg-catalog"
	imageCatalogCluster   = "pg-ic-cluster"
)

var _ = Describe("ImageCatalog live rollout (D.5.9)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", imageCatalogNamespace))

		catalogManifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: ImageCatalog
metadata:
  name: %s
  namespace: %s
spec:
  images:
    - major: "17"
      image: ghcr.io/keiailab/pg:17
    - major: "18"
      image: ghcr.io/keiailab/pg:18
`, imageCatalogName, imageCatalogNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(catalogManifest)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		clusterManifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  imageCatalogRef:
    apiGroup: postgres.keiailab.io
    kind: ImageCatalog
    name: %s
    major: "17"
  shards:
    initialCount: 1
    replicas: 0
    storage:
      size: 1Gi
`, imageCatalogCluster, imageCatalogNamespace, imageCatalogName)
		cmd2 := exec.Command("kubectl", "apply", "-f", "-")
		cmd2.Stdin = strings.NewReader(clusterManifest)
		_, err = utils.Run(cmd2)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", imageCatalogNamespace, "--wait=false"))
	})

	Context("ImageCatalog ↔ Cluster 통합", func() {
		It("초기 STS image=ghcr.io/keiailab/pg:17", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
					imageCatalogCluster+"-shard-0", "-n", imageCatalogNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[0].image}"))
				return strings.TrimSpace(out)
			}, 3*time.Minute, 5*time.Second).Should(Equal("ghcr.io/keiailab/pg:17"))
		})

		It("Cluster Ready=True", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					imageCatalogCluster, "-n", imageCatalogNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
				return out
			}, 5*time.Minute, 10*time.Second).Should(Equal("True"))
		})
	})

	Context("imageCatalogRef.major 17 → 18 rollout", func() {
		It("Cluster spec.imageCatalogRef.major patch", func() {
			_, err := utils.Run(exec.Command("kubectl", "patch", "postgrescluster",
				imageCatalogCluster, "-n", imageCatalogNamespace, "--type=merge",
				"-p", `{"spec":{"imageCatalogRef":{"major":"18"}}}`))
			Expect(err).NotTo(HaveOccurred())
		})

		It("STS image 18 로 rollout", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
					imageCatalogCluster+"-shard-0", "-n", imageCatalogNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[0].image}"))
				return strings.TrimSpace(out)
			}, 5*time.Minute, 10*time.Second).Should(ContainSubstring("ghcr.io/keiailab/pg:18"))
		})

		It("image-hash annotation drift 추적", func() {
			out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
				imageCatalogCluster+"-shard-0", "-n", imageCatalogNamespace,
				"-o", "jsonpath={.metadata.annotations.postgres\\.keiailab\\.io/image-hash}"))
			Expect(strings.TrimSpace(out)).NotTo(BeEmpty(), "image-hash annotation must be set")
		})
	})
})
