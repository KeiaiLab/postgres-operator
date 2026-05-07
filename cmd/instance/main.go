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
// 메커니즘으로 사용하며, 모든 shard PG 인스턴스에서 동일 코드가 동작한다
// (RFC 0001 v2 — coordinator/worker 모델 폐기, shard ordinal 기반).
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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/keiailab/postgres-operator/internal/instance/election"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
	"github.com/keiailab/postgres-operator/internal/instance/supervise"
)

func main() {
	var (
		probeAddr         string
		electionDisabled  bool
		fencingDisabled   bool
		superviseDisabled bool
		leaseDuration     time.Duration
		renewDeadline     time.Duration
		retryPeriod       time.Duration
	)
	flag.StringVar(&probeAddr, "probe-bind-address", ":8080",
		"The address the healthz/readyz endpoints bind to.")
	flag.BoolVar(&electionDisabled, "election-disabled", false,
		"Disable K8s lease leader election (use Null election — always Leader). For dev mode only.")
	flag.BoolVar(&fencingDisabled, "fencing-disabled", false,
		"Disable PVC fence label lifecycle (RFC 0003 부록 A). For dev mode only — disables split-brain protection.")
	flag.BoolVar(&superviseDisabled, "supervise-disabled", false,
		"Disable postgres child supervision (skip fork + Promote/Stop wiring). For dev mode and unit tests only.")
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

	// downward API 환경 변수 (RFC 0001 v2 — shard ordinal 기반).
	// role 은 현재 "shard" 만 지원. router 는 별도 binary 분기 (F02b 도입).
	podName := envOrDie("POD_NAME")
	podUID := envOrDie("POD_UID")
	namespace := envOrDie("POD_NAMESPACE")
	cluster := envOrDie("POSTGRES_CLUSTER")
	role := envOrDie("POSTGRES_ROLE")
	if role != "shard" {
		fmt.Fprintf(os.Stderr, "instance: unsupported POSTGRES_ROLE=%q (only \"shard\" supported in this binary)\n", role)
		os.Exit(1)
	}
	shardOrdinalRaw := envOrDie("POSTGRES_SHARD_ORDINAL")
	shardOrdinal, err := strconv.ParseInt(shardOrdinalRaw, 10, 32)
	if err != nil || shardOrdinal < 0 {
		fmt.Fprintf(os.Stderr, "instance: POSTGRES_SHARD_ORDINAL must be a non-negative int32, got %q (err=%v)\n",
			shardOrdinalRaw, err)
		os.Exit(1)
	}
	memberCount := parsePositiveIntEnv("POSTGRES_MEMBER_COUNT")
	podOrdinal := parsePodOrdinalOrDie(podName)
	electionIdentity := buildElectionIdentity(podName, podUID)

	logger.Info("Instance manager starting",
		"version", "v0.0.0-pillar-p2-t1",
		"probeAddr", probeAddr,
		"podName", podName,
		"podUID", podUID,
		"identity", electionIdentity,
		"namespace", namespace,
		"cluster", cluster,
		"role", role,
		"shardOrdinal", shardOrdinal,
		"podOrdinal", podOrdinal,
		"memberCount", memberCount,
		"electionDisabled", electionDisabled,
	)

	leaseName := election.PrimaryLeaseName(cluster, role, int32(shardOrdinal))
	logger.Info("Resolved lease name", "lease", leaseName)

	// dataDir — election callback 의 standby.signal lifecycle (RFC 0006 R3) 와
	// supervise.NewReal 양쪽에서 사용. 한 번만 읽고 클로저로 캡쳐한다.
	dataDir := envOrDie("POSTGRES_DATA_DIR")
	restartedPrimaryAsStandby := prepareRestartedPrimaryAsStandby(
		dataDir, cluster, namespace, int32(shardOrdinal), podOrdinal, memberCount, logger,
	)

	// Supervisor — postgres 자식 fork + Promote/Stop SQL 추상.
	// supervise-disabled 모드에서는 nil 로 두고 callback 안에서 분기.
	sup := buildSupervisor(superviseDisabled, dataDir, logger)

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

	fencer = buildFencer(fencingDisabled, clientset, namespace, podName, logger)

	// Election 인스턴스 결정 (Real | Null).
	var elect election.Election
	cb := election.Callbacks{
		OnStartedLeading: func(ctx context.Context) {
			runOnStartedLeading(ctx, fencer, sup, dataDir, podName, leaseName, fencingErrCh, logger)
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
				logger.Warn("Leadership lost — fenced own PVC, demoting postgres",
					"identity", podName, "lease", leaseName)
			}
			// Demote — PostgreSQL 은 native pg_demote() 가 없으므로 fast Stop
			// (SIGINT) 으로 primary 를 종료. 본 instance 는 ExitCh 가 fire 하면
			// 통째 exit → K8s 가 Pod 재시작 → 다음 부팅 시 standby 로 진입
			// (standby.signal 재구성 로직은 F03 후속).
			// RFC 0006 R3: 다음 부팅 시 standby 로 진입하도록 signal 파일을
			// 미리 생성. best-effort — 실패해도 demote 자체는 진행 (Pod 재시작 +
			// bootstrap init container 가 보조 mechanism).
			if err := supervise.CreateStandbySignal(dataDir); err != nil {
				logger.Error("CreateStandbySignal failed (best-effort)", "identity", podName, "error", err)
			}
			if sup != nil {
				demoteCtx, demoteCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer demoteCancel()
				if err := sup.Stop(demoteCtx, true); err != nil {
					logger.Error("Demote (fast stop) failed", "identity", podName, "error", err)
				}
			}
		},
		OnNewLeader: func(id string) {
			logger.Info("Observed new leader", "identity", id, "self", podName)
		},
	}

	if electionDisabled {
		elect = election.NewNull(electionIdentity, cb)
		logger.Warn("Election disabled — Null election (always Leader). Use only in development.")
	} else {
		real, err := election.NewReal(election.RealConfig{
			Client:    clientset,
			LeaseName: leaseName,
			Namespace: namespace,
			Identity:  electionIdentity,
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
	mux.HandleFunc("/readyz", makeReadyzHandler(elect, sup))

	srv := &http.Server{
		Addr:              probeAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Supervisor 시작 — postgres child 종료를 감지하면 main 도 종료.
	// election 보다 먼저 Start 해야 OnStartedLeading 안에서 Promote 호출 가능.
	supExitCh := startSupervisor(ctx, sup, logger)
	if restartedPrimaryAsStandby {
		delayElectionForRestartedPrimary(ctx, leaseDuration+retryPeriod, podName, logger)
	}

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

	startStatusReporterIfPossible(ctx, clientset, namespace, podName, cluster, int32(shardOrdinal), elect, sup, logger)

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
	case err := <-supExitCh:
		// postgres child 가 죽으면 instance 도 함께 종료 — K8s 가 Pod 재시작.
		if err != nil {
			logger.Error("Exiting because postgres child exited", "error", err)
			os.Exit(1)
		}
		logger.Info("Exiting because postgres child exited cleanly")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Probe server graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	gracefulStopSupervisor(sup, logger)
	logger.Info("Instance manager exited cleanly")
}

// runOnStartedLeading 은 election leader acquisition 시 실행되는 promote 시퀀스다.
// 단일 함수로 추출하여 main 의 cyclomatic complexity 를 30 이하로 유지한다.
//
// 시퀀스:
//  1. PVC fence 검사 — fenced 면 promote 거절 + fencingErrCh 로 신호.
//  2. postgres readiness 대기 (30s).
//  3. standby.signal 제거 (RFC 0006 R3) — 실패 시 promote 차단.
//  4. pg_promote() 호출.
func runOnStartedLeading(
	ctx context.Context,
	fencer fencing.Fencer,
	sup supervise.Supervisor,
	dataDir, podName, leaseName string,
	fencingErrCh chan<- error,
	logger *slog.Logger,
) {
	if err := fencer.VerifyNotFenced(ctx); err != nil {
		logger.Error("PVC is fenced — refusing to promote",
			"identity", podName, "lease", leaseName, "error", err)
		select {
		case fencingErrCh <- err:
		default:
		}
		return
	}
	if sup != nil {
		// postgres unix socket 가 listen 시작할 때까지 대기 (race 회피).
		// 30s 타임아웃 — 정상 부팅 시간보다 충분.
		if err := waitSupReady(ctx, sup, 30*time.Second); err != nil {
			logger.Error("postgres readiness wait failed", "identity", podName, "error", err)
			select {
			case fencingErrCh <- fmt.Errorf("readiness: %w", err):
			default:
			}
			return
		}
		// RFC 0006 R3: pg_promote() 호출 전 standby.signal 정리. 실패 시
		// promote 를 차단해야 — 파일이 남아 있는 상태로 promote 가 성공해도
		// 다음 재시작 때 다시 standby 로 돌아가 split-role 이 발생.
		if err := supervise.RemoveStandbySignal(dataDir); err != nil {
			logger.Error("RemoveStandbySignal failed", "identity", podName, "error", err)
			select {
			case fencingErrCh <- fmt.Errorf("standby-signal cleanup: %w", err):
			default:
			}
			return
		}
		if err := sup.Promote(ctx); err != nil {
			logger.Error("Promote failed", "identity", podName, "error", err)
			select {
			case fencingErrCh <- fmt.Errorf("promote: %w", err):
			default:
			}
			return
		}
	}
	logger.Info("Leadership acquired — postgres promoted to primary",
		"identity", podName, "lease", leaseName,
		"todo", "분산 SQL metadata 갱신 (RFC 0002 ShardRange 후속)")
}

func buildElectionIdentity(podName, podUID string) string {
	return podName + "/" + podUID
}

func parsePositiveIntEnv(key string) int {
	raw := envOrDie(key)
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		fmt.Fprintf(os.Stderr, "instance: %s must be a positive int, got %q (err=%v)\n", key, raw, err)
		os.Exit(1)
	}
	return v
}

func parsePodOrdinalOrDie(podName string) int {
	for i := len(podName) - 1; i >= 0; i-- {
		if podName[i] != '-' {
			continue
		}
		ord, err := strconv.Atoi(podName[i+1:])
		if err != nil || ord < 0 {
			fmt.Fprintf(os.Stderr, "instance: POD_NAME must end with StatefulSet ordinal, got %q (err=%v)\n", podName, err)
			os.Exit(1)
		}
		return ord
	}
	fmt.Fprintf(os.Stderr, "instance: POD_NAME must contain StatefulSet ordinal suffix, got %q\n", podName)
	os.Exit(1)
	return 0
}

func prepareRestartedPrimaryAsStandby(
	dataDir, cluster, namespace string,
	shardOrdinal int32,
	podOrdinal, memberCount int,
	logger *slog.Logger,
) bool {
	if podOrdinal != 0 || memberCount <= 1 {
		return false
	}
	endpoint := standbyCandidateEndpoint(cluster, shardOrdinal, namespace)
	prepared, err := supervise.PrepareRestartedPrimaryAsStandby(dataDir, endpoint)
	if err != nil {
		logger.Error("Failed to prepare restarted ordinal-0 primary as standby", "error", err)
		os.Exit(1)
	}
	if prepared {
		logger.Warn("Restarted ordinal-0 primary prepared as standby",
			"endpoint", endpoint,
			"reason", "HA failover prefers existing standby promotion before same ordinal reuse")
	}
	return prepared
}

func standbyCandidateEndpoint(cluster string, shardOrdinal int32, namespace string) string {
	return fmt.Sprintf("%s-shard-%d-1.%s-shard-%d-headless.%s.svc.cluster.local:5432",
		cluster, shardOrdinal, cluster, shardOrdinal, namespace)
}

func delayElectionForRestartedPrimary(ctx context.Context, delay time.Duration, podName string, logger *slog.Logger) {
	if delay <= 0 {
		return
	}
	logger.Warn("Delaying election for restarted ordinal-0 primary",
		"podName", podName,
		"delay", delay.String())
	select {
	case <-ctx.Done():
	case <-time.After(delay):
	}
}

// startStatusReporterIfPossible 는 clientset 이 사용 가능하면 status reporter goroutine 을
// 띄운다. clientset 부재 (election+fencing 모두 disabled 인 dev 시나리오) 시 silent skip.
func startStatusReporterIfPossible(
	ctx context.Context,
	clientset kubernetes.Interface,
	namespace, podName, cluster string,
	shardOrdinal int32,
	elect election.Election,
	sup supervise.Supervisor,
	logger *slog.Logger,
) {
	if clientset == nil {
		return
	}
	endpoint := fmt.Sprintf("%s.%s-shard-%d-headless.%s.svc.cluster.local:5432",
		podName, cluster, shardOrdinal, namespace)
	go runStatusReporter(ctx, clientset, namespace, podName, endpoint, elect, sup, logger)
}

// runStatusReporter 는 5s 주기로 Pod annotation 에 Status 를 patch 한다 (RFC 0006 R2).
//
// ctx 종료 시 마지막 한 번 더 RoleStopping 으로 patch 후 return — controller 가 즉시
// "shutdown 진행 중" 상태로 인지 가능 (failover 가시성).
//
// patch 실패는 error 로 expose 안 함 — 일시적 API 실패가 instance 본체를 죽이면
// 안 됨 (status reporting 은 best-effort).
func runStatusReporter(
	ctx context.Context,
	clientset kubernetes.Interface,
	namespace, podName, endpoint string,
	elect election.Election,
	sup supervise.Supervisor,
	logger *slog.Logger,
) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	patchOnce := func(role statusapi.Role) {
		ready := false
		lag := int64(-1)
		if sup != nil {
			probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
			ready = sup.IsReady(probeCtx)
			cancel()
			lagCtx, lagCancel := context.WithTimeout(ctx, 1*time.Second)
			lag = sup.LagBytes(lagCtx)
			lagCancel()
		} else {
			ready = role == statusapi.RolePrimary || role == statusapi.RoleReplica
		}
		st := statusapi.Status{
			Role:       role,
			Ready:      ready,
			Endpoint:   endpoint,
			LagBytes:   lag,
			LastUpdate: time.Now().UTC(),
		}
		if err := patchPodAnnotation(ctx, clientset, namespace, podName, st); err != nil {
			logger.Warn("status reporter patch failed (best-effort)", "error", err)
		}
	}

	// 즉시 첫 patch — Pod 부팅 직후 controller 가 Starting 인지하도록.
	patchOnce(roleFromElection(elect.Status()))

	for {
		select {
		case <-ctx.Done():
			// 종료 마커 — controller 가 stale annotation 검사 시 보조.
			finalCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			st := statusapi.Status{Role: statusapi.RoleStopping, LastUpdate: time.Now().UTC()}
			_ = patchPodAnnotation(finalCtx, clientset, namespace, podName, st)
			return
		case <-tick.C:
			patchOnce(roleFromElection(elect.Status()))
		}
	}
}

// roleFromElection 은 election Status 를 statusapi.Role 로 매핑한다.
func roleFromElection(s election.Status) statusapi.Role {
	switch s {
	case election.StatusLeader:
		return statusapi.RolePrimary
	case election.StatusFollower:
		return statusapi.RoleReplica
	case election.StatusStarting:
		return statusapi.RoleStarting
	default:
		return statusapi.RoleUnknown
	}
}

// patchPodAnnotation 은 자기 Pod 의 annotation 에 status JSON 을 strategic merge
// patch 로 갱신한다. RBAC 은 instance Role 의 pods/get;patch 에 의존.
func patchPodAnnotation(
	ctx context.Context,
	clientset kubernetes.Interface,
	namespace, podName string,
	st statusapi.Status,
) error {
	body, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// strategic merge patch 의 metadata.annotations 합성 — 기존 다른 annotation 보존.
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`,
		statusapi.AnnotationKey, string(body))
	_, err = clientset.CoreV1().Pods(namespace).Patch(
		ctx, podName, types.StrategicMergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

// waitSupReady 는 sup.IsReady 가 true 일 때까지 polling 한다 (500ms interval).
// timeout 만료 시 error. supervise-disabled 모드 (sup==nil) 에서는 즉시 nil.
func waitSupReady(ctx context.Context, sup supervise.Supervisor, timeout time.Duration) error {
	if sup == nil {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		ready := sup.IsReady(probeCtx)
		cancel()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("postgres not ready after %s", timeout)
}

// makeReadyzHandler 는 /readyz HTTP handler 를 생성한다.
//
// 두 단계 검사:
//  1. election 부트스트랩 — Starting 이면 503 ("starting election").
//  2. postgres round-trip — sup != nil 이고 IsReady false 면 503 ("postgres not ready").
//
// 둘 다 OK 일 때 200 + election Status 본문 출력.
func makeReadyzHandler(elect election.Election, sup supervise.Supervisor) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if elect.Status() == election.StatusStarting {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "starting election\n")
			return
		}
		if sup != nil {
			pgCtx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
			defer cancel()
			if !sup.IsReady(pgCtx) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, "postgres not ready\n")
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "%s\n", elect.Status())
	}
}

// buildFencer 는 fencingDisabled 가 true 면 Mock fencer 를, false 면 Real fencer
// (PVC fence label lifecycle) 를 반환한다. Real 생성 실패 시 즉시 종료.
func buildFencer(
	fencingDisabled bool,
	clientset kubernetes.Interface,
	namespace, podName string,
	logger *slog.Logger,
) fencing.Fencer {
	if fencingDisabled {
		logger.Warn("PVC fencing disabled — split-brain protection OFF. Use only in development.")
		return fencing.NewMock()
	}
	realFencer, err := fencing.NewReal(fencing.RealConfig{
		Client:    clientset,
		Namespace: namespace,
		PVCName:   fencing.PVCName(podName),
	})
	if err != nil {
		logger.Error("Failed to construct fencer", "error", err)
		os.Exit(1)
	}
	return realFencer
}

// buildSupervisor 는 superviseDisabled 가 false 면 supervise.NewReal 로 production
// supervisor 를 생성, true 면 nil 을 반환한다 (callback 측에서 nil 분기).
// 환경 변수 부재 시 envOrDie 가 즉시 종료한다.
func buildSupervisor(superviseDisabled bool, dataDir string, logger *slog.Logger) supervise.Supervisor {
	if superviseDisabled {
		logger.Warn("Supervise disabled — postgres child not forked. Use only in development.")
		return nil
	}
	sup, err := supervise.NewReal(supervise.Config{
		BinDir:     envOrDie("POSTGRES_BIN_DIR"),
		DataDir:    dataDir,
		ConfigFile: envOrDie("POSTGRES_CONFIG_FILE"),
		HbaFile:    envOrDie("POSTGRES_HBA_FILE"),
		LocalDSN:   envOrDie("POSTGRES_LOCAL_DSN"),
	})
	if err != nil {
		logger.Error("Failed to construct supervisor", "error", err)
		os.Exit(1)
	}
	return sup
}

// startSupervisor 는 sup 이 nil 이 아니면 Start 하고 ExitCh 감시 goroutine 을
// 띄운다. 반환되는 채널은 child 가 종료되면 한 번 송출 후 close 된다.
// sup 이 nil 이면 항상 비어 있는 채널을 반환 (select 분기 무력화).
func startSupervisor(ctx context.Context, sup supervise.Supervisor, logger *slog.Logger) <-chan error {
	if sup == nil {
		return make(chan error)
	}
	if err := sup.Start(ctx); err != nil {
		logger.Error("Failed to start postgres supervisor", "error", err)
		os.Exit(1)
	}
	logger.Info("Postgres supervisor started", "pid", sup.PID())
	out := make(chan error, 1)
	go func() {
		err := <-sup.ExitCh()
		if err != nil {
			logger.Error("postgres child exited unexpectedly", "error", err)
		} else {
			logger.Info("postgres child exited cleanly")
		}
		out <- err
		close(out)
	}()
	return out
}

// gracefulStopSupervisor 는 main 정상 종료 경로에서 postgres child 를 smart
// shutdown (SIGTERM) 한다. 실패는 best-effort 로 로깅만 — K8s 가 PID1 종료 시
// SIGKILL 로 cleanup 한다.
func gracefulStopSupervisor(sup supervise.Supervisor, logger *slog.Logger) {
	if sup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sup.Stop(ctx, false); err != nil {
		logger.Error("Postgres graceful shutdown failed", "error", err)
	}
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
