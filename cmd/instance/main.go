/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package main은 keiailab/postgres-operator의 instance manager 바이너리다.
//
// 본 바이너리는 PG Pod의 PID 1로 동작하여 postgres 자식 프로세스를 supervise
// 한다(ADR 0002). 외부 DCS(etcd/Consul) 없이 K8s API의 lease 객체를 합의
// 메커니즘으로 사용하며, 모든 PG 인스턴스(coordinator/worker)에서 동일 코드가
// 동작한다.
//
// 본 파일의 현 책임 (P2-T2까지):
//   - 신호 처리(SIGTERM/SIGINT graceful shutdown)
//   - HTTP healthz/readyz 엔드포인트 (readyz는 election Status 반영)
//   - K8s lease 기반 leader election (RFC 0003, internal/instance/election/)
//   - PVC fence label 라이프사이클 (RFC 0003 부록 A, internal/instance/fencing/)
//   - 구조화 로깅
//
// 후속 task에서 추가될 책임:
//   - postgres 자식 프로세스 fork + 감독 + restart 정책 (P2-T3 + P1-T3 보강)
//   - pg_rewind 자동화 (P2-T4)
//   - failover 시 분산 SQL metadata 갱신 (RFC 0002 ShardRange 도입 후)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/keiailab/postgres-operator/internal/instance/election"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
)

func main() {
	var (
		probeAddr        string
		electionDisabled bool
		fencingDisabled  bool
		leaseDuration    time.Duration
		renewDeadline    time.Duration
		retryPeriod      time.Duration
	)
	flag.StringVar(&probeAddr, "probe-bind-address", ":8080",
		"The address the healthz/readyz endpoints bind to.")
	flag.BoolVar(&electionDisabled, "election-disabled", false,
		"Disable K8s lease leader election (use Null election — always Leader). For dev mode only.")
	flag.BoolVar(&fencingDisabled, "fencing-disabled", false,
		"Disable PVC fence label lifecycle (RFC 0003 부록 A). For dev mode only — disables split-brain protection.")
	flag.DurationVar(&leaseDuration, "lease-duration", election.DefaultLeaseDuration,
		"Lease duration for leader election (RFC 0003 §2).")
	flag.DurationVar(&renewDeadline, "renew-deadline", election.DefaultRenewDeadline,
		"Renew deadline for leader election. Must be < lease-duration.")
	flag.DurationVar(&retryPeriod, "retry-period", election.DefaultRetryPeriod,
		"Retry period for leader election. Must be < renew-deadline.")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// downward API 환경 변수 (RFC 0003 §3).
	podName := envOrDie("POD_NAME")
	namespace := envOrDie("POD_NAMESPACE")
	cluster := envOrDie("POSTGRES_CLUSTER")
	role := envOrDie("POSTGRES_ROLE")
	pool := os.Getenv("POSTGRES_POOL") // worker만 의미 있음

	logger.Info("Instance manager starting",
		"version", "v0.0.0-pillar-p2-t1",
		"probeAddr", probeAddr,
		"podName", podName,
		"namespace", namespace,
		"cluster", cluster,
		"role", role,
		"pool", pool,
		"electionDisabled", electionDisabled,
	)

	leaseName := election.PrimaryLeaseName(cluster, role, pool)
	logger.Info("Resolved lease name", "lease", leaseName)

	// Fencing — Null(disabled) 또는 Real. fencer는 election callback에서
	// 호출되며 fence 위반 시 fencingErrCh로 신호를 보내 main이 exit non-zero
	// 응답한다(RFC 0003 부록 A §3 fail-fast).
	fencingErrCh := make(chan error, 1)
	var fencer fencing.Fencer
	var clientset kubernetes.Interface

	if !electionDisabled || !fencingDisabled {
		var err error
		clientset, err = buildKubernetesClient()
		if err != nil {
			logger.Error("Failed to build K8s client", "error", err)
			os.Exit(1)
		}
	}

	if fencingDisabled {
		logger.Warn("PVC fencing disabled — split-brain protection OFF. Use only in development.")
		fencer = fencing.NewMock() // no-op in production sense, never returns ErrFenced
	} else {
		realFencer, err := fencing.NewReal(fencing.RealConfig{
			Client:    clientset,
			Namespace: namespace,
			PVCName:   fencing.PVCName(podName),
		})
		if err != nil {
			logger.Error("Failed to construct fencer", "error", err)
			os.Exit(1)
		}
		fencer = realFencer
	}

	// Election 인스턴스 결정 (Real | Null).
	var elect election.Election
	cb := election.Callbacks{
		OnStartedLeading: func(ctx context.Context) {
			// Promote 직전 PVC fence 검사 — split-brain 보호의 단일 동기 게이트.
			if err := fencer.VerifyNotFenced(ctx); err != nil {
				logger.Error("PVC is fenced — refusing to promote",
					"identity", podName, "lease", leaseName, "error", err)
				select {
				case fencingErrCh <- err:
				default:
				}
				return
			}
			logger.Info("Leadership acquired — would promote postgres to primary",
				"identity", podName, "lease", leaseName,
				"todo", "supervise postgres process + 분산 SQL metadata 갱신 (RFC 0002 후속)")
		},
		OnStoppedLeading: func() {
			// 자기 PVC를 fence 처리하여 좀비 부활 시 split-brain 방지.
			// background ctx 사용 — election ctx는 이미 종료 중일 수 있음.
			markCtx, markCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer markCancel()
			if err := fencer.MarkFenced(markCtx); err != nil {
				logger.Error("Failed to fence own PVC after losing leadership",
					"identity", podName, "error", err)
			} else {
				logger.Warn("Leadership lost — fenced own PVC, would demote postgres to standby",
					"identity", podName, "lease", leaseName)
			}
		},
		OnNewLeader: func(id string) {
			logger.Info("Observed new leader", "identity", id, "self", podName)
		},
	}

	if electionDisabled {
		elect = election.NewNull(podName, cb)
		logger.Warn("Election disabled — Null election (always Leader). Use only in development.")
	} else {
		real, err := election.NewReal(election.RealConfig{
			Client:    clientset,
			LeaseName: leaseName,
			Namespace: namespace,
			Identity:  podName,
			Callbacks: cb,
			Durations: election.Durations{
				LeaseDuration: leaseDuration,
				RenewDeadline: renewDeadline,
				RetryPeriod:   retryPeriod,
			},
		})
		if err != nil {
			logger.Error("Failed to construct election", "error", err)
			os.Exit(1)
		}
		elect = real
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		// 현재는 프로세스 생존만 보고. P2-T3 보강 시 postgres 자식 PID alive
		// 검사 추가.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		// election 부트스트랩 중에는 503, leader/follower 결정 후에는 200.
		// (P12 라우터 사이드카는 별도로 router_metadata_lag_seconds 임계 검사)
		switch elect.Status() {
		case election.StatusStarting:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "starting election\n")
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s\n", elect.Status())
		}
	})

	srv := &http.Server{
		Addr:              probeAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// HTTP 서버 goroutine
	srvErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErrCh <- err
		}
		close(srvErrCh)
	}()
	logger.Info("Probe endpoints listening", "addr", probeAddr)

	// Election goroutine
	electErrCh := make(chan error, 1)
	go func() {
		if err := elect.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			electErrCh <- err
		}
		close(electErrCh)
	}()
	logger.Info("Election started", "identity", elect.Identity(), "lease", leaseName)

	select {
	case <-ctx.Done():
		logger.Info("Received shutdown signal")
	case err := <-srvErrCh:
		logger.Error("Probe server failed", "error", err)
		os.Exit(1)
	case err := <-electErrCh:
		logger.Error("Election failed", "error", err)
		os.Exit(1)
	case err := <-fencingErrCh:
		// PVC가 fenced 상태인데 promote 시도가 발생 — 운영자가 수동으로
		// fence 해제(또는 PVC 교체)할 때까지 leadership 점유를 거절한다.
		logger.Error("Fencing violation — exiting to defer to operator intervention", "error", err)
		os.Exit(2)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Probe server graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("Instance manager exited cleanly")
}

// envOrDie는 필수 환경변수를 읽고 미설정 시 즉시 실패한다.
// downward API로 주입되어야 할 변수들이 누락된 상태로 부팅하면 election lease
// 명명이 깨지므로 fail-fast가 옳다.
func envOrDie(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "instance: required env %s is empty (set via downward API)\n", key)
		os.Exit(1)
	}
	return v
}

// buildKubernetesClient는 Pod 안에서 InClusterConfig를 사용해 clientset을 만든다.
// Pod 외부 실행 환경(예: 로컬 디버그)은 후속 task에서 KUBECONFIG 폴백 추가.
func buildKubernetesClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("InClusterConfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("NewForConfig: %w", err)
	}
	return cs, nil
}
