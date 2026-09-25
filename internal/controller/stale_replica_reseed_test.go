/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	// blockedReadSlack 는 nodeReadTimeout 위에 얹는 시험 여유다(스케줄링 지연 흡수).
	blockedReadSlack = 2 * time.Second

	// eventBuffer 는 FakeRecorder 채널 크기다 — 넘치면 Eventf 가 막힌다.
	eventBuffer = 8
)

// stuckMemberNS 는 Terminating 멤버 시험의 네임스페이스다.
const stuckMemberNS = "default"

// stuckMember 는 finalizer 로 Terminating 에 걸린 멤버 파드다 — deletedAgo 전에 지워졌고 node 위에 있다.
func stuckMember(node string, deletedAgo time.Duration, now time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-shard-0-0", Namespace: stuckMemberNS,
			Finalizers:        []string{"keiailab.io/test-stuck"},
			DeletionTimestamp: &metav1.Time{Time: now.Add(-deletedAgo)},
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
}

// nodeWithReady 는 Ready condition 이 status 인 노드다.
func nodeWithReady(name string, status corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}},
	}
}

// deleteStuck 는 Terminating 멤버 파드에 재구축 엔진의 삭제(deleteMember)를 다시 부르고 남은 파드를 준다
// (없으면 nil).
func deleteStuck(t *testing.T, node *corev1.Node, deletedAgo time.Duration) *corev1.Pod {
	t.Helper()
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: stuckMemberNS}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, stuckMember(node.Name, deletedAgo, now), cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	var pod corev1.Pod
	key := types.NamespacedName{Namespace: stuckMemberNS, Name: "demo-shard-0-0"}
	if err := c.Get(ctx, key, &pod); err != nil {
		t.Fatalf("get stuck pod: %v", err)
	}
	if err := r.deleteMember(ctx, cluster, &pod, now); err != nil {
		t.Fatalf("deleteMember: %v", err)
	}
	var got corev1.Pod
	if err := c.Get(ctx, key, &got); err != nil {
		return nil
	}
	return &got
}

// TestForceDeleteOnDeadNode 는 #226 F2 다: stuckTerminatingForceDeleteThreshold 넘게 Terminating(finalizer)이고
// 노드가 따로 NotReady 이며 파드도 Ready 가 아니면 다시 지울 때 force delete 한다 — 수동
// `kubectl delete pod --grace-period=0 --force` 와 같다. 없으면 죽은 노드의 kubelet 이 삭제를 받지 않아
// 재구축이 영영 기다린다.
func TestForceDeleteOnDeadNode(t *testing.T) {
	t.Parallel()
	if got := deleteStuck(t, nodeWithReady("node-dead", corev1.ConditionFalse), 5*time.Minute); got != nil {
		t.Fatalf("stuck pod on a NotReady node should have been force-deleted, still present: %+v", got)
	}
}

// TestNoForceDeleteOnReadyNode 는 안전 게이트다: 노드가 Ready 면 문턱을 넘겨도 force delete 하지 않는다 — 느린
// 노드는 볼륨을 붙든 채일 수 있어 다른 노드에서 다시 뜨면 multi-attach 다.
func TestNoForceDeleteOnReadyNode(t *testing.T) {
	t.Parallel()
	got := deleteStuck(t, nodeWithReady("node-ready", corev1.ConditionTrue), 5*time.Minute)
	if got == nil {
		t.Fatal("pod should still exist (finalizer holds it, safety gate must not force-delete on a Ready node)")
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizers must not have been cleared — force delete must not have been attempted on a Ready node")
	}
}

// TestNoForceDeleteWhenFresh 는 stuckTerminatingForceDeleteThreshold 게이트다: 방금 낸 삭제는 노드가 NotReady
// 여도 force delete 로 올리지 않는다 — 정상 종료가 먼저다.
func TestNoForceDeleteWhenFresh(t *testing.T) {
	t.Parallel()
	got := deleteStuck(t, nodeWithReady("node-dead", corev1.ConditionFalse), 10*time.Second)
	if got == nil {
		t.Fatal("pod should still exist — DeletionTimestamp is too fresh to escalate")
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizers must not have been cleared — force delete must not fire before the stuck-terminating threshold")
	}
}

// TestNodeReadTimesOut 은 응답 없는 Node GET 에서도 nodeNotReady 가 nodeReadTimeout
// 안에 false(보수 기본값)로 돌아오는지 본다 — 막힌 읽기가 reconcile worker 를
// 영구히 잡던 2026-09-24 정지의 회귀 방지.
func TestNodeReadTimesOut(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	ctx := t.Context()

	// Node GET 은 호출자 ctx 가 끝날 때까지 막힌다(동기화 안 되는 informer·무응답 apiserver).
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Node); !ok {
					return cli.Get(ctx, key, obj, opts...)
				}
				<-ctx.Done()
				return ctx.Err()
			},
		}).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"}}

	done := make(chan bool, 1)
	go func() {
		done <- r.nodeNotReady(ctx, cluster, "node-hung", time.Now())
	}()

	select {
	case notReady := <-done:
		if notReady {
			t.Fatal("nodeNotReady = true on an unanswered read, want false (conservative)")
		}
	case <-time.After(nodeReadTimeout + blockedReadSlack):
		t.Fatalf("nodeNotReady still blocked after %s", nodeReadTimeout+blockedReadSlack)
	}
}

// TestNodeReadDeniedSignal 은 Node 읽기 거부(403)가 false(보수 기본값)로 끝나면서
// api_read_denied_total{kind=Node} 를 세고, Warning NodeReadDenied 를 클러스터당
// statusStaleThresh 에 한 번만 내는지 본다. 병렬 금지 — 전역 카운터 증분을 잰다.
func TestNodeReadDeniedSignal(t *testing.T) {
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "node-a", errors.New("nodes get not granted"))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Node); ok {
					return denied
				}
				return cli.Get(ctx, key, obj, opts...)
			},
		}).Build()
	recorder := events.NewFakeRecorder(eventBuffer)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, Recorder: recorder}
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"}}
	counter := MetricAPIReadDenied.WithLabelValues(apiReadKindNode)
	before := testutil.ToFloat64(counter)

	// 같은 창 안 두 번 — 카운터는 매번, 이벤트는 한 번.
	sameWindow := []time.Time{now, now.Add(statusStaleThresh - time.Second)}
	for _, at := range sameWindow {
		if r.nodeNotReady(ctx, cluster, "node-a", at) {
			t.Fatal("nodeNotReady = true on a denied read, want false (conservative)")
		}
	}
	if got := testutil.ToFloat64(counter) - before; got != float64(len(sameWindow)) {
		t.Fatalf("api_read_denied_total{kind=Node} delta = %v, want %d", got, len(sameWindow))
	}
	if got := len(recorder.Events); got != 1 {
		t.Fatalf("events within statusStaleThresh = %d, want 1", got)
	}
	if ev := <-recorder.Events; !strings.HasPrefix(ev, "Warning "+nodeReadDeniedReason+" ") {
		t.Fatalf("event = %q, want Warning %s", ev, nodeReadDeniedReason)
	}

	// 창이 지나면 다시 한 번.
	r.nodeNotReady(ctx, cluster, "node-a", now.Add(statusStaleThresh))
	if got := len(recorder.Events); got != 1 {
		t.Fatalf("events after statusStaleThresh = %d, want 1", got)
	}
}
