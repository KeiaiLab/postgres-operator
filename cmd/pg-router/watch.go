/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// watch.go — ShardRange / PostgresCluster watch 기반 즉시 hot-reload. 변경 이벤트를
// changeCh 로 흘려 refreshLoop 이 interval 을 기다리지 않고 즉시 토폴로지/primary status
// 를 다시 읽게 한다(failover / resharding 반영 지연 단축). watch 가 드롭되면 backoff 후
// 재접속하고, ctx 취소 시 종료한다. interval refresh 는 fallback 으로 유지된다.
package main

import (
	"context"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// watchReloader 는 한 리소스의 watch 를 유지하며 변경 이벤트를 notify 로 전달한다.
// watch 세션이 닫히면(서버 timeout / 드롭) backoff 후 재접속한다. connect 는 fresh watch
// 를 여는 함수로 추상화되어 fake watcher 로 결정론 테스트 가능하다.
type watchReloader struct {
	connect func(ctx context.Context) (watch.Interface, error)
	backoff time.Duration
	name    string
}

func (w watchReloader) run(ctx context.Context, notify chan<- struct{}) {
	backoff := w.backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		wi, err := w.connect(ctx)
		if err != nil {
			log.Printf("pg-router: watch %s connect: %v (retry in %s)", w.name, err, backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		reconnect := w.drain(ctx, wi, notify)
		wi.Stop()
		if !reconnect {
			return // ctx 취소.
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// drain 은 단일 watch 세션의 이벤트를 notify 로 전달한다. watch 채널이 닫히거나 Error
// 이벤트가 오면 true(재접속)를 반환하고, ctx 취소면 false 를 반환한다.
func (w watchReloader) drain(ctx context.Context, wi watch.Interface, notify chan<- struct{}) bool {
	ch := wi.ResultChan()
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-ch:
			if !ok || ev.Type == watch.Error {
				return true // 세션 종료 / 에러 → 재접속.
			}
			// non-blocking notify — changeCh(cap 1)가 차 있으면 이미 pending refresh 가
			// 이 변경을 커버하므로 drop(coalesce).
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}
}

// sleepCtx 는 d 만큼 대기하되 ctx 취소 시 즉시 false 로 반환한다.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// watchShardRangesAndCluster 는 ShardRange + PostgresCluster 를 watch 해 변경 시 notify 로
// 신호한다(토폴로지 flip / primary status 변경 → 즉시 hot-reload). 각 리소스별 goroutine.
func watchShardRangesAndCluster(
	ctx context.Context,
	wc client.WithWatch,
	ns string,
	notify chan<- struct{},
	backoff time.Duration,
) {
	srConnect := func(ctx context.Context) (watch.Interface, error) {
		var list v1alpha1.ShardRangeList
		return wc.Watch(ctx, &list, client.InNamespace(ns))
	}
	pcConnect := func(ctx context.Context) (watch.Interface, error) {
		var list v1alpha1.PostgresClusterList
		return wc.Watch(ctx, &list, client.InNamespace(ns))
	}
	go watchReloader{connect: srConnect, backoff: backoff, name: "shardrange"}.run(ctx, notify)
	go watchReloader{connect: pcConnect, backoff: backoff, name: "postgrescluster"}.run(ctx, notify)
}
