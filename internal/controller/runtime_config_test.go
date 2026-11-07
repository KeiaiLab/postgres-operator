/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 승격 상태가 파드 템플릿 밖(런타임 ConfigMap)에 있는지 본다 — ADR-0030,
// 설계 2026-09-25 §3.2. 뼈대는 reconcile_stall_test.go(fake client 전체 Reconcile)다.

const runtimeTestNS = "runtime"

// runtimeScheme 은 Reconcile 이 쓰는 종류(instance RBAC, router HPA 정리)까지 등록한다.
func runtimeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := newScheme(t)
	for _, add := range []func(*runtime.Scheme) error{rbacv1.AddToScheme, autoscalingv2.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add to scheme: %v", err)
		}
	}
	return scheme
}

// haCluster 는 primary 1 + replica 1 샤드 하나의 클러스터다.
func haCluster(name string, mode postgresv1alpha1.ShardingMode) *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: runtimeTestNS},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			PostgresVersion: "18",
			ShardingMode:    mode,
			Shards: postgresv1alpha1.ShardsSpec{
				InitialCount: 1,
				Replicas:     1,
				Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
			},
		},
	}
}

// withCanon 은 샤드 0 의 CR 정본 애노테이션을 단다.
func withCanon(cluster *postgresv1alpha1.PostgresCluster, canon shardCanon) *postgresv1alpha1.PostgresCluster {
	raw, _ := json.Marshal(runtimeState{Version: runtimeStateVersion, Shards: map[string]shardCanon{shard0: canon}})
	if cluster.Annotations == nil {
		cluster.Annotations = map[string]string{}
	}
	cluster.Annotations[shardRuntimeAnnotation] = string(raw)
	return cluster
}

// withRecordedPrimary 는 이전 reconcile 이 status 에 primary 를 기록한 상태다.
func withRecordedPrimary(cluster *postgresv1alpha1.PostgresCluster, pod string) *postgresv1alpha1.PostgresCluster {
	cluster.Status.Shards = []postgresv1alpha1.ShardStatus{{
		Name:    ShardIDForOrdinal(0),
		Primary: &postgresv1alpha1.ShardEndpoint{Pod: pod, Endpoint: podDNS(cluster.Name, pod), Ready: true},
	}}
	return cluster
}

// memberPod 는 instance-status 애노테이션을 단 Ready 멤버 파드다.
func memberPod(name, service string, labels map[string]string, role statusapi.Role) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: runtimeTestNS, Labels: labels},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: pgContainerName, Ready: true}},
		},
	}
	setMemberRole(pod, service, role, time.Now())
	return pod
}

// setMemberRole 은 instance manager 가 5 s 마다 쓰는 상태 애노테이션을 흉내 낸다.
func setMemberRole(pod *corev1.Pod, service string, role statusapi.Role, at time.Time) {
	raw, _ := json.Marshal(statusapi.Status{
		Role:       role,
		Ready:      true,
		Endpoint:   defaultEndpoint(pod.Name, service, runtimeTestNS),
		LastUpdate: at.UTC(),
	})
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[statusapi.AnnotationKey] = string(raw)
}

// shardLabels 는 ordinal 샤드 멤버 파드 라벨이다(STS 템플릿과 같은 집합).
func shardLabels(cluster string) map[string]string {
	labels := SelectorLabels(cluster, "shard", 0)
	labels[ShardIDLabelKey] = ShardIDForOrdinal(0)
	return labels
}

// shardMember 는 ordinal 샤드 0 의 멤버 파드다.
func shardMember(cluster string, ordinal int, role statusapi.Role) *corev1.Pod {
	return memberPod(fmt.Sprintf("%s-%d", ShardStatefulSetName(cluster, 0), ordinal), ShardServiceName(cluster, 0), shardLabels(cluster), role)
}

// podDNS 는 ordinal 샤드 0 멤버의 headless endpoint 다.
func podDNS(cluster, pod string) string {
	return defaultEndpoint(pod, ShardServiceName(cluster, 0), runtimeTestNS)
}

// oldTemplateSTS 는 L1 전 판이 만든 StatefulSet(PRIMARY_ENDPOINT 인라인) 이다.
func oldTemplateSTS(cluster *postgresv1alpha1.PostgresCluster) *appsv1.StatefulSet {
	sts := buildPGStatefulSet(
		cluster, ShardStatefulSetName(cluster.Name, 0), ShardServiceName(cluster.Name, 0), 0,
		"example.com/postgres:18", ShardConfigMapName(cluster.Name, 0), "18", 2,
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, "", "old-hash", "",
	)
	sts.Spec.Template.Spec.InitContainers[0].Env[1].Value = podDNS(cluster.Name, ShardStatefulSetName(cluster.Name, 0)+"-0")
	return sts
}

func reconcileCluster(t *testing.T, r *PostgresClusterReconciler, cluster *postgresv1alpha1.PostgresCluster) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func stsTemplate(t *testing.T, c client.Client, name string) corev1.PodTemplateSpec {
	t.Helper()
	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: name}, &sts); err != nil {
		t.Fatalf("get StatefulSet %q: %v", name, err)
	}
	return sts.Spec.Template
}

// runtimeCM 은 shard-0 런타임 CM 데이터다(없으면 nil).
func runtimeCM(t *testing.T, c client.Client, cluster string) map[string]string {
	t.Helper()
	var cm corev1.ConfigMap
	err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: ShardRuntimeConfigMapName(cluster, "shard-0")}, &cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("get runtime ConfigMap: %v", err)
	}
	return cm.Data
}

// storedCanon 은 CR 에 저장된 정본이다.
func storedCanon(t *testing.T, c client.Client, cluster *postgresv1alpha1.PostgresCluster, shard string) shardCanon {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	state, err := loadRuntimeState(&got)
	if err != nil {
		t.Fatalf("load runtime state: %v", err)
	}
	return state.Shards[shard]
}

func runtimeCondition(t *testing.T, c client.Client, cluster *postgresv1alpha1.PostgresCluster) *metav1.Condition {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	return meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeConfigPending)
}

// flipPrimary 는 primary 를 from → to 로 옮긴 상태 애노테이션을 쓴다(승격 exec 없이).
func flipPrimary(t *testing.T, c client.Client, service, from, to string) {
	t.Helper()
	for pod, role := range map[string]statusapi.Role{from: statusapi.RoleReplica, to: statusapi.RolePrimary} {
		var got corev1.Pod
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &got); err != nil {
			t.Fatalf("get pod %q: %v", pod, err)
		}
		setMemberRole(&got, service, role, time.Now())
		if err := c.Update(context.Background(), &got); err != nil {
			t.Fatalf("update pod %q: %v", pod, err)
		}
	}
}

func envByNameOf(ctr corev1.Container) map[string]corev1.EnvVar {
	out := make(map[string]corev1.EnvVar, len(ctr.Env))
	for _, e := range ctr.Env {
		out[e.Name] = e
	}
	return out
}

// assertRuntimeEnv 는 env 가 런타임 CM 의 같은 이름 키를 Optional=false 로 참조하는지 본다.
func assertRuntimeEnv(t *testing.T, env map[string]corev1.EnvVar, cmName string, keys ...string) {
	t.Helper()
	for _, key := range keys {
		e, ok := env[key]
		if !ok {
			t.Errorf("env %s missing", key)
			continue
		}
		var ref *corev1.ConfigMapKeySelector
		if e.ValueFrom != nil {
			ref = e.ValueFrom.ConfigMapKeyRef
		}
		if e.Value != "" || ref == nil || ref.Name != cmName || ref.Key != key || ref.Optional == nil || *ref.Optional {
			t.Errorf("env %s = %+v, want configMapKeyRef{%s, %s, optional false}", key, e, cmName, key)
		}
	}
}

// writeLog 는 객체 쓰기와 exec 를 한 줄씩 기록한다(순서 단언용).
type writeLog struct {
	mu    sync.Mutex
	lines []string
}

func (w *writeLog) add(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, line)
}

// first 는 prefix 로 시작하는 첫 줄 번호다(없으면 -1).
func (w *writeLog) first(prefix string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, line := range w.lines {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

func (w *writeLog) count(prefix string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, line := range w.lines {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func objKind(obj client.Object) string {
	switch obj.(type) {
	case *corev1.ConfigMap:
		return "cm"
	case *appsv1.StatefulSet:
		return "sts"
	case *postgresv1alpha1.PostgresCluster:
		return "cluster"
	default:
		return fmt.Sprintf("%T", obj)
	}
}

// funcs 는 Create·Update·Patch 를 "<verb> <kind>/<name>" 으로 기록한다.
func (w *writeLog) funcs() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			w.add("create " + objKind(obj) + "/" + obj.GetName())
			return cli.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			w.add("update " + objKind(obj) + "/" + obj.GetName())
			return cli.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			w.add("patch " + objKind(obj) + "/" + obj.GetName())
			return cli.Patch(ctx, obj, patch, opts...)
		},
	}
}

// funcExecutor 는 명령으로 응답을 고르고 exec 를 writeLog 에 남긴다.
type funcExecutor struct {
	log     *writeLog
	respond func(target BackupSidecarTarget, command []string) ([]byte, error)
}

func (f *funcExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	joined := strings.Join(command, " ")
	switch {
	case strings.Contains(joined, "pg_promote"):
		f.log.add("exec promote " + target.Pod + " epoch=" + command[len(command)-1])
	case strings.Contains(joined, "pg_read_file"):
		f.log.add("exec history " + target.Pod)
	case strings.Contains(joined, promotedPrimaryMarker):
		f.log.add("exec marker " + target.Pod)
	default:
		f.log.add("exec other " + target.Pod)
	}
	return f.respond(target, command)
}

// recoveryExecutor 는 pg_is_in_recovery 와 승격 마커 exec 에 파드별로 답한다 — recovery 답이
// 없는 파드는 exec 가 실패한다.
func recoveryExecutor(log *writeLog, recovery, markers map[string]string) *funcExecutor {
	return &funcExecutor{log: log, respond: func(target BackupSidecarTarget, command []string) ([]byte, error) {
		joined := strings.Join(command, " ")
		switch {
		case strings.Contains(joined, "pg_is_in_recovery"):
			answer, ok := recovery[target.Pod]
			if !ok {
				return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
			}
			return []byte(answer + "\n"), nil
		case strings.Contains(joined, promotedPrimaryMarker):
			return []byte(markers[target.Pod]), nil
		}
		return nil, nil
	}}
}

// TestPrimaryChangeKeepsTemplate 는 primary 가 바뀌어도 STS 파드 템플릿이 그대로인지 본다.
// 템플릿이 바뀌면 RollingUpdate 가 primary 까지 재시작해 failover 루프가 된다(§2.1).
func TestPrimaryChangeKeepsTemplate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mode   postgresv1alpha1.ShardingMode
		sts    func(cluster string) string
		svc    func(cluster string) string
		labels func(cluster string) map[string]string
		extra  func(cluster string) []client.Object
	}{
		{
			name:   "ordinal",
			mode:   postgresv1alpha1.ShardingModeNone,
			sts:    func(c string) string { return ShardStatefulSetName(c, 0) },
			svc:    func(c string) string { return ShardServiceName(c, 0) },
			labels: shardLabels,
			extra:  func(string) []client.Object { return nil },
		},
		{
			name:   "named",
			mode:   postgresv1alpha1.ShardingModeNative,
			sts:    func(c string) string { return TargetShardStatefulSetName(c, "t1") },
			svc:    func(c string) string { return TargetShardServiceName(c, "t1") },
			labels: func(c string) map[string]string { return ReshardTargetSelectorLabels(c, "t1") },
			extra: func(c string) []client.Object {
				return []client.Object{&postgresv1alpha1.ShardRange{
					ObjectMeta: metav1.ObjectMeta{Name: c + "-sr", Namespace: runtimeTestNS},
					Spec: postgresv1alpha1.ShardRangeSpec{
						Cluster:  c,
						Keyspace: "default",
						Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: "t1"}},
					},
				}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "tpl-" + tc.name
			cluster := haCluster(name, tc.mode)
			sts, svc := tc.sts(name), tc.svc(name)
			first, second := sts+"-0", sts+"-1"
			objs := append([]client.Object{
				cluster,
				memberPod(first, svc, tc.labels(name), statusapi.RolePrimary),
				memberPod(second, svc, tc.labels(name), statusapi.RoleReplica),
			}, tc.extra(name)...)
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
				WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
			defer DeleteMetricsFor(runtimeTestNS, name)

			// 수렴(status 가 첫 primary 를 기록) 뒤 템플릿을 적는다.
			reconcileCluster(t, r, cluster)
			reconcileCluster(t, r, cluster)
			before := stsTemplate(t, c, sts)

			flipPrimary(t, c, svc, first, second)
			reconcileCluster(t, r, cluster)
			reconcileCluster(t, r, cluster)

			if after := stsTemplate(t, c, sts); !equality.Semantic.DeepEqual(before, after) {
				t.Fatalf("pod template changed after primary %s → %s:\nbefore=%s\nafter=%s",
					first, second, envDump(before), envDump(after))
			}
		})
	}
}

// envDump 는 템플릿 env 를 읽기 쉽게 보인다(실패 메시지용).
func envDump(tpl corev1.PodTemplateSpec) string {
	var b strings.Builder
	for _, ctr := range append(append([]corev1.Container{}, tpl.Spec.InitContainers...), tpl.Spec.Containers...) {
		for _, e := range ctr.Env {
			switch {
			case e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil:
				fmt.Fprintf(&b, "%s:%s=cm(%s/%s) ", ctr.Name, e.Name, e.ValueFrom.ConfigMapKeyRef.Name, e.ValueFrom.ConfigMapKeyRef.Key)
			case e.ValueFrom == nil:
				fmt.Fprintf(&b, "%s:%s=%q ", ctr.Name, e.Name, e.Value)
			}
		}
	}
	return b.String()
}

// TestRuntimeEnvFromConfigMap 은 init·main PRIMARY_ENDPOINT 가 런타임 CM 참조이고 init 에
// PROMOTION_EPOCH·CLUSTER_INITIALIZED 가 붙는지, 리샤드 타깃은 인라인 ” 인지 본다.
func TestRuntimeEnvFromConfigMap(t *testing.T) {
	t.Parallel()
	cluster := haCluster("env", postgresv1alpha1.ShardingModeNone)
	cmName := ShardRuntimeConfigMapName("env", "shard-0")
	sts := buildPGStatefulSet(
		cluster, ShardStatefulSetName("env", 0), ShardServiceName("env", 0), 0,
		"example.com/postgres:18", ShardConfigMapName("env", 0), "18", 2,
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, cmName, "hash", "",
	)
	if cmName != "env-shard-0-runtime" {
		t.Fatalf("runtime ConfigMap name = %q, want env-shard-0-runtime", cmName)
	}
	initEnv := envByNameOf(sts.Spec.Template.Spec.InitContainers[0])
	assertRuntimeEnv(t, initEnv, cmName, runtimeKeyPrimaryEndpoint, runtimeKeyPromotionEpoch, runtimeKeyClusterInitialized)
	mainEnv := envByNameOf(sts.Spec.Template.Spec.Containers[0])
	assertRuntimeEnv(t, mainEnv, cmName, runtimeKeyPrimaryEndpoint)
	for _, key := range []string{runtimeKeyPromotionEpoch, runtimeKeyClusterInitialized} {
		if _, ok := mainEnv[key]; ok {
			t.Errorf("main container must not carry %s (init only)", key)
		}
	}

	target := buildTargetShardStatefulSet(cluster, "t0", "example.com/postgres:18", "18",
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, TargetShardConfigMapName("env", "t0"), "hash")
	for _, ctr := range []corev1.Container{target.Spec.Template.Spec.InitContainers[0], target.Spec.Template.Spec.Containers[0]} {
		if got := envByNameOf(ctr)[runtimeKeyPrimaryEndpoint]; got.ValueFrom != nil || got.Value != "" {
			t.Errorf("reshard target %s PRIMARY_ENDPOINT = %+v, want inline empty", ctr.Name, got)
		}
	}
}

// TestPromoteAndPublishOrder 는 승격 경로의 쓰기 순서를 본다 — CR epoch E+1 이 exec 보다
// 먼저, exec 인자에 epoch, promoted 뒤 CM 쓰기 한 번(endpoint·epoch), 분기점 기록.
func TestPromoteAndPublishOrder(t *testing.T) {
	t.Parallel()
	const history = "1\t0/3000160\tno recovery target specified\n2\t0/5000060\tno recovery target specified\n"
	cases := []struct {
		name    string
		promote func(r *PostgresClusterReconciler, cluster *postgresv1alpha1.PostgresCluster, target string) error
	}{
		{"failover", func(r *PostgresClusterReconciler, cluster *postgresv1alpha1.PostgresCluster, target string) error {
			return r.executeClusterPromotion(context.Background(), cluster, "shard-0", failover.Decision{
				Failed: true,
				Reason: failover.ReasonNoPrimary,
				PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
					Pod: target, Endpoint: podDNS(cluster.Name, target), Ready: true,
				},
			})
		}},
		{"switchover", func(r *PostgresClusterReconciler, cluster *postgresv1alpha1.PostgresCluster, target string) error {
			promoted := failover.PromotionTarget{ShardName: "shard-0", Pod: target, Endpoint: podDNS(cluster.Name, target)}
			return r.promoteAndPublish(context.Background(), cluster, "shard-0", promoted,
				ShardStatefulSetName(cluster.Name, 0)+"-0", promotionSwitchover)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "pub-" + tc.name
			target := ShardStatefulSetName(name, 0) + "-1"
			cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
				shardCanon{Epoch: 2, Initialized: true})
			cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
				PrimaryEndpoint: podDNS(name, ShardStatefulSetName(name, 0)+"-0"), PromotionEpoch: 2, ClusterInitialized: true,
			})
			log := &writeLog{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, cm, promotionMember(runtimeTestNS, name, target)).
				WithInterceptorFuncs(log.funcs()).Build()
			// failover 는 승격 전에 유일한 멤버를 끊고 잰다(ADR-0036 F2·F3).
			exec := &funcExecutor{log: log, respond: func(_ BackupSidecarTarget, command []string) ([]byte, error) {
				switch fakeOp(strings.Join(command, " ")) {
				case "history":
					return []byte(history), nil
				case "cut":
					return []byte(cutResultPrefix + cutResultCut + "\n"), nil
				case "lsn":
					return []byte(pgBoolTrue + psqlFieldSeparator + "0/3000200\n"), nil
				}
				return []byte("PROMOTE_RESULT=promoted\n"), nil
			}}
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}

			if err := tc.promote(r, cluster, target); err != nil {
				t.Fatalf("promote: %v", err)
			}

			epochWrite := log.first("patch cluster/" + name)
			promote := log.first("exec promote " + target + " epoch=3")
			publish := log.first("patch cm/" + cm.Name)
			if epochWrite < 0 || promote < 0 || publish < 0 || epochWrite >= promote || promote >= publish {
				t.Fatalf("order must be CR epoch → exec(epoch 3) → CM publish, got %q", log.lines)
			}
			if n := log.count("patch cm/") + log.count("update cm/") + log.count("create cm/"); n != 1 {
				t.Fatalf("CM writes = %d, want exactly 1: %q", n, log.lines)
			}
			want := map[string]string{
				runtimeKeyPrimaryEndpoint:    podDNS(name, target),
				runtimeKeyPromotionEpoch:     "3",
				runtimeKeyClusterInitialized: runtimeFlagOn,
				runtimeKeyMemberHold:         "",
				runtimeKeyReseedRequests:     "",
			}
			if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
				t.Fatalf("runtime CM = %v, want %v", got, want)
			}
			if got := storedCanon(t, c, cluster, "shard-0"); got != (shardCanon{Epoch: 3, Initialized: true, PromotionLSN: "0/5000060"}) {
				t.Fatalf("canon = %+v, want epoch 3, initialized, promotionLSN 0/5000060", got)
			}
		})
	}
}

// TestRuntimeCreatedFromHealthyPrimary 는 CM 이 없고 건강한 primary 가 정확히 하나일 때
// 정본 첫 파생(마커 epoch 최대 · status 기록)으로 CM 을 STS upsert 보다 먼저 만드는지 본다.
func TestRuntimeCreatedFromHealthyPrimary(t *testing.T) {
	t.Parallel()
	const name = "first"
	cluster := withRecordedPrimary(haCluster(name, postgresv1alpha1.ShardingModeNone), ShardStatefulSetName(name, 0)+"-0")
	secret := withSuperuserRef(cluster)
	primary, replica := shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RoleReplica)
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, secret, oldTemplateSTS(cluster), primary, replica).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	// L1 첫 reconcile — postgres 비밀번호는 이미 Secret 것이다(ADR-0034 이 템플릿 upsert 앞에 대조한다).
	exec := newPGSim(log, primary.Name, replica.Name)
	exec.markers[primary.Name] = "2\n"
	exec.stored = knownVerifier(t, testSuperuserPassword)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)

	want := map[string]string{
		runtimeKeyPrimaryEndpoint:    podDNS(name, primary.Name),
		runtimeKeyPromotionEpoch:     "2",
		runtimeKeyClusterInitialized: runtimeFlagOn,
		runtimeKeyMemberHold:         "",
		runtimeKeyReseedRequests:     "",
	}
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("runtime CM = %v, want %v", got, want)
	}
	if got := storedCanon(t, c, cluster, "shard-0"); got != (shardCanon{Epoch: 2, Initialized: true}) {
		t.Fatalf("canon = %+v, want first derivation epoch 2 initialized", got)
	}
	create, upsert := log.first("create cm/"+ShardRuntimeConfigMapName(name, "shard-0")), log.first("update sts/")
	if create < 0 || upsert < 0 || create >= upsert {
		t.Fatalf("runtime CM must be created before the STS upsert, got %q", log.lines)
	}
	assertRuntimeEnv(t, envByNameOf(stsTemplate(t, c, ShardStatefulSetName(name, 0)).Spec.Containers[0]),
		ShardRuntimeConfigMapName(name, "shard-0"), runtimeKeyPrimaryEndpoint)
	if cond := runtimeCondition(t, c, cluster); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("RuntimeConfigPending = %+v, want False", cond)
	}
}

// TestFreshShardRuntime 은 새 샤드(STS·멤버 없음)가 첫 값 폴백(ordinal-0 DNS · epoch 0 ·
// 미초기화)으로 CM 을 만들고, 건강한 primary 가 보고되면 initialized 만 올라가는지 본다(I6).
func TestFreshShardRuntime(t *testing.T) {
	t.Parallel()
	const name = "fresh"
	cluster := haCluster(name, postgresv1alpha1.ShardingModeNone)
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)

	seed := ShardStatefulSetName(name, 0) + "-0"
	want := map[string]string{
		runtimeKeyPrimaryEndpoint:    podDNS(name, seed),
		runtimeKeyPromotionEpoch:     "0",
		runtimeKeyClusterInitialized: runtimeFlagOff,
		runtimeKeyMemberHold:         "",
		runtimeKeyReseedRequests:     "",
	}
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("fresh runtime CM = %v, want %v", got, want)
	}
	assertRuntimeEnv(t, envByNameOf(stsTemplate(t, c, ShardStatefulSetName(name, 0)).Spec.InitContainers[0]),
		ShardRuntimeConfigMapName(name, "shard-0"), bootstrapRuntimeKeys...)

	// ordinal-0 이 initdb 뒤 primary 로 보고한다 → initialized 가 끈적하게 올라간다.
	if err := c.Create(context.Background(), shardMember(name, 0, statusapi.RolePrimary)); err != nil {
		t.Fatalf("create primary pod: %v", err)
	}
	reconcileCluster(t, r, cluster)

	want[runtimeKeyClusterInitialized] = runtimeFlagOn
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("runtime CM after first primary = %v, want %v", got, want)
	}
	if got := storedCanon(t, c, cluster, "shard-0"); got != (shardCanon{Initialized: true}) {
		t.Fatalf("canon = %+v, want epoch 0 initialized", got)
	}
}

// TestRuntimeWithheldWithoutSinglePrimary 는 CM 이 없고 건강한 primary 가 0 또는 둘이면
// CM 0 · STS upsert 0 · RuntimeConfigPending=True 인지 본다(옛 템플릿 유지).
func TestRuntimeWithheldWithoutSinglePrimary(t *testing.T) {
	t.Parallel()
	cases := map[string][2]statusapi.Role{
		"no primary":  {statusapi.RoleReplica, statusapi.RoleReplica},
		"two primary": {statusapi.RolePrimary, statusapi.RolePrimary},
	}
	for label, roles := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			name := "hold-" + strings.ReplaceAll(label, " ", "-")
			cluster := haCluster(name, postgresv1alpha1.ShardingModeNone)
			old := oldTemplateSTS(cluster)
			log := &writeLog{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, old, shardMember(name, 0, roles[0]), shardMember(name, 1, roles[1])).
				WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
				WithInterceptorFuncs(log.funcs()).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
			defer DeleteMetricsFor(runtimeTestNS, name)

			reconcileCluster(t, r, cluster)

			if got := runtimeCM(t, c, name); got != nil {
				t.Fatalf("runtime CM created without a single healthy primary: %v", got)
			}
			if n := log.count("update sts/") + log.count("patch sts/") + log.count("create sts/"); n != 0 {
				t.Fatalf("STS writes = %d, want 0 (old template kept): %q", n, log.lines)
			}
			if got := stsTemplate(t, c, old.Name); !equality.Semantic.DeepEqual(got, old.Spec.Template) {
				t.Fatalf("old template changed:\nwant=%s\ngot=%s", envDump(old.Spec.Template), envDump(got))
			}
			cond := runtimeCondition(t, c, cluster)
			if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonPrimaryNotUnique {
				t.Fatalf("RuntimeConfigPending = %+v, want True/%s", cond, ReasonPrimaryNotUnique)
			}
		})
	}
}

// TestRuntimeRecreatedFromCanon 은 CM 이 지워지면 epoch·initialized 를 CR 정본으로(0 아님)
// 다시 만드는지 본다 — 마커는 다시 읽지 않는다.
func TestRuntimeRecreatedFromCanon(t *testing.T) {
	t.Parallel()
	const name = "recreate"
	cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
		shardCanon{Epoch: 3, Initialized: true, PromotionLSN: "0/5000060"})
	primary := shardMember(name, 1, statusapi.RolePrimary)
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, oldTemplateSTS(cluster), shardMember(name, 0, statusapi.RoleReplica), primary).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	exec := &funcExecutor{log: log, respond: func(BackupSidecarTarget, []string) ([]byte, error) {
		return []byte("9\n"), nil
	}}
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ShardRuntimeConfigMapName(name, "shard-0"), Namespace: runtimeTestNS}}
	if err := c.Delete(context.Background(), cm); err != nil {
		t.Fatalf("delete runtime CM: %v", err)
	}
	reconcileCluster(t, r, cluster)

	want := map[string]string{
		runtimeKeyPrimaryEndpoint:    podDNS(name, primary.Name),
		runtimeKeyPromotionEpoch:     "3",
		runtimeKeyClusterInitialized: runtimeFlagOn,
		runtimeKeyMemberHold:         "",
		runtimeKeyReseedRequests:     "",
	}
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("recreated runtime CM = %v, want canon %v", got, want)
	}
	if n := log.count("exec marker "); n != 0 {
		t.Fatalf("markers read %d times with a canon present, want 0", n)
	}
}

// TestStalePrimaryLeavesRuntime 은 낡은(하트비트가 끊긴) Role=Primary 만으로 CM 이 바뀌지
// 않는지 본다 — CM primary·epoch 는 promoteAndPublish·채택만 바꾼다(I28).
func TestStalePrimaryLeavesRuntime(t *testing.T) {
	t.Parallel()
	const name = "stale"
	cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
		shardCanon{Epoch: 1, Initialized: true})
	primary0 := ShardStatefulSetName(name, 0) + "-0"
	cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, primary0), PromotionEpoch: 1, ClusterInitialized: true,
	})
	stale := shardMember(name, 1, statusapi.RolePrimary)
	setMemberRole(stale, ShardServiceName(name, 0), statusapi.RolePrimary, time.Now().Add(-2*statusStaleThresh))
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, shardMember(name, 0, statusapi.RoleReplica), stale).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)
	reconcileCluster(t, r, cluster)

	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, cm.Data) {
		t.Fatalf("runtime CM changed on a stale Role=Primary: got %v, want %v", got, cm.Data)
	}
	if n := log.count("patch cm/"+cm.Name) + log.count("update cm/"+cm.Name); n != 0 {
		t.Fatalf("runtime CM writes = %d, want 0: %q", n, log.lines)
	}
}

// TestAdoptPrimary 는 발행 전 크래시를 잇는지 본다(§3.2-2): CR epoch E · CM epoch E−1 이고
// 마커 epoch 이 E 인 실제 primary 가 있으면 CM 을 한 번 쓴다(endpoint·epoch=E · primary 를 보고하지
// 않는 옛 CM primary hold). 마커가 E 보다 작으면(승격 exec 실패) 채택하지 않는다.
func TestAdoptPrimary(t *testing.T) {
	t.Parallel()
	const epoch = 3
	cases := []struct {
		name   string
		marker int64
		adopt  bool
	}{
		{"marker at canon epoch", epoch, true},
		{"marker behind canon epoch", epoch - 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "adopt-" + strings.ReplaceAll(tc.name, " ", "-")
			cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
				shardCanon{Epoch: epoch, Initialized: true})
			old := shardMember(name, 0, statusapi.RoleReplica)
			promoted := shardMember(name, 1, statusapi.RolePrimary)
			cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
				PrimaryEndpoint: podDNS(name, old.Name), PromotionEpoch: epoch - 1, ClusterInitialized: true,
			})
			log := &writeLog{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, cm, old, promoted).
				WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
				WithInterceptorFuncs(log.funcs()).Build()
			exec := recoveryExecutor(log,
				map[string]string{old.Name: pgBoolTrue, promoted.Name: pgBoolFalse},
				map[string]string{promoted.Name: strconv.FormatInt(tc.marker, 10) + "\n"})
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}
			defer DeleteMetricsFor(runtimeTestNS, name)

			reconcileCluster(t, r, cluster)

			want, writes := cm.Data, 0
			if tc.adopt {
				want = map[string]string{
					runtimeKeyPrimaryEndpoint:    podDNS(name, promoted.Name),
					runtimeKeyPromotionEpoch:     strconv.FormatInt(epoch, 10),
					runtimeKeyClusterInitialized: runtimeFlagOn,
					runtimeKeyMemberHold:         old.Name,
					runtimeKeyReseedRequests:     "",
				}
				writes = 1
			}
			if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, want) {
				t.Fatalf("runtime CM = %v, want %v", got, want)
			}
			if n := log.count("patch cm/"+cm.Name) + log.count("update cm/"+cm.Name) + log.count("create cm/"+cm.Name); n != writes {
				t.Fatalf("runtime CM writes = %d, want %d: %q", n, writes, log.lines)
			}
		})
	}
}

// TestCheckPrimary 는 실제 primary 판정(pg_is_in_recovery=f ∧ 하트비트 신선 ∧ unfenced, §3.2)이
// 그 밖의 모든 경우 — exec 실패·엉뚱한 답 포함 — 를 비확인으로 두는지 본다. 싼 판정에서 걸리면
// exec 하지 않는다(죽은 노드의 파드에 exec 하지 않는다).
func TestCheckPrimary(t *testing.T) {
	t.Parallel()
	const name = "check"
	now := time.Now()
	cases := []struct {
		name   string
		mutate func(*corev1.Pod)
		fenced bool
		answer string // pg_is_in_recovery 답 — 빈 문자열이면 exec 실패
		want   primaryCheck
		execs  int
	}{
		{name: "confirmed", answer: pgBoolFalse, want: primaryConfirmed, execs: 1},
		{name: "in recovery", answer: pgBoolTrue, want: primaryInRecovery, execs: 1},
		{name: "exec fails", want: primaryUnknown, execs: 1},
		{name: "odd answer", answer: "x", want: primaryUnknown, execs: 1},
		{name: "fenced", fenced: true, answer: pgBoolFalse, want: primaryFenced},
		{name: "stale heartbeat", answer: pgBoolFalse, want: primaryStale, mutate: func(p *corev1.Pod) {
			setMemberRole(p, ShardServiceName(name, 0), statusapi.RolePrimary, now.Add(-2*statusStaleThresh))
		}},
		{name: "no heartbeat", answer: pgBoolFalse, want: primaryStale, mutate: func(p *corev1.Pod) {
			delete(p.Annotations, statusapi.AnnotationKey)
		}},
		{name: "pending", answer: pgBoolFalse, want: primaryNotRunning, mutate: func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
		}},
		{name: "deleting", answer: pgBoolFalse, want: primaryNotRunning, mutate: func(p *corev1.Pod) {
			p.DeletionTimestamp = &metav1.Time{Time: now}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pod := shardMember(name, 0, statusapi.RolePrimary)
			if tc.mutate != nil {
				tc.mutate(pod)
			}
			answers := map[string]string{}
			if tc.answer != "" {
				answers[pod.Name] = tc.answer
			}
			fenced := map[string]bool{}
			if tc.fenced {
				fenced[fencing.PVCName(pod.Name)] = true
			}
			log := &writeLog{}
			r := &PostgresClusterReconciler{PromotionPodExecutor: recoveryExecutor(log, answers, nil)}

			if got := r.checkPrimary(context.Background(), pod, fenced, now); got != tc.want {
				t.Fatalf("checkPrimary = %q, want %q", got, tc.want)
			}
			if n := log.count("exec "); n != tc.execs {
				t.Fatalf("execs = %d, want %d: %q", n, tc.execs, log.lines)
			}
		})
	}

	r := &PostgresClusterReconciler{}
	if got := r.checkPrimary(context.Background(), nil, map[string]bool{}, now); got != primaryAbsent {
		t.Fatalf("checkPrimary(nil) = %q, want %q", got, primaryAbsent)
	}
	if got := r.checkPrimary(context.Background(), shardMember(name, 0, statusapi.RolePrimary), map[string]bool{}, now); got != primaryUnknown {
		t.Fatalf("checkPrimary without executor = %q, want %q", got, primaryUnknown)
	}
}

// TestLastSwitchPoint 는 timeline history 의 마지막 분기 LSN 을 읽는지 본다.
func TestLastSwitchPoint(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"1\t0/3000160\tno recovery target specified\n":                       "0/3000160",
		"1\t0/3000160\tx\n\n2\t1A/5000060\tno recovery target specified\n\n": "1A/5000060",
		"PROMOTE_RESULT=promoted\n":                                          "",
		"":                                                                   "",
		"# comment\n3\t0/0\tbefore\n":                                        "0/0",
		"2\tnot-an-lsn\treason\n":                                            "",
	}
	for history, want := range cases {
		got, ok := lastSwitchPoint(history)
		if got != want || ok != (want != "") {
			t.Errorf("lastSwitchPoint(%q) = %q, %v; want %q", history, got, ok, want)
		}
	}
}
