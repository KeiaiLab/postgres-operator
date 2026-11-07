/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestMetricsHandler_ReportsActiveConnections(t *testing.T) {
	// 격리: 테스트 시작 시 0 으로 정규화(다른 테스트와 공유되는 package var).
	activeConns.Store(0)

	// 3 개의 연결이 진행 중인 상태를 흉내낸다.
	activeConns.Store(3)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	metricsHandler().ServeHTTP(w, req)

	body := w.Body.String()
	name := v1alpha1.RouterActiveConnectionsMetric
	if !strings.Contains(body, "# TYPE "+name+" gauge") {
		t.Fatalf("missing TYPE line for %q:\n%s", name, body)
	}
	if !strings.Contains(body, name+" 3\n") {
		t.Fatalf("expected %q 3 in body:\n%s", name, body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain...", ct)
	}
}

func TestTrackConn_IncrementsAndRestores(t *testing.T) {
	activeConns.Store(0)

	done := make(chan struct{})
	observed := int64(-1)
	go trackConn(func() {
		observed = activeConns.Load()
		close(done)
	})
	<-done

	if observed != 1 {
		t.Fatalf("active during handler = %d, want 1", observed)
	}
	// handler 종료 후 defer 감소로 게이지가 0 으로 복원됨을 시간 제한 폴링으로 확인.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if activeConns.Load() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active after handler = %d, want 0 (defer decrement)", activeConns.Load())
}

func TestServeMetrics_EmptyAddrIsNoop(_ *testing.T) {
	// 빈 주소는 즉시 반환(블로킹 없이) — 서버 미기동.
	serveMetrics("")
}

func TestReadyzHandler_ReflectsRoutingReadiness(t *testing.T) {
	// 라우팅 테이블 미확보 → 503.
	setRouterReady(false)
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	readyzHandler().ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("not-ready code = %d, want 503", w.Code)
	}

	// 라우팅 테이블 확보 → 200.
	setRouterReady(true)
	w = httptest.NewRecorder()
	readyzHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ready code = %d, want 200", w.Code)
	}
	// 격리: 다른 테스트에 영향 주지 않도록 리셋.
	setRouterReady(false)
}
