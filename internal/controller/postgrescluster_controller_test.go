/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 envtest 는 RFC 0001 PostgresCluster CRD v2 위에서의 reconcile 를 검증한다.
// 두 시나리오:
//
//  1. SingleShardNoRouter — shardingMode=none, shards.initialCount=1, router=nil
//     → STS/SVC/CM 각 1 개 생성 + Phase=Provisioning
//     → STS readyReplicas=1 mock + reconcile trigger → Phase=Ready
//
//  2. NativeMultiShardWithRouter — shardingMode=native, initialCount=2, router.replicas=2
//     → STS 2 + Router Deployment 1 + ClientService 1 + ConfigMap 3 (shard×2 + router×1)
//
// envtest 에는 STS / Deployment controller 가 없으므로 readyReplicas 는 수동으로
// 설정하고, reconcile re-trigger 는 spec annotation bump 로 수행한다.

const (
	envtestTimeout  = 10 * time.Second
	envtestInterval = 200 * time.Millisecond
)

var _ = Describe("PostgresClusterReconciler — RFC 0001 spec", func() {
	var (
		ctx       context.Context
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = fmt.Sprintf("f01b-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		})).To(Succeed())
	})

	Context("when shardingMode=none with single shard and no router", func() {
		It("sets deterministic ordinal-zero PRIMARY_ENDPOINT on initial HA bootstrap", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "ha-bootstrap", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("ha-bootstrap", 0)
			svcName := ShardServiceName("ha-bootstrap", 0)
			wantEndpoint := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:5432", stsName, svcName, namespace)

			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "primary 1 + async 1")

				initEnv := envMap(sts.Spec.Template.Spec.InitContainers[0].Env)
				g.Expect(initEnv["PRIMARY_ENDPOINT"].Value).To(Equal(wantEndpoint))

				mainEnv := envMap(sts.Spec.Template.Spec.Containers[0].Env)
				g.Expect(mainEnv["PRIMARY_ENDPOINT"].Value).To(Equal(wantEndpoint))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})

		It("creates exactly one shard's resources and reaches Ready after STS readiness", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "single", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						// Replicas 는 CRD default=1 (omitempty + kubebuilder:default=1) 이라
						// 명시적으로 1 을 보내거나 생략해도 server-side 가 1 로 채운다.
						// members = primary 1 + async 1 = 2.
						Replicas: 1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("single", 0)
			svcName := ShardServiceName("single", 0)
			cmName := ShardConfigMapName("single", 0)

			By("provisioning shard subresources")
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "primary 1 + async 1")
				g.Expect(sts.Spec.ServiceName).To(Equal(svcName))

				var svc corev1.Service
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: svcName}, &svc)).To(Succeed())
				g.Expect(svc.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))

				var cm corev1.ConfigMap
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, &cm)).To(Succeed())
				g.Expect(cm.Data).To(HaveKey("postgresql.conf"))
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("observing Provisioning phase before STS becomes ready")
			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "single"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseProvisioning))
				g.Expect(got.Status.Shards).To(HaveLen(1))
				g.Expect(got.Status.Shards[0].Ordinal).To(Equal(int32(0)))
				g.Expect(got.Status.Shards[0].Primary.Ready).To(BeFalse())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("simulating STS primary readiness and re-triggering reconcile")
			markSTSReady(ctx, namespace, stsName, 1)
			bumpAnnotation(ctx, cluster)

			By("reaching Ready phase")
			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "single"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseReady))
				g.Expect(got.Status.Shards[0].Primary.Ready).To(BeTrue())
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when shardingMode=native with 2 shards and router", func() {
		It("creates 2 shard STSes plus router Deployment and ClientService", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 2,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
					Router: &postgresv1alpha1.RouterSpec{
						Enabled:  true,
						Replicas: 2,
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			By("creating both shard STSes")
			Eventually(func(g Gomega) {
				for _, ord := range []int32{0, 1} {
					var sts appsv1.StatefulSet
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{
						Namespace: namespace, Name: ShardStatefulSetName("multi", ord),
					}, &sts)).To(Succeed())
					g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)), "primary 1 + replica 1")
				}
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("creating router Deployment + ClusterIP Service")
			Eventually(func(g Gomega) {
				var dep appsv1.Deployment
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterDeploymentName("multi"),
				}, &dep)).To(Succeed())
				g.Expect(*dep.Spec.Replicas).To(Equal(int32(2)))

				var svc corev1.Service
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterServiceName("multi"),
				}, &svc)).To(Succeed())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
				g.Expect(svc.Spec.ClusterIP).NotTo(Equal(corev1.ClusterIPNone))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})

		It("router autoscale creates router HPA when enabled", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "autoscale", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
					},
					Router: &postgresv1alpha1.RouterSpec{
						Enabled:  true,
						Replicas: 2,
						Autoscale: &postgresv1alpha1.RouterAutoscaleSpec{
							Enabled:     true,
							MinReplicas: 2,
							MaxReplicas: 5,
							TargetCPU:   60,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			Eventually(func(g Gomega) {
				var hpa autoscalingv2.HorizontalPodAutoscaler
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterHPAName("autoscale"),
				}, &hpa)).To(Succeed())
				g.Expect(hpa.Spec.ScaleTargetRef.Kind).To(Equal("Deployment"))
				g.Expect(hpa.Spec.ScaleTargetRef.Name).To(Equal(RouterDeploymentName("autoscale")))
				g.Expect(hpa.Spec.MinReplicas).NotTo(BeNil())
				g.Expect(*hpa.Spec.MinReplicas).To(Equal(int32(2)))
				g.Expect(hpa.Spec.MaxReplicas).To(Equal(int32(5)))
				g.Expect(hpa.Spec.Metrics[0].Resource.Target.AverageUtilization).NotTo(BeNil())
				g.Expect(*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization).To(Equal(int32(60)))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})

		It("router autoscale deletes router HPA when disabled", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "autoscale-off", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
					},
					Router: &postgresv1alpha1.RouterSpec{
						Enabled:  true,
						Replicas: 2,
						Autoscale: &postgresv1alpha1.RouterAutoscaleSpec{
							Enabled:     true,
							MinReplicas: 2,
							MaxReplicas: 5,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			Eventually(func(g Gomega) {
				var hpa autoscalingv2.HorizontalPodAutoscaler
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterHPAName("autoscale-off"),
				}, &hpa)).To(Succeed())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "autoscale-off"}, &got)).To(Succeed())
				got.Spec.Router.Autoscale.Enabled = false
				g.Expect(k8sClient.Update(ctx, &got)).To(Succeed())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				var hpa autoscalingv2.HorizontalPodAutoscaler
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterHPAName("autoscale-off"),
				}, &hpa)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})

		It("router autoscale preserves existing router Deployment replicas", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "autoscale-preserve", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
					},
					Router: &postgresv1alpha1.RouterSpec{
						Enabled:  true,
						Replicas: 2,
						Autoscale: &postgresv1alpha1.RouterAutoscaleSpec{
							Enabled:     true,
							MinReplicas: 2,
							MaxReplicas: 6,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			Eventually(func(g Gomega) {
				var dep appsv1.Deployment
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterDeploymentName("autoscale-preserve"),
				}, &dep)).To(Succeed())
				replicas := int32(4)
				dep.Spec.Replicas = &replicas
				g.Expect(k8sClient.Update(ctx, &dep)).To(Succeed())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			bumpAnnotation(ctx, cluster)

			Consistently(func(g Gomega) {
				var dep appsv1.Deployment
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: RouterDeploymentName("autoscale-preserve"),
				}, &dep)).To(Succeed())
				g.Expect(dep.Spec.Replicas).NotTo(BeNil())
				g.Expect(*dep.Spec.Replicas).To(Equal(int32(4)))
			}, 2*time.Second, envtestInterval).Should(Succeed())
		})

		It("adds active named reshard targets to cluster shard status", func() {
			clusterName := "named-status"
			keyspace := "default"
			targetShard := "t1"
			targetPod := TargetShardStatefulSetName(clusterName, targetShard) + "-0"
			targetService := TargetShardServiceName(clusterName, targetShard)
			targetEndpoint := fmt.Sprintf("%s.%s.%s.svc.cluster.local:5432", targetPod, targetService, namespace)

			raw, err := json.Marshal(statusapi.Status{
				Role:       statusapi.RolePrimary,
				Ready:      true,
				Endpoint:   targetEndpoint,
				LastUpdate: time.Now().UTC(),
			})
			Expect(err).NotTo(HaveOccurred())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        targetPod,
					Namespace:   namespace,
					Labels:      ReshardTargetSelectorLabels(clusterName, targetShard),
					Annotations: map[string]string{statusapi.AnnotationKey: string(raw)},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: pgContainerName, Image: "postgres:18"}}},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: pgContainerName, Ready: true, Image: "postgres:18", ImageID: "postgres:18"}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
			Expect(k8sClient.Create(ctx, &postgresv1alpha1.ShardRange{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName + "-sr", Namespace: namespace},
				Spec: postgresv1alpha1.ShardRangeSpec{
					Cluster:  clusterName,
					Keyspace: keyspace,
					Vindex:   postgresv1alpha1.VindexSpec{Type: postgresv1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
					Ranges:   []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0xffffffff", Shard: targetShard}},
				},
			})).To(Succeed())

			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNative,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, BuildShardPDB(cluster, 0, 2))).To(Succeed())
			sourcePVC := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("data-%s-0", ShardStatefulSetName(clusterName, 0)),
					Namespace: namespace,
					Labels:    SelectorLabels(clusterName, "shard", 0),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sourcePVC)).To(Succeed())
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: clusterName}, &got)).To(Succeed())
				var named *postgresv1alpha1.ShardStatus
				var source *postgresv1alpha1.ShardStatus
				for i := range got.Status.Shards {
					if got.Status.Shards[i].Name == targetShard {
						named = &got.Status.Shards[i]
					}
					if got.Status.Shards[i].Name == "shard-0" {
						source = &got.Status.Shards[i]
					}
				}
				g.Expect(source).To(BeNil(), "inactive source shard must be excluded from active status topology")
				g.Expect(named).NotTo(BeNil(), "active ShardRange target must appear in status.shards")
				g.Expect(named.Ordinal).To(Equal(int32(-1)))
				g.Expect(named.Primary).NotTo(BeNil())
				g.Expect(named.Primary.Pod).To(Equal(targetPod))
				g.Expect(named.Primary.Endpoint).To(Equal(targetEndpoint))
				g.Expect(named.Primary.Ready).To(BeTrue())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseReady))

				var sourceSTS appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: ShardStatefulSetName(clusterName, 0),
				}, &sourceSTS)).To(Succeed())
				g.Expect(sourceSTS.Spec.Replicas).NotTo(BeNil())
				g.Expect(*sourceSTS.Spec.Replicas).To(Equal(int32(0)), "inactive source ordinal STS must scale to zero")

				var sourceSvc corev1.Service
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: ShardServiceName(clusterName, 0),
				}, &sourceSvc)).To(Succeed(), "inactive source Service is retained for conservative rollback/debug")

				var sourcePDB policyv1.PodDisruptionBudget
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: ShardPDBName(clusterName, 0),
				}, &sourcePDB)).To(Succeed(), "pre-existing source PDB is retained by default")

				var retainedPVC corev1.PersistentVolumeClaim
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: sourcePVC.Name,
				}, &retainedPVC)).To(Succeed(), "source PVC is retained by default")

				var targetSTS appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace, Name: TargetShardStatefulSetName(clusterName, targetShard),
				}, &targetSTS)).To(Succeed())
				g.Expect(targetSTS.Spec.Replicas).NotTo(BeNil())
				g.Expect(*targetSTS.Spec.Replicas).To(Equal(int32(2)), "active target STS must match cluster members")
				mainEnv := envMap(targetSTS.Spec.Template.Spec.Containers[0].Env)
				g.Expect(mainEnv["POSTGRES_MEMBER_COUNT"].Value).To(Equal("2"))
				g.Expect(mainEnv["PRIMARY_ENDPOINT"].Value).To(Equal(targetEndpoint))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when cnpg-compatible hibernation annotation is enabled", func() {
		It("scales database Pods to zero while keeping StatefulSet/PVC ownership and reports hibernation", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sleepy",
					Namespace: namespace,
					Annotations: map[string]string{
						AnnotationHibernation: "on",
					},
				},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("sleepy", 0)
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Replicas).NotTo(BeNil())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(0)))
				g.Expect(sts.Spec.VolumeClaimTemplates).To(HaveLen(1), "StatefulSet must retain PVC template while hibernated")

				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "sleepy"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseHibernated))
				cond := meta.FindStatusCondition(got.Status.Conditions, ConditionHibernation)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(Equal(ReasonHibernated))
				g.Expect(meta.FindStatusCondition(got.Status.Conditions, ConditionReady).Status).To(Equal(metav1.ConditionFalse))
				g.Expect(got.Status.Shards).To(HaveLen(1))
				g.Expect(got.Status.Shards[0].Primary).To(BeNil())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("rehydrating when annotation is set to off")
			Eventually(func() error {
				var got postgresv1alpha1.PostgresCluster
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &got); err != nil {
					return err
				}
				got.Annotations[AnnotationHibernation] = "off"
				return k8sClient.Update(ctx, &got)
			}, envtestTimeout, envtestInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Replicas).NotTo(BeNil())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(2)))

				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "sleepy"}, &got)).To(Succeed())
				cond := meta.FindStatusCondition(got.Status.Conditions, ConditionHibernation)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(ReasonNotHibernated))
				g.Expect(got.Status.Phase).NotTo(Equal(postgresv1alpha1.ClusterPhaseHibernated))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when offline restore is in progress", func() {
		It("keeps shard StatefulSets scaled to zero without reporting user hibernation", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore-freeze",
					Namespace: namespace,
					Annotations: map[string]string{
						"postgres.keiailab.io/restore-in-progress": "restore-bj",
					},
				},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("restore-freeze", 0)
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Replicas).NotTo(BeNil())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(0)))

				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "restore-freeze"}, &got)).To(Succeed())
				hibernation := meta.FindStatusCondition(got.Status.Conditions, ConditionHibernation)
				g.Expect(hibernation).NotTo(BeNil())
				g.Expect(hibernation.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(hibernation.Reason).To(Equal(ReasonNotHibernated))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when imageCatalogRef selects a runtime image", func() {
		It("uses the catalog image and rolls the StatefulSet when the catalog entry changes", func() {
			catalog := &postgresv1alpha1.ImageCatalog{
				ObjectMeta: metav1.ObjectMeta{Name: "postgresql", Namespace: namespace},
				Spec: postgresv1alpha1.ImageCatalogSpec{
					Images: []postgresv1alpha1.ImageCatalogEntry{{
						Major: 18,
						Image: "registry.local/postgres:18.1",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, catalog)).To(Succeed())

			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cataloged", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ImageCatalogRef: &postgresv1alpha1.ImageCatalogRef{
						APIGroup: "postgresql.cnpg.io",
						Kind:     "ImageCatalog",
						Name:     "postgresql",
						Major:    18,
					},
					ShardingMode: postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     1,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("cataloged", 0)
			var firstCatalogHash string
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.InitContainers).To(HaveLen(1))
				g.Expect(sts.Spec.Template.Spec.InitContainers[0].Image).To(Equal("registry.local/postgres:18.1"))
				g.Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.local/postgres:18.1"))
				firstCatalogHash = sts.Spec.Template.Annotations[postgresImageCatalogHashAnnotation]
				g.Expect(firstCatalogHash).NotTo(BeEmpty())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			By("updating the catalog entry")
			Eventually(func() error {
				var got postgresv1alpha1.ImageCatalog
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(catalog), &got); err != nil {
					return err
				}
				got.Spec.Images[0].Image = "registry.local/postgres:18.2"
				return k8sClient.Update(ctx, &got)
			}, envtestTimeout, envtestInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.InitContainers[0].Image).To(Equal("registry.local/postgres:18.2"))
				g.Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.local/postgres:18.2"))
				g.Expect(sts.Spec.Template.Annotations[postgresImageCatalogHashAnnotation]).NotTo(Equal(firstCatalogHash))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when configured as a standalone replica cluster", func() {
		It("bootstraps ordinal zero from the external source and disables local promotion", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					ExternalClusters: []postgresv1alpha1.ExternalClusterSpec{{
						Name: "primary-eu",
						ConnectionParameters: map[string]string{
							"host":    "primary-eu-rw.data.svc",
							"port":    "5432",
							"user":    "streaming_replica",
							"dbname":  "postgres",
							"sslmode": "prefer",
						},
						Password: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "primary-eu-password"},
							Key:                  "password",
						},
						SSLKey: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "primary-eu-replication"},
							Key:                  "tls.key",
						},
						SSLCert: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "primary-eu-replication"},
							Key:                  "tls.crt",
						},
						SSLRootCert: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "primary-eu-ca"},
							Key:                  "ca.crt",
						},
					}},
					Bootstrap: &postgresv1alpha1.BootstrapSpec{
						PgBaseBackup: &postgresv1alpha1.PgBaseBackupBootstrapSpec{
							Source: "primary-eu",
						},
					},
					Replica: &postgresv1alpha1.ReplicaClusterSpec{
						Enabled: true,
						Source:  "primary-eu",
					},
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     0,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			stsName := ShardStatefulSetName("replica", 0)
			Eventually(func(g Gomega) {
				var sts appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: stsName}, &sts)).To(Succeed())
				g.Expect(sts.Spec.Template.Spec.InitContainers).To(HaveLen(1))
				init := sts.Spec.Template.Spec.InitContainers[0]
				initEnv := envMap(init.Env)
				g.Expect(initEnv["PRIMARY_ENDPOINT"].Value).To(Equal("primary-eu-rw.data.svc:5432"))
				g.Expect(initEnv["PRIMARY_USER"].Value).To(Equal("streaming_replica"))
				g.Expect(initEnv["PRIMARY_DBNAME"].Value).To(Equal("postgres"))
				g.Expect(initEnv["PRIMARY_SSLMODE"].Value).To(Equal("prefer"))
				g.Expect(initEnv["REPLICA_CLUSTER_ENABLED"].Value).To(Equal("1"))
				g.Expect(initEnv["PRIMARY_PASSWORD"].ValueFrom.SecretKeyRef.Name).To(Equal("primary-eu-password"))
				g.Expect(initEnv["PRIMARY_PASSWORD"].ValueFrom.SecretKeyRef.Key).To(Equal("password"))
				g.Expect(initEnv["PRIMARY_SSLKEY_FILE"].Value).To(Equal("/etc/postgres-external/source/tls.key"))
				g.Expect(initEnv["PRIMARY_SSLCERT_FILE"].Value).To(Equal("/etc/postgres-external/source/tls.crt"))
				g.Expect(initEnv["PRIMARY_SSLROOTCERT_FILE"].Value).To(Equal("/etc/postgres-external/source/ca.crt"))
				g.Expect(init.Args).To(HaveLen(1))
				g.Expect(init.Args[0]).To(ContainSubstring(`REPLICA_CLUSTER_ENABLED`))
				g.Expect(init.Args[0]).To(ContainSubstring(`pg_basebackup`))
				g.Expect(init.Args[0]).To(ContainSubstring(`-d "$PRIMARY_CONNINFO"`))
				g.Expect(init.Args[0]).To(ContainSubstring(`passfile=/tmp/primary.pgpass`))
				g.Expect(init.Args[0]).To(ContainSubstring(`sslkey=/tmp/primary-client.key`))
				g.Expect(init.Args[0]).To(ContainSubstring(`sslcert=/tmp/primary-client.crt`))
				g.Expect(init.Args[0]).To(ContainSubstring(`sslrootcert=/tmp/primary-root.crt`))
				g.Expect(init.Args[0]).To(ContainSubstring(`standby.signal`))
				g.Expect(init.VolumeMounts).To(ContainElement(corev1.VolumeMount{
					Name:      "external-cluster-credentials",
					MountPath: "/etc/postgres-external/source",
					ReadOnly:  true,
				}))

				mainEnv := envMap(sts.Spec.Template.Spec.Containers[0].Env)
				g.Expect(mainEnv["PRIMARY_ENDPOINT"].Value).To(Equal("primary-eu-rw.data.svc:5432"))
				g.Expect(mainEnv["POSTGRES_REPLICA_CLUSTER"].Value).To(Equal("standalone"))

				var credentialVolume *corev1.Volume
				for i := range sts.Spec.Template.Spec.Volumes {
					if sts.Spec.Template.Spec.Volumes[i].Name == "external-cluster-credentials" {
						credentialVolume = &sts.Spec.Template.Spec.Volumes[i]
					}
				}
				g.Expect(credentialVolume).NotTo(BeNil())
				g.Expect(credentialVolume.Projected).NotTo(BeNil())
				g.Expect(credentialVolume.Projected.Sources).To(HaveLen(3))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})

		It("rejects an invalid external source before creating database pods", func() {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "bad-replica", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					ExternalClusters: []postgresv1alpha1.ExternalClusterSpec{{
						Name: "primary-eu",
						ConnectionParameters: map[string]string{
							"port": "5432",
						},
					}},
					Bootstrap: &postgresv1alpha1.BootstrapSpec{
						PgBaseBackup: &postgresv1alpha1.PgBaseBackupBootstrapSpec{
							Source: "primary-eu",
						},
					},
					Replica: &postgresv1alpha1.ReplicaClusterSpec{
						Enabled: true,
						Source:  "primary-eu",
					},
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     0,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "bad-replica"}, &got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(postgresv1alpha1.ClusterPhaseDegraded))
				g.Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))

				ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
				g.Expect(ready).NotTo(BeNil())
				g.Expect(ready.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal(ReasonReplicaClusterRejected))
				g.Expect(ready.Message).To(ContainSubstring("connectionParameters.host is required"))

				progressing := meta.FindStatusCondition(got.Status.Conditions, ConditionProgressing)
				g.Expect(progressing).NotTo(BeNil())
				g.Expect(progressing.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(progressing.Reason).To(Equal(ReasonReplicaClusterRejected))

				var sts appsv1.StatefulSet
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: namespace,
					Name:      ShardStatefulSetName("bad-replica", 0),
				}, &sts)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})

	Context("when PostgresUser CRs target the cluster", func() {
		It("publishes managedRolesStatus on the owning PostgresCluster", func() {
			user := newManagedRoleUser("app", "roles")
			user.Namespace = namespace
			Expect(k8sClient.Create(ctx, user)).To(Succeed())
			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresUser
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(user), &got)).To(Succeed())
				got.Status.Applied = true
				got.Status.ObservedGeneration = got.Generation
				got.Status.PasswordSecretResourceVersion = "rv-envtest"
				g.Expect(k8sClient.Status().Update(ctx, &got)).To(Succeed())
			}, envtestTimeout, envtestInterval).Should(Succeed())

			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "roles", Namespace: namespace},
				Spec: postgresv1alpha1.PostgresClusterSpec{
					PostgresVersion: "18",
					ShardingMode:    postgresv1alpha1.ShardingModeNone,
					Shards: postgresv1alpha1.ShardsSpec{
						InitialCount: 1,
						Replicas:     0,
						Storage: postgresv1alpha1.StorageSpec{
							Size: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

			Eventually(func(g Gomega) {
				var got postgresv1alpha1.PostgresCluster
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "roles"}, &got)).To(Succeed())
				g.Expect(got.Status.ManagedRolesStatus).NotTo(BeNil())
				g.Expect(got.Status.ManagedRolesStatus.ByStatus["reconciled"]).To(ContainElement("app"))
				g.Expect(got.Status.ManagedRolesStatus.ByStatus["reserved"]).To(ContainElements("postgres", "streaming_replica"))
				g.Expect(got.Status.ManagedRolesStatus.PasswordStatus["app"].SecretResourceVersion).To(Equal("rv-envtest"))
			}, envtestTimeout, envtestInterval).Should(Succeed())
		})
	})
})

func envMap(env []corev1.EnvVar) map[string]corev1.EnvVar {
	out := make(map[string]corev1.EnvVar, len(env))
	for _, item := range env {
		out[item.Name] = item
	}
	return out
}

// markSTSReady 는 envtest 에서 부재한 STS controller 를 흉내내어 readyReplicas 를
// 강제로 설정한다. status subresource 라 별도 Update 호출이 필요하다.
func markSTSReady(ctx context.Context, ns, name string, ready int32) {
	GinkgoHelper()
	var sts appsv1.StatefulSet
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sts)).To(Succeed())
	sts.Status.Replicas = ready
	sts.Status.ReadyReplicas = ready
	sts.Status.AvailableReplicas = ready
	Expect(k8sClient.Status().Update(ctx, &sts)).To(Succeed())
}

// bumpAnnotation 은 reconcile 를 재트리거하기 위해 spec 외부의 annotation 을
// 갱신한다 (status 변경만으로는 reconcile 가 항상 트리거되지는 않음).
func bumpAnnotation(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster) {
	GinkgoHelper()
	Eventually(func() error {
		var got postgresv1alpha1.PostgresCluster
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), &got); err != nil {
			return err
		}
		if got.Annotations == nil {
			got.Annotations = map[string]string{}
		}
		got.Annotations["postgres.keiailab.io/test-bump"] = fmt.Sprintf("%d", time.Now().UnixNano())
		return k8sClient.Update(ctx, &got)
	}, envtestTimeout, envtestInterval).Should(Or(Succeed(), MatchError(ContainSubstring("conflict"))))
	// conflict 발생해도 reconcile 트리거 목적은 달성됐다 — 다른 reconcile 가 spec 을
	// 이미 건드렸다는 뜻이라 watch event 가 이미 흘러갔다.
}
