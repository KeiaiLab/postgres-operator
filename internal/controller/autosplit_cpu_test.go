/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// fakePodMetrics 는 pod 이름 → CPU 사용량(millicores) 을 반환하는 테스트 reader.
// present=false 인 pod 은 미관측(ok=false)으로 취급한다.
type fakePodMetrics struct {
	millis  map[string]int64
	present map[string]bool
	err     error
}

func (f fakePodMetrics) PodCPUUsageMillis(_ context.Context, _, podName string) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	if f.present != nil && !f.present[podName] {
		return 0, false, nil
	}
	m, ok := f.millis[podName]
	return m, ok, nil
}

func podWithCPURequest(ns, name, cpu string) *corev1.Pod {
	reqs := corev1.ResourceList{}
	if cpu != "" {
		reqs[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:      "postgres",
			Resources: corev1.ResourceRequirements{Requests: reqs},
		}}},
	}
}

func clusterWithPrimary(name, ns, shard, pod string) *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{
				Name:      shard,
				SizeBytes: 3 * bytesPerGB,
				Primary:   &postgresv1alpha1.ShardEndpoint{Pod: pod, Ready: true},
			}},
		},
	}
}

func TestCPUAugmentingObserver(t *testing.T) {
	scheme := newScheme(t)
	ns := metav1.NamespaceDefault
	cluster := clusterWithPrimary("demo", ns, "shard-0", "demo-shard-0-0")

	tests := []struct {
		name       string
		cpuRequest string
		metrics    fakePodMetrics
		wantCPU    int32
		wantSize   int64
	}{
		{
			name:       "usage 800m / request 1000m → 80%",
			cpuRequest: "1000m",
			metrics:    fakePodMetrics{millis: map[string]int64{"demo-shard-0-0": 800}},
			wantCPU:    80,
			wantSize:   3 * bytesPerGB,
		},
		{
			name:       "usage 500m / request 250m → 200% (over request)",
			cpuRequest: "250m",
			metrics:    fakePodMetrics{millis: map[string]int64{"demo-shard-0-0": 500}},
			wantCPU:    200,
		},
		{
			name:       "metrics 미관측 → CPU 0 (size 유지)",
			cpuRequest: "1000m",
			metrics:    fakePodMetrics{present: map[string]bool{"demo-shard-0-0": false}},
			wantCPU:    0,
			wantSize:   3 * bytesPerGB,
		},
		{
			name:       "int32 를 넘는 % → MaxInt32 로 포화(wrap 금지)",
			cpuRequest: "1m",
			metrics:    fakePodMetrics{millis: map[string]int64{"demo-shard-0-0": 1 << 40}},
			wantCPU:    math.MaxInt32,
		},
		{
			name:       "request 미설정 → CPU 0 (utilization % 정의 불가)",
			cpuRequest: "",
			metrics:    fakePodMetrics{millis: map[string]int64{"demo-shard-0-0": 800}},
			wantCPU:    0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := podWithCPURequest(ns, "demo-shard-0-0", tc.cpuRequest)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			o := cpuAugmentingObserver{base: statusShardObserver{}, reader: c, metrics: tc.metrics}
			obs := o.ObserveShards(context.Background(), cluster)
			if len(obs) != 1 {
				t.Fatalf("expected 1 observation, got %d", len(obs))
			}
			if obs[0].CPUPercent != tc.wantCPU {
				t.Fatalf("CPUPercent = %d, want %d", obs[0].CPUPercent, tc.wantCPU)
			}
			if tc.wantSize != 0 && obs[0].SizeBytes != tc.wantSize {
				t.Fatalf("SizeBytes = %d, want %d (base observer must be preserved)", obs[0].SizeBytes, tc.wantSize)
			}
		})
	}
}

func TestCPUAugmentingObserver_NoPrimaryPod(t *testing.T) {
	scheme := newScheme(t)
	// primary Pod 이 없는 shard → CPU 보강 skip(그래도 base size 는 유지).
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{Name: "shard-0", SizeBytes: 5 * bytesPerGB}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	o := cpuAugmentingObserver{
		base:    statusShardObserver{},
		reader:  c,
		metrics: fakePodMetrics{millis: map[string]int64{"x": 999}},
	}
	obs := o.ObserveShards(context.Background(), cluster)
	if len(obs) != 1 || obs[0].CPUPercent != 0 || obs[0].SizeBytes != 5*bytesPerGB {
		t.Fatalf("unexpected obs: %+v", obs)
	}
}
