// Package controller — autosplit_cpu.go 는 AutoSplit 의 CPU 트리거 관측 소스를 결선한다.
//
// AutoSplitTriggers.CPUPercent 는 "per-shard 평균 CPU 사용률(%)" 이다. size 트리거가
// status.shards 에서 오는 것과 달리, CPU 사용량은 metrics-server(metrics.k8s.io) 에서
// 온다. cpuAugmentingObserver 는 base observer(statusShardObserver, size)를 감싸 각
// shard 의 primary Pod 에 대해 CPU 사용량(metrics.k8s.io PodMetrics) ÷ CPU request
// (Pod spec) × 100 을 계산해 ShardObservation.CPUPercent 를 채운다.
//
// 의존성 0: metrics.k8s.io 타입을 새 모듈로 추가하지 않고 controller-runtime 의
// unstructured GET 으로 PodMetrics 를 읽는다(레포 미니멀리즘). metrics-server 부재/미설치
// 시 graceful degrade — CPU 관측 0(오탐 없음, AND 조건상 CPU 트리거 미발동).
//
// P99 latency 트리거는 라우터가 per-shard 지연 히스토그램을 노출해야 해 별개 후속이다
// (여기서 latency 는 여전히 0).
package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// podMetricsGVK 는 metrics-server 가 제공하는 PodMetrics 리소스의 GVK.
var podMetricsGVK = schema.GroupVersionKind{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics"}

// PodMetricsReader 는 Pod 의 현재 CPU 사용량(millicores)을 읽는다. 테스트에서 fake 로
// 대체 가능. ok=false 는 미관측(metrics-server 부재/Pod metrics 없음) — 에러 아님.
type PodMetricsReader interface {
	PodCPUUsageMillis(ctx context.Context, namespace, podName string) (millis int64, ok bool, err error)
}

// unstructuredPodMetricsReader 는 controller-runtime reader 로 metrics.k8s.io PodMetrics
// 를 unstructured 로 GET 해 컨테이너 CPU 사용량 합(millicores)을 반환한다. NotFound
// (metrics-server 미설치 또는 Pod metrics 아직 없음) 시 (0, false, nil) 로 graceful.
type unstructuredPodMetricsReader struct {
	reader client.Reader
}

func (u unstructuredPodMetricsReader) PodCPUUsageMillis(ctx context.Context, namespace, podName string) (int64, bool, error) {
	pm := &unstructured.Unstructured{}
	pm.SetGroupVersionKind(podMetricsGVK)
	if err := u.reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, pm); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return 0, false, nil // metrics-server 부재/Pod metrics 없음 — 미관측.
		}
		return 0, false, err
	}
	containers, found, err := unstructured.NestedSlice(pm.Object, "containers")
	if err != nil || !found {
		return 0, false, nil
	}
	var total int64
	for _, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		usage, ok := cm["usage"].(map[string]any)
		if !ok {
			continue
		}
		cpuStr, ok := usage["cpu"].(string)
		if !ok {
			continue
		}
		q, err := resource.ParseQuantity(cpuStr)
		if err != nil {
			continue
		}
		total += q.MilliValue()
	}
	return total, true, nil
}

// cpuAugmentingObserver 는 base observer 결과에 CPU% 를 보강한다.
type cpuAugmentingObserver struct {
	base    ShardMetricsObserver
	reader  client.Reader
	metrics PodMetricsReader
}

// newDefaultShardObserver 는 production 기본 observer 를 만든다: size(statusShardObserver)
// + CPU(metrics.k8s.io) 보강. reader 는 reconciler 의 client.
func newDefaultShardObserver(reader client.Reader) ShardMetricsObserver {
	return cpuAugmentingObserver{
		base:    statusShardObserver{},
		reader:  reader,
		metrics: unstructuredPodMetricsReader{reader: reader},
	}
}

func (o cpuAugmentingObserver) ObserveShards(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) []ShardObservation {
	logger := log.FromContext(ctx)
	obs := o.base.ObserveShards(ctx, cluster)
	if cluster == nil {
		return obs
	}
	// shard 이름 → primary Pod 이름.
	primaryPod := map[string]string{}
	for i := range cluster.Status.Shards {
		s := &cluster.Status.Shards[i]
		if s.Primary != nil && s.Primary.Pod != "" {
			primaryPod[s.Name] = s.Primary.Pod
		}
	}
	for i := range obs {
		pod := primaryPod[obs[i].ShardID]
		if pod == "" {
			continue
		}
		pct, ok := o.cpuPercentForPod(ctx, cluster.Namespace, pod)
		if ok {
			obs[i].CPUPercent = pct
		} else {
			logger.V(1).Info("autoSplit: CPU 관측 불가(metrics-server 부재/request 미설정) — CPU 트리거 미발동",
				"shard", obs[i].ShardID, "pod", pod)
		}
	}
	return obs
}

// cpuPercentForPod 는 Pod 의 CPU 사용량 ÷ CPU request × 100 을 반환한다. 사용량 미관측
// 또는 request 미설정(=% 정의 불가) 시 ok=false.
func (o cpuAugmentingObserver) cpuPercentForPod(ctx context.Context, namespace, podName string) (int32, bool) {
	usedMillis, ok, err := o.metrics.PodCPUUsageMillis(ctx, namespace, podName)
	if err != nil || !ok || usedMillis <= 0 {
		return 0, false
	}
	reqMillis := o.podCPURequestMillis(ctx, namespace, podName)
	if reqMillis <= 0 {
		return 0, false // request 미설정 → utilization % 정의 불가.
	}
	pct := max(usedMillis*100/reqMillis, 0)
	return int32(pct), true
}

// podCPURequestMillis 는 Pod 의 컨테이너 CPU request 합(millicores)을 반환한다.
func (o cpuAugmentingObserver) podCPURequestMillis(ctx context.Context, namespace, podName string) int64 {
	var pod corev1.Pod
	if err := o.reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		return 0
	}
	var total int64
	for i := range pod.Spec.Containers {
		if req, ok := pod.Spec.Containers[i].Resources.Requests[corev1.ResourceCPU]; ok {
			total += req.MilliValue()
		}
	}
	return total
}
