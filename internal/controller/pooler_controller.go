/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller 의 Pooler reconciler. CNPG Pooler 핵심 표면을 PgBouncer
// Deployment/Service/ConfigMap 으로 구현한다.
package controller

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"maps"
	"math/big"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonstopology "github.com/keiailab/operator-commons/pkg/topology"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	// poolerBuiltinRoleName 은 spec.pgbouncer.authSecretRef 가 비었을 때 operator
	// 가 PostgresCluster 에 자동 생성하는 PgBouncer 전용 LOGIN role 이름이다.
	// CNPG 의 `cnpg_pooler_pgbouncer` 패턴과 호환되는 keiailab prefix.
	poolerBuiltinRoleName = "keiailab_pooler_pgbouncer"

	// poolerBuiltinAuthSecretSuffix 는 auto-generated userlist.txt Secret 이름의
	// suffix 다. 최종 이름은 `<pooler-name><suffix>` 형식이다.
	poolerBuiltinAuthSecretSuffix = "-builtin-auth"

	// PoolerRotateAuthAnnotation 은 사용자가 built-in auth password 의 force
	// rotation 을 트리거하는 annotation key 다. value 가 `true` 면 reconciler 가
	// 다음 cycle 에서 새 password 를 생성하고 annotation 을 제거하면서
	// status.builtinAuthLastRotation 을 갱신한다.
	PoolerRotateAuthAnnotation = "postgres.keiailab.io/rotate-pooler-password"

	// poolerAutoTLSServerSecretSuffix / poolerAutoTLSClientSecretSuffix 는
	// AutoTLS 가 cert-manager Certificate CR 로 발급하는 Secret 이름의 suffix 다.
	// 사용자가 ServerTLSSecret / ClientTLSSecret 을 명시한 경우 그 값을 우선한다.
	poolerAutoTLSServerSecretSuffix = "-server-tls"
	poolerAutoTLSClientSecretSuffix = "-client-tls"

	// poolerAutoTLSCertificateGroup 는 cert-manager API group + version.
	poolerAutoTLSCertificateGroup   = "cert-manager.io"
	poolerAutoTLSCertificateVersion = "v1"
	poolerAutoTLSCertificateKind    = "Certificate"
)

const (
	defaultPgBouncerListenPort = 5432
	defaultPoolerExporterPort  = 9127
	poolerContainerName        = "pgbouncer"
	poolerExporterName         = "pgbouncer-exporter"
	poolerMetricsPortName      = "metrics"
	poolerConfigDir            = "/etc/pgbouncer/config"
	poolerConfigMountPath      = poolerConfigDir + "/pgbouncer.ini"
	poolerHBAMountPath         = poolerConfigDir + "/pg_hba.conf"
	poolerAuthMountPath        = "/etc/pgbouncer/userlist.txt"
	poolerTLSMountBase         = "/etc/pgbouncer/tls"
	poolerConfigHashFileName   = "config.sha256"
	poolerConfigHashFilePath   = poolerConfigDir + "/" + poolerConfigHashFileName
	poolerConfigHashKey        = "postgres.keiailab.io/pgbouncer-config-sha256"
	poolerPausedAnnotation     = "postgres.keiailab.io/pgbouncer-paused"
	poolerPausedValueTrue      = "true"
	poolerPausedValueFalse     = "false"

	// poolerTargetWaitRequeue is the cadence at which we re-check whether the
	// upstream PostgresCluster has produced a ready backend target for this
	// Pooler. The Watches() registration in SetupWithManager will fire much
	// sooner on a real status flip; this constant is the conservative backup.
	poolerTargetWaitRequeue = 10 * time.Second

	pgBouncerIgnoreStartupParametersKey = "ignore_startup_parameters"
	pgBouncerExtraFloatDigitsParameter  = "extra_float_digits"
	pgBouncerOptionsParameter           = "options"

	PoolerConditionReady            = "Ready"
	PoolerReasonClusterNotFound     = "ClusterNotFound"
	PoolerReasonInvalidSpec         = "InvalidSpec"
	PoolerReasonTargetNotFound      = "TargetNotFound"
	PoolerReasonPausePending        = "PausePending"
	PoolerReasonPauseFailed         = "PauseFailed"
	PoolerReasonConfigReloadPending = "ConfigReloadPending"
	PoolerReasonConfigReloadFailed  = "ConfigReloadFailed"
	PoolerReasonResourcesCreated    = "ResourcesCreated"
	PoolerReasonReady               = "Ready"
)

// PoolerPodExecutor 는 준비된 PgBouncer Pod 안에서 PAUSE/RESUME 신호 명령을 실행한다.
type PoolerPodExecutor interface {
	Exec(ctx context.Context, target BackupSidecarTarget, command []string) ([]byte, error)
}

// PoolerReconciler 는 Pooler CR 을 PgBouncer 하위 리소스로 수렴시킨다.
type PoolerReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PodExecutor PoolerPodExecutor
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=poolers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=poolers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=poolers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 Pooler CR 을 처리한다.
func (r *PoolerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("pooler", req.NamespacedName)

	var pooler postgresv1alpha1.Pooler
	if err := r.Get(ctx, req.NamespacedName, &pooler); err != nil {
		if apierrors.IsNotFound(err) {
			DeletePoolerMetricsFor(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch Pooler")
		return ctrl.Result{}, err
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: pooler.Namespace, Name: pooler.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.markPoolerFailed(&pooler, PoolerReasonClusterNotFound,
				"Referenced PostgresCluster "+pooler.Spec.Cluster.Name+" not found in namespace "+pooler.Namespace)
			return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
		}
		return ctrl.Result{}, err
	}

	if invalid := validatePoolerSpec(&pooler, &cluster); invalid != "" {
		r.markPoolerFailed(&pooler, PoolerReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}
	if err := r.ensurePoolerAutoTLS(ctx, &pooler); err != nil {
		return ctrl.Result{}, err
	}

	if invalid, requeue, err := r.validatePoolerAuthSecret(ctx, &pooler, &cluster); invalid != "" || requeue != nil || err != nil {
		if err != nil {
			return ctrl.Result{}, err
		}
		if requeue != nil {
			// PostgresCluster primary 가 아직 ready 가 아닌 built-in auth path —
			// Pending 상태를 유지하고 짧은 간격 뒤 재시도.
			r.markPoolerPending(&pooler, "BuiltinAuthWaitingForPrimary",
				"PostgresCluster "+cluster.Name+" primary not ready yet — built-in auth role provisioning will retry")
			if statusErr := r.statusUpdate(ctx, &pooler); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return *requeue, nil
		}
		r.markPoolerFailed(&pooler, PoolerReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}
	if invalid, err := r.validatePoolerTLSSecrets(ctx, &pooler); invalid != "" || err != nil {
		if err != nil {
			return ctrl.Result{}, err
		}
		r.markPoolerFailed(&pooler, PoolerReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}

	targets, ok := poolerTargets(&pooler, &cluster)
	if !ok {
		// The cluster has no ready backend target yet — its primary may not be
		// promoted, the replicas may still be starting, etc. We mark the
		// Pooler as Pending (not Failed — it can still converge) and requeue
		// so we re-evaluate when the cluster has had a chance to advance.
		// Combined with the explicit Watches on PostgresCluster in
		// SetupWithManager this also re-enqueues immediately on status flip.
		r.markPoolerPending(&pooler, PoolerReasonTargetNotFound,
			"Waiting for a ready backend target for Pooler type "+string(defaultPoolerType(pooler.Spec.Type)))
		if err := r.statusUpdate(ctx, &pooler); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: poolerTargetWaitRequeue}, nil
	}

	previousConfigHash := pooler.Status.ConfigHash
	config := renderPgBouncerConfig(&pooler, targets)
	hbaConfig := renderPgBouncerHBA(&pooler)
	// auth Secret 의 userlist.txt 도 hash 에 포함 — built-in auth password
	// rotation (T27 ⑥) 시 ConfigHash 가 변경되어 PgBouncer SIGHUP/auto-reload
	// 회로가 자동 trigger 되도록 한다.
	authUserlist, err := r.readPoolerAuthUserlist(ctx, &pooler)
	if err != nil {
		return ctrl.Result{}, err
	}
	configHash := poolerConfigHash(config, hbaConfig, authUserlist)
	if err := r.reconcilePoolerConfigMap(ctx, &pooler, config, hbaConfig, configHash); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcilePoolerDeployment(ctx, &pooler); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcilePoolerPDB(ctx, &pooler); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcilePoolerService(ctx, &pooler); err != nil {
		return ctrl.Result{}, err
	}

	var observed appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: pooler.Namespace, Name: PoolerDeploymentName(pooler.Name)}, &observed); err != nil {
		return ctrl.Result{}, err
	}
	pooler.Status.Instances = defaultPoolerInstances(pooler.Spec.Instances)
	pooler.Status.ReadyReplicas = observed.Status.ReadyReplicas
	pooler.Status.BackendTargets = append([]string{}, targets...)
	pooler.Status.ObservedGeneration = pooler.Generation

	configReloadReady, err := r.reconcilePoolerConfigReload(ctx, &pooler, observed.Status.ReadyReplicas, previousConfigHash, configHash)
	if err != nil {
		pooler.Status.ConfigHash = previousConfigHash
		pooler.Status.Phase = postgresv1alpha1.PoolerFailed
		setPoolerCondition(&pooler, metav1.ConditionFalse, PoolerReasonConfigReloadFailed, err.Error())
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}
	pooler.Status.ConfigHash = poolerStatusConfigHash(previousConfigHash, configHash, configReloadReady)
	if !configReloadReady {
		pooler.Status.Phase = postgresv1alpha1.PoolerPending
		setPoolerCondition(&pooler, metav1.ConditionFalse, PoolerReasonConfigReloadPending,
			fmt.Sprintf("waiting for %d ready PgBouncer pods to reload config hash %s",
				defaultPoolerInstances(pooler.Spec.Instances), configHash))
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}

	paused, pauseReady, err := r.reconcilePoolerPause(ctx, &pooler, observed.Status.ReadyReplicas)
	if err != nil {
		pooler.Status.Paused = paused
		pooler.Status.Phase = postgresv1alpha1.PoolerFailed
		setPoolerCondition(&pooler, metav1.ConditionFalse, PoolerReasonPauseFailed, err.Error())
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}
	pooler.Status.Paused = paused
	if !pauseReady {
		pooler.Status.Phase = postgresv1alpha1.PoolerPending
		setPoolerCondition(&pooler, metav1.ConditionFalse, PoolerReasonPausePending,
			fmt.Sprintf("waiting for %d ready PgBouncer pods to apply paused=%t",
				defaultPoolerInstances(pooler.Spec.Instances), pooler.Spec.Paused))
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}

	if observed.Status.ReadyReplicas >= defaultPoolerInstances(pooler.Spec.Instances) {
		pooler.Status.Phase = postgresv1alpha1.PoolerReady
		message := fmt.Sprintf("%d/%d PgBouncer replicas ready", observed.Status.ReadyReplicas, defaultPoolerInstances(pooler.Spec.Instances))
		if pooler.Status.Paused {
			message += "; PAUSE applied"
		}
		setPoolerCondition(&pooler, metav1.ConditionTrue, PoolerReasonReady, message)
	} else {
		pooler.Status.Phase = postgresv1alpha1.PoolerPending
		setPoolerCondition(&pooler, metav1.ConditionFalse, PoolerReasonResourcesCreated,
			fmt.Sprintf("PgBouncer resources created; %d/%d replicas ready", observed.Status.ReadyReplicas, defaultPoolerInstances(pooler.Spec.Instances)))
	}
	return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
}

func validatePoolerSpec(pooler *postgresv1alpha1.Pooler, cluster *postgresv1alpha1.PostgresCluster) string {
	if pooler.Name == cluster.Name {
		return "Pooler name must differ from referenced PostgresCluster name"
	}
	if strings.TrimSpace(pooler.Spec.PgBouncer.Image) == "" {
		return "Pooler spec.pgbouncer.image is required"
	}
	// AuthSecretRef 가 비어 있으면 built-in auth path (operator 가 PostgresCluster
	// 에 LOGIN role 을 자동 생성 + userlist.txt Secret 자동 생성) 로 진행한다.
	// 명시된 경우에는 기존 user-supplied path 검증이 validatePoolerAuthSecret 에서
	// 별도 수행된다.
	if pooler.Spec.PgBouncer.Exporter != nil && strings.TrimSpace(pooler.Spec.PgBouncer.Exporter.Image) == "" {
		return "Pooler spec.pgbouncer.exporter.image is required when exporter is configured"
	}
	if invalid := validatePgBouncerParameters(pooler.Spec.PgBouncer.Parameters); invalid != "" {
		return invalid
	}
	if invalid := validatePoolerHBA(pooler); invalid != "" {
		return invalid
	}
	mode := defaultPoolerPoolMode(pooler.Spec.PgBouncer.PoolMode)
	switch mode {
	case postgresv1alpha1.PoolerPoolModeSession,
		postgresv1alpha1.PoolerPoolModeTransaction,
		postgresv1alpha1.PoolerPoolModeStatement:
	default:
		return "Pooler spec.pgbouncer.poolMode must be one of session, transaction, statement"
	}
	switch defaultPoolerType(pooler.Spec.Type) {
	case postgresv1alpha1.PoolerTypeRW, postgresv1alpha1.PoolerTypeRO:
	default:
		return "Pooler spec.type must be one of rw, ro"
	}
	return ""
}

func validatePoolerHBA(pooler *postgresv1alpha1.Pooler) string {
	if len(pooler.Spec.PgBouncer.PgHBA) == 0 {
		return ""
	}
	if _, found := pooler.Spec.PgBouncer.Parameters["auth_type"]; found {
		return "Pooler spec.pgbouncer.pg_hba manages auth_type; remove spec.pgbouncer.parameters.auth_type"
	}
	for _, line := range pooler.Spec.PgBouncer.PgHBA {
		if strings.TrimSpace(line) == "" {
			return "Pooler spec.pgbouncer.pg_hba must not contain empty lines"
		}
	}
	return ""
}

func validatePgBouncerParameters(params map[string]string) string {
	for key := range params {
		normalized := strings.TrimSpace(key)
		if normalized == "" {
			return "Pooler spec.pgbouncer.parameters must not contain an empty key"
		}
		if normalized != key {
			return "Pooler spec.pgbouncer.parameters key " + key + " must not contain surrounding whitespace"
		}
		if isOperatorOwnedPgBouncerParameter(key) {
			return "Pooler spec.pgbouncer.parameters." + key + " is managed by the operator"
		}
		if !isSupportedPgBouncerParameter(key) {
			return "Pooler spec.pgbouncer.parameters." + key + " is not in the CNPG-compatible PgBouncer allowlist"
		}
	}
	return ""
}

func isOperatorOwnedPgBouncerParameter(key string) bool {
	switch key {
	case "listen_addr", "listen_port", "auth_file", "auth_hba_file", "pool_mode", "unix_socket_dir":
		return true
	default:
		return false
	}
}

func isSupportedPgBouncerParameter(key string) bool {
	switch key {
	case "auth_type",
		"application_name_add_host",
		"autodb_idle_timeout",
		"cancel_wait_timeout",
		"client_idle_timeout",
		"client_login_timeout",
		"client_tls_ciphers",
		"client_tls_sslmode",
		"client_tls13_ciphers",
		"default_pool_size",
		"disable_pqexec",
		"dns_max_ttl",
		"dns_nxdomain_ttl",
		"idle_transaction_timeout",
		pgBouncerIgnoreStartupParametersKey,
		"listen_backlog",
		"log_connections",
		"log_disconnections",
		"log_pooler_errors",
		"log_stats",
		"max_client_conn",
		"max_db_connections",
		"max_packet_size",
		"max_prepared_statements",
		"max_user_connections",
		"min_pool_size",
		"pkt_buf",
		"query_timeout",
		"query_wait_timeout",
		"reserve_pool_size",
		"reserve_pool_timeout",
		"sbuf_loopcnt",
		"server_check_delay",
		"server_check_query",
		"server_connect_timeout",
		"server_fast_close",
		"server_idle_timeout",
		"server_lifetime",
		"server_login_retry",
		"server_reset_query",
		"server_reset_query_always",
		"server_round_robin",
		"server_tls_ciphers",
		"server_tls13_ciphers",
		"server_tls_protocols",
		"server_tls_sslmode",
		"stats_period",
		"suspend_timeout",
		"tcp_defer_accept",
		"tcp_keepalive",
		"tcp_keepcnt",
		"tcp_keepidle",
		"tcp_keepintvl",
		"tcp_socket_buffer",
		"tcp_user_timeout",
		"track_extra_parameters",
		"verbose":
		return true
	default:
		return false
	}
}

// poolerAuthSecretName 은 Pooler 가 mount 할 userlist.txt Secret 의 이름을 결정한다.
// 사용자가 spec.pgbouncer.authSecretRef.name 을 지정한 경우 그 값, 비어 있으면
// `<pooler-name>-builtin-auth` (operator 가 자동 생성).
func poolerAuthSecretName(pooler *postgresv1alpha1.Pooler) string {
	if pooler.Spec.PgBouncer.AuthSecretRef != nil && strings.TrimSpace(pooler.Spec.PgBouncer.AuthSecretRef.Name) != "" {
		return pooler.Spec.PgBouncer.AuthSecretRef.Name
	}
	return pooler.Name + poolerBuiltinAuthSecretSuffix
}

// poolerAuthIsBuiltin 은 현재 Pooler 가 operator-managed built-in auth 경로를 쓰는지
// 판정한다 (AuthSecretRef 미지정 시 true).
func poolerAuthIsBuiltin(pooler *postgresv1alpha1.Pooler) bool {
	return pooler.Spec.PgBouncer.AuthSecretRef == nil || strings.TrimSpace(pooler.Spec.PgBouncer.AuthSecretRef.Name) == ""
}

// generatePoolerPassword 는 24 byte crypto/rand 를 base64 url-encoding 으로
// 인코딩해 PgBouncer userlist.txt 가 안전하게 보관할 수 있는 평문 password
// 를 만든다. 결과 길이는 32 character.
func generatePoolerPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read crypto/rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// poolerMD5Password 는 PostgreSQL md5 형식 password 를 만든다 — `md5` 접두사
// + md5(password+role) hex. userlist.txt 가 사용하는 형식.
func poolerMD5Password(password, role string) string {
	sum := md5.Sum([]byte(password + role)) //nolint:gosec // PostgreSQL md5 password 형식이 mandate
	return "md5" + hex.EncodeToString(sum[:])
}

// validatePoolerAuthSecret 은 user-supplied 또는 built-in auth path 를 분기 처리한다.
// 결과:
//   - invalid != "" : spec 위반 — caller 가 PoolerFailed 로 mark.
//   - result != nil : reconcile 의 조기 종료 (requeue) 신호.
//   - err != nil    : transient error.
func (r *PoolerReconciler) validatePoolerAuthSecret(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	cluster *postgresv1alpha1.PostgresCluster,
) (string, *ctrl.Result, error) {
	if !poolerAuthIsBuiltin(pooler) {
		var secret corev1.Secret
		key := client.ObjectKey{Namespace: pooler.Namespace, Name: pooler.Spec.PgBouncer.AuthSecretRef.Name}
		if err := r.Get(ctx, key, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "Pooler spec.pgbouncer.authSecretRef.name must reference an existing Secret", nil, nil
			}
			return "", nil, err
		}
		if strings.TrimSpace(string(secret.Data["userlist.txt"])) == "" {
			return "Pooler auth Secret must contain non-empty userlist.txt", nil, nil
		}
		return "", nil, nil
	}
	return r.ensurePoolerBuiltinAuth(ctx, pooler, cluster)
}

// ensurePoolerBuiltinAuth 는 operator-managed userlist.txt Secret 을 동기화한다.
//  1. Secret 이 이미 있으면 OwnerReference 검증 + ready 반환.
//  2. 없으면 PostgresCluster ready primary Pod 의 postgres 컨테이너에서 psql
//     로 `keiailab_pooler_pgbouncer` LOGIN role 을 CREATE/ALTER 한다.
//  3. 생성된 평문 password 의 PostgreSQL md5 형식을 userlist.txt 로 Secret
//     에 기록한다. Secret 는 Pooler OwnerReference 로 자동 GC 된다.
//  4. PostgresCluster primary 가 아직 ready 가 아니면 10s requeue.
func (r *PoolerReconciler) ensurePoolerBuiltinAuth(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	cluster *postgresv1alpha1.PostgresCluster,
) (string, *ctrl.Result, error) {
	secretName := poolerAuthSecretName(pooler)
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: secretName}

	rotateRequested := strings.EqualFold(pooler.Annotations[PoolerRotateAuthAnnotation], "true")

	var existing corev1.Secret
	existingErr := r.Get(ctx, key, &existing)
	switch {
	case existingErr == nil:
		if !metav1.IsControlledBy(&existing, pooler) {
			return "Pooler built-in auth Secret " + secretName + " exists but is not owned by this Pooler — delete it or set spec.pgbouncer.authSecretRef", nil, nil
		}
		if strings.TrimSpace(string(existing.Data["userlist.txt"])) == "" {
			return "Pooler built-in auth Secret " + secretName + " is empty — delete it to let the operator regenerate", nil, nil
		}
		if !rotateRequested {
			return "", nil, nil
		}
		// rotation 요청 — 아래의 신규 password 생성 + ALTER ROLE + Secret update path 진입.
	case apierrors.IsNotFound(existingErr):
		// 첫 생성 — 아래 path.
	default:
		return "", nil, existingErr
	}

	if r.PodExecutor == nil {
		return "Pooler PodExecutor is not configured — built-in auth requires SQL exec capability (cmd/main.go wiring missing)", nil, nil
	}
	target, ok := backupSidecarTarget(cluster)
	if !ok {
		return "", &ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	password, err := generatePoolerPassword()
	if err != nil {
		return "", nil, err
	}
	hashed := poolerMD5Password(password, poolerBuiltinRoleName)

	sql := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
    CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s';
  ELSE
    ALTER ROLE %[1]s WITH LOGIN PASSWORD '%[2]s';
  END IF;
END$$;`, poolerBuiltinRoleName, password)

	if _, execErr := r.PodExecutor.Exec(ctx, target, []string{
		"psql", "-h", "/var/run/postgresql", "-U", "postgres", "-d", "postgres",
		"-v", "ON_ERROR_STOP=1", "-c", sql,
	}); execErr != nil {
		return "", nil, fmt.Errorf("apply Pooler built-in role %s: %w", poolerBuiltinRoleName, execErr)
	}

	userlist := fmt.Sprintf("\"%s\" \"%s\"\n", poolerBuiltinRoleName, hashed)
	desiredLabels := map[string]string{
		"app.kubernetes.io/managed-by": "postgres-operator",
		"postgres.keiailab.io/pooler":  pooler.Name,
		"postgres.keiailab.io/cluster": cluster.Name,
	}

	if existingErr == nil {
		// rotation path — in-place update. OwnerReference 는 이미 우리 소유 (위에서 검증).
		existing.Labels = desiredLabels
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		existing.Data["userlist.txt"] = []byte(userlist)
		if err := r.Update(ctx, &existing); err != nil {
			return "", nil, fmt.Errorf("rotate Pooler built-in auth Secret: %w", err)
		}
	} else {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: pooler.Namespace,
				Labels:    desiredLabels,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"userlist.txt": []byte(userlist),
			},
		}
		if err := controllerutil.SetControllerReference(pooler, secret, r.Scheme); err != nil {
			return "", nil, fmt.Errorf("set OwnerReference on Pooler built-in auth Secret: %w", err)
		}
		if err := r.Create(ctx, secret); err != nil {
			return "", nil, fmt.Errorf("create Pooler built-in auth Secret: %w", err)
		}
	}

	now := metav1.NewTime(time.Now().UTC())
	pooler.Status.BuiltinAuthLastRotation = &now

	if rotateRequested {
		// rotation annotation 제거 — controller 의 spec mutation 은 client.Update 로
		// 처리. status subresource 와 별개 round-trip.
		updated := pooler.DeepCopy()
		delete(updated.Annotations, PoolerRotateAuthAnnotation)
		if err := r.Update(ctx, updated); err != nil && !apierrors.IsConflict(err) {
			return "", nil, fmt.Errorf("clear %s annotation after rotation: %w",
				PoolerRotateAuthAnnotation, err)
		}
		// caller 가 reconcile 의 후속 단계에서 같은 pooler 객체를 더 쓰므로 메모리상
		// annotation 도 정리.
		if pooler.Annotations != nil {
			delete(pooler.Annotations, PoolerRotateAuthAnnotation)
		}
		pooler.ResourceVersion = updated.ResourceVersion
	}

	return "", nil, nil
}

// poolerAutoTLSServerActive 는 AutoTLS 가 server 측 Secret 자동 발급을 수행해야 하는지 판정한다.
// 사용자가 ServerTLSSecret 을 명시한 경우 자동 발급 path 는 비활성.
func poolerAutoTLSServerActive(pooler *postgresv1alpha1.Pooler) bool {
	if pooler.Spec.PgBouncer.AutoTLS == nil {
		return false
	}
	if pooler.Spec.PgBouncer.ServerTLSSecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ServerTLSSecret.Name) != "" {
		return false
	}
	return pooler.Spec.PgBouncer.AutoTLS.ServerEnabled
}

// poolerAutoTLSClientActive 는 AutoTLS 가 client 측 Secret 자동 발급을 수행해야 하는지 판정한다.
func poolerAutoTLSClientActive(pooler *postgresv1alpha1.Pooler) bool {
	if pooler.Spec.PgBouncer.AutoTLS == nil {
		return false
	}
	if pooler.Spec.PgBouncer.ClientTLSSecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ClientTLSSecret.Name) != "" {
		return false
	}
	return pooler.Spec.PgBouncer.AutoTLS.ClientEnabled
}

// poolerAutoTLSServerSecretName 은 AutoTLS server 발급 Secret 이름이다.
func poolerAutoTLSServerSecretName(pooler *postgresv1alpha1.Pooler) string {
	return pooler.Name + poolerAutoTLSServerSecretSuffix
}

// poolerAutoTLSClientSecretName 은 AutoTLS client 발급 Secret 이름이다.
func poolerAutoTLSClientSecretName(pooler *postgresv1alpha1.Pooler) string {
	return pooler.Name + poolerAutoTLSClientSecretSuffix
}

// poolerEffectiveServerTLSSecretName 은 reconcilePoolerDeployment 가 Volume 으로 mount 할 server TLS Secret 이름.
func poolerEffectiveServerTLSSecretName(pooler *postgresv1alpha1.Pooler) string {
	if pooler.Spec.PgBouncer.ServerTLSSecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ServerTLSSecret.Name) != "" {
		return pooler.Spec.PgBouncer.ServerTLSSecret.Name
	}
	if poolerAutoTLSServerActive(pooler) {
		return poolerAutoTLSServerSecretName(pooler)
	}
	return ""
}

// poolerEffectiveServerCASecretName — cert-manager 가 발급한 Secret 은 ca.crt 도 포함하므로
// AutoTLS 시 Server TLS Secret 자체를 CA Secret 으로도 사용한다.
func poolerEffectiveServerCASecretName(pooler *postgresv1alpha1.Pooler) string {
	if pooler.Spec.PgBouncer.ServerCASecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ServerCASecret.Name) != "" {
		return pooler.Spec.PgBouncer.ServerCASecret.Name
	}
	if poolerAutoTLSServerActive(pooler) {
		return poolerAutoTLSServerSecretName(pooler)
	}
	return ""
}

// poolerEffectiveClientTLSSecretName / poolerEffectiveClientCASecretName 은 동일 패턴.
func poolerEffectiveClientTLSSecretName(pooler *postgresv1alpha1.Pooler) string {
	if pooler.Spec.PgBouncer.ClientTLSSecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ClientTLSSecret.Name) != "" {
		return pooler.Spec.PgBouncer.ClientTLSSecret.Name
	}
	if poolerAutoTLSClientActive(pooler) {
		return poolerAutoTLSClientSecretName(pooler)
	}
	return ""
}

func poolerEffectiveClientCASecretName(pooler *postgresv1alpha1.Pooler) string {
	if pooler.Spec.PgBouncer.ClientCASecret != nil && strings.TrimSpace(pooler.Spec.PgBouncer.ClientCASecret.Name) != "" {
		return pooler.Spec.PgBouncer.ClientCASecret.Name
	}
	if poolerAutoTLSClientActive(pooler) {
		return poolerAutoTLSClientSecretName(pooler)
	}
	return ""
}

// poolerAutoTLSDefaultDNSNames 는 Pooler Service 의 in-cluster DNS 형식을 반환한다.
func poolerAutoTLSDefaultDNSNames(pooler *postgresv1alpha1.Pooler) []string {
	svc := PoolerServiceName(pooler.Name)
	return []string{
		svc,
		svc + "." + pooler.Namespace,
		svc + "." + pooler.Namespace + ".svc",
		svc + "." + pooler.Namespace + ".svc.cluster.local",
	}
}

// ensurePoolerAutoTLS 는 cert-manager Certificate CR 두 종 (server / client) 의
// upsert 를 수행한다. cert-manager 가 발급한 Secret 자체가 적용되어 PgBouncer Pod 에
// mount 되기 전까지는 Pooler 의 reconcile 이 Pending 상태로 유지될 수 있다.
func (r *PoolerReconciler) ensurePoolerAutoTLS(ctx context.Context, pooler *postgresv1alpha1.Pooler) error {
	if pooler.Spec.PgBouncer.AutoTLS == nil {
		return nil
	}
	if poolerAutoTLSServerActive(pooler) {
		secretName := poolerAutoTLSServerSecretName(pooler)
		if err := r.applyPoolerAutoCertificate(
			ctx,
			pooler,
			secretName,
			"server",
			[]string{"server auth", "client auth"},
		); err != nil {
			return err
		}
		pooler.Status.AutoTLSServerCertNotAfter = r.readPoolerCertificateNotAfter(ctx, pooler, secretName)
	} else {
		pooler.Status.AutoTLSServerCertNotAfter = nil
	}
	if poolerAutoTLSClientActive(pooler) {
		secretName := poolerAutoTLSClientSecretName(pooler)
		if err := r.applyPoolerAutoCertificate(
			ctx,
			pooler,
			secretName,
			"client",
			[]string{"server auth"},
		); err != nil {
			return err
		}
		pooler.Status.AutoTLSClientCertNotAfter = r.readPoolerCertificateNotAfter(ctx, pooler, secretName)
	} else {
		pooler.Status.AutoTLSClientCertNotAfter = nil
	}
	return nil
}

// readPoolerCertificateNotAfter returns the NotAfter time for the
// currently issued Pooler TLS Secret. For the cert-manager-backed path it
// reads `Certificate.status.notAfter`; for the self-signed path
// (T29 stage 4 — `AutoTLS.SelfSigned=true`) it parses the cert directly
// out of the issued Secret. Returns nil when no source is observable
// yet — the next reconcile will retry. Lookup errors are logged at V(1)
// and treated as "unknown" so a slow cert-manager install or transient
// Secret-read error does not block the rest of the reconcile.
func (r *PoolerReconciler) readPoolerCertificateNotAfter(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	certName string,
) *metav1.Time {
	logger := log.FromContext(ctx)
	// Self-signed path — the Certificate CR does not exist; we read the
	// Secret instead.
	if spec := pooler.Spec.PgBouncer.AutoTLS; spec != nil && spec.SelfSigned {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: pooler.Namespace, Name: certName}, &secret); err != nil {
			logger.V(1).Info("readPoolerCertificateNotAfter: self-signed Secret not yet observable",
				"pooler", pooler.Name, "secret", certName, "error", err.Error())
			return nil
		}
		if cert := parseLeafCertFromPEM(secret.Data[corev1.TLSCertKey]); cert != nil {
			mt := metav1.NewTime(cert.NotAfter)
			return &mt
		}
		return nil
	}
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   poolerAutoTLSCertificateGroup,
		Version: poolerAutoTLSCertificateVersion,
		Kind:    poolerAutoTLSCertificateKind,
	})
	if err := r.Get(ctx, client.ObjectKey{Namespace: pooler.Namespace, Name: certName}, cert); err != nil {
		logger.V(1).Info("readPoolerCertificateNotAfter: Certificate not yet observable",
			"pooler", pooler.Name, "cert", certName, "error", err.Error())
		return nil
	}
	raw, found, err := unstructured.NestedString(cert.Object, "status", "notAfter")
	if err != nil || !found || raw == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		logger.V(1).Info("readPoolerCertificateNotAfter: notAfter unparseable",
			"pooler", pooler.Name, "cert", certName, "raw", raw, "error", err.Error())
		return nil
	}
	mt := metav1.NewTime(parsed)
	return &mt
}

func (r *PoolerReconciler) applyPoolerAutoCertificate(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	secretName string,
	role string,
	usages []string,
) error {
	spec := pooler.Spec.PgBouncer.AutoTLS
	// Stage 4 self-signed path: when SelfSigned=true, do not emit a
	// cert-manager Certificate CR at all — generate and rotate the
	// Secret in-process. The caller's notAfter-mirror logic still works
	// because we set the Secret-embedded cert's NotAfter and
	// read it back via x509 parse.
	if spec.SelfSigned {
		return r.applyPoolerSelfSignedCertificate(ctx, pooler, secretName, role)
	}
	if spec.IssuerRef == nil {
		return fmt.Errorf("AutoTLS spec must have issuerRef or selfSigned set (CRD CEL should have caught this)")
	}
	issuerKind := spec.IssuerRef.Kind
	if issuerKind == "" {
		issuerKind = "Issuer"
	}

	dnsNames := poolerAutoTLSDefaultDNSNames(pooler)
	if len(spec.DNSNames) > 0 {
		seen := map[string]struct{}{}
		merged := make([]string, 0, len(dnsNames)+len(spec.DNSNames))
		for _, d := range append(dnsNames, spec.DNSNames...) {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			merged = append(merged, d)
		}
		dnsNames = merged
	}
	commonName := spec.CommonName
	if commonName == "" {
		commonName = dnsNames[0]
	}

	dnsNamesIface := make([]any, 0, len(dnsNames))
	for _, d := range dnsNames {
		dnsNamesIface = append(dnsNamesIface, d)
	}
	usagesIface := make([]any, 0, len(usages))
	for _, u := range usages {
		usagesIface = append(usagesIface, u)
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   poolerAutoTLSCertificateGroup,
		Version: poolerAutoTLSCertificateVersion,
		Kind:    poolerAutoTLSCertificateKind,
	})
	cert.SetNamespace(pooler.Namespace)
	cert.SetName(secretName)
	cert.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":  "postgres-operator",
		"postgres.keiailab.io/pooler":   pooler.Name,
		"postgres.keiailab.io/cluster":  pooler.Spec.Cluster.Name,
		"postgres.keiailab.io/auto-tls": role,
	})

	if err := unstructured.SetNestedField(cert.Object, secretName, "spec", "secretName"); err != nil {
		return fmt.Errorf("set cert spec.secretName: %w", err)
	}
	if err := unstructured.SetNestedField(cert.Object, commonName, "spec", "commonName"); err != nil {
		return fmt.Errorf("set cert spec.commonName: %w", err)
	}
	if err := unstructured.SetNestedSlice(cert.Object, dnsNamesIface, "spec", "dnsNames"); err != nil {
		return fmt.Errorf("set cert spec.dnsNames: %w", err)
	}
	if err := unstructured.SetNestedSlice(cert.Object, usagesIface, "spec", "usages"); err != nil {
		return fmt.Errorf("set cert spec.usages: %w", err)
	}
	if err := unstructured.SetNestedMap(cert.Object, map[string]any{
		"name": spec.IssuerRef.Name,
		"kind": issuerKind,
	}, "spec", "issuerRef"); err != nil {
		return fmt.Errorf("set cert spec.issuerRef: %w", err)
	}

	if err := controllerutil.SetControllerReference(pooler, cert, r.Scheme); err != nil {
		return fmt.Errorf("set OwnerReference on Pooler AutoTLS Certificate: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(cert.GroupVersionKind())
	key := client.ObjectKey{Namespace: cert.GetNamespace(), Name: cert.GetName()}
	if err := r.Get(ctx, key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get existing Pooler AutoTLS Certificate: %w", err)
		}
		if err := r.Create(ctx, cert); err != nil {
			return fmt.Errorf("create Pooler AutoTLS Certificate: %w", err)
		}
		return nil
	}

	// existing — spec 만 갱신 (resourceVersion / status 보존).
	if existingSpec, ok, _ := unstructured.NestedMap(cert.Object, "spec"); ok {
		if err := unstructured.SetNestedMap(existing.Object, existingSpec, "spec"); err != nil {
			return fmt.Errorf("update Pooler AutoTLS Certificate spec: %w", err)
		}
	}
	labels := cert.GetLabels()
	if labels != nil {
		existing.SetLabels(labels)
	}
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update Pooler AutoTLS Certificate: %w", err)
	}
	return nil
}

// poolerSelfSignedRenewalSkew is the minimum remaining lifetime before
// applyPoolerSelfSignedCertificate regenerates the self-signed cert.
// 30 days ≈ standard 90-day rotation budget minus a buffer for slow
// reconcile triggers.
const poolerSelfSignedRenewalSkew = 30 * 24 * time.Hour

// poolerSelfSignedValidity is the lifetime of a freshly generated
// self-signed cert. 1 year is the practical sweet spot between rotation
// overhead and long-term embed risk.
const poolerSelfSignedValidity = 365 * 24 * time.Hour

// applyPoolerSelfSignedCertificate implements T29 stage 4 — in-process
// generation of a self-signed CA + leaf certificate so the AutoTLS path
// works in environments that don't run cert-manager. Idempotent: when
// the existing Secret's tls.crt parses cleanly and `NotAfter` is more
// than `poolerSelfSignedRenewalSkew` in the future, no regeneration is
// performed and the function returns nil.
func (r *PoolerReconciler) applyPoolerSelfSignedCertificate(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	secretName, role string,
) error {
	logger := log.FromContext(ctx)
	var existing corev1.Secret
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: secretName}
	getErr := r.Get(ctx, key, &existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("get existing self-signed Secret: %w", getErr)
	}

	// Existing-cert reuse window.
	if getErr == nil {
		if cert := parseLeafCertFromPEM(existing.Data["tls.crt"]); cert != nil {
			if time.Until(cert.NotAfter) > poolerSelfSignedRenewalSkew {
				return nil
			}
			logger.Info("self-signed Pooler TLS approaching expiry — regenerating",
				"pooler", pooler.Name, "secret", secretName,
				"notAfter", cert.NotAfter)
		} else {
			logger.Info("self-signed Pooler TLS Secret missing or unparseable tls.crt — regenerating",
				"pooler", pooler.Name, "secret", secretName)
		}
	}

	desired, err := generatePoolerSelfSignedSecret(pooler, secretName, role)
	if err != nil {
		return fmt.Errorf("generate self-signed cert: %w", err)
	}
	if err := controllerutil.SetControllerReference(pooler, desired, r.Scheme); err != nil {
		return fmt.Errorf("set OwnerReference on self-signed Secret: %w", err)
	}
	if apierrors.IsNotFound(getErr) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create self-signed Secret: %w", err)
		}
		return nil
	}
	// Update in place — preserve resourceVersion + immutable fields.
	existing.Data = desired.Data
	existing.Type = desired.Type
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	maps.Copy(existing.Labels, desired.GetLabels())
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("update self-signed Secret: %w", err)
	}
	return nil
}

// parseLeafCertFromPEM returns the first PEM-encoded x509 certificate in
// the given byte slice, or nil when the input is empty / malformed.
func parseLeafCertFromPEM(raw []byte) *x509.Certificate {
	if len(raw) == 0 {
		return nil
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

// generatePoolerSelfSignedSecret produces a fresh self-signed leaf cert
// + matching RSA key + the same cert duplicated as the CA bundle (so the
// rendered Secret layout matches cert-manager's `Certificate`-issued
// Secrets — tls.crt, tls.key, ca.crt). The leaf cert IS its own CA, which
// is acceptable for the dev / pre-prod scope of stage 4.
func generatePoolerSelfSignedSecret(
	pooler *postgresv1alpha1.Pooler,
	secretName, role string,
) (*corev1.Secret, error) {
	dnsNames := poolerAutoTLSDefaultDNSNames(pooler)
	spec := pooler.Spec.PgBouncer.AutoTLS
	if spec != nil && len(spec.DNSNames) > 0 {
		seen := map[string]struct{}{}
		merged := make([]string, 0, len(dnsNames)+len(spec.DNSNames))
		for _, d := range append(dnsNames, spec.DNSNames...) {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			merged = append(merged, d)
		}
		dnsNames = merged
	}
	commonName := ""
	if spec != nil {
		commonName = spec.CommonName
	}
	if commonName == "" && len(dnsNames) > 0 {
		commonName = dnsNames[0]
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("rsa.GenerateKey: %w", err)
	}

	serialMax := new(big.Int).Lsh(big.NewInt(1), 127)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, fmt.Errorf("rand serial: %w", err)
	}
	notBefore := time.Now().UTC().Add(-1 * time.Minute) // clock-skew tolerance
	notAfter := notBefore.Add(poolerSelfSignedValidity)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"keiailab.postgres-operator"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("x509.CreateCertificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("MarshalPKCS8PrivateKey: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: pooler.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":  "postgres-operator",
				"postgres.keiailab.io/pooler":   pooler.Name,
				"postgres.keiailab.io/cluster":  pooler.Spec.Cluster.Name,
				"postgres.keiailab.io/auto-tls": role + "-selfsigned",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       certPEM,
			corev1.TLSPrivateKeyKey: keyPEM,
			"ca.crt":                certPEM,
		},
	}
	return secret, nil
}

func (r *PoolerReconciler) validatePoolerTLSSecrets(ctx context.Context, pooler *postgresv1alpha1.Pooler) (string, error) {
	checks := []struct {
		field string
		ref   *corev1.LocalObjectReference
		keys  []string
	}{
		{field: "serverTLSSecret", ref: pooler.Spec.PgBouncer.ServerTLSSecret, keys: []string{"tls.crt", "tls.key"}},
		{field: "serverCASecret", ref: pooler.Spec.PgBouncer.ServerCASecret, keys: []string{"ca.crt"}},
		{field: "clientTLSSecret", ref: pooler.Spec.PgBouncer.ClientTLSSecret, keys: []string{"tls.crt", "tls.key"}},
		{field: "clientCASecret", ref: pooler.Spec.PgBouncer.ClientCASecret, keys: []string{"ca.crt"}},
	}
	for _, check := range checks {
		invalid, err := r.validatePoolerSecretKeys(ctx, pooler, check.field, check.ref, check.keys...)
		if invalid != "" || err != nil {
			return invalid, err
		}
	}
	return "", nil
}

func (r *PoolerReconciler) validatePoolerSecretKeys(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	field string,
	ref *corev1.LocalObjectReference,
	keys ...string,
) (string, error) {
	if ref == nil {
		return "", nil
	}
	if strings.TrimSpace(ref.Name) == "" {
		return "Pooler spec.pgbouncer." + field + ".name must not be empty", nil
	}
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: ref.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "Pooler spec.pgbouncer." + field + " must reference an existing Secret", nil
		}
		return "", err
	}
	for _, key := range keys {
		if strings.TrimSpace(string(secret.Data[key])) == "" {
			return "Pooler spec.pgbouncer." + field + " Secret must contain non-empty " + key, nil
		}
	}
	return "", nil
}

func (r *PoolerReconciler) reconcilePoolerConfigMap(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	config string,
	hbaConfig string,
	configHash string,
) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      PoolerConfigMapName(pooler.Name),
		Namespace: pooler.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = poolerLabels(pooler)
		cm.Data = map[string]string{
			"pgbouncer.ini":          config,
			poolerConfigHashFileName: configHash,
		}
		if hbaConfig != "" {
			cm.Data["pg_hba.conf"] = hbaConfig
		}
		return controllerutil.SetControllerReference(pooler, cm, r.Scheme)
	})
	return err
}

func (r *PoolerReconciler) reconcilePoolerDeployment(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
) error {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name:      PoolerDeploymentName(pooler.Name),
		Namespace: pooler.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		desired := buildPoolerDeployment(pooler)
		dep.Labels = desired.Labels
		dep.Spec = desired.Spec
		return controllerutil.SetControllerReference(pooler, dep, r.Scheme)
	})
	return err
}

func (r *PoolerReconciler) reconcilePoolerPDB(ctx context.Context, pooler *postgresv1alpha1.Pooler) error {
	instances := defaultPoolerInstances(pooler.Spec.Instances)
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: PoolerPDBName(pooler.Name)}
	if !shouldAutoCreatePDB(instances) {
		var existing policyv1.PodDisruptionBudget
		if err := r.Get(ctx, key, &existing); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		return r.Delete(ctx, &existing)
	}

	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{
		Name:      PoolerPDBName(pooler.Name),
		Namespace: pooler.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		desired := BuildPoolerPDB(pooler, instances)
		pdb.Labels = desired.Labels
		pdb.Spec = desired.Spec
		return controllerutil.SetControllerReference(pooler, pdb, r.Scheme)
	})
	return err
}

func (r *PoolerReconciler) reconcilePoolerService(ctx context.Context, pooler *postgresv1alpha1.Pooler) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:      PoolerServiceName(pooler.Name),
		Namespace: pooler.Namespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		desired := buildPoolerService(pooler)
		svc.Labels = desired.Labels
		svc.Annotations = desired.Annotations
		svc.Spec.Type = desired.Spec.Type
		svc.Spec.Selector = desired.Spec.Selector
		svc.Spec.Ports = desired.Spec.Ports
		return controllerutil.SetControllerReference(pooler, svc, r.Scheme)
	})
	return err
}

func (r *PoolerReconciler) reconcilePoolerConfigReload(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	readyReplicas int32,
	previousHash string,
	desiredHash string,
) (bool, error) {
	if previousHash == "" || previousHash == desiredHash {
		return true, nil
	}
	instances := defaultPoolerInstances(pooler.Spec.Instances)
	if readyReplicas < instances {
		return false, nil
	}

	readyPods, err := r.listReadyPoolerPods(ctx, pooler)
	if err != nil {
		return false, err
	}
	if int32(len(readyPods)) < instances {
		return false, nil
	}

	for i := range readyPods {
		pod := readyPods[i]
		if pod.Annotations[poolerConfigHashKey] == desiredHash {
			continue
		}
		if r.PodExecutor == nil {
			return false, fmt.Errorf("pooler Pod executor is not configured")
		}
		if _, err := r.PodExecutor.Exec(ctx, BackupSidecarTarget{
			Namespace: pod.Namespace,
			Pod:       pod.Name,
			Container: poolerContainerName,
		}, poolerReloadCommand(desiredHash)); err != nil {
			return false, fmt.Errorf("PgBouncer config reload failed on pod %s: %w", pod.Name, err)
		}
		if err := r.patchPoolerPodConfigHash(ctx, &pod, desiredHash); err != nil {
			return false, err
		}
	}
	return true, nil
}

func poolerStatusConfigHash(previousHash string, desiredHash string, reloadReady bool) string {
	if previousHash == "" || reloadReady {
		return desiredHash
	}
	return previousHash
}

func (r *PoolerReconciler) reconcilePoolerPause(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
	readyReplicas int32,
) (bool, bool, error) {
	desiredPaused := pooler.Spec.Paused
	instances := defaultPoolerInstances(pooler.Spec.Instances)
	if !desiredPaused && !pooler.Status.Paused {
		return false, true, nil
	}
	if readyReplicas < instances {
		return pooler.Status.Paused, false, nil
	}

	readyPods, err := r.listReadyPoolerPods(ctx, pooler)
	if err != nil {
		return pooler.Status.Paused, false, err
	}
	if int32(len(readyPods)) < instances {
		return pooler.Status.Paused, false, nil
	}

	for i := range readyPods {
		pod := readyPods[i]
		if poolerPodPaused(&pod) == desiredPaused {
			continue
		}
		if r.PodExecutor == nil {
			return pooler.Status.Paused, false, fmt.Errorf("pooler Pod executor is not configured")
		}
		if _, err := r.PodExecutor.Exec(ctx, BackupSidecarTarget{
			Namespace: pod.Namespace,
			Pod:       pod.Name,
			Container: poolerContainerName,
		}, poolerPauseCommand(desiredPaused)); err != nil {
			return pooler.Status.Paused, false, fmt.Errorf("PgBouncer %s failed on pod %s: %w",
				poolerPauseAction(desiredPaused), pod.Name, err)
		}
		if err := r.patchPoolerPodPaused(ctx, &pod, desiredPaused); err != nil {
			return pooler.Status.Paused, false, err
		}
	}
	return desiredPaused, true, nil
}

func (r *PoolerReconciler) listReadyPoolerPods(
	ctx context.Context,
	pooler *postgresv1alpha1.Pooler,
) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(pooler.Namespace),
		client.MatchingLabels(poolerLabels(pooler)),
	); err != nil {
		return nil, err
	}
	ready := []corev1.Pod{}
	for _, pod := range pods.Items {
		if isPoolerPodReady(&pod) {
			ready = append(ready, pod)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })
	return ready, nil
}

func isPoolerPodReady(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func poolerPodPaused(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	return pod.Annotations[poolerPausedAnnotation] == poolerPausedValueTrue
}

func (r *PoolerReconciler) patchPoolerPodPaused(ctx context.Context, pod *corev1.Pod, paused bool) error {
	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	if paused {
		pod.Annotations[poolerPausedAnnotation] = poolerPausedValueTrue
	} else {
		pod.Annotations[poolerPausedAnnotation] = poolerPausedValueFalse
	}
	return r.Patch(ctx, pod, client.MergeFrom(before))
}

func (r *PoolerReconciler) patchPoolerPodConfigHash(ctx context.Context, pod *corev1.Pod, configHash string) error {
	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[poolerConfigHashKey] = configHash
	return r.Patch(ctx, pod, client.MergeFrom(before))
}

func poolerReloadCommand(configHash string) []string {
	return []string{"/bin/sh", "-ec", poolerReloadShellScript(), "--", configHash}
}

func poolerReloadShellScript() string {
	return `i=0
while [ "$i" -lt 60 ]; do
    current="$(cat ` + poolerConfigHashFilePath + ` 2>/dev/null || true)"
    if [ "$current" = "$1" ]; then
        exec /usr/bin/pkill -HUP pgbouncer
    fi
    i=$((i + 1))
    sleep 2
done
echo "timed out waiting for projected PgBouncer config hash $1" >&2
exit 1`
}

func poolerPauseCommand(paused bool) []string {
	if paused {
		return []string{"/usr/bin/pkill", "-USR1", "pgbouncer"}
	}
	return []string{"/usr/bin/pkill", "-USR2", "pgbouncer"}
}

func poolerPauseAction(paused bool) string {
	if paused {
		return "PAUSE"
	}
	return "RESUME"
}

func poolerTargets(
	pooler *postgresv1alpha1.Pooler,
	cluster *postgresv1alpha1.PostgresCluster,
) ([]string, bool) {
	if len(cluster.Status.Shards) == 0 {
		return nil, false
	}
	shard := cluster.Status.Shards[0]
	service := ShardServiceName(cluster.Name, shard.Ordinal)
	if defaultPoolerType(pooler.Spec.Type) == postgresv1alpha1.PoolerTypeRO {
		targets := []string{}
		for _, replica := range shard.Replicas {
			if replica.Ready && replica.Pod != "" {
				targets = append(targets, poolerPodDNS(replica.Pod, service, cluster.Namespace))
			}
		}
		sort.Strings(targets)
		return targets, len(targets) > 0
	}
	if shard.Primary != nil && shard.Primary.Ready && shard.Primary.Pod != "" {
		return []string{poolerPodDNS(shard.Primary.Pod, service, cluster.Namespace)}, true
	}
	return nil, false
}

func poolerPodDNS(pod, service, namespace string) string {
	return fmt.Sprintf("%s.%s.%s.svc", pod, service, namespace)
}

func renderPgBouncerConfig(pooler *postgresv1alpha1.Pooler, targets []string) string {
	params := map[string]string{
		"listen_addr":     "0.0.0.0",
		"listen_port":     fmt.Sprintf("%d", defaultPgBouncerListenPort),
		"auth_type":       "scram-sha-256",
		"auth_file":       poolerAuthMountPath,
		"pool_mode":       string(defaultPoolerPoolMode(pooler.Spec.PgBouncer.PoolMode)),
		"unix_socket_dir": "",
		pgBouncerIgnoreStartupParametersKey: strings.Join([]string{
			pgBouncerExtraFloatDigitsParameter,
			pgBouncerOptionsParameter,
		}, ","),
	}
	maps.Copy(params, pooler.Spec.PgBouncer.Parameters)
	applyPoolerHBAParameters(params, pooler)
	applyPoolerTLSParameters(params, pooler)
	params[pgBouncerIgnoreStartupParametersKey] = mergePgBouncerCSVParameter(
		params[pgBouncerIgnoreStartupParametersKey],
		pgBouncerExtraFloatDigitsParameter,
		pgBouncerOptionsParameter,
	)
	if defaultPoolerType(pooler.Spec.Type) == postgresv1alpha1.PoolerTypeRO && len(targets) > 1 {
		if _, found := params["server_round_robin"]; !found {
			params["server_round_robin"] = "1"
		}
		if _, found := params["server_login_retry"]; !found {
			params["server_login_retry"] = "2"
		}
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("[databases]\n")
	b.WriteString("* = host=")
	b.WriteString(strings.Join(targets, ","))
	b.WriteString(" port=5432\n\n[pgbouncer]\n")
	for _, key := range keys {
		b.WriteString(key)
		b.WriteString(" = ")
		b.WriteString(params[key])
		b.WriteByte('\n')
	}
	return b.String()
}

func applyPoolerHBAParameters(params map[string]string, pooler *postgresv1alpha1.Pooler) {
	if len(pooler.Spec.PgBouncer.PgHBA) == 0 {
		return
	}
	params["auth_type"] = "hba"
	params["auth_hba_file"] = poolerHBAMountPath
}

func renderPgBouncerHBA(pooler *postgresv1alpha1.Pooler) string {
	if len(pooler.Spec.PgBouncer.PgHBA) == 0 {
		return ""
	}
	return strings.Join(pooler.Spec.PgBouncer.PgHBA, "\n") + "\n"
}

func applyPoolerTLSParameters(params map[string]string, pooler *postgresv1alpha1.Pooler) {
	if poolerEffectiveServerTLSSecretName(pooler) != "" {
		params["server_tls_key_file"] = poolerTLSFile("server", "tls.key")
		params["server_tls_cert_file"] = poolerTLSFile("server", "tls.crt")
		params["server_tls_sslmode"] = "require"
	}
	if poolerEffectiveServerCASecretName(pooler) != "" {
		params["server_tls_ca_file"] = poolerTLSFile("server-ca", "ca.crt")
		params["server_tls_sslmode"] = "verify-ca"
	}
	if poolerEffectiveClientTLSSecretName(pooler) != "" {
		params["client_tls_key_file"] = poolerTLSFile("client", "tls.key")
		params["client_tls_cert_file"] = poolerTLSFile("client", "tls.crt")
		params["client_tls_sslmode"] = "require"
	}
	if poolerEffectiveClientCASecretName(pooler) != "" {
		params["client_tls_ca_file"] = poolerTLSFile("client-ca", "ca.crt")
		params["client_tls_sslmode"] = "verify-ca"
	}
}

func poolerTLSFile(role, file string) string {
	return poolerTLSMountBase + "/" + role + "/" + file
}

func mergePgBouncerCSVParameter(value string, required ...string) string {
	seen := map[string]bool{}
	items := []string{}
	for raw := range strings.SplitSeq(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		items = append(items, item)
	}
	for _, item := range required {
		if seen[item] {
			continue
		}
		seen[item] = true
		items = append(items, item)
	}
	return strings.Join(items, ",")
}

func buildPoolerDeployment(pooler *postgresv1alpha1.Pooler) *appsv1.Deployment {
	labels := poolerLabels(pooler)
	replicas := defaultPoolerInstances(pooler.Spec.Instances)
	revisionHistoryLimit := int32(3)
	templateLabels := map[string]string{}
	maps.Copy(templateLabels, labels)
	templateAnnotations := map[string]string{}

	podSpec := corev1.PodSpec{SecurityContext: dataplanePodSecurityContext()}
	if pooler.Spec.Template != nil {
		maps.Copy(templateLabels, pooler.Spec.Template.Labels)
		maps.Copy(templateAnnotations, pooler.Spec.Template.Annotations)
		delete(templateAnnotations, poolerConfigHashKey)
		podSpec = pooler.Spec.Template.Spec
		if podSpec.SecurityContext == nil {
			podSpec.SecurityContext = dataplanePodSecurityContext()
		}
	}
	if pooler.Spec.ServiceAccountName != "" {
		podSpec.ServiceAccountName = pooler.Spec.ServiceAccountName
	}
	podSpec.TopologySpreadConstraints = commonstopology.Defaulted(
		podSpec.TopologySpreadConstraints,
		replicas-1,
		labels,
		commonstopology.WithMinReplicas(1),
	)

	container := poolerContainer(pooler)
	podSpec.Containers = upsertPoolerContainer(podSpec.Containers, container)
	if pooler.Spec.PgBouncer.Exporter != nil {
		podSpec.Containers = upsertPoolerContainer(podSpec.Containers, poolerExporterContainer(pooler.Spec.PgBouncer.Exporter))
	}
	podSpec.Volumes = upsertPoolerVolumes(podSpec.Volumes, pooler)
	strategy := poolerDeploymentStrategy(pooler)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PoolerDeploymentName(pooler.Name),
			Namespace: pooler.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             &replicas,
			Selector:             &metav1.LabelSelector{MatchLabels: labels},
			Strategy:             strategy,
			MinReadySeconds:      5,
			RevisionHistoryLimit: &revisionHistoryLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      templateLabels,
					Annotations: templateAnnotations,
				},
				Spec: podSpec,
			},
		},
	}
}

func poolerDeploymentStrategy(pooler *postgresv1alpha1.Pooler) appsv1.DeploymentStrategy {
	if pooler.Spec.DeploymentStrategy != nil {
		return *pooler.Spec.DeploymentStrategy
	}
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: intstrPtr(0),
			MaxSurge:       intstrPtr(1),
		},
	}
}

func intstrPtr(value int) *intstr.IntOrString {
	out := intstr.FromInt(value)
	return &out
}

func poolerContainer(pooler *postgresv1alpha1.Pooler) corev1.Container {
	container := corev1.Container{
		Name:            poolerContainerName,
		Image:           pooler.Spec.PgBouncer.Image,
		SecurityContext: dataplaneContainerSecurityContext(),
		Ports: []corev1.ContainerPort{{
			Name:          poolerContainerName,
			ContainerPort: defaultPgBouncerListenPort,
			Protocol:      corev1.ProtocolTCP,
		}},
		Command:        []string{"/usr/bin/pgbouncer"},
		Args:           []string{poolerConfigMountPath},
		ReadinessProbe: poolerTCPProbe(3, 3, 1, 3),
		LivenessProbe:  poolerTCPProbe(30, 10, 2, 6),
		StartupProbe:   poolerTCPProbe(0, 3, 1, 20),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "pgbouncer-config", MountPath: poolerConfigDir, ReadOnly: true},
			{Name: "pgbouncer-auth", MountPath: poolerAuthMountPath, SubPath: "userlist.txt", ReadOnly: true},
		},
	}
	container.VolumeMounts = append(container.VolumeMounts, poolerTLSVolumeMounts(pooler)...)
	return container
}

func poolerExporterContainer(exporter *postgresv1alpha1.PgBouncerExporterSpec) corev1.Container {
	resources := exporter.Resources
	if len(resources.Requests) == 0 && len(resources.Limits) == 0 {
		resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("25m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
	}
	return corev1.Container{
		Name:            poolerExporterName,
		Image:           exporter.Image,
		Args:            exporter.Args,
		Env:             exporter.Env,
		SecurityContext: dataplaneContainerSecurityContext(),
		Ports: []corev1.ContainerPort{{
			Name:          poolerMetricsPortName,
			ContainerPort: defaultPoolerExporterPortValue(exporter.Port),
			Protocol:      corev1.ProtocolTCP,
		}},
		ReadinessProbe: poolerHTTPProbe(poolerMetricsPortName, "/metrics", 5, 10, 2, 3),
		LivenessProbe:  poolerHTTPProbe(poolerMetricsPortName, "/metrics", 15, 30, 2, 3),
		Resources:      resources,
	}
}

func poolerTCPProbe(initialDelay, period, timeout, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{
			Port: intstr.FromString(poolerContainerName),
		}},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		TimeoutSeconds:      timeout,
		FailureThreshold:    failureThreshold,
	}
}

func poolerHTTPProbe(portName, path string, initialDelay, period, timeout, failureThreshold int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromString(portName),
		}},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		TimeoutSeconds:      timeout,
		FailureThreshold:    failureThreshold,
	}
}

func upsertPoolerContainer(containers []corev1.Container, desired corev1.Container) []corev1.Container {
	for i := range containers {
		if containers[i].Name == desired.Name {
			if containers[i].Image == "" {
				containers[i].Image = desired.Image
			}
			if len(containers[i].Args) == 0 {
				containers[i].Args = desired.Args
			}
			if containers[i].SecurityContext == nil {
				containers[i].SecurityContext = desired.SecurityContext
			}
			if len(containers[i].Ports) == 0 {
				containers[i].Ports = desired.Ports
			}
			if containers[i].ReadinessProbe == nil {
				containers[i].ReadinessProbe = desired.ReadinessProbe
			}
			if containers[i].LivenessProbe == nil {
				containers[i].LivenessProbe = desired.LivenessProbe
			}
			if containers[i].StartupProbe == nil {
				containers[i].StartupProbe = desired.StartupProbe
			}
			containers[i].VolumeMounts = mergeVolumeMounts(containers[i].VolumeMounts, desired.VolumeMounts)
			return containers
		}
	}
	return append([]corev1.Container{desired}, containers...)
}

func mergeVolumeMounts(base, required []corev1.VolumeMount) []corev1.VolumeMount {
	out := append([]corev1.VolumeMount{}, base...)
	for _, item := range required {
		found := false
		for _, existing := range out {
			if existing.Name == item.Name || existing.MountPath == item.MountPath {
				found = true
				break
			}
		}
		if !found {
			out = append(out, item)
		}
	}
	return out
}

func upsertPoolerVolumes(volumes []corev1.Volume, pooler *postgresv1alpha1.Pooler) []corev1.Volume {
	tlsVolumes := poolerTLSVolumes(pooler)
	required := make([]corev1.Volume, 0, 2+len(tlsVolumes))
	required = append(required,
		corev1.Volume{
			Name: "pgbouncer-config",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: PoolerConfigMapName(pooler.Name)},
			}},
		},
		corev1.Volume{
			Name: "pgbouncer-auth",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: poolerAuthSecretName(pooler),
			}},
		},
	)
	required = append(required, tlsVolumes...)
	out := append([]corev1.Volume{}, volumes...)
	for _, item := range required {
		found := false
		for _, existing := range out {
			if existing.Name == item.Name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, item)
		}
	}
	return out
}

func poolerTLSVolumeMounts(pooler *postgresv1alpha1.Pooler) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{}
	for _, ref := range poolerTLSRefs(pooler) {
		if ref.secretName == "" {
			continue
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      ref.volumeName,
			MountPath: poolerTLSMountBase + "/" + ref.role,
			ReadOnly:  true,
		})
	}
	return mounts
}

func poolerTLSVolumes(pooler *postgresv1alpha1.Pooler) []corev1.Volume {
	volumes := []corev1.Volume{}
	for _, ref := range poolerTLSRefs(pooler) {
		if ref.secretName == "" {
			continue
		}
		volumes = append(volumes, corev1.Volume{
			Name: ref.volumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: ref.secretName,
			}},
		})
	}
	return volumes
}

type poolerTLSRef struct {
	role       string
	volumeName string
	secretName string
}

func poolerTLSRefs(pooler *postgresv1alpha1.Pooler) []poolerTLSRef {
	return []poolerTLSRef{
		{
			role:       "server",
			volumeName: "pgbouncer-tls-server",
			secretName: poolerEffectiveServerTLSSecretName(pooler),
		},
		{
			role:       "server-ca",
			volumeName: "pgbouncer-tls-server-ca",
			secretName: poolerEffectiveServerCASecretName(pooler),
		},
		{
			role:       "client",
			volumeName: "pgbouncer-tls-client",
			secretName: poolerEffectiveClientTLSSecretName(pooler),
		},
		{
			role:       "client-ca",
			volumeName: "pgbouncer-tls-client-ca",
			secretName: poolerEffectiveClientCASecretName(pooler),
		},
	}
}

func buildPoolerService(pooler *postgresv1alpha1.Pooler) *corev1.Service {
	labels := poolerLabels(pooler)
	annotations := map[string]string{}
	serviceType := corev1.ServiceTypeClusterIP
	ports := []corev1.ServicePort{}
	if pooler.Spec.ServiceTemplate != nil {
		if pooler.Spec.ServiceTemplate.Type != "" {
			serviceType = pooler.Spec.ServiceTemplate.Type
		}
		maps.Copy(labels, pooler.Spec.ServiceTemplate.Labels)
		maps.Copy(annotations, pooler.Spec.ServiceTemplate.Annotations)
		ports = append(ports, pooler.Spec.ServiceTemplate.Ports...)
	}
	ports = appendDefaultPoolerServicePort(ports)
	if pooler.Spec.PgBouncer.Exporter != nil {
		ports = appendPoolerExporterServicePort(ports, pooler.Spec.PgBouncer.Exporter.Port)
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        PoolerServiceName(pooler.Name),
			Namespace:   pooler.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: poolerLabels(pooler),
			Ports:    ports,
		},
	}
}

func appendPoolerExporterServicePort(ports []corev1.ServicePort, exporterPort int32) []corev1.ServicePort {
	portNumber := defaultPoolerExporterPortValue(exporterPort)
	for _, port := range ports {
		if port.Name == poolerMetricsPortName || port.Port == portNumber {
			return ports
		}
	}
	return append(ports, corev1.ServicePort{
		Name:       poolerMetricsPortName,
		Port:       portNumber,
		TargetPort: intstr.FromString(poolerMetricsPortName),
		Protocol:   corev1.ProtocolTCP,
	})
}

func defaultPoolerExporterPortValue(port int32) int32 {
	if port <= 0 {
		return defaultPoolerExporterPort
	}
	return port
}

func appendDefaultPoolerServicePort(ports []corev1.ServicePort) []corev1.ServicePort {
	for _, port := range ports {
		if port.Name == poolerContainerName || port.Port == defaultPgBouncerListenPort {
			return ports
		}
	}
	return append(ports, corev1.ServicePort{
		Name:       poolerContainerName,
		Port:       defaultPgBouncerListenPort,
		TargetPort: intstr.FromString(poolerContainerName),
		Protocol:   corev1.ProtocolTCP,
	})
}

func poolerLabels(pooler *postgresv1alpha1.Pooler) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "pooler",
		"app.kubernetes.io/instance":   pooler.Name,
		"app.kubernetes.io/component":  "pgbouncer",
		"app.kubernetes.io/managed-by": "keiailab-postgres-operator",
		"postgres.keiailab.io/cluster": pooler.Spec.Cluster.Name,
		"postgres.keiailab.io/pooler":  pooler.Name,
		"postgres.keiailab.io/pooler-type": string(defaultPoolerType(
			pooler.Spec.Type,
		)),
	}
}

func defaultPoolerInstances(instances int32) int32 {
	if instances <= 0 {
		return 1
	}
	return instances
}

func defaultPoolerType(poolerType postgresv1alpha1.PoolerType) postgresv1alpha1.PoolerType {
	if poolerType == "" {
		return postgresv1alpha1.PoolerTypeRW
	}
	return poolerType
}

func defaultPoolerPoolMode(mode postgresv1alpha1.PoolerPoolMode) postgresv1alpha1.PoolerPoolMode {
	if mode == "" {
		return postgresv1alpha1.PoolerPoolModeSession
	}
	return mode
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func poolerConfigHash(config, hbaConfig, authUserlist string) string {
	return sha256Hex(config + "\x00" + hbaConfig + "\x00" + authUserlist)
}

// readPoolerAuthUserlist 는 ConfigHash 계산에 포함할 userlist.txt 본문을 읽는다.
// validatePoolerAuthSecret 이 이미 Secret 존재성 + non-empty userlist 를 보장하므로
// 본 함수는 단순 lookup. transient error 만 propagate.
func (r *PoolerReconciler) readPoolerAuthUserlist(ctx context.Context, pooler *postgresv1alpha1.Pooler) (string, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: poolerAuthSecretName(pooler)}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return string(secret.Data["userlist.txt"]), nil
}

func (r *PoolerReconciler) markPoolerFailed(pooler *postgresv1alpha1.Pooler, reason, message string) {
	pooler.Status.Phase = postgresv1alpha1.PoolerFailed
	pooler.Status.Instances = 0
	pooler.Status.ReadyReplicas = 0
	pooler.Status.Paused = false
	pooler.Status.BackendTargets = nil
	pooler.Status.ConfigHash = ""
	pooler.Status.ObservedGeneration = pooler.Generation
	setPoolerCondition(pooler, metav1.ConditionFalse, reason, message)
}

// markPoolerPending 은 spec 위반은 아니지만 prerequisite (예: PostgresCluster
// primary 미준비) 가 아직 갖춰지지 않은 상태를 표면화한다. ConfigHash 와
// BackendTargets 등 누적 상태는 유지해 다음 reconcile cycle 에서 재사용 가능.
func (r *PoolerReconciler) markPoolerPending(pooler *postgresv1alpha1.Pooler, reason, message string) {
	pooler.Status.Phase = postgresv1alpha1.PoolerPending
	pooler.Status.ObservedGeneration = pooler.Generation
	setPoolerCondition(pooler, metav1.ConditionFalse, reason, message)
}

func setPoolerCondition(
	pooler *postgresv1alpha1.Pooler,
	status metav1.ConditionStatus,
	reason,
	message string,
) {
	meta.SetStatusCondition(&pooler.Status.Conditions, metav1.Condition{
		Type:               PoolerConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: pooler.Generation,
	})
}

// statusUpdate mirrors the conflict-retry pattern used by
// PostgresDatabase / PostgresUser / BackupJob reconcilers.
// See backupjob_controller.go::statusUpdate for the rationale.
func (r *PoolerReconciler) statusUpdate(ctx context.Context, pooler *postgresv1alpha1.Pooler) error {
	desired := pooler.Status.DeepCopy()
	err := r.Status().Update(ctx, pooler)
	if err == nil {
		ObservePoolerMetrics(pooler)
		return nil
	}
	if !apierrors.IsConflict(err) {
		return err
	}
	var fresh postgresv1alpha1.Pooler
	if getErr := r.Get(ctx, client.ObjectKeyFromObject(pooler), &fresh); getErr != nil {
		return getErr
	}
	fresh.Status = *desired
	if retryErr := r.Status().Update(ctx, &fresh); retryErr != nil {
		if apierrors.IsConflict(retryErr) {
			return nil
		}
		return retryErr
	}
	pooler.ResourceVersion = fresh.ResourceVersion
	ObservePoolerMetrics(pooler)
	return nil
}

// SetupWithManager registers the Pooler reconciler with the
// controller-runtime Manager.
//
// We additionally Watch PostgresCluster — when a referenced cluster's
// status flips (primary becomes Ready, replicas become Ready, etc.),
// any Pooler that targets that cluster is re-enqueued. Without this
// watch a Pooler whose first reconcile races the cluster's primary
// promotion stays in phase=Pending|Failed forever (observed during
// the PG18 kind smoke iter#4).
func (r *PoolerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.PodExecutor == nil {
		executor, err := NewKubernetesBackupSidecarExecutor(mgr.GetConfig())
		if err != nil {
			return err
		}
		r.PodExecutor = executor
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.Pooler{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Watches(
			&postgresv1alpha1.PostgresCluster{},
			handler.EnqueueRequestsFromMapFunc(r.enqueuePoolersForCluster),
		).
		Named("pooler").
		Complete(r)
}

// enqueuePoolersForCluster returns Reconcile requests for every Pooler in
// the same namespace whose spec.cluster.name matches the changed
// PostgresCluster. Used by the Watches above.
func (r *PoolerReconciler) enqueuePoolersForCluster(ctx context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*postgresv1alpha1.PostgresCluster)
	if !ok {
		return nil
	}
	var list postgresv1alpha1.PoolerList
	if err := r.List(ctx, &list, client.InNamespace(cluster.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "enqueuePoolersForCluster list failed",
			"cluster", cluster.Name)
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.Cluster.Name != cluster.Name {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(p),
		})
	}
	return out
}
