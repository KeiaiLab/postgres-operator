/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/plugin"
	pluginbackuppgbackrest "github.com/keiailab/postgres-operator/internal/plugin/backup/pgbackrest"
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

// leaderElectionAgnosticRunnable runs on every manager replica regardless of
// the controller-runtime manager's own leader election. The failover lease must
// be contested by all replicas (it is a separate lease from the manager lease),
// so its runnable must not be gated behind manager leadership.
type leaderElectionAgnosticRunnable struct {
	start func(context.Context) error
}

func (r leaderElectionAgnosticRunnable) Start(ctx context.Context) error { return r.start(ctx) }
func (r leaderElectionAgnosticRunnable) NeedLeaderElection() bool        { return false }

// failoverIdentity returns a unique-per-Pod identity for the failover lease.
// In-cluster, os.Hostname() returns the Pod name; POD_NAME env overrides for
// explicit Downward-API wiring.
func failoverIdentity() string {
	if v := os.Getenv("POD_NAME"); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "postgres-operator-failover"
}

// operatorNamespace resolves the namespace the operator runs in: POD_NAMESPACE
// env first, then the in-cluster ServiceAccount namespace file.
func operatorNamespace() (string, error) {
	if v := os.Getenv("POD_NAMESPACE"); v != "" {
		return v, nil
	}
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("resolve operator namespace: %w", err)
	}
	ns := strings.TrimSpace(string(data))
	if ns == "" {
		return "", fmt.Errorf("resolve operator namespace: empty ServiceAccount namespace file")
	}
	return ns, nil
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
	// 기본값 true (B-P0-9 cross-cut) — 단일 replica 환경에서도
	// leader-election lease 를 보유하여 graceful shutdown / restart 시 active
	// 단일성 보장. 사용자가 명시적으로 비활성화하려면 --leader-elect=false.
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
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

	webhookEnabled := len(webhookCertPath) > 0
	webhookServerOptions := webhook.Options{
		TLSOpts: tlsOpts,
	}

	if webhookEnabled {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

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

	managerOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
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
	}
	if webhookEnabled {
		managerOptions.WebhookServer = webhook.NewServer(webhookServerOptions)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions)
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
	// 2026-05-03 RFC 0006 R1: Register 는 *operator 가 알 수 있는 extension 의
	// 카탈로그 등록* 일 뿐. 실 활성화는 spec.extensions 에 명시된 cluster 만.
	// 따라서 cluster 가 opt-in 안 하면 vanilla PG 그대로 부팅 (cross-validation
	// bug 2 의 영구 fix).
	pluginextpgaudit.Register(plugins)  // order=100
	pluginextpgvector.Register(plugins) // order=100 (AI 차별화)
	pluginextpgcron.Register(plugins)   // order=200
	pluginextpgnodemx.Register(plugins) // order=300 (pgMonitor 의존)
	pluginextpostgis.Register(plugins)  // order=300
	pluginextsetuser.Register(plugins)  // order=300 (PgUser 권한 모델)
	pluginbackuppgbackrest.Register(plugins)
	// 향후 P6(ExporterPlugin), P7(AuthPlugin), P12(RouterPlugin) 등록 위치.

	// Feature gates는 현재 placeholder. P10-T4(extension version pinning)에서
	// CLI 플래그로 노출된다. PG18 활성화 시 "PostgresEighteen": true 추가.
	featureGates := map[string]bool{}

	// 0.3.0-alpha (ADR 0001): 자체 분산 SQL metadata sync는 RFC 0002 ShardRange CRD
	// + RFC 0004 stateless QueryRouter 로 단계 도입된다. 외부 backend SQL executor
	// 부트스트랩은 ADR 0003 정책상 영구 제거.
	podExecutor, err := controller.NewKubernetesBackupSidecarExecutor(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Failed to create pod exec executor")
		os.Exit(1)
	}

	if err := (&controller.PostgresClusterReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Plugins:      plugins,
		FeatureGates: featureGates,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PostgresCluster")
		os.Exit(1)
	}

	// BackupJob reconciler 등록 (RFC 0004 §3). in-process BackupPlugin 호출과
	// executionMode=job runner Job lifecycle 을 모두 처리한다.
	if err := (&controller.BackupJobReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Plugins:         plugins,
		SidecarExecutor: podExecutor,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "BackupJob")
		os.Exit(1)
	}

	if err := (&controller.PostgresDatabaseReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		SQLExecutor: podExecutor,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PostgresDatabase")
		os.Exit(1)
	}

	if err := (&controller.PostgresUserReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		SQLExecutor: podExecutor,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "PostgresUser")
		os.Exit(1)
	}

	if err := (&controller.ScheduledBackupReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ScheduledBackup")
		os.Exit(1)
	}

	if err := (&controller.ShardSplitJobReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ShardSplitJob")
		os.Exit(1)
	}

	if err := (&controller.PoolerReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		PodExecutor: podExecutor,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "Pooler")
		os.Exit(1)
	}

	// 본 webhook 등록은 webhookCertPath가 설정된 경우(즉 인증서가 준비된 배포)에만
	// 의미가 있다. 기본 Kustomize/Helm 배포는 아직 webhook 인증서를 제공하지 않으므로
	// webhook을 등록하지 않아 smoke 배포가 인증서 부재로 CrashLoopBackOff되지 않게 한다.
	if webhookEnabled {
		if err := webhookv1alpha1.SetupPostgresClusterWebhookWithManager(mgr, featureGates, plugins); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "PostgresCluster")
			os.Exit(1)
		}
	} else {
		setupLog.Info("Skipping webhook setup because webhook-cert-path is not set")
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

	// HA failover leader election (ROADMAP G1 §automatic failover). A dedicated
	// lease (failover.FailoverLeaseName), separate from the controller-runtime
	// manager lease, elects the single operator replica responsible for failover
	// decisions. Registered as a leader-election-agnostic runnable so every
	// replica contests the lease and it hands off when the holder Pod is lost.
	failoverClientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Failed to build clientset for failover lease")
		os.Exit(1)
	}
	failoverNS, err := operatorNamespace()
	if err != nil {
		setupLog.Error(err, "Failed to resolve operator namespace for failover lease")
		os.Exit(1)
	}
	failoverLease, err := failover.NewLease(failover.LeaseConfig{
		Client:    failoverClientset,
		Namespace: failoverNS,
		Identity:  failoverIdentity(),
		OnStartedLeading: func(context.Context) {
			setupLog.Info("Acquired failover leadership", "lease", failover.FailoverLeaseName, "identity", failoverIdentity())
		},
		OnStoppedLeading: func() {
			setupLog.Info("Lost failover leadership", "lease", failover.FailoverLeaseName, "identity", failoverIdentity())
		},
	})
	if err != nil {
		setupLog.Error(err, "Failed to construct failover lease")
		os.Exit(1)
	}
	if err := mgr.Add(leaderElectionAgnosticRunnable{start: func(ctx context.Context) error {
		if rerr := failoverLease.Run(ctx); rerr != nil && ctx.Err() == nil {
			return rerr
		}
		return nil
	}}); err != nil {
		setupLog.Error(err, "Failed to register failover lease runnable")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
