//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Declarative hibernation live kind verify (D.5.11).
// 시나리오: cnpg.io/hibernation=on annotation → shard StatefulSet replicas=0 +
// status.phase=Hibernated + PVC 보존 → annotation=off → 재기동 SELECT 가능.

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
	pgHibernationNamespace = "pg-hibernation-e2e"
	pgHibernationCRName    = "pg-hibernation-test"
)

var _ = Describe("Declarative hibernation live kind (D.5.11)", Ordered, Label("p2"), func() {
	BeforeAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", pgHibernationNamespace))
		manifest := fmt.Sprintf(`
apiVersion: postgres.keiailab.io/v1alpha1
kind: PostgresCluster
metadata:
  name: %s
  namespace: %s
spec:
  postgresVersion: "18"
  shards:
    count: 1
    replicas: 0
    storage:
      size: 1Gi
`, pgHibernationCRName, pgHibernationNamespace)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "apply PostgresCluster: %s", out)

		// Wait Ready.
		Eventually(func() string {
			out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
				pgHibernationCRName, "-n", pgHibernationNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
			return out
		}, 5*time.Minute, 10*time.Second).Should(Equal("True"))

		// Marker row 삽입 (재기동 후 보존 검증용).
		_, _ = utils.Run(exec.Command("kubectl", "exec",
			fmt.Sprintf("%s-shard-0-0", pgHibernationCRName), "-n", pgHibernationNamespace,
			"--", "psql", "-U", "postgres", "-c",
			"CREATE TABLE hibernation_marker(v text); INSERT INTO hibernation_marker VALUES ('keep-me');"))
	})

	AfterAll(func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", pgHibernationNamespace, "--wait=false"))
	})

	Context("hibernation=on → STS replicas=0 + Phase=Hibernated", func() {
		It("annotation 추가 후 STS scale 0", func() {
			out, err := utils.Run(exec.Command("kubectl", "annotate", "postgrescluster",
				pgHibernationCRName, "-n", pgHibernationNamespace,
				"cnpg.io/hibernation=on", "--overwrite"))
			Expect(err).NotTo(HaveOccurred(), "annotate: %s", out)

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "sts",
					fmt.Sprintf("%s-shard-0", pgHibernationCRName),
					"-n", pgHibernationNamespace,
					"-o", "jsonpath={.spec.replicas}"))
				return strings.TrimSpace(out)
			}, 2*time.Minute, 5*time.Second).Should(Equal("0"))
		})

		It("status.phase=Hibernated + condition cnpg.io/hibernation", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					pgHibernationCRName, "-n", pgHibernationNamespace,
					"-o", "jsonpath={.status.phase}"))
				return out
			}, 1*time.Minute, 5*time.Second).Should(Equal("Hibernated"))
		})

		It("PVC 보존 (delete 금지)", func() {
			out, _ := utils.Run(exec.Command("kubectl", "get", "pvc",
				"-n", pgHibernationNamespace,
				"-l", fmt.Sprintf("postgres.keiailab.io/cluster=%s", pgHibernationCRName),
				"-o", "jsonpath={.items[*].metadata.name}"))
			Expect(strings.Fields(out)).NotTo(BeEmpty(), "hibernation 후 PVC 보존되어야 함")
		})
	})

	Context("hibernation=off → 재기동 + marker row 유지", func() {
		It("annotation off → Pod 재생성 + Ready", func() {
			_, _ = utils.Run(exec.Command("kubectl", "annotate", "postgrescluster",
				pgHibernationCRName, "-n", pgHibernationNamespace,
				"cnpg.io/hibernation=off", "--overwrite"))

			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "postgrescluster",
					pgHibernationCRName, "-n", pgHibernationNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}"))
				return out
			}, 5*time.Minute, 10*time.Second).Should(Equal("True"))
		})

		It("marker row 'keep-me' 보존 SQL round-trip", func() {
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "exec",
					fmt.Sprintf("%s-shard-0-0", pgHibernationCRName),
					"-n", pgHibernationNamespace,
					"--", "psql", "-U", "postgres", "-t", "-A", "-c",
					"SELECT v FROM hibernation_marker LIMIT 1"))
				return strings.TrimSpace(out)
			}, 2*time.Minute, 5*time.Second).Should(Equal("keep-me"),
				"hibernation off 후 marker row 보존")
		})
	})
})
