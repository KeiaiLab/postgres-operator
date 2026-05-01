/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
	pluginextcitus "github.com/keiailab/postgres-operator/internal/plugin/extension/citus"
)

// 본 파일은 envtest(in-process K8s API server + etcd)를 사용해 reconciler가
// 실제 K8s API에 대해 desired state를 적용하는지 검증하는 통합 테스트의 setup이다.
//
// envtest는 Docker/Kind 의존이 없으므로 로컬 개발과 CI 양쪽에서 동일하게 동작한다.
// 실 클러스터(Kind 기반) e2e는 test/e2e/ 패키지가 별도 담당한다(Pillar P14
// distribution 검증 시).
//
// envtest 동작 조건:
//   - bin/k8s/<version>/{etcd,kube-apiserver,kubectl} 바이너리 존재
//   - Makefile의 `make test` 타겟이 KUBEBUILDER_ASSETS 환경변수 자동 주입
//   - 바이너리 부재 시 `make setup-envtest` 한 번 실행

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	cancelMgr context.CancelFunc

	// Plugins는 reconciler가 사용하는 Registry. citus extension 1개만 등록한 상태로
	// reconcile 결과 ConfigMap에서 shared_preload_libraries='citus'를 검증한다.
	testPlugins *plugin.Registry
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite (envtest)")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	// 본 경로는 internal/controller/ → 프로젝트 루트로 두 단계.
	// runtime.Caller로 호출 위치 기준 경로를 만들어 KUBEBUILDER_ASSETS와 같이
	// 사용자가 어디서 `go test`를 실행해도 일관되게 CRD를 찾는다.
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	crdPath := filepath.Join(projectRoot, "config", "crd", "bases")

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdPath},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: envtestAssetsPath(projectRoot),
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	Expect(postgresv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	By("starting controller manager")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	testPlugins = plugin.NewRegistry()
	pluginextcitus.Register(testPlugins)

	Expect((&PostgresClusterReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Plugins:      testPlugins,
		FeatureGates: map[string]bool{},
	}).SetupWithManager(mgr)).To(Succeed())

	mgrCtx, cancel := context.WithCancel(context.Background())
	cancelMgr = cancel
	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	By("tearing down test environment")
	if cancelMgr != nil {
		cancelMgr()
		// manager goroutine 종료 대기 — race detector가 깨끗하게 종료되도록.
		time.Sleep(200 * time.Millisecond)
	}
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

func envtestAssetsPath(projectRoot string) string {
	if assetsPath := os.Getenv("KUBEBUILDER_ASSETS"); assetsPath != "" {
		return assetsPath
	}

	candidates, err := filepath.Glob(filepath.Join(projectRoot, "bin", "k8s", "*-"+runtime.GOOS+"-"+runtime.GOARCH))
	if err != nil || len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	for i := len(candidates) - 1; i >= 0; i-- {
		if hasEnvtestBinaries(candidates[i]) {
			return candidates[i]
		}
	}
	return ""
}

func hasEnvtestBinaries(dir string) bool {
	for _, name := range []string{"etcd", "kube-apiserver", "kubectl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
