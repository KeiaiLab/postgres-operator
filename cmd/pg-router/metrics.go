/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// metrics.go — pg-router 의 active client-connection 게이지를 Prometheus 텍스트
// exposition 형식으로 노출한다(별 client 의존성 없이 직접 렌더 — 저장소 zero-dep
// 철학). 이 게이지를 custom-metrics adapter(예: prometheus-adapter)가
// custom.metrics.k8s.io 로 매핑하면 router HPA 의 ScaleOnActiveConnections Pods
// 메트릭이 이를 소비한다(RFC 0001 §3.1).
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// activeConns 는 현재 진행 중인 client 연결 수다(trackConn 이 inc/dec).
var activeConns atomic.Int64

// routerReady 는 라우터가 *사용 가능한 라우팅 테이블*(토폴로지)을 확보했는지 나타낸다.
// /readyz 가 이를 반영한다 — 토폴로지 로드 전에는 not-ready 로 응답해 k8s 가 아직
// 라우팅 불가한 Pod 로 트래픽을 보내지 않게 한다(라우팅 오류 대신 미준비 신호).
var routerReady atomic.Bool

// setRouterReady 는 초기 토폴로지 로드 성공 시(그리고 refresh 성공 시) true 로 세팅한다.
// 일단 라우팅 테이블을 확보하면 캐시가 서빙하므로 일시적 refresh 실패로 내리지 않는다.
func setRouterReady(v bool) { routerReady.Store(v) }

// trackConn 은 handler 실행 동안 active-connection 게이지를 1 증가시켰다가 복원한다.
// handler 의 panic 여부와 무관하게 defer 로 감소를 보장한다.
func trackConn(handler func()) {
	activeConns.Add(1)
	defer activeConns.Add(-1)
	handler()
}

// writeMetrics 는 Prometheus 텍스트 exposition 을 w 에 쓴다. 게이지 이름은 HPA 가
// 참조하는 v1alpha1.RouterActiveConnectionsMetric 과 동일해 둘이 어긋나지 않는다.
func writeMetrics(w io.Writer) error {
	name := v1alpha1.RouterActiveConnectionsMetric
	_, err := fmt.Fprintf(w,
		"# HELP %s Current number of active client connections proxied by pg-router.\n"+
			"# TYPE %s gauge\n"+
			"%s %d\n",
		name, name, name, activeConns.Load())
	return err
}

// metricsHandler 는 /metrics 응답 핸들러다(테스트에서 httptest 로 직접 호출 가능).
func metricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := writeMetrics(w); err != nil {
			log.Printf("pg-router: write metrics: %v", err)
		}
	}
}

// readyzHandler 는 라우팅 테이블 확보 여부(routerReady)를 반영한다 — 준비 전 503.
func readyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if routerReady.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("no routing table yet"))
	}
}

// serveMetrics 는 addr 에서 /metrics + /healthz(liveness) + /readyz(readiness) HTTP
// 서버를 띄운다(블로킹 — goroutine 으로 호출). addr 이 빈 문자열이면 no-op(비활성).
func serveMetrics(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metricsHandler())
	mux.Handle("/readyz", readyzHandler())
	// /healthz = liveness(프로세스 살아있음, 항상 200). readiness 는 /readyz.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("pg-router metrics listening on %s (/metrics, /healthz)", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("pg-router: metrics server: %v", err)
	}
}
