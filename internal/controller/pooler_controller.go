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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
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
	if invalid, err := r.validatePoolerAuthSecret(ctx, &pooler); invalid != "" || err != nil {
		if err != nil {
			return ctrl.Result{}, err
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
		r.markPoolerFailed(&pooler, PoolerReasonTargetNotFound,
			"No ready backend target found for Pooler type "+string(defaultPoolerType(pooler.Spec.Type)))
		return ctrl.Result{}, r.statusUpdate(ctx, &pooler)
	}

	previousConfigHash := pooler.Status.ConfigHash
	config := renderPgBouncerConfig(&pooler, targets)
	hbaConfig := renderPgBouncerHBA(&pooler)
	configHash := poolerConfigHash(config, hbaConfig)
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
	if pooler.Spec.PgBouncer.AuthSecretRef == nil || strings.TrimSpace(pooler.Spec.PgBouncer.AuthSecretRef.Name) == "" {
		return "Pooler spec.pgbouncer.authSecretRef.name is required and must provide userlist.txt"
	}
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

func (r *PoolerReconciler) validatePoolerAuthSecret(ctx context.Context, pooler *postgresv1alpha1.Pooler) (string, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: pooler.Namespace, Name: pooler.Spec.PgBouncer.AuthSecretRef.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "Pooler spec.pgbouncer.authSecretRef.name must reference an existing Secret", nil
		}
		return "", err
	}
	if strings.TrimSpace(string(secret.Data["userlist.txt"])) == "" {
		return "Pooler auth Secret must contain non-empty userlist.txt", nil
	}
	return "", nil
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
	if pooler.Spec.PgBouncer.ServerTLSSecret != nil {
		params["server_tls_key_file"] = poolerTLSFile("server", "tls.key")
		params["server_tls_cert_file"] = poolerTLSFile("server", "tls.crt")
		params["server_tls_sslmode"] = "require"
	}
	if pooler.Spec.PgBouncer.ServerCASecret != nil {
		params["server_tls_ca_file"] = poolerTLSFile("server-ca", "ca.crt")
		params["server_tls_sslmode"] = "verify-ca"
	}
	if pooler.Spec.PgBouncer.ClientTLSSecret != nil {
		params["client_tls_key_file"] = poolerTLSFile("client", "tls.key")
		params["client_tls_cert_file"] = poolerTLSFile("client", "tls.crt")
		params["client_tls_sslmode"] = "require"
	}
	if pooler.Spec.PgBouncer.ClientCASecret != nil {
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
	podSpec.TopologySpreadConstraints = defaultedTopologySpread(
		podSpec.TopologySpreadConstraints,
		replicas-1,
		labels,
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
				SecretName: pooler.Spec.PgBouncer.AuthSecretRef.Name,
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
			secretName: localObjectName(pooler.Spec.PgBouncer.ServerTLSSecret),
		},
		{
			role:       "server-ca",
			volumeName: "pgbouncer-tls-server-ca",
			secretName: localObjectName(pooler.Spec.PgBouncer.ServerCASecret),
		},
		{
			role:       "client",
			volumeName: "pgbouncer-tls-client",
			secretName: localObjectName(pooler.Spec.PgBouncer.ClientTLSSecret),
		},
		{
			role:       "client-ca",
			volumeName: "pgbouncer-tls-client-ca",
			secretName: localObjectName(pooler.Spec.PgBouncer.ClientCASecret),
		},
	}
}

func localObjectName(ref *corev1.LocalObjectReference) string {
	if ref == nil {
		return ""
	}
	return ref.Name
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

func poolerConfigHash(config string, hbaConfig string) string {
	return sha256Hex(config + "\x00" + hbaConfig)
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

func (r *PoolerReconciler) statusUpdate(ctx context.Context, pooler *postgresv1alpha1.Pooler) error {
	if err := r.Status().Update(ctx, pooler); err != nil {
		if apierrors.IsConflict(err) {
			return nil
		}
		return err
	}
	ObservePoolerMetrics(pooler)
	return nil
}

// SetupWithManager 는 Pooler reconciler 를 controller-runtime Manager 에 등록한다.
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
		Named("pooler").
		Complete(r)
}
