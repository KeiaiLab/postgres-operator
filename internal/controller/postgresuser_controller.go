/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonsfinalizer "github.com/keiailab/keiailab-commons/pkg/finalizer"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// PostgresUserReconciler 는 PostgresUser CRD 를 ready primary Pod 의 psql 로 반영한다.
type PostgresUserReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	SQLExecutor BackupSidecarExecutor
}

const (
	PostgresUserConditionReady = "Ready"

	PostgresUserReasonClusterNotFound     = "ClusterNotFound"
	PostgresUserReasonPrimaryNotReady     = "PrimaryNotReady"
	PostgresUserReasonSQLExecutorMissing  = "SQLExecutorMissing"
	PostgresUserReasonSQLExecutionFailed  = "SQLExecutionFailed"
	PostgresUserReasonReconciled          = "UserReconciled"
	PostgresUserReasonInvalidSpec         = "InvalidSpec"
	PostgresUserReasonPasswordSecretError = "PasswordSecretError"
	postgresUserPrimaryRequeueWait        = 15 * time.Second
	// postgresUserFinalizer — keiailab 표준 finalizer (commons pkg/finalizer
	// 컨벤션 "<resource>.keiailab.com/finalizer", mongodb/valkey operator 정합).
	postgresUserFinalizer = "postgresuser.keiailab.com/finalizer"
	// Deprecated: postgresUserFinalizerLegacy 는 구 prefix finalizer.
	// 신규 부착 금지 — 라이브 객체에 잔존하는 구 finalizer 의 인식/제거
	// (both-recognize) 전환 경로 전용. 라이브 잔존 0 확인 후 제거 예정.
	postgresUserFinalizerLegacy      = "postgres.keiailab.io/postgresuser-finalizer"
	defaultPostgresUserEnsure        = postgresv1alpha1.DatabaseEnsurePresent
	postgresUserReservedNamePostgres = "postgres"
	postgresUserReservedPrefixPG     = "pg_"
)

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

func (r *PostgresUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("postgresuser", req.NamespacedName)

	var user postgresv1alpha1.PostgresUser
	if err := r.Get(ctx, req.NamespacedName, &user); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !user.DeletionTimestamp.IsZero() {
		return r.reconcilePostgresUserDelete(ctx, &user)
	}

	if invalid := validatePostgresUserSpec(&user); invalid != "" {
		markPostgresUserStatus(&user, false, PostgresUserReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &user)
	}

	// Attach the finalizer when the user opted into reclaimPolicy=delete so
	// `DROP ROLE` runs before CR garbage-collection.
	if defaultedDatabaseReclaimPolicy(user.Spec.UserReclaimPolicy) == postgresv1alpha1.DatabaseReclaimDelete &&
		commonsfinalizer.Add(&user, postgresUserFinalizer) {
		if err := r.Update(ctx, &user); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("PostgresUser finalizer added (continuing reconcile in same pass)",
			"reclaimPolicy", "delete")
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: user.Namespace, Name: user.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			message := "Referenced PostgresCluster " + user.Spec.Cluster.Name + " not found in namespace " + user.Namespace
			markPostgresUserStatus(&user, false, PostgresUserReasonClusterNotFound, message)
			logger.Info("PostgresUser target cluster not found", "cluster", user.Spec.Cluster.Name)
			return ctrl.Result{}, r.statusUpdate(ctx, &user)
		}
		return ctrl.Result{}, err
	}

	target, ok := backupSidecarTarget(&cluster)
	if !ok {
		message := "ready primary Pod for PostgresCluster " + cluster.Name + " not found"
		markPostgresUserStatus(&user, false, PostgresUserReasonPrimaryNotReady, message)
		logger.Info("PostgresUser primary not ready — requeue",
			"cluster", cluster.Name,
			"shardCount", len(cluster.Status.Shards))
		if err := r.statusUpdate(ctx, &user); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: postgresUserPrimaryRequeueWait}, nil
	}

	if r.SQLExecutor == nil {
		markPostgresUserStatus(&user, false, PostgresUserReasonSQLExecutorMissing,
			"PostgresUser SQL executor is not configured")
		return ctrl.Result{}, r.statusUpdate(ctx, &user)
	}

	passwordClause, passwordSecretResourceVersion, passwordError, err := r.postgresUserPasswordClause(ctx, &user)
	if err != nil {
		return ctrl.Result{}, err
	}
	if passwordError != "" {
		markPostgresUserStatus(&user, false, PostgresUserReasonPasswordSecretError, passwordError)
		return ctrl.Result{}, r.statusUpdate(ctx, &user)
	}

	command := postgresUserReconcileCommand(&user, passwordClause)
	if _, err := r.SQLExecutor.Exec(ctx, target, command); err != nil {
		logger.Error(err, "PostgresUser SQL execution failed")
		markPostgresUserStatus(&user, false, PostgresUserReasonSQLExecutionFailed,
			"PostgresUser SQL execution failed: "+err.Error())
		return ctrl.Result{}, r.statusUpdate(ctx, &user)
	}

	user.Status.PasswordSecretResourceVersion = passwordSecretResourceVersion
	markPostgresUserStatus(&user, true, PostgresUserReasonReconciled,
		"PostgresUser "+user.Spec.Name+" reconciled")
	logger.Info("PostgresUser reconciled",
		"spec.name", user.Spec.Name,
		"passwordSecretRV", passwordSecretResourceVersion)
	return ctrl.Result{}, r.statusUpdate(ctx, &user)
}

// reconcilePostgresUserDelete handles CR deletion. When userReclaimPolicy=delete
// and the finalizer is present, run `DROP ROLE` against the cluster's ready
// primary before removing the finalizer (mirrors PostgresDatabase). If the
// reclaim policy is retain (or the cluster is already gone), just strip the
// finalizer so the CR can be garbage-collected.
func (r *PostgresUserReconciler) reconcilePostgresUserDelete(
	ctx context.Context,
	user *postgresv1alpha1.PostgresUser,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("postgresuser", client.ObjectKeyFromObject(user))
	// both-recognize: 신규 + legacy finalizer 어느 쪽이든 부착되어 있으면 cleanup 진행.
	if !commonsfinalizer.Has(user, postgresUserFinalizer) &&
		!commonsfinalizer.Has(user, postgresUserFinalizerLegacy) {
		return ctrl.Result{}, nil
	}
	if defaultedDatabaseReclaimPolicy(user.Spec.UserReclaimPolicy) != postgresv1alpha1.DatabaseReclaimDelete {
		removePostgresUserFinalizers(user)
		return ctrl.Result{}, r.Update(ctx, user)
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: user.Namespace, Name: user.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		logger.Info("PostgresUser delete skipped because referenced PostgresCluster is gone",
			"cluster", user.Spec.Cluster.Name)
		removePostgresUserFinalizers(user)
		return ctrl.Result{}, r.Update(ctx, user)
	}

	target, ok := backupSidecarTarget(&cluster)
	if !ok {
		logger.Info("PostgresUser delete waiting for ready primary Pod",
			"cluster", user.Spec.Cluster.Name)
		return ctrl.Result{RequeueAfter: postgresUserPrimaryRequeueWait}, nil
	}

	if r.SQLExecutor == nil {
		return ctrl.Result{}, fmt.Errorf("postgresuser SQL executor is not configured")
	}

	dropUser := user.DeepCopy()
	dropUser.Spec.Ensure = postgresv1alpha1.DatabaseEnsureAbsent
	if _, err := r.SQLExecutor.Exec(ctx, target, postgresUserReconcileCommand(dropUser, "")); err != nil {
		logger.Error(err, "PostgresUser SQL DROP ROLE failed")
		return ctrl.Result{}, err
	}

	removePostgresUserFinalizers(user)
	return ctrl.Result{}, r.Update(ctx, user)
}

// removePostgresUserFinalizers 는 신규 + legacy finalizer 를 모두 제거한다
// (both-recognize 전환 — 라이브 객체의 구 prefix finalizer 잔존 대응).
func removePostgresUserFinalizers(user *postgresv1alpha1.PostgresUser) {
	commonsfinalizer.Remove(user, postgresUserFinalizer)
	commonsfinalizer.Remove(user, postgresUserFinalizerLegacy)
}

func validatePostgresUserSpec(user *postgresv1alpha1.PostgresUser) string {
	if strings.TrimSpace(user.Spec.Cluster.Name) == "" {
		return "spec.cluster.name is required"
	}
	name := strings.TrimSpace(user.Spec.Name)
	if name == "" {
		return "spec.name is required"
	}
	if name == postgresUserReservedNamePostgres || strings.HasPrefix(name, postgresUserReservedPrefixPG) {
		return "spec.name " + name + " is reserved"
	}
	if user.Spec.ConnectionLimit != nil && *user.Spec.ConnectionLimit < -1 {
		return "spec.connectionLimit must be -1 or greater"
	}
	if user.Spec.PasswordSecretRef != nil && strings.TrimSpace(user.Spec.PasswordSecretRef.Name) == "" {
		return "spec.passwordSecretRef.name is required when passwordSecretRef is set"
	}
	if user.Spec.PasswordSecretRef != nil && user.Spec.DisablePassword {
		return "spec.passwordSecretRef and spec.disablePassword cannot both be set"
	}
	return ""
}

func (r *PostgresUserReconciler) postgresUserPasswordClause(
	ctx context.Context,
	user *postgresv1alpha1.PostgresUser,
) (string, string, string, error) {
	if user.Spec.DisablePassword {
		return "PASSWORD NULL", "", "", nil
	}
	if user.Spec.PasswordSecretRef == nil {
		return "", "", "", nil
	}

	var secret corev1.Secret
	key := client.ObjectKey{Namespace: user.Namespace, Name: user.Spec.PasswordSecretRef.Name}
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", "password Secret " + key.Name + " not found in namespace " + key.Namespace, nil
		}
		return "", "", "", err
	}
	username, ok := secret.Data["username"]
	if !ok {
		return "", "", "password Secret " + key.Name + " must contain data.username", nil
	}
	if string(username) != strings.TrimSpace(user.Spec.Name) {
		return "", "", "password Secret " + key.Name + " data.username must match spec.name", nil
	}
	password, ok := secret.Data["password"]
	if !ok {
		return "", "", "password Secret " + key.Name + " must contain data.password", nil
	}
	return "PASSWORD " + sqlLiteral(string(password)), secret.ResourceVersion, "", nil
}

func markPostgresUserStatus(user *postgresv1alpha1.PostgresUser, applied bool, reason, message string) {
	user.Status.Applied = applied
	user.Status.ObservedGeneration = user.Generation
	user.Status.Message = message
	conditionStatus := metav1.ConditionFalse
	if applied {
		conditionStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
		Type:               PostgresUserConditionReady,
		Status:             conditionStatus,
		ObservedGeneration: user.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// statusUpdate persists the in-memory status on the API. On a transient
// conflict (HTTP 409) we re-fetch the resource, replay the desired
// status snapshot, and retry once. Silently swallowing the conflict
// was the root cause of PostgresDatabase / PostgresUser status never
// converging on a busy reconcile (PG18 kind smoke iter#3).
func (r *PostgresUserReconciler) statusUpdate(ctx context.Context, user *postgresv1alpha1.PostgresUser) error {
	desired := user.Status.DeepCopy()
	err := r.Status().Update(ctx, user)
	if err == nil {
		return nil
	}
	if !apierrors.IsConflict(err) {
		return err
	}
	var fresh postgresv1alpha1.PostgresUser
	if getErr := r.Get(ctx, client.ObjectKeyFromObject(user), &fresh); getErr != nil {
		return getErr
	}
	fresh.Status = *desired
	if retryErr := r.Status().Update(ctx, &fresh); retryErr != nil {
		if apierrors.IsConflict(retryErr) {
			return nil
		}
		return retryErr
	}
	user.ResourceVersion = fresh.ResourceVersion
	return nil
}

func postgresUserReconcileCommand(user *postgresv1alpha1.PostgresUser, passwordClause string) []string {
	return []string{"/bin/sh", "-ec", postgresUserReconcileScript(user, passwordClause)}
}

func postgresUserReconcileScript(user *postgresv1alpha1.PostgresUser, passwordClause string) string {
	name := strings.TrimSpace(user.Spec.Name)
	ensure := defaultedPostgresUserEnsure(user.Spec.Ensure)

	// See postgresdatabase_controller.go::postgresDatabaseReconcileScript for
	// the rationale: inlining the full psql invocation avoids the eval
	// re-tokenisation bug that drops shell-level quotes around the SQL.
	// `-U postgres` is required because the postgres container's USER is
	// `pg-keiailab` — psql would otherwise default to the OS user and emit
	// `FATAL: role "pg-keiailab" does not exist` (PG18 kind smoke iter#6).
	const psqlBase = "psql -v ON_ERROR_STOP=1 -X -q -U postgres -d postgres"

	var b strings.Builder
	b.WriteString("set -eu\n")
	if ensure == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(&b, "%s -c %s\n",
			psqlBase,
			shellQuote("DROP ROLE IF EXISTS "+sqlIdent(name)),
		)
		return b.String()
	}

	options := postgresUserRoleOptions(user, passwordClause)
	fmt.Fprintf(&b,
		"if [ \"$(%s -At -c %s)\" = \"1\" ]; then\n  %s -c %s\nelse\n  %s -c %s\nfi\n",
		psqlBase,
		shellQuote("SELECT 1 FROM pg_roles WHERE rolname = "+sqlLiteral(name)),
		psqlBase,
		shellQuote("ALTER ROLE "+sqlIdent(name)+" WITH "+options),
		psqlBase,
		shellQuote("CREATE ROLE "+sqlIdent(name)+" WITH "+options),
	)
	appendPostgresUserMembershipRevokeScript(&b, name, user.Spec.InRoles)
	for _, parent := range user.Spec.InRoles {
		parent = strings.TrimSpace(parent)
		if parent == "" {
			continue
		}
		fmt.Fprintf(&b, "%s -c %s\n",
			psqlBase,
			shellQuote("GRANT "+sqlIdent(parent)+" TO "+sqlIdent(name)),
		)
	}
	return b.String()
}

func appendPostgresUserMembershipRevokeScript(b *strings.Builder, name string, inRoles []string) {
	query := strings.Join([]string{
		"SELECT quote_ident(parent.rolname)",
		"FROM pg_auth_members membership",
		"JOIN pg_roles parent ON parent.oid = membership.roleid",
		"JOIN pg_roles member ON member.oid = membership.member",
		"WHERE member.rolname = " + sqlLiteral(name),
		"AND NOT parent.rolname = ANY (" + postgresUserNameArray(inRoles) + ")",
	}, " ")
	const psqlBase = "psql -v ON_ERROR_STOP=1 -X -q -U postgres -d postgres"
	// $managed_role is a controlled SQL identifier (sqlIdent output, embedded
	// inside double quotes) and $parent_role is the output of pg_auth_members
	// piped through quote_ident, so the inner `psql -c "REVOKE … FROM …"` is
	// safe under shell expansion without eval.
	fmt.Fprintf(b,
		"managed_role=%s\n"+
			"%s -At -c %s | while IFS= read -r parent_role; do\n"+
			"  [ -n \"$parent_role\" ] || continue\n"+
			"  %s -c \"REVOKE $parent_role FROM $managed_role\"\n"+
			"done\n",
		shellQuote(sqlIdent(name)),
		psqlBase,
		shellQuote(query),
		psqlBase,
	)
}

func postgresUserNameArray(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parts = append(parts, sqlLiteral(value))
	}
	return "ARRAY[" + strings.Join(parts, ",") + "]::name[]"
}

func postgresUserRoleOptions(user *postgresv1alpha1.PostgresUser, passwordClause string) string {
	options := []string{
		postgresUserBoolOption(user.Spec.Login, "LOGIN", "NOLOGIN"),
		postgresUserBoolOption(user.Spec.Superuser, "SUPERUSER", "NOSUPERUSER"),
		postgresUserBoolOption(user.Spec.CreateDB, "CREATEDB", "NOCREATEDB"),
		postgresUserBoolOption(user.Spec.CreateRole, "CREATEROLE", "NOCREATEROLE"),
		postgresUserBoolOption(user.Spec.Replication, "REPLICATION", "NOREPLICATION"),
		postgresUserBoolOption(user.Spec.BypassRLS, "BYPASSRLS", "NOBYPASSRLS"),
	}
	inherit := true
	if user.Spec.Inherit != nil {
		inherit = *user.Spec.Inherit
	}
	options = append(options, postgresUserBoolOption(inherit, "INHERIT", "NOINHERIT"))
	if user.Spec.ConnectionLimit != nil {
		options = append(options, fmt.Sprintf("CONNECTION LIMIT %d", *user.Spec.ConnectionLimit))
	}
	if passwordClause != "" {
		options = append(options, passwordClause)
	}
	if validUntil := strings.TrimSpace(user.Spec.ValidUntil); validUntil != "" {
		options = append(options, "VALID UNTIL "+sqlLiteral(validUntil))
	}
	return strings.Join(options, " ")
}

func postgresUserBoolOption(value bool, enabled, disabled string) string {
	if value {
		return enabled
	}
	return disabled
}

func defaultedPostgresUserEnsure(value postgresv1alpha1.DatabaseEnsure) postgresv1alpha1.DatabaseEnsure {
	if value == "" {
		return defaultPostgresUserEnsure
	}
	return value
}

func (r *PostgresUserReconciler) postgresUsersForPasswordSecret(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	var users postgresv1alpha1.PostgresUserList
	if err := r.List(ctx, &users, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(users.Items))
	for i := range users.Items {
		user := &users.Items[i]
		if user.Spec.PasswordSecretRef == nil || user.Spec.PasswordSecretRef.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(user)})
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].String() < requests[j].String()
	})
	return requests
}

func (r *PostgresUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.PostgresUser{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.postgresUsersForPasswordSecret),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("postgresuser").
		Complete(r)
}
