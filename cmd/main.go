/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/citus"
	"github.com/keiailab/postgres-operator/internal/controller"
	"github.com/keiailab/postgres-operator/internal/plugin"
	pluginextcitus "github.com/keiailab/postgres-operator/internal/plugin/extension/citus"
	pluginextpgaudit "github.com/keiailab/postgres-operator/internal/plugin/extension/pgaudit"
	pluginextpgcron "github.com/keiailab/postgres-operator/internal/plugin/extension/pgcron"
	pluginextpgnodemx "github.com/keiailab/postgres-operator/internal/plugin/extension/pgnodemx"
	pluginextpgvector "github.com/keiailab/postgres-operator/internal/plugin/extension/pgvector"
	pluginextpostgis "github.com/keiailab/postgres-operator/internal/plugin/extension/postgis"
	pluginextsetuser "github.com/keiailab/postgres-operator/internal/plugin/extension/setuser"
	webhookv1alpha1 "github.com/keiailab/postgres-operator/internal/webhook/v1alpha1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(postgresv1alpha1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "bdce7c33.keiailab.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// Plugin SDK Registry 부트스트랩(ADR 0005). 핵심 reconciler는 본 Registry만
	// 참조하며, 구체 플러그인 패키지는 본 함수에서만 명시적으로 등록된다.
	// 외부 컨트리뷰터의 새 플러그인 추가 = 인터페이스 구현 + 본 위치에 한 줄 추가.
	plugins := plugin.NewRegistry()
	// ADR 0005 권장 SharedPreloadOrder 표 + RFC 0011(P10-T2) 화이트리스트.
	// 추가 ExtensionPlugin 등록은 본 위치에서만, internal/plugin/extension/<name>/
	// 패키지의 Register() 함수만 호출 (구체 import는 depguard로 차단됨).
	pluginextcitus.Register(plugins)    // order=0 — must be first (PGO Issue #3194)
	pluginextpgaudit.Register(plugins)  // order=100
	pluginextpgvector.Register(plugins) // order=100 (AI 차별화)
	pluginextpgcron.Register(plugins)   // order=200
	pluginextpgnodemx.Register(plugins) // order=300 (pgMonitor 의존)
	pluginextpostgis.Register(plugins)  // order=300
	pluginextsetuser.Register(plugins)  // order=300 (PgUser 권한 모델)
	// 향후 P4(BackupPlugin), P6(ExporterPlugin), P7(AuthPlugin), P12(RouterPlugin)
	// 등록 위치.

	// Feature gates는 현재 placeholder. P10-T4(extension version pinning)에서
	// CLI 플래그로 노출된다. PG18 활성화 시 "PostgresEighteen": true 추가.
	featureGates := map[string]bool{}

	// P0-6 phase 2 — LibPQExecutor 환경 변수 기반 opt-in.
	//
	// CITUS_LIBPQ_DSN이 설정되어 있으면 LibPQExecutor를 reconciler에 주입.
	// 미설정 시 reconciler가 NullExecutor를 자동 fallback (P11-M0 spike default,
	// postgrescluster_controller.go:refreshStatus의 r.CitusExec == nil 분기).
	//
	// 본 phase 2a는 single-cluster 환경에 한정. 다중 cluster 지원(ctx-based
	// DSN lookup)은 phase 2b에서. P7 Security/TLS 통합 후 admin Secret 자동
	// 합성으로 환경 변수 의존 제거 예정.
	//
	// 사용 예 (Helm values 또는 Deployment.spec.containers[0].env):
	//   - name: CITUS_LIBPQ_DSN
	//     value: "host=<coord-svc-dns> port=5432 user=postgres dbname=postgres sslmode=require connect_timeout=5"
	//   (실 production은 sslrootcert, password file, application_name 등 추가)
	var citusExec citus.SQLExecutor // nil이면 reconciler가 NullExecutor 자동 사용
	if dsn := os.Getenv("CITUS_LIBPQ_DSN"); dsn != "" {
		setupLog.Info("CITUS_LIBPQ_DSN detected — LibPQExecutor enabled (single-cluster mode, P0-6 phase 2a)")
		citusExec = &citus.LibPQExecutor{
			DSNFunc: func(_ context.Context) (string, error) {
				return dsn, nil
			},
		}
	} else {
		setupLog.Info("CITUS_LIBPQ_DSN not set — using NullExecutor (P11-M0 spike default; pg_dist_node SQL not applied)")
	}

	if err := (&controller.PostgresClusterReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Plugins:      plugins,
		FeatureGates: featureGates,
		CitusExec:    citusExec,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PostgresCluster")
		os.Exit(1)
	}

	// 본 webhook 등록은 webhookCertPath가 설정된 경우(즉 매니페스트 배포 시)에만
	// 의미가 있다. 로컬 `make run` 실행 시에는 webhook 서버가 시작되지 않아도
	// reconciler는 정상 동작한다(K8s API server가 webhook 호출을 시도하지 않으므로).
	if err := webhookv1alpha1.SetupPostgresClusterWebhookWithManager(mgr, featureGates, plugins); err != nil {
		setupLog.Error(err, "Failed to create webhook", "webhook", "PostgresCluster")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
