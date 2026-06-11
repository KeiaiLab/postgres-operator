/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// keiailab-commons v0.11.0 채택 회귀 가드.
//
//  1. buildCertificate — commons pkg/certmanager 위임 후에도 기존 자체 구현과
//     출력(unstructured spec)이 동일함을 봉인 (운영 cert 재발급 트리거 0 보장).
//  2. finalizer both-recognize — 구 prefix ("postgres.keiailab.io/...") finalizer
//     만 부착된 라이브 객체도 cleanup 경로가 인식/제거함을 봉인 (commons
//     pkg/finalizer + 신규 "<resource>.keiailab.com/finalizer" 전환 거동 변화).
package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestBuildCertificateCommonsShapePreserved 는 commons BuildCertificate 위임 후
// Certificate CR 의 핵심 spec (secretName / commonName / SAN 4단 FQDN /
// issuerRef kind fallback + group / usages / privateKey ECDSA-256-Always) 이
// 기존 자체 구현 출력과 동일함을 검증한다.
func TestBuildCertificateCommonsShapePreserved(t *testing.T) {
	t.Parallel()
	const namespace = "default" // package 테스트 컨벤션 정합 (goconst)
	cluster := newCluster()
	cluster.Spec.Shards.InitialCount = 1
	cluster.Spec.TLS = &postgresv1alpha1.TLSSpec{
		Enabled:   true,
		IssuerRef: &postgresv1alpha1.TLSIssuerRef{Name: "demo-issuer"}, // Kind 미지정 → "Issuer" fallback
	}

	cert := buildCertificate(cluster)
	if cert == nil {
		t.Fatal("buildCertificate = nil, want Certificate CR")
	}

	if cert.GetName() != "demo-tls" || cert.GetNamespace() != namespace {
		t.Fatalf("metadata = %s/%s, want default/demo-tls", cert.GetNamespace(), cert.GetName())
	}
	if gvk := cert.GroupVersionKind(); gvk.Group != "cert-manager.io" || gvk.Version != "v1" || gvk.Kind != "Certificate" {
		t.Fatalf("GVK = %v, want cert-manager.io/v1 Certificate", gvk)
	}
	wantLabels := map[string]string{
		"app.kubernetes.io/name":       "postgrescluster",
		"app.kubernetes.io/instance":   "demo",
		"app.kubernetes.io/managed-by": "keiailab-postgres-operator",
		"postgres.keiailab.io/role":    "server-tls",
	}
	if !reflect.DeepEqual(cert.GetLabels(), wantLabels) {
		t.Fatalf("labels = %v, want %v", cert.GetLabels(), wantLabels)
	}

	secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if secretName != "demo-tls" {
		t.Fatalf("spec.secretName = %q, want demo-tls", secretName)
	}
	commonName, _, _ := unstructured.NestedString(cert.Object, "spec", "commonName")
	if commonName != "demo" {
		t.Fatalf("spec.commonName = %q, want demo", commonName)
	}
	dnsNames, _, _ := unstructured.NestedSlice(cert.Object, "spec", "dnsNames")
	wantDNS := []any{
		"demo",
		"demo-shard-0-headless",
		"demo-shard-0-headless.default",
		"demo-shard-0-headless.default.svc",
		"demo-shard-0-headless.default.svc.cluster.local",
	}
	if !reflect.DeepEqual(dnsNames, wantDNS) {
		t.Fatalf("spec.dnsNames = %v, want %v", dnsNames, wantDNS)
	}
	issuerRef, _, _ := unstructured.NestedMap(cert.Object, "spec", "issuerRef")
	wantIssuer := map[string]any{"name": "demo-issuer", "kind": "Issuer", "group": "cert-manager.io"}
	if !reflect.DeepEqual(issuerRef, wantIssuer) {
		t.Fatalf("spec.issuerRef = %v, want %v", issuerRef, wantIssuer)
	}
	usages, _, _ := unstructured.NestedSlice(cert.Object, "spec", "usages")
	if !reflect.DeepEqual(usages, []any{"server auth", "client auth"}) {
		t.Fatalf("spec.usages = %v, want [server auth client auth]", usages)
	}
	privateKey, _, _ := unstructured.NestedMap(cert.Object, "spec", "privateKey")
	wantKey := map[string]any{"algorithm": "ECDSA", "size": int64(256), "rotationPolicy": "Always"}
	if !reflect.DeepEqual(privateKey, wantKey) {
		t.Fatalf("spec.privateKey = %v, want %v", privateKey, wantKey)
	}
}

// TestPostgresDatabaseDeleteRecognizesLegacyFinalizer 는 구 prefix finalizer 만
// 부착된 PostgresDatabase (구 operator 가 부착한 라이브 객체 시나리오) 의 삭제가
// DROP DATABASE 실행 + legacy finalizer 제거로 완결됨을 검증한다.
func TestPostgresDatabaseDeleteRecognizesLegacyFinalizer(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	cluster := newPostgresDatabaseCluster()
	db := newPostgresDatabase()
	db.Spec.DatabaseReclaimPolicy = postgresv1alpha1.DatabaseReclaimDelete
	db.Finalizers = []string{postgresDatabaseFinalizerLegacy}
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, db).
		WithStatusSubresource(&postgresv1alpha1.PostgresDatabase{}).
		Build()
	if err := c.Delete(context.Background(), db); err != nil {
		t.Fatalf("Delete PostgresDatabase: %v", err)
	}
	r := &PostgresDatabaseReconciler{Client: c, Scheme: scheme, SQLExecutor: executor}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: db.Namespace, Name: db.Name},
	}); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1 (DROP via legacy finalizer 인식)", len(executor.calls))
	}
	if command := strings.Join(executor.calls[0].command, " "); !strings.Contains(command, `DROP DATABASE "appdb"`) {
		t.Fatalf("delete command missing DROP DATABASE:\n%s", command)
	}
	var got postgresv1alpha1.PostgresDatabase
	err := c.Get(context.Background(), client.ObjectKey{Namespace: db.Namespace, Name: db.Name}, &got)
	if apierrors.IsNotFound(err) {
		return // 마지막 finalizer 제거 → GC 완료
	}
	if err != nil {
		t.Fatalf("Get back: %v", err)
	}
	if len(got.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want legacy finalizer removed", got.Finalizers)
	}
}

// TestPostgresUserDeleteRecognizesLegacyFinalizer 는 구 prefix finalizer 만
// 부착된 PostgresUser 의 retain 정책 삭제가 SQL 실행 없이 legacy finalizer
// 제거로 완결됨을 검증한다.
func TestPostgresUserDeleteRecognizesLegacyFinalizer(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	user := newPostgresUser()
	user.Finalizers = []string{postgresUserFinalizerLegacy} // reclaim 미지정 = retain default
	executor := &fakeDatabaseSQLExecutor{}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&postgresv1alpha1.PostgresUser{}).
		Build()
	if err := c.Delete(context.Background(), user); err != nil {
		t.Fatalf("Delete PostgresUser: %v", err)
	}
	r := &PostgresUserReconciler{Client: c, Scheme: scheme, SQLExecutor: executor}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: user.Namespace, Name: user.Name},
	}); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0 (retain 정책)", len(executor.calls))
	}
	var got postgresv1alpha1.PostgresUser
	err := c.Get(context.Background(), client.ObjectKey{Namespace: user.Namespace, Name: user.Name}, &got)
	if apierrors.IsNotFound(err) {
		return // 마지막 finalizer 제거 → GC 완료
	}
	if err != nil {
		t.Fatalf("Get back: %v", err)
	}
	if len(got.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want legacy finalizer removed", got.Finalizers)
	}
}
