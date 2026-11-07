/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Command pg-router is a PoC PostgreSQL wire-protocol v3 proxy that routes a
// client connection to a shard backend using the in-tree vindex
// (internal/router.ResolveShard). It revives the G3 router package (restored from
// before its #124 dead-code removal) as a *live consumer*, so that code is no
// longer dead (ROADMAP G3 / RFC 0004).
//
// Scope (PoC): single-shard fast-path. The routing key is taken from the startup
// "database" (else "user") parameter; the connection is proxied to the resolved
// shard backend. Full SQL-parse routing and multi-shard scatter-gather forwarding
// are future work (see docs/sharding/ROUTER-GAP-ANALYSIS.ko.md).
//
// Two pluggable, swappable concerns:
//   - Topology (key -> shard): PGROUTER_TOPOLOGY=static|crd.
//   - Backend  (shard -> addr): PGROUTER_BACKEND=env|template|status.
//   - status = read the *current Ready primary* from PostgresCluster.status
//     (failover-aware): when a shard primary dies, the operator promotes a
//     replica and updates status; the router follows. A shard with no Ready
//     primary yields a graceful PostgreSQL ErrorResponse to the client (no hang,
//     no silent drop).
//
// Config (env): PGROUTER_LISTEN (:5432), PGROUTER_TOPOLOGY, PGROUTER_BACKEND
// (env|template|primary-service|status), PGROUTER_CLUSTER, PGROUTER_KEYSPACE (default),
// PGROUTER_NAMESPACE (default), PGROUTER_REFRESH (10s), PGROUTER_DIAL_TIMEOUT (5s),
// PGROUTER_BACKEND_TEMPLATE ({cluster}/{shard}/{namespace}),
// PGROUTER_BACKEND_SHARD_0 / _1 (env mode host:port). primary-service mode resolves each
// shard to `<cluster>-<shard>-primary` (operator-published failover-following Service).
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

func main() {
	ctx := context.Background()
	addr := env("PGROUTER_LISTEN", ":5432")
	provider, resolve, readResolve, err := buildRouting(ctx)
	if err != nil {
		log.Fatalf("pg-router: build routing: %v", err)
	}
	dialer := newBackendDialer(
		envDuration("PGROUTER_DIAL_TIMEOUT", 5*time.Second),
		envDuration("PGROUTER_DIAL_BACKOFF", 100*time.Millisecond),
		envDuration("PGROUTER_BREAKER_COOLDOWN", 5*time.Second),
		envInt("PGROUTER_DIAL_RETRIES", 1),
		envInt("PGROUTER_BREAKER_THRESHOLD", 3),
	)
	// 라우팅 모드: connection(기본, startup param) | query(첫 쿼리 인지 라우팅, PoC).
	mode := strings.ToLower(env("PGROUTER_MODE", "connection"))
	qr := newQueryRouter(provider, resolve, readResolve)
	serverVersion := env("PGROUTER_SERVER_VERSION", "18.0")
	backendPassword := env("PGROUTER_BACKEND_PASSWORD", "") // 백엔드 인증 대행(scram/cleartext)용. ""=trust.

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("pg-router: listen %s: %v", addr, err)
	}
	// active-connection 게이지를 노출하는 /metrics 서버(HPA ScaleOnActiveConnections
	// 의 custom-metrics 소스). PGROUTER_METRICS_ADDR="" 이면 비활성.
	go serveMetrics(env("PGROUTER_METRICS_ADDR", ":9187"))
	log.Printf("pg-router PoC listening on %s (mode=%s topology=%s backend=%s)",
		addr, mode, env("PGROUTER_TOPOLOGY", "static"), env("PGROUTER_BACKEND", "env"))
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("pg-router: accept: %v", err)
			continue
		}
		// trackConn 이 active-connection 게이지를 연결 수명 동안 유지한다.
		if mode == "query" {
			go trackConn(func() { handleQueryMode(conn, qr, dialer, serverVersion, backendPassword) })
		} else {
			go trackConn(func() { handleConn(conn, provider, resolve, dialer) })
		}
	}
}

// buildRouting wires the (pluggable) topology provider, the write (primary)
// backend resolver, and the read (replica) backend resolver; it also starts a
// refresh loop for any dynamic source. The read resolver routes read-only queries
// to replicas (env: PGROUTER_BACKEND_<SHARD>_REPLICA; status: Ready replica from
// PostgresCluster.status, failover-aware). nil read resolver ⇒ reads use primary.
func buildRouting(
	ctx context.Context,
) (router.TopologyProvider, router.BackendResolver, router.BackendResolver, error) {
	topoMode := strings.ToLower(env("PGROUTER_TOPOLOGY", "static"))
	backendMode := strings.ToLower(env("PGROUTER_BACKEND", "env"))
	ns := env("PGROUTER_NAMESPACE", "default")
	cluster := env("PGROUTER_CLUSTER", "quickstart")
	keyspace := env("PGROUTER_KEYSPACE", "default")

	var k8s client.WithWatch
	if topoMode == "crd" || backendMode == "status" {
		c, err := newK8sClient()
		if err != nil {
			return nil, nil, nil, err
		}
		k8s = c
	}

	// Topology provider (key -> shard).
	var provider router.TopologyProvider
	var crdProvider *router.CRDTopologyProvider
	switch topoMode {
	case "", "static":
		provider = router.StaticTopologyProvider{T: router.Topology{Cluster: cluster, Keyspace: keyspace, Spec: shardSpec()}}
		setRouterReady(true) // static 토폴로지는 즉시 라우팅 가능.
	case "crd":
		crdProvider = &router.CRDTopologyProvider{
			Lister: clientLister{c: k8s}, Namespace: ns, Cluster: cluster, Keyspace: keyspace,
		}
		if _, err := crdProvider.Refresh(ctx); err != nil {
			log.Printf("pg-router: initial topology refresh: %v (will retry)", err)
		} else {
			setRouterReady(true) // 초기 토폴로지 확보 → readiness.
		}
		provider = crdProvider
	default:
		return nil, nil, nil, fmt.Errorf("unknown PGROUTER_TOPOLOGY %q (want static|crd)", topoMode)
	}

	// Backend resolver (shard -> addr): write=primary, read=replica.
	var resolve, readResolve router.BackendResolver
	var statusRes *router.StatusBackendResolver
	var statusReader router.ClusterStatusReader
	switch backendMode {
	case "", "env":
		resolve = envBackendResolver
		readResolve = envReadBackendResolver
	case "template":
		resolve = templateResolver()
	case "primary-service":
		// operator-published per-shard primary Service(ExternalName failover-follow) 소비.
		// reads 도 primary Service 로(이 모드는 replica read 미지원 — 필요 시 status 모드).
		r := primaryServiceResolver(cluster, ns)
		resolve = r
		readResolve = r
	case "status":
		statusRes = router.NewStatusBackendResolver()
		statusReader = clusterStatusReader{c: k8s}
		if err := updateStatus(ctx, statusReader, statusRes, ns, cluster); err != nil {
			log.Printf("pg-router: initial status read: %v (will retry)", err)
		}
		resolve = statusRes.Resolve
		readResolve = statusRes.ResolveRead // Ready replica, falls back to primary.
	default:
		return nil, nil, nil, fmt.Errorf(
			"unknown PGROUTER_BACKEND %q (want env|template|primary-service|status)", backendMode)
	}

	if crdProvider != nil || statusRes != nil {
		// changeCh: watch 이벤트가 즉시 refresh 를 트리거한다(interval 은 fallback).
		// 버퍼 cap 1 로 버스트를 자연 coalesce.
		changeCh := make(chan struct{}, 1)
		if k8s != nil {
			watchShardRangesAndCluster(ctx, k8s, ns, changeCh, envDuration("PGROUTER_WATCH_BACKOFF", 2*time.Second))
		}
		go refreshLoop(ctx, crdProvider, statusReader, statusRes, ns, cluster, changeCh)
	}
	return provider, resolve, readResolve, nil
}

// newK8sClient builds a controller-runtime watching client with the operator scheme.
// WithWatch lets the router watch ShardRange/PostgresCluster for immediate hot-reload
// (interval polling remains as a fallback).
func newK8sClient() (client.WithWatch, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("scheme: %w", err)
	}
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}
	return client.NewWithWatch(cfg, client.Options{Scheme: scheme})
}

// refreshLoop re-reads dynamic sources on the PGROUTER_REFRESH interval (fallback) and
// *immediately* on a watch change signal (changeCh) — the ShardRange topology and/or the
// PostgresCluster primary-endpoint status. Watch-driven refresh shortens the failover /
// resharding hot-reload window vs. interval-only polling; the interval remains as a safety
// net if watches drop.
func refreshLoop(
	ctx context.Context,
	cp *router.CRDTopologyProvider,
	reader router.ClusterStatusReader,
	res *router.StatusBackendResolver,
	ns, cluster string,
	changeCh <-chan struct{},
) {
	t := time.NewTicker(envDuration("PGROUTER_REFRESH", 10*time.Second))
	defer t.Stop()
	debounce := envDuration("PGROUTER_WATCH_DEBOUNCE", 200*time.Millisecond)

	doRefresh := func() {
		if cp != nil {
			if _, err := cp.Refresh(ctx); err != nil {
				log.Printf("pg-router: topology refresh: %v", err)
			} else {
				setRouterReady(true) // 초기 실패 후 refresh 로 토폴로지 확보 시 readiness 회복.
			}
		}
		if res != nil && reader != nil {
			if err := updateStatus(ctx, reader, res, ns, cluster); err != nil {
				log.Printf("pg-router: status refresh: %v", err)
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			doRefresh()
		case <-changeCh:
			// 짧은 debounce 로 연속 변경(예: ShardRange 여러 항목 편집)을 1회 refresh 로 합침.
			coalesce(ctx, changeCh, debounce)
			doRefresh()
		}
	}
}

// coalesce 는 debounce 창 동안 changeCh 에 쌓인 추가 신호를 흡수한다(버스트 → 1 refresh).
func coalesce(ctx context.Context, changeCh <-chan struct{}, debounce time.Duration) {
	if debounce <= 0 {
		return
	}
	t := time.NewTimer(debounce)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changeCh:
			// 추가 신호 흡수 — 타이머는 유지(고정 창).
		case <-t.C:
			return
		}
	}
}

// updateStatus reads the cluster's per-shard status and updates the failover-aware
// backend resolver with the current Ready primary endpoints.
func updateStatus(
	ctx context.Context,
	reader router.ClusterStatusReader,
	res *router.StatusBackendResolver,
	ns, cluster string,
) error {
	shards, err := reader.ClusterShardStatus(ctx, ns, cluster)
	if err != nil {
		return err
	}
	res.Update(shards)
	return nil
}

// clientLister reads ShardRange via controller-runtime (K8s isolated at the edge).
type clientLister struct{ c client.Client }

func (l clientLister) ListShardRanges(ctx context.Context, ns string) ([]v1alpha1.ShardRange, error) {
	var list v1alpha1.ShardRangeList
	if err := l.c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// clusterStatusReader reads PostgresCluster.status.shards via controller-runtime.
type clusterStatusReader struct{ c client.Client }

func (r clusterStatusReader) ClusterShardStatus(
	ctx context.Context, ns, cluster string,
) ([]v1alpha1.ShardStatus, error) {
	var pc v1alpha1.PostgresCluster
	if err := r.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: cluster}, &pc); err != nil {
		return nil, err
	}
	return pc.Status.Shards, nil
}

func handleConn(
	clientConn net.Conn,
	provider router.TopologyProvider,
	resolve router.BackendResolver,
	dialer *backendDialer,
) {
	defer func() { _ = clientConn.Close() }()

	raw, params, err := readStartup(clientConn)
	if err != nil {
		log.Printf("pg-router: startup read: %v", err)
		return
	}
	key := params["database"]
	if key == "" {
		key = params["user"]
	}
	topo, err := provider.Current(context.Background())
	if err != nil {
		log.Printf("pg-router: topology unavailable: %v", err)
		writePgError(clientConn, "08006", "router topology unavailable: "+err.Error())
		return
	}
	shardID, err := topo.Shard(key)
	if err != nil {
		log.Printf("pg-router: resolve key %q: %v", key, err)
		writePgError(clientConn, "08006", fmt.Sprintf("no shard for key %q: %v", key, err))
		return
	}
	backend, err := resolve(shardID)
	if err != nil {
		// Shard down / mid-failover: fail the client gracefully (no silent drop).
		log.Printf("pg-router: backend for shard %s: %v", shardID, err)
		writePgError(clientConn, "08006", fmt.Sprintf("shard %s unavailable: %v", shardID, err))
		return
	}
	server, err := dialer.Dial(backend)
	if err != nil {
		log.Printf("pg-router: dial backend %s (shard %s): %v", backend, shardID, err)
		writePgError(clientConn, "08006", fmt.Sprintf("cannot reach shard %s (%s): %v", shardID, backend, err))
		return
	}
	defer func() { _ = server.Close() }()
	log.Printf("pg-router: routed key=%q -> shard=%s backend=%s", key, shardID, backend)

	// Forward the original startup message, then proxy both directions.
	if _, err := server.Write(raw); err != nil {
		log.Printf("pg-router: forward startup: %v", err)
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, clientConn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(clientConn, server); done <- struct{}{} }()
	<-done
}

// writePgError sends a PostgreSQL v3 ErrorResponse ('E') so the client sees a clear
// failure (e.g. shard unavailable) instead of a silently dropped connection. Valid
// as a server's response to a StartupMessage.
func writePgError(conn net.Conn, code, msg string) {
	var f []byte
	add := func(t byte, s string) {
		f = append(f, t)
		f = append(f, s...)
		f = append(f, 0)
	}
	add('S', "ERROR")
	add('V', "ERROR")
	add('C', code) // SQLSTATE (08006 = connection_failure)
	add('M', msg)
	f = append(f, 0) // field terminator
	out := make([]byte, 5+len(f))
	out[0] = 'E'
	binary.BigEndian.PutUint32(out[1:5], uint32(4+len(f)))
	copy(out[5:], f)
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write(out)
}

// readStartup reads a PostgreSQL v3 startup message and returns its raw bytes
// (for forwarding) plus the parsed parameters.
func readStartup(conn net.Conn) ([]byte, map[string]string, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return nil, nil, err
	}
	length := binary.BigEndian.Uint32(hdr)
	if length < 8 || length > 1<<20 {
		return nil, nil, fmt.Errorf("invalid startup length %d", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, nil, err
	}
	// SSLRequest (80877103) / GSSENCRequest (80877104) precede the real
	// StartupMessage. Real psql clients send SSLRequest first; the PoC speaks
	// plaintext only, so decline with 'N' and read the StartupMessage that
	// follows. Without this the request was mis-parsed as a (param-less) startup
	// and every connection routed to shard-0 (live-found, pg-e2e 2026-06-04).
	if code := binary.BigEndian.Uint32(body[0:4]); code == 80877103 || code == 80877104 {
		if _, err := conn.Write([]byte{'N'}); err != nil {
			return nil, nil, err
		}
		return readStartup(conn)
	}
	raw := append(hdr, body...)
	// body[0:4] = protocol version; params follow as key\0value\0...\0\0
	params := map[string]string{}
	for parts := strings.Split(string(body[4:]), "\x00"); len(parts) >= 2; parts = parts[2:] {
		if parts[0] == "" {
			break
		}
		params[parts[0]] = parts[1]
	}
	return raw, params, nil
}

// shardSpec is the PoC static routing table (a 2-shard hash vindex). With
// PGROUTER_TOPOLOGY=crd this is replaced by the live ShardRange CRD.
// PGROUTER_REFERENCE_TABLES (CSV) declares replicated reference tables so that
// reference-only queries route to any shard (no key) instead of scatter.
func shardSpec() v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Cluster:         env("PGROUTER_CLUSTER", "quickstart"),
		Keyspace:        env("PGROUTER_KEYSPACE", "default"),
		Vindex:          v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
		ReferenceTables: csv(env("PGROUTER_REFERENCE_TABLES", "")),
		WriteBlocked:    env("PGROUTER_WRITE_BLOCKED", "") != "", // cutover write-block (static 모드 테스트/수동 knob).
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

// csv splits a comma-separated env value, trimming spaces and dropping empties.
func csv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// templateResolver maps a shard to a backend via a DNS template
// (PGROUTER_BACKEND_TEMPLATE) with {cluster}/{shard}/{namespace} substitution —
// no per-shard env needed.
func templateResolver() router.BackendResolver {
	tmpl := env("PGROUTER_BACKEND_TEMPLATE",
		"{cluster}-{shard}-0.{cluster}-{shard}-headless.{namespace}.svc.cluster.local:5432")
	base := strings.NewReplacer(
		"{cluster}", env("PGROUTER_CLUSTER", "quickstart"),
		"{namespace}", env("PGROUTER_NAMESPACE", "default"),
	).Replace(tmpl)
	return func(shardID string) (string, error) {
		return strings.NewReplacer("{shard}", shardID).Replace(base), nil
	}
}

// primaryServiceResolver maps each shard to its operator-published per-shard *primary*
// Service DNS (`<cluster>-<shard>-primary.<ns>.svc.cluster.local:5432`). The operator
// keeps that Service pointing at the shard's current primary (ExternalName failover-
// follow), so the router follows failover via DNS without polling PostgresCluster status.
func primaryServiceResolver(cluster, ns string) router.BackendResolver {
	return func(shardID string) (string, error) {
		return fmt.Sprintf("%s-%s-primary.%s.svc.cluster.local:5432", cluster, shardID, ns), nil
	}
}

// envBackendResolver maps a shard ID to its primary backend via
// PGROUTER_BACKEND_<SHARD>.
func envBackendResolver(shardID string) (string, error) {
	return backendFor(shardID), nil
}

// envReadBackendResolver maps a shard ID to its *replica* backend via
// PGROUTER_BACKEND_<SHARD>_REPLICA, falling back to the primary when no replica is
// configured (so read-only queries still work without a replica deployed).
func envReadBackendResolver(shardID string) (string, error) {
	envKey := "PGROUTER_BACKEND_" + strings.ToUpper(strings.ReplaceAll(shardID, "-", "_")) + "_REPLICA"
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	return backendFor(shardID), nil
}

func backendFor(shardID string) string {
	envKey := "PGROUTER_BACKEND_" + strings.ToUpper(strings.ReplaceAll(shardID, "-", "_"))
	return env(envKey, "127.0.0.1:5432")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envDuration parses a duration env var, falling back to def on absence/parse error.
func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("pg-router: invalid %s=%q, using %s", k, v, def)
		return def
	}
	return d
}

// envInt parses an int env var, falling back to def on absence/parse error.
func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("pg-router: invalid %s=%q, using %d", k, v, def)
		return def
	}
	return n
}
