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
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// PostgresDatabaseReconciler 는 PostgresDatabase CRD 를 ready primary Pod 의 psql 로 반영한다.
type PostgresDatabaseReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	SQLExecutor BackupSidecarExecutor
}

const (
	PostgresDatabaseConditionReady = "Ready"

	PostgresDatabaseReasonClusterNotFound    = "ClusterNotFound"
	PostgresDatabaseReasonPrimaryNotReady    = "PrimaryNotReady"
	PostgresDatabaseReasonSQLExecutorMissing = "SQLExecutorMissing"
	PostgresDatabaseReasonSQLExecutionFailed = "SQLExecutionFailed"
	PostgresDatabaseReasonReconciled         = "DatabaseReconciled"
	PostgresDatabaseReasonInvalidSpec        = "InvalidSpec"
	postgresDatabasePrimaryRequeueWait       = 15 * time.Second
	postgresDatabaseFinalizer                = "postgres.keiailab.io/postgresdatabase-finalizer"
	defaultPostgresDatabaseEnsure            = postgresv1alpha1.DatabaseEnsurePresent
	defaultPostgresDatabaseReclaimPolicy     = postgresv1alpha1.DatabaseReclaimRetain
	postgresDatabaseReservedNamePostgres     = "postgres"
	postgresDatabaseReservedNameTemplate0    = "template0"
	postgresDatabaseReservedNameTemplate1    = "template1"
	postgresDatabaseObjectKindDatabase       = "DATABASE"
	postgresDatabaseObjectKindSchema         = "SCHEMA"
)

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresdatabases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresdatabases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresdatabases/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

func (r *PostgresDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("postgresdatabase", req.NamespacedName)

	var db postgresv1alpha1.PostgresDatabase
	if err := r.Get(ctx, req.NamespacedName, &db); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !db.DeletionTimestamp.IsZero() {
		return r.reconcilePostgresDatabaseDelete(ctx, &db)
	}

	if invalid := validatePostgresDatabaseSpec(&db); invalid != "" {
		markPostgresDatabaseStatus(&db, false, PostgresDatabaseReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &db)
	}

	// reclaimPolicy=delete 인 경우 finalizer 를 *즉시* 부착하고 같은 reconcile 안에서
	// SQL 반영까지 마친다. 이전 구현은 finalizer 추가 후 Requeue:true 로 돌려보냈는데,
	// 이때 두 번째 reconcile 이 watch propagation 지연으로 늦거나 conflict-swallow 와
	// 만나서 status 가 영영 비는 케이스가 PG18 kind smoke 에서 관측되었다 (iter#3).
	// AddFinalizer + Update 가 성공하면 in-memory db 의 ResourceVersion 이 갱신되므로
	// 이후 r.Status().Update 도 충돌 없이 같은 호출에서 수행할 수 있다.
	if defaultedDatabaseReclaimPolicy(db.Spec.DatabaseReclaimPolicy) == postgresv1alpha1.DatabaseReclaimDelete &&
		controllerutil.AddFinalizer(&db, postgresDatabaseFinalizer) {
		if err := r.Update(ctx, &db); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("PostgresDatabase finalizer added (continuing reconcile in same pass)",
			"reclaimPolicy", "delete")
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: db.Namespace, Name: db.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			message := "Referenced PostgresCluster " + db.Spec.Cluster.Name + " not found in namespace " + db.Namespace
			markPostgresDatabaseStatus(&db, false, PostgresDatabaseReasonClusterNotFound, message)
			logger.Info("PostgresDatabase target cluster not found", "cluster", db.Spec.Cluster.Name)
			return ctrl.Result{}, r.statusUpdate(ctx, &db)
		}
		return ctrl.Result{}, err
	}

	target, ok := backupSidecarTarget(&cluster)
	if !ok {
		message := "ready primary Pod for PostgresCluster " + cluster.Name + " not found"
		markPostgresDatabaseStatus(&db, false, PostgresDatabaseReasonPrimaryNotReady, message)
		logger.Info("PostgresDatabase primary not ready — requeue",
			"cluster", cluster.Name,
			"shardCount", len(cluster.Status.Shards))
		if err := r.statusUpdate(ctx, &db); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: postgresDatabasePrimaryRequeueWait}, nil
	}

	if r.SQLExecutor == nil {
		markPostgresDatabaseStatus(&db, false, PostgresDatabaseReasonSQLExecutorMissing,
			"PostgresDatabase SQL executor is not configured")
		return ctrl.Result{}, r.statusUpdate(ctx, &db)
	}

	command := postgresDatabaseReconcileCommand(&db)
	if _, err := r.SQLExecutor.Exec(ctx, target, command); err != nil {
		logger.Error(err, "PostgresDatabase SQL execution failed")
		markPostgresDatabaseStatus(&db, false, PostgresDatabaseReasonSQLExecutionFailed,
			"PostgresDatabase SQL execution failed: "+err.Error())
		return ctrl.Result{}, r.statusUpdate(ctx, &db)
	}

	markPostgresDatabaseStatus(&db, true, PostgresDatabaseReasonReconciled,
		"PostgresDatabase "+db.Spec.Name+" reconciled")
	logger.Info("PostgresDatabase reconciled", "spec.name", db.Spec.Name, "owner", db.Spec.Owner)
	return ctrl.Result{}, r.statusUpdate(ctx, &db)
}

func (r *PostgresDatabaseReconciler) reconcilePostgresDatabaseDelete(
	ctx context.Context,
	db *postgresv1alpha1.PostgresDatabase,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("postgresdatabase", client.ObjectKeyFromObject(db))
	if !controllerutil.ContainsFinalizer(db, postgresDatabaseFinalizer) {
		return ctrl.Result{}, nil
	}
	if defaultedDatabaseReclaimPolicy(db.Spec.DatabaseReclaimPolicy) != postgresv1alpha1.DatabaseReclaimDelete {
		controllerutil.RemoveFinalizer(db, postgresDatabaseFinalizer)
		return ctrl.Result{}, r.Update(ctx, db)
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: db.Namespace, Name: db.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		logger.Info("PostgresDatabase delete skipped because referenced PostgresCluster is gone",
			"cluster", db.Spec.Cluster.Name)
		controllerutil.RemoveFinalizer(db, postgresDatabaseFinalizer)
		return ctrl.Result{}, r.Update(ctx, db)
	}

	target, ok := backupSidecarTarget(&cluster)
	if !ok {
		logger.Info("PostgresDatabase delete waiting for ready primary Pod",
			"cluster", db.Spec.Cluster.Name)
		return ctrl.Result{RequeueAfter: postgresDatabasePrimaryRequeueWait}, nil
	}

	if r.SQLExecutor == nil {
		return ctrl.Result{}, fmt.Errorf("postgresdatabase SQL executor is not configured")
	}

	dropDB := db.DeepCopy()
	dropDB.Spec.Ensure = postgresv1alpha1.DatabaseEnsureAbsent
	if _, err := r.SQLExecutor.Exec(ctx, target, postgresDatabaseReconcileCommand(dropDB)); err != nil {
		logger.Error(err, "PostgresDatabase SQL delete execution failed")
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(db, postgresDatabaseFinalizer)
	return ctrl.Result{}, r.Update(ctx, db)
}

func validatePostgresDatabaseSpec(db *postgresv1alpha1.PostgresDatabase) string {
	if strings.TrimSpace(db.Spec.Cluster.Name) == "" {
		return "spec.cluster.name is required"
	}
	name := strings.TrimSpace(db.Spec.Name)
	if name == "" {
		return "spec.name is required"
	}
	switch name {
	case postgresDatabaseReservedNamePostgres, postgresDatabaseReservedNameTemplate0, postgresDatabaseReservedNameTemplate1:
		return "spec.name " + name + " is reserved"
	}
	if defaultedDatabaseEnsure(db.Spec.Ensure) == postgresv1alpha1.DatabaseEnsurePresent &&
		strings.TrimSpace(db.Spec.Owner) == "" {
		return "spec.owner is required when ensure=present"
	}
	if invalid := validateDatabaseGrantSpecs(db.Spec.Privileges, postgresDatabaseObjectKindDatabase, "spec.privileges"); invalid != "" {
		return invalid
	}
	for i, schema := range db.Spec.Schemas {
		if invalid := validateDatabaseGrantSpecs(
			schema.Privileges,
			postgresDatabaseObjectKindSchema,
			fmt.Sprintf("spec.schemas[%d].privileges", i),
		); invalid != "" {
			return invalid
		}
	}
	return ""
}

func validateDatabaseGrantSpecs(
	grants []postgresv1alpha1.DatabaseGrantSpec,
	objectKind string,
	path string,
) string {
	for i, grant := range grants {
		if strings.TrimSpace(grant.Role) == "" {
			return fmt.Sprintf("%s[%d].role is required", path, i)
		}
		if len(grant.Privileges) == 0 {
			return fmt.Sprintf("%s[%d].privileges is required", path, i)
		}
		for j, privilege := range grant.Privileges {
			if _, ok := normalizeDatabasePrivilege(objectKind, privilege); !ok {
				return fmt.Sprintf("%s[%d].privileges[%d] %q is not supported for %s",
					path, i, j, privilege, objectKind)
			}
		}
	}
	return ""
}

func markPostgresDatabaseStatus(db *postgresv1alpha1.PostgresDatabase, applied bool, reason, message string) {
	db.Status.Applied = applied
	db.Status.ObservedGeneration = db.Generation
	db.Status.Message = message
	conditionStatus := metav1.ConditionFalse
	if applied {
		conditionStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type:               PostgresDatabaseConditionReady,
		Status:             conditionStatus,
		ObservedGeneration: db.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// statusUpdate persists the in-memory status on the API. On a transient
// conflict (HTTP 409) we re-fetch the resource, copy our desired status
// snapshot back onto it, and retry once — silently dropping the
// conflict caused PostgresDatabase to remain `status: {}` indefinitely
// when finalizer-add + status-update collided on the same generation
// (PG18 kind smoke iter#3 observation).
func (r *PostgresDatabaseReconciler) statusUpdate(ctx context.Context, db *postgresv1alpha1.PostgresDatabase) error {
	desired := db.Status.DeepCopy()
	err := r.Status().Update(ctx, db)
	if err == nil {
		return nil
	}
	if !apierrors.IsConflict(err) {
		return err
	}
	var fresh postgresv1alpha1.PostgresDatabase
	if getErr := r.Get(ctx, client.ObjectKeyFromObject(db), &fresh); getErr != nil {
		return getErr
	}
	fresh.Status = *desired
	if retryErr := r.Status().Update(ctx, &fresh); retryErr != nil {
		if apierrors.IsConflict(retryErr) {
			return nil // give up after one retry; the next reconcile will refresh.
		}
		return retryErr
	}
	db.ResourceVersion = fresh.ResourceVersion
	return nil
}

func postgresDatabaseReconcileCommand(db *postgresv1alpha1.PostgresDatabase) []string {
	return []string{"/bin/sh", "-ec", postgresDatabaseReconcileScript(db)}
}

func postgresDatabaseReconcileScript(db *postgresv1alpha1.PostgresDatabase) string {
	name := strings.TrimSpace(db.Spec.Name)
	owner := strings.TrimSpace(db.Spec.Owner)
	tablespace := strings.TrimSpace(db.Spec.Tablespace)
	ensure := defaultedDatabaseEnsure(db.Spec.Ensure)

	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("psql_base='psql -v ON_ERROR_STOP=1 -X -q -d postgres'\n")
	if ensure == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(&b,
			"if [ \"$(eval \"$psql_base\" -At -c %s)\" = \"1\" ]; then\n  eval \"$psql_base\" -c %s\nfi\n",
			shellQuote("SELECT 1 FROM pg_database WHERE datname = "+sqlLiteral(name)),
			shellQuote("DROP DATABASE "+sqlIdent(name)),
		)
		return b.String()
	}

	fmt.Fprintf(&b,
		"if [ \"$(eval \"$psql_base\" -At -c %s)\" = \"1\" ]; then\n  eval \"$psql_base\" -c %s\n",
		shellQuote("SELECT 1 FROM pg_database WHERE datname = "+sqlLiteral(name)),
		shellQuote("ALTER DATABASE "+sqlIdent(name)+" OWNER TO "+sqlIdent(owner)),
	)
	if tablespace != "" {
		fmt.Fprintf(&b, "  eval \"$psql_base\" -c %s\n",
			shellQuote("ALTER DATABASE "+sqlIdent(name)+" SET TABLESPACE "+sqlIdent(tablespace)),
		)
	}
	fmt.Fprintf(&b, "else\n  eval \"$psql_base\" -c %s\nfi\n",
		shellQuote(postgresDatabaseCreateStatement(name, owner, tablespace)),
	)
	appendPostgresDatabaseGrantScripts(&b, name, postgresDatabaseObjectKindDatabase, name, db.Spec.Privileges)
	for _, schema := range db.Spec.Schemas {
		appendPostgresDatabaseSchemaScript(&b, name, schema)
	}
	for _, extension := range db.Spec.Extensions {
		appendPostgresDatabaseExtensionScript(&b, name, extension)
	}
	for _, server := range db.Spec.Servers {
		if defaultedDatabaseEnsure(server.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
			appendPostgresDatabaseServerScript(&b, name, server)
		}
	}
	for _, fdw := range db.Spec.FDWs {
		appendPostgresDatabaseFDWScript(&b, name, fdw)
	}
	for _, server := range db.Spec.Servers {
		if defaultedDatabaseEnsure(server.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
			continue
		}
		appendPostgresDatabaseServerScript(&b, name, server)
	}
	return b.String()
}

func postgresDatabaseCreateStatement(name, owner, tablespace string) string {
	stmt := "CREATE DATABASE " + sqlIdent(name) + " OWNER " + sqlIdent(owner)
	if tablespace != "" {
		stmt += " TABLESPACE " + sqlIdent(tablespace)
	}
	return stmt
}

func appendPostgresDatabaseSchemaScript(
	b *strings.Builder,
	dbName string,
	schema postgresv1alpha1.DatabaseSchemaSpec,
) {
	name := strings.TrimSpace(schema.Name)
	if name == "" {
		return
	}
	ensure := defaultedDatabaseEnsure(schema.Ensure)
	if ensure == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote("DROP SCHEMA IF EXISTS "+sqlIdent(name)),
		)
		return
	}
	owner := strings.TrimSpace(schema.Owner)
	if owner == "" {
		owner = strings.TrimSpace(dbName)
	}
	fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
		shellQuote(dbName),
		shellQuote("CREATE SCHEMA IF NOT EXISTS "+sqlIdent(name)+" AUTHORIZATION "+sqlIdent(owner)+"; ALTER SCHEMA "+sqlIdent(name)+" OWNER TO "+sqlIdent(owner)),
	)
	appendPostgresDatabaseGrantScripts(b, dbName, postgresDatabaseObjectKindSchema, name, schema.Privileges)
}

func appendPostgresDatabaseExtensionScript(
	b *strings.Builder,
	dbName string,
	extension postgresv1alpha1.DatabaseExtensionSpec,
) {
	name := strings.TrimSpace(extension.Name)
	if name == "" {
		return
	}
	ensure := defaultedDatabaseEnsure(extension.Ensure)
	if ensure == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote("DROP EXTENSION IF EXISTS "+sqlIdent(name)),
		)
		return
	}

	var stmt strings.Builder
	stmt.WriteString("CREATE EXTENSION IF NOT EXISTS ")
	stmt.WriteString(sqlIdent(name))
	if version := strings.TrimSpace(extension.Version); version != "" {
		stmt.WriteString(" VERSION ")
		stmt.WriteString(sqlLiteral(version))
	}
	if schema := strings.TrimSpace(extension.Schema); schema != "" {
		stmt.WriteString(" WITH SCHEMA ")
		stmt.WriteString(sqlIdent(schema))
	}
	fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
		shellQuote(dbName),
		shellQuote(stmt.String()),
	)
}

func appendPostgresDatabaseFDWScript(
	b *strings.Builder,
	dbName string,
	fdw postgresv1alpha1.DatabaseFDWSpec,
) {
	name := strings.TrimSpace(fdw.Name)
	if name == "" {
		return
	}
	if defaultedDatabaseEnsure(fdw.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote("DROP FOREIGN DATA WRAPPER IF EXISTS "+sqlIdent(name)),
		)
		return
	}

	existsQuery := "SELECT 1 FROM pg_foreign_data_wrapper WHERE fdwname = " + sqlLiteral(name)
	createStmt := postgresDatabaseCreateFDWStatement(fdw)
	fmt.Fprintf(b,
		"if [ \"$(psql -v ON_ERROR_STOP=1 -X -q -d %s -At -c %s)\" = \"1\" ]; then\n",
		shellQuote(dbName),
		shellQuote(existsQuery),
	)
	if alterStmt := postgresDatabaseAlterFDWStatement(fdw); alterStmt != "" {
		fmt.Fprintf(b, "  psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote(alterStmt),
		)
	} else {
		b.WriteString("  true\n")
	}
	fmt.Fprintf(b, "else\n  psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\nfi\n",
		shellQuote(dbName),
		shellQuote(createStmt),
	)
	if owner := strings.TrimSpace(fdw.Owner); owner != "" {
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote("ALTER FOREIGN DATA WRAPPER "+sqlIdent(name)+" OWNER TO "+sqlIdent(owner)),
		)
	}
	appendPostgresDatabaseFDWOptionScripts(b, dbName, name, fdw.Options)
	for _, usage := range fdw.Usage {
		appendPostgresDatabaseUsageScript(b, dbName, "FOREIGN DATA WRAPPER", name, usage)
	}
}

func appendPostgresDatabaseServerScript(
	b *strings.Builder,
	dbName string,
	server postgresv1alpha1.DatabaseServerSpec,
) {
	name := strings.TrimSpace(server.Name)
	fdw := strings.TrimSpace(server.FDW)
	if name == "" || fdw == "" {
		return
	}
	if defaultedDatabaseEnsure(server.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote("DROP SERVER IF EXISTS "+sqlIdent(name)),
		)
		return
	}

	fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
		shellQuote(dbName),
		shellQuote(postgresDatabaseCreateServerStatement(server)),
	)
	appendPostgresDatabaseServerOptionScripts(b, dbName, name, server.Options)
	for _, usage := range server.Usage {
		appendPostgresDatabaseUsageScript(b, dbName, "FOREIGN SERVER", name, usage)
	}
}

func postgresDatabaseCreateFDWStatement(fdw postgresv1alpha1.DatabaseFDWSpec) string {
	var stmt strings.Builder
	stmt.WriteString("CREATE FOREIGN DATA WRAPPER ")
	stmt.WriteString(sqlIdent(strings.TrimSpace(fdw.Name)))
	if clauses := postgresDatabaseFDWDefinitionClauses(fdw); clauses != "" {
		stmt.WriteString(" ")
		stmt.WriteString(clauses)
	}
	if options := postgresDatabaseCreateOptionsClause(fdw.Options); options != "" {
		stmt.WriteString(" ")
		stmt.WriteString(options)
	}
	return stmt.String()
}

func postgresDatabaseAlterFDWStatement(fdw postgresv1alpha1.DatabaseFDWSpec) string {
	clauses := postgresDatabaseFDWDefinitionClauses(fdw)
	if clauses == "" {
		return ""
	}
	return "ALTER FOREIGN DATA WRAPPER " + sqlIdent(strings.TrimSpace(fdw.Name)) + " " + clauses
}

func postgresDatabaseFDWDefinitionClauses(fdw postgresv1alpha1.DatabaseFDWSpec) string {
	clauses := make([]string, 0, 2)
	if handler := strings.TrimSpace(fdw.Handler); handler != "" {
		if handler == "-" {
			clauses = append(clauses, "NO HANDLER")
		} else {
			clauses = append(clauses, "HANDLER "+sqlQualifiedIdent(handler))
		}
	}
	if validator := strings.TrimSpace(fdw.Validator); validator != "" {
		if validator == "-" {
			clauses = append(clauses, "NO VALIDATOR")
		} else {
			clauses = append(clauses, "VALIDATOR "+sqlQualifiedIdent(validator))
		}
	}
	return strings.Join(clauses, " ")
}

func postgresDatabaseCreateServerStatement(server postgresv1alpha1.DatabaseServerSpec) string {
	var stmt strings.Builder
	stmt.WriteString("CREATE SERVER IF NOT EXISTS ")
	stmt.WriteString(sqlIdent(strings.TrimSpace(server.Name)))
	stmt.WriteString(" FOREIGN DATA WRAPPER ")
	stmt.WriteString(sqlIdent(strings.TrimSpace(server.FDW)))
	if options := postgresDatabaseCreateOptionsClause(server.Options); options != "" {
		stmt.WriteString(" ")
		stmt.WriteString(options)
	}
	return stmt.String()
}

func postgresDatabaseCreateOptionsClause(options []postgresv1alpha1.DatabaseOptionSpec) string {
	parts := make([]string, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.Name) == "" ||
			defaultedDatabaseEnsure(option.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
			continue
		}
		parts = append(parts, sqlIdent(strings.TrimSpace(option.Name))+" "+sqlLiteral(option.Value))
	}
	if len(parts) == 0 {
		return ""
	}
	return "OPTIONS (" + strings.Join(parts, ", ") + ")"
}

func appendPostgresDatabaseFDWOptionScripts(
	b *strings.Builder,
	dbName string,
	fdwName string,
	options []postgresv1alpha1.DatabaseOptionSpec,
) {
	existsQuery := func(option string) string {
		return "SELECT 1 FROM pg_options_to_table((SELECT fdwoptions FROM pg_foreign_data_wrapper WHERE fdwname = " +
			sqlLiteral(fdwName) + ")) WHERE option_name = " + sqlLiteral(option)
	}
	statement := func(action, option, value string) string {
		return "ALTER FOREIGN DATA WRAPPER " + sqlIdent(fdwName) + " OPTIONS (" +
			postgresDatabaseOptionAction(action, option, value) + ")"
	}
	appendPostgresDatabaseOptionScripts(b, dbName, options, existsQuery, statement)
}

func appendPostgresDatabaseServerOptionScripts(
	b *strings.Builder,
	dbName string,
	serverName string,
	options []postgresv1alpha1.DatabaseOptionSpec,
) {
	existsQuery := func(option string) string {
		return "SELECT 1 FROM pg_options_to_table((SELECT srvoptions FROM pg_foreign_server WHERE srvname = " +
			sqlLiteral(serverName) + ")) WHERE option_name = " + sqlLiteral(option)
	}
	statement := func(action, option, value string) string {
		return "ALTER SERVER " + sqlIdent(serverName) + " OPTIONS (" +
			postgresDatabaseOptionAction(action, option, value) + ")"
	}
	appendPostgresDatabaseOptionScripts(b, dbName, options, existsQuery, statement)
}

func appendPostgresDatabaseOptionScripts(
	b *strings.Builder,
	dbName string,
	options []postgresv1alpha1.DatabaseOptionSpec,
	existsQuery func(string) string,
	statement func(action, option, value string) string,
) {
	for _, option := range options {
		name := strings.TrimSpace(option.Name)
		if name == "" {
			continue
		}
		if defaultedDatabaseEnsure(option.Ensure) == postgresv1alpha1.DatabaseEnsureAbsent {
			fmt.Fprintf(b,
				"if [ \"$(psql -v ON_ERROR_STOP=1 -X -q -d %s -At -c %s)\" = \"1\" ]; then\n  psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\nfi\n",
				shellQuote(dbName),
				shellQuote(existsQuery(name)),
				shellQuote(dbName),
				shellQuote(statement("DROP", name, "")),
			)
			continue
		}
		fmt.Fprintf(b,
			"if [ \"$(psql -v ON_ERROR_STOP=1 -X -q -d %s -At -c %s)\" = \"1\" ]; then\n  psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\nelse\n  psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\nfi\n",
			shellQuote(dbName),
			shellQuote(existsQuery(name)),
			shellQuote(dbName),
			shellQuote(statement("SET", name, option.Value)),
			shellQuote(dbName),
			shellQuote(statement("ADD", name, option.Value)),
		)
	}
}

func postgresDatabaseOptionAction(action, option, value string) string {
	if action == "DROP" {
		return action + " " + sqlIdent(option)
	}
	return action + " " + sqlIdent(option) + " " + sqlLiteral(value)
}

func appendPostgresDatabaseUsageScript(
	b *strings.Builder,
	dbName string,
	objectKind string,
	objectName string,
	usage postgresv1alpha1.DatabaseUsageSpec,
) {
	role := strings.TrimSpace(usage.Name)
	if role == "" {
		return
	}
	action := "GRANT USAGE ON " + objectKind + " " + sqlIdent(objectName) + " TO " + sqlIdent(role)
	if defaultedDatabaseUsageType(usage.Type) == postgresv1alpha1.DatabaseUsageRevoke {
		action = "REVOKE USAGE ON " + objectKind + " " + sqlIdent(objectName) + " FROM " + sqlIdent(role)
	}
	fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
		shellQuote(dbName),
		shellQuote(action),
	)
}

func appendPostgresDatabaseGrantScripts(
	b *strings.Builder,
	dbName string,
	objectKind string,
	objectName string,
	grants []postgresv1alpha1.DatabaseGrantSpec,
) {
	for _, grant := range grants {
		role := strings.TrimSpace(grant.Role)
		if role == "" {
			continue
		}
		privileges := make([]string, 0, len(grant.Privileges))
		for _, privilege := range grant.Privileges {
			if normalized, ok := normalizeDatabasePrivilege(objectKind, privilege); ok {
				privileges = append(privileges, normalized)
			}
		}
		if len(privileges) == 0 {
			continue
		}

		action := "GRANT " + strings.Join(privileges, ", ") +
			" ON " + objectKind + " " + sqlIdent(objectName) +
			" TO " + sqlIdent(role)
		if defaultedDatabaseUsageType(grant.Type) == postgresv1alpha1.DatabaseUsageRevoke {
			action = "REVOKE " + strings.Join(privileges, ", ") +
				" ON " + objectKind + " " + sqlIdent(objectName) +
				" FROM " + sqlIdent(role)
		}
		fmt.Fprintf(b, "psql -v ON_ERROR_STOP=1 -X -q -d %s -c %s\n",
			shellQuote(dbName),
			shellQuote(action),
		)
	}
}

func normalizeDatabasePrivilege(objectKind string, privilege string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(privilege)) {
	case "ALL PRIVILEGES":
		return "ALL PRIVILEGES", true
	case "CONNECT":
		return "CONNECT", objectKind == postgresDatabaseObjectKindDatabase
	case "TEMP", "TEMPORARY":
		return "TEMPORARY", objectKind == postgresDatabaseObjectKindDatabase
	case "CREATE":
		return "CREATE", objectKind == postgresDatabaseObjectKindDatabase || objectKind == postgresDatabaseObjectKindSchema
	case "USAGE":
		return "USAGE", objectKind == postgresDatabaseObjectKindSchema
	default:
		return "", false
	}
}

func defaultedDatabaseEnsure(value postgresv1alpha1.DatabaseEnsure) postgresv1alpha1.DatabaseEnsure {
	if value == "" {
		return defaultPostgresDatabaseEnsure
	}
	return value
}

func defaultedDatabaseReclaimPolicy(
	value postgresv1alpha1.DatabaseReclaimPolicy,
) postgresv1alpha1.DatabaseReclaimPolicy {
	if value == "" {
		return defaultPostgresDatabaseReclaimPolicy
	}
	return value
}

func defaultedDatabaseUsageType(value postgresv1alpha1.DatabaseUsageType) postgresv1alpha1.DatabaseUsageType {
	if value == "" {
		return postgresv1alpha1.DatabaseUsageGrant
	}
	return value
}

func sqlIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqlQualifiedIdent(value string) string {
	parts := strings.Split(value, ".")
	for i, part := range parts {
		parts[i] = sqlIdent(strings.TrimSpace(part))
	}
	return strings.Join(parts, ".")
}

func sqlLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func shellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'"'"'`) + `'`
}

func (r *PostgresDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.PostgresDatabase{}).
		Named("postgresdatabase").
		Complete(r)
}
