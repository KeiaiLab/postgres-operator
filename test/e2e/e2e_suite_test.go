//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/keiailab/postgres-operator/test/utils"
)

var (
	// managerImage 는 dist/install.yaml 및 config/manager/kustomization.yaml 의
	// controller image 와 정렬돼야 한다. 동일 tag 재사용 시에도 BeforeSuite 가
	// 이미지를 다시 build/load 하고 rollout을 강제하므로, 세 manifest의 출처가
	// 갈라지면 E2E가 오래된 manager Pod로 실행될 수 있다.
	managerImage = "ghcr.io/keiailab/postgres-operator:0.4.0-beta.8"
	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

const (
	managerNamespace  = "postgres-operator-system"
	managerDeployment = "postgres-operator-controller-manager"
)

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting postgres-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager image")
	// 로컬 kind 노드 arch(arm64 Mac 등) 정합 — operator 이미지를 노드와 동일 arch 로
	// 빌드해야 ImagePull "no match for platform"/not-found 회피. 운영 release 는 amd64 고정.
	cmd := exec.Command("make", "docker-build",
		fmt.Sprintf("IMG=%s", managerImage),
		fmt.Sprintf("PLATFORM=linux/%s", runtime.GOARCH))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	// TODO(user): If you want to change the e2e test vendor from Kind,
	// ensure the image is built and available, then remove the following block.
	By("loading the manager image on Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	By("installing operator + CRDs (kubectl apply --server-side -f dist/install.yaml)")
	// dist/install.yaml 는 build-installer 타겟이 생성한다. e2e go test 의 prerequisites
	// (manifests/generate/fmt/vet) 만으로는 install.yaml 까지 도달하지 않으므로 명시적 호출.
	cmd = exec.Command("make", "build-installer", fmt.Sprintf("IMG=%s", managerImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to run build-installer")

	// 대형 CRD(backupjobs/poolers/scheduledbackups)는 client-side apply 의
	// last-applied-configuration annotation 262144 byte 한계를 초과한다.
	// server-side apply 는 해당 annotation 을 쓰지 않아 한계를 회피한다.
	cmd = exec.Command("kubectl", "apply", "--server-side", "--force-conflicts", "-f", "dist/install.yaml")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to apply dist/install.yaml")

	By("forcing operator manager Deployment rollout for same-tag e2e reruns")
	err = refreshManagerDeployment()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "operator manager Deployment did not roll out")

	By("waiting for operator manager Deployment to become Available")
	cmd = exec.Command("kubectl",
		"-n", managerNamespace,
		"wait", "--for=condition=Available",
		"deployment/"+managerDeployment,
		"--timeout=180s")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "operator manager Deployment did not become Available")

	setupCertManager()
})

var _ = AfterSuite(func() {
	teardownCertManager()
})

func refreshManagerDeployment() error {
	for _, cmd := range managerDeploymentRefreshCommands(managerNamespace, managerDeployment) {
		out, err := utils.Run(cmd)
		if err != nil {
			return fmt.Errorf("refresh manager deployment: %w\n%s", err, out)
		}
	}
	return nil
}

func managerDeploymentRefreshCommands(namespace, deployment string) []*exec.Cmd {
	deployRef := "deployment/" + deployment
	return []*exec.Cmd{
		exec.Command("kubectl",
			"-n", namespace,
			"rollout", "restart",
			deployRef),
		exec.Command("kubectl",
			"-n", namespace,
			"rollout", "status",
			deployRef,
			"--timeout=180s"),
	}
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
