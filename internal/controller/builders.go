/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/keiailab/keiailab-commons/pkg/probes"
	"github.com/keiailab/keiailab-commons/pkg/security"
	commonstopology "github.com/keiailab/keiailab-commons/pkg/topology"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// 본 파일은 PostgresCluster CR로부터 K8s 하위 자원(StatefulSet, Service,
// ConfigMap, Deployment)의 desired state를 생성하는 순수 함수들의 모음이다.
//
// 설계 원칙:
//   - 모든 함수는 입력에서 출력으로 결정적(stateless, side-effect 0).
//   - controllerutil.SetControllerReference 호출은 reconciler가 담당. 본 함수는
//     ObjectMeta까지만 채운다.
//   - 컨테이너 이미지 lookup은 internal/version/matrix.go의 결과만 사용한다.
//     본 파일에 imageRef:tag 하드코딩 금지.
//   - PostgreSQL 컨테이너 환경 변수, 볼륨 마운트, postgresql.conf의 세부
//     defaulting은 P1-M1 후속 작업에서 보강한다. 현재는 PG가 부팅 가능한 최소
//     스펙만 보장한다.

const (
	// pgContainerName은 PG 컨테이너의 식별자다. status 보고에서 동일 값을 참조.
	pgContainerName = "postgres"

	// bootstrapContainerName 은 init container (initdb 또는 pg_basebackup) 식별자.
	bootstrapContainerName = "bootstrap"

	// pgPort는 PostgreSQL의 표준 포트다.
	pgPort int32 = 5432

	// routerMetricsPort 는 pg-router 가 /metrics(Prometheus 텍스트, active-connection
	// 게이지)를 노출하는 HTTP 포트다. pg-router 의 PGROUTER_METRICS_ADDR 기본값과 정합.
	routerMetricsPort int32 = 9187

	// instanceProbePort 는 instance manager 의 healthz/readyz HTTP 포트.
	instanceProbePort int32 = 8080

	// pgDataMountPath는 PVC가 마운트되는 위치다.
	pgDataMountPath = "/var/lib/postgresql/data"

	// pgDataSubdir 는 PVC root 안 PGDATA subdir. lost+found 충돌 회피.
	pgDataSubdir = pgDataMountPath + "/pgdata"

	// pgConfigMountPath는 ConfigMap이 마운트되는 위치다.
	pgConfigMountPath = "/etc/postgres-operator/conf"

	// pgConfigFile / pgHbaFile 은 ConfigMap mount 안 파일 경로 (instance 의 BinDir/CmdLine 인자).
	pgConfigFile = pgConfigMountPath + "/postgresql.conf"
	pgHbaFile    = pgConfigMountPath + "/pg_hba.conf"

	// pgRunDir 는 Unix socket directory (peer auth). dataplaneEphemeralVolumeMounts 에서
	// emptyDir 로 마운트되며 instance 가 LocalDSN 에서 사용한다.
	pgRunDir = "/var/run/postgresql"

	// postgresConfigHashAnnotation 은 mounted postgresql.conf/pg_hba.conf 변경 시
	// StatefulSet template 을 바꿔 rolling reconcile 을 유도한다.
	postgresConfigHashAnnotation = "postgres.keiailab.io/postgres-config-sha256"

	// postgresImageCatalogHashAnnotation 은 ImageCatalog/ClusterImageCatalog 의 image
	// 선택값이 바뀔 때 StatefulSet template drift 를 운영자가 쉽게 추적하도록 남긴다.
	postgresImageCatalogHashAnnotation = "postgres.keiailab.io/postgres-image-catalog-sha256"

	externalClusterCredentialsVolumeName = "external-cluster-credentials"
	externalClusterCredentialsMountPath  = "/etc/postgres-external/source"

	// backupRepoMountPath 는 filesystem pgBackRest repo (#209) 위치다.
	// 별도 subPath mount는 kubelet이 root-owned 디렉터리를 만들 수 있어 non-root
	// postgres 컨테이너가 쓰지 못한다. 이미 writable인 data PVC 내부 경로를 쓴다.
	backupRepoMountPath   = pgDataMountPath + "/pgbackrest"
	primaryPGPassFile     = "/tmp/primary.pgpass"
	primaryClientKeyFile  = "/tmp/primary-client.key"
	primaryClientCertFile = "/tmp/primary-client.crt"
	primaryRootCertFile   = "/tmp/primary-root.crt"

	// primaryConninfoUser·DBName·SSLMode 는 HA standby primary_conninfo 의 기본값이다 — 부트스트랩
	// 스크립트와 복제 그래프 재지정(standbyConninfo)이 같은 값을 쓴다.
	primaryConninfoUser    = "postgres"
	primaryConninfoDBName  = "postgres"
	primaryConninfoSSLMode = "prefer"

	// postgresUserUID는 PostgreSQL 표준 postgres user의 UID/GID다.
	// ADR 0006에 의해 동결된 데이터플레인 Pod의 runAsUser/runAsGroup/fsGroup 기본값.
	postgresUserUID int64 = 70

	restartPrimaryAsStandbyMarker = ".keiailab-restart-primary-as-standby"

	// promotedPrimaryMarker 는 operator exec-promote (failover_promoter.go
	// postgresPromotionCommand) 가 승격된 pod 의 PGDATA 에 쓰는 durable marker 다.
	// 이게 존재하면 본 pod 은 *operator 가 승격한 primary* 이므로, 재시작 시
	// bootstrap init 이 stale PRIMARY_ENDPOINT 로 standby.signal 을 복원해 자신을
	// standby 로 강등(→ 옛 primary 로 pg_rewind → post-failover write 손실)하면 안 된다.
	// #220 failback: 승격 primary 는 절대 stale-env 로 standby 복원 금지.
	promotedPrimaryMarker = ".keiailab-promoted-primary"

	// reseedPendingMarker·reseedAppliedMarker 는 PVC 루트(PGDATA 밖)의 재시드 토큰 파일이다 —
	// PGDATA 를 비워도 남는다. pending 은 비우기 직전에, applied 는 basebackup 이 끝나면 쓴다(ADR-0031).
	reseedPendingMarker = ".keiailab-reseed-pending"
	reseedAppliedMarker = ".keiailab-reseed-applied"
)

// pgBinDir 는 base PG image 안 postgres binary 디렉터리. Dockerfile.pg 의
// postgres:${PG_MAJOR}-bookworm 표준 경로 (/usr/lib/postgresql/${PG_MAJOR}/bin).
func pgBinDir(pgMajor string) string {
	return "/usr/lib/postgresql/" + pgMajor + "/bin"
}

// ptrBool/ptrInt64는 외부 의존 없이 inline pointer를 만드는 헬퍼다.
// (K8s API의 *bool/*int64 필드용. k8s.io/utils/ptr import 회피로 SDK 의존 최소화.)
//
//nolint:modernize // helpers preserve typed callers (ptrBool(true) ≠ new(bool))
func ptrBool(b bool) *bool { return &b }

//nolint:modernize // helpers preserve typed callers (ptrInt64(70) ≠ new(int64))
func ptrInt64(i int64) *int64 { return &i }

// storageClassPtr 는 빈 문자열이면 nil (클러스터 default), 아니면 ptr 을 반환한다.
// PVC.StorageClassName 의미: nil = default class, "" = no class, "<name>" = explicit.
// 우리는 빈 문자열을 "default 사용" 으로 해석한다.
func storageClassPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// dataplanePodSecurityContext는 데이터플레인 Pod(PG StatefulSet, Router Deployment)
// 의 PodSecurityContext 기본값을 반환한다. ADR 0006 §결정에 의해 동결.
//
// 구성:
//   - runAsNonRoot=true (root 거부)
//   - runAsUser/Group/FSGroup=70 (PG postgres user)
//   - seccompProfile=RuntimeDefault (커널 syscall 화이트리스트)
//
// 사용자 override는 향후 PostgresCluster.Spec.SecurityContext 필드 + webhook에서
// 처리한다(ADR 0006 §트레이드오프). 현 시점은 *opt-out 강제* — 운영자가 잊으면
// root 가능 상태로 떨어지지 않도록 default를 항상 강제한다.
func dataplanePodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptrBool(true),             //nolint:modernize // typed-value pointer required
		RunAsUser:    ptrInt64(postgresUserUID), //nolint:modernize
		RunAsGroup:   ptrInt64(postgresUserUID), //nolint:modernize
		FSGroup:      ptrInt64(postgresUserUID), //nolint:modernize
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// dataplaneContainerSecurityContext는 데이터플레인 Container의 SecurityContext
// 기본값을 반환한다.
//
// 구성 (commons.RestrictedContainer 기반 — PodSecurity restricted invariant):
//   - allowPrivilegeEscalation=false (suid/setuid 비활성, commons 가드)
//   - readOnlyRootFilesystem=true (공급망 공격 완화, postgres-specific)
//   - capabilities.drop=[ALL] (commons 가드)
//   - seccompProfile.type=RuntimeDefault (commons 가드, iteration 8 강화)
//   - runAsNonRoot=true (commons 가드, iteration 8 강화)
//
// readOnlyRootFilesystem 동반: PG가 /tmp, /run, /var/run/postgresql에 socket/lock
// 작성하므로 emptyDir mount 3개 추가(dataplaneEphemeralVolumeMounts/Volumes).
//
// iteration 8 (2026-05-07): keiailab-commons/pkg/security 위임 — 3 operator 공통
// PodSecurity restricted invariant 단일 진실원. 이전에는 SeccompProfile + RunAsNonRoot
// 가 container-level 에서 누락되어 Pod-level inherit 에 의존. 이제 명시.
func dataplaneContainerSecurityContext() *corev1.SecurityContext {
	return security.RestrictedContainer(security.WithReadOnlyRootFilesystem(true))
}

// dataplaneEphemeralVolumeMounts는 readOnlyRootFilesystem=true 동반에 필요한
// 쓰기 가능 mount point들을 반환한다(/tmp, /run, /var/run/postgresql).
func dataplaneEphemeralVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "ephemeral-tmp", MountPath: "/tmp"},
		{Name: "ephemeral-run", MountPath: "/run"},
		{Name: "ephemeral-pg-run", MountPath: "/var/run/postgresql"},
		{Name: "ephemeral-pgbackrest-spool", MountPath: "/var/spool/pgbackrest"},
	}
}

// dataplaneEphemeralVolumes는 dataplaneEphemeralVolumeMounts와 짝이 되는
// emptyDir Volume 정의를 반환한다.
func dataplaneEphemeralVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "ephemeral-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-pg-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-pgbackrest-spool", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}

func externalClusterCredentialEnv(config *replicaBootstrapConfig) []corev1.EnvVar {
	if config == nil {
		return nil
	}
	env := []corev1.EnvVar{}
	if secretKeySelectorConfigured(config.Password) {
		env = append(env, corev1.EnvVar{
			Name: "PRIMARY_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: config.Password,
			},
		})
	}
	if secretKeySelectorConfigured(config.SSLKey) {
		env = append(env, corev1.EnvVar{Name: "PRIMARY_SSLKEY_FILE", Value: externalClusterCredentialsMountPath + "/tls.key"})
	}
	if secretKeySelectorConfigured(config.SSLCert) {
		env = append(env, corev1.EnvVar{Name: "PRIMARY_SSLCERT_FILE", Value: externalClusterCredentialsMountPath + "/tls.crt"})
	}
	if secretKeySelectorConfigured(config.SSLRootCert) {
		env = append(env, corev1.EnvVar{Name: "PRIMARY_SSLROOTCERT_FILE", Value: externalClusterCredentialsMountPath + "/ca.crt"})
	}
	return env
}

func externalClusterCredentialVolumeMounts(config *replicaBootstrapConfig) []corev1.VolumeMount {
	if !externalClusterTLSConfigured(config) {
		return nil
	}
	return []corev1.VolumeMount{{
		Name:      externalClusterCredentialsVolumeName,
		MountPath: externalClusterCredentialsMountPath,
		ReadOnly:  true,
	}}
}

func externalClusterCredentialVolumes(config *replicaBootstrapConfig) []corev1.Volume {
	if !externalClusterTLSConfigured(config) {
		return nil
	}
	mode := int32(0o444)
	sources := []corev1.VolumeProjection{}
	if secretKeySelectorConfigured(config.SSLKey) {
		sources = append(sources, externalClusterSecretProjection(config.SSLKey, "tls.key"))
	}
	if secretKeySelectorConfigured(config.SSLCert) {
		sources = append(sources, externalClusterSecretProjection(config.SSLCert, "tls.crt"))
	}
	if secretKeySelectorConfigured(config.SSLRootCert) {
		sources = append(sources, externalClusterSecretProjection(config.SSLRootCert, "ca.crt"))
	}
	return []corev1.Volume{{
		Name: externalClusterCredentialsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: &mode,
				Sources:     sources,
			},
		},
	}}
}

func externalClusterSecretProjection(ref *corev1.SecretKeySelector, path string) corev1.VolumeProjection {
	return corev1.VolumeProjection{
		Secret: &corev1.SecretProjection{
			LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
			Items: []corev1.KeyToPath{{
				Key:  ref.Key,
				Path: path,
			}},
		},
	}
}

func externalClusterTLSConfigured(config *replicaBootstrapConfig) bool {
	return config != nil &&
		(secretKeySelectorConfigured(config.SSLKey) ||
			secretKeySelectorConfigured(config.SSLCert) ||
			secretKeySelectorConfigured(config.SSLRootCert))
}

func secretKeySelectorConfigured(ref *corev1.SecretKeySelector) bool {
	return ref != nil && ref.Name != "" && ref.Key != ""
}

// renderSharedPreloadLibraries는 enabledNames 에 매칭되는 ExtensionPlugin 만
// 우선순위 순으로 직렬화하여 shared_preload_libraries 값을 만든다 (RFC 0006 R1).
//
// 우선순위는 Registry.EnabledExtensions 가 SharedPreloadOrder 오름차순으로 정렬해
// 반환하는 것으로 보장된다 (ADR 0005). 본 함수는 그 결과를 콤마로 join.
//
// enabledNames 가 비어있거나 reg 가 nil 이면 빈 문자열 반환 — ConfigMap 에서
// shared_preload_libraries 라인 생략 (vanilla PG 부팅 보장).
func renderSharedPreloadLibraries(reg *plugin.Registry, enabledNames []string) string {
	if reg == nil || len(enabledNames) == 0 {
		return ""
	}
	exts, _ := reg.EnabledExtensions(enabledNames)
	names := make([]string, 0, len(exts))
	for _, e := range exts {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

type synchronousPostgresConfig struct {
	Method       string
	Number       int32
	StandbyNames []string
}

// renderPostgresConf는 postgresql.conf의 본문을 생성한다 (RFC 0006 R1 — per-cluster
// extension list). tuning 은 spec 에서 온 설정이다(ADR-0035 — 값이 없으면 줄도 없다).
func renderPostgresConf(
	reg *plugin.Registry,
	enabledExtensions []string,
	tlsOn bool,
	syncConfig *synchronousPostgresConfig,
	archiveConfig *archivePostgresConfig,
	tuning pgTuning,
) string {
	var sb strings.Builder
	sb.WriteString("# Generated by keiailab-postgres-operator. Do not edit by hand.\n")
	sb.WriteString("listen_addresses = '*'\n")
	sb.WriteString("port = 5432\n")
	// Unix socket 위치 — instance manager 의 LocalDSN 이 본 경로에 의존.
	fmt.Fprintf(&sb, "unix_socket_directories = '%s'\n", pgRunDir)
	// WAL + replication 기본값. logical: 물리 streaming replication(HA)의 상위집합이라
	// replicas HA 와 호환되며, online resharding 의 CDC 증분 catch-up(논리복제 subscription)
	// 을 가능케 한다. 약간의 WAL 증가가 있으나 분산 SQL(resharding) 제품엔 필수.
	sb.WriteString("wal_level = logical\n")
	// pg_rewind 전제. data checksums 없는 기존 스토리지에서도 failover 후
	// former primary 를 current primary timeline 으로 되감을 수 있게 한다.
	sb.WriteString("wal_log_hints = on\n")
	sb.WriteString("max_wal_senders = 10\n")
	sb.WriteString("max_replication_slots = 10\n")
	sb.WriteString("hot_standby = on\n")
	tuning.writeTo(&sb)
	if spl := renderSharedPreloadLibraries(reg, enabledExtensions); spl != "" {
		fmt.Fprintf(&sb, "shared_preload_libraries = '%s'\n", spl)
	}
	if syncConfig != nil && syncConfig.Number > 0 && len(syncConfig.StandbyNames) > 0 {
		fmt.Fprintf(&sb, "synchronous_standby_names = '%s %d (%s)'\n",
			syncConfig.Method,
			syncConfig.Number,
			strings.Join(quoteSynchronousStandbyNames(syncConfig.StandbyNames), ","),
		)
		sb.WriteString("synchronous_commit = on\n")
	}
	// Pillar P7 §7 Phase 3b: TLS server cert 활성. cert-manager Certificate (Phase 2)
	// 가 발급한 Secret 이 STS volume mount (Phase 3a) 로 /etc/ssl/postgres 경로에
	// tls.crt + tls.key + ca.crt 형태로 노출. 본 conditional 은 ssl=on + 경로 명시.
	if archiveConfig != nil && archiveConfig.Enabled {
		sb.WriteString("archive_mode = on\n")
		fmt.Fprintf(&sb, "archive_command = '%s'\n", archiveConfig.Command)
		sb.WriteString("archive_timeout = 60\n")
	}
	if tlsOn {
		sb.WriteString("ssl = on\n")
		fmt.Fprintf(&sb, "ssl_cert_file = '%s/tls.crt'\n", pgTLSMountPath)
		fmt.Fprintf(&sb, "ssl_key_file = '%s/tls.key'\n", pgTLSMountPath)
		fmt.Fprintf(&sb, "ssl_ca_file = '%s/ca.crt'\n", pgTLSMountPath)
		sb.WriteString("ssl_min_protocol_version = 'TLSv1.2'\n")
	}
	return sb.String()
}

const (
	// sharedBuffersDivisor 는 shared_buffers = limits.memory ÷ 4 다 — PG 문서 25%(부록 B-2).
	sharedBuffersDivisor = 4
	// effectiveCacheRatioNum/Den 은 effective_cache_size = limits.memory × 3/4 다 — PG 문서 50–75% 의 상단.
	effectiveCacheRatioNum = 3
	effectiveCacheRatioDen = 4
	// mebibyte 는 렌더 단위(PG 의 MB = 1024²)다.
	mebibyte int64 = 1 << 20
)

// pgTuning 은 spec 에서 렌더하는 PG 설정이다(ADR-0035) — 0 은 줄 없음(PG 기본값)이다.
type pgTuning struct {
	WALKeepSizeMB        int64
	SharedBuffersMB      int64
	EffectiveCacheSizeMB int64
	MaxConnections       int32
}

// tuningForCluster 는 CR 의 PG 설정이다(부록 B-2, 예: limits.memory 8Gi · walKeepSize 5Gi):
//
//	shared_buffers       = limits.memory ÷ 4       → 2048MB
//	effective_cache_size = limits.memory × 3/4     → 6144MB
//	wal_keep_size        = walKeepSize(MiB 올림)    → 5120MB — 보존은 요청보다 줄이지 않는다
//	max_connections      = maxConnections
func tuningForCluster(cluster *postgresv1alpha1.PostgresCluster) pgTuning {
	var tuning pgTuning
	if limit, ok := cluster.Spec.Shards.Resources.Limits[corev1.ResourceMemory]; ok {
		limitMB := limit.Value() / mebibyte
		tuning.SharedBuffersMB = limitMB / sharedBuffersDivisor
		tuning.EffectiveCacheSizeMB = limitMB * effectiveCacheRatioNum / effectiveCacheRatioDen
	}

	pg := cluster.Spec.PostgreSQL
	if pg == nil {
		return tuning
	}
	if pg.WALKeepSize != nil {
		size := pg.WALKeepSize.Value()
		tuning.WALKeepSizeMB = size / mebibyte
		if size%mebibyte > 0 {
			tuning.WALKeepSizeMB++
		}
	}
	if pg.MaxConnections != nil {
		tuning.MaxConnections = *pg.MaxConnections
	}
	return tuning
}

// writeTo 는 설정 줄을 쓴다 — 값이 없는 칸은 줄도 없다.
func (t pgTuning) writeTo(sb *strings.Builder) {
	if t.SharedBuffersMB > 0 {
		fmt.Fprintf(sb, "shared_buffers = '%dMB'\n", t.SharedBuffersMB)
	}
	if t.EffectiveCacheSizeMB > 0 {
		fmt.Fprintf(sb, "effective_cache_size = '%dMB'\n", t.EffectiveCacheSizeMB)
	}
	if t.WALKeepSizeMB > 0 {
		fmt.Fprintf(sb, "wal_keep_size = '%dMB'\n", t.WALKeepSizeMB)
	}
	if t.MaxConnections > 0 {
		fmt.Fprintf(sb, "max_connections = %d\n", t.MaxConnections)
	}
}

type archivePostgresConfig struct {
	Enabled bool
	Command string
}

func archiveConfigForCluster(cluster *postgresv1alpha1.PostgresCluster) *archivePostgresConfig {
	if cluster.Spec.Backup == nil || !cluster.Spec.Backup.Enabled {
		return nil
	}
	stanza := cluster.Name
	// #209: pgBackRest needs a configured repository or every archive-push/backup
	// fails immediately. For a filesystem repo, pass repo config inline via env
	// (repo1-type=posix, repo1-path) and create the stanza on first push
	// (idempotent), so WAL archiving lands in the repo. Non-filesystem repos
	// (s3/gcs/azure) are future work.
	repoPath := backupRepoMountPath
	if repo := cluster.Spec.Backup.Repo; repo != nil && repo.Path != "" {
		repoPath = sanitizeBackupRepoPath(repo.Path)
	}
	repoEnv := fmt.Sprintf("PGBACKREST_REPO1_TYPE=posix PGBACKREST_REPO1_PATH=%s", repoPath)
	// archive_command 는 postgresql.conf 에 `archive_command = '<cmd>'` 로 single-quote
	// 감싸 렌더되므로 (renderPostgresConfig line ~340), cmd 자체에 single quote 를 쓰면
	// conf 파싱이 깨진다 (FATAL: configuration file contains errors). double-quote
	// wrapper 로 single quote 를 회피한다 — repoPath 는 sanitizeBackupRepoPath 로
	// 검증되어 double quote/$/백틱 등 주입 문자가 없다.
	// `exec VAR=val cmd` 는 POSIX 에서 VAR=val 을 실행 파일로 오인한다 (exec 는 special
	// builtin → env 할당 prefix 불가, "exec: VAR=val: not found"). `env` 명령으로 감싸
	// 변수 설정 후 pgbackrest 를 exec 한다 (라이브 sidecar exec 2026-06-04 회귀 fix).
	// stanza-create는 DB 접속 옵션이 필요하지만, archive-push는 해당 옵션을 받지 않는다.
	// WAL path는 PostgreSQL의 %p placeholder를 직접 전달해 shell positional argument
	// escape 문제를 피한다.
	archiveArgs := "--config=/dev/null --log-level-file=off --pg1-path=" + pgDataSubdir
	stanzaArgs := archiveArgs + " --pg1-user=postgres --pg1-database=postgres"
	cmd := fmt.Sprintf(
		`sh -c "env %s pgbackrest %s --stanza=%s stanza-create 2>/dev/null || true; exec env %s pgbackrest %s --stanza=%s archive-push \"%%p\""`,
		repoEnv, stanzaArgs, stanza, repoEnv, archiveArgs, stanza)
	return &archivePostgresConfig{
		Enabled: true,
		Command: cmd,
	}
}

// backupRepoPathPattern 은 filesystem repo 경로에 허용되는 문자 집합 (절대/상대 경로).
var backupRepoPathPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// sanitizeBackupRepoPath 는 사용자 제어 repo.Path 를 inline shell archive_command 에
// 안전하게 삽입하기 위해 filesystem 경로 문자만 허용한다. 따옴표·세미콜론·개행 등
// 위반 문자가 있으면 기본 mount path 로 fallback — shell injection 차단
// (repo.Path 는 PostgresCluster CRD 의 사용자 제어 필드).
func sanitizeBackupRepoPath(p string) string {
	if p == "" || !backupRepoPathPattern.MatchString(p) {
		return backupRepoMountPath
	}
	return p
}

func quoteSynchronousStandbyNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	return out
}

func synchronousConfigForShard(
	cluster *postgresv1alpha1.PostgresCluster,
	shardOrdinal int32,
) *synchronousPostgresConfig {
	if cluster == nil || shardOrdinal < 0 || cluster.Spec.PostgreSQL == nil ||
		cluster.Spec.PostgreSQL.Synchronous == nil {
		return nil
	}
	sync := cluster.Spec.PostgreSQL.Synchronous
	if sync.Number <= 0 || cluster.Spec.Shards.Replicas < sync.Number {
		return nil
	}

	method := "ANY"
	if sync.Method == postgresv1alpha1.SynchronousReplicationMethodFirst {
		method = "FIRST"
	}

	durability := sync.DataDurability
	if durability == "" {
		durability = postgresv1alpha1.SynchronousReplicationDataDurabilityRequired
	}

	names := requiredSynchronousStandbyNames(cluster, shardOrdinal)
	number := sync.Number
	if durability == postgresv1alpha1.SynchronousReplicationDataDurabilityPreferred {
		names = preferredSynchronousStandbyNames(cluster, shardOrdinal)
		if int32(len(names)) < number {
			number = int32(len(names))
		}
	}
	if number <= 0 || len(names) == 0 {
		return nil
	}
	return &synchronousPostgresConfig{
		Method:       method,
		Number:       number,
		StandbyNames: names,
	}
}

func requiredSynchronousStandbyNames(cluster *postgresv1alpha1.PostgresCluster, shardOrdinal int32) []string {
	desired := desiredShardPodNames(cluster.Name, shardOrdinal, cluster.Spec.Shards.Replicas, true)
	shard := shardStatusByOrdinal(cluster.Status.Shards, shardOrdinal)
	if shard == nil {
		return desired
	}

	var readyReplicas []string
	var unreadyReplicas []string
	for _, replica := range shard.Replicas {
		if replica.Pod == "" {
			continue
		}
		if replica.Ready {
			readyReplicas = append(readyReplicas, replica.Pod)
		} else {
			unreadyReplicas = append(unreadyReplicas, replica.Pod)
		}
	}
	sort.Strings(readyReplicas)
	sort.Strings(unreadyReplicas)

	seen := map[string]bool{}
	out := make([]string, 0, len(desired))
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range readyReplicas {
		add(name)
	}
	for _, name := range unreadyReplicas {
		add(name)
	}
	if shard.Primary != nil {
		add(shard.Primary.Pod)
	}
	for _, name := range desired {
		add(name)
	}
	return out
}

func preferredSynchronousStandbyNames(cluster *postgresv1alpha1.PostgresCluster, shardOrdinal int32) []string {
	shard := shardStatusByOrdinal(cluster.Status.Shards, shardOrdinal)
	if shard == nil {
		return nil
	}
	names := make([]string, 0, len(shard.Replicas))
	for _, replica := range shard.Replicas {
		if replica.Pod != "" && replica.Ready {
			names = append(names, replica.Pod)
		}
	}
	sort.Strings(names)
	return names
}

func shardStatusByOrdinal(shards []postgresv1alpha1.ShardStatus, ordinal int32) *postgresv1alpha1.ShardStatus {
	for i := range shards {
		if shards[i].Ordinal == ordinal {
			return &shards[i]
		}
	}
	return nil
}

func desiredShardPodNames(clusterName string, shardOrdinal, replicas int32, includePrimary bool) []string {
	first := int32(1)
	if includePrimary {
		first = 0
	}
	names := make([]string, 0, int(replicas)+1)
	stsName := ShardStatefulSetName(clusterName, shardOrdinal)
	for podOrdinal := first; podOrdinal <= replicas; podOrdinal++ {
		names = append(names, fmt.Sprintf("%s-%d", stsName, podOrdinal))
	}
	return names
}

// defaultHostAuthCIDRs 는 hostAuthCIDRs 가 비었을 때 TCP 로 받는 클라이언트 대역이다(사설 대역 셋).
var defaultHostAuthCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

const (
	// hbaLocalMethod 는 local 소켓 인증이다 — 오퍼레이터 exec 가 -U postgres 로 붙는다(컨테이너 사용자 매핑이
	// 없어 peer 는 전부 거부된다).
	hbaLocalMethod = "trust"
	// hbaHostMethod 는 TCP 인증이다 — 네트워크 대역은 어떤 것도 trust 하지 않는다(ADR-0034).
	hbaHostMethod = "scram-sha-256"
)

// renderPGHBAConf 는 pg_hba.conf 본문이다(ADR-0034). 비-local trust 는 없다 — 복제·pg_rewind·pg_basebackup 은
// superuser passfile(PGPASSFILE)로 scram 인증한다:
//
//	local   all          all                 trust
//	host    all          all       <CIDR>    scram-sha-256   (TLS 면 hostssl)
//	host    replication  postgres  <CIDR>    scram-sha-256
//
// cidrs 는 spec.postgresql.hostAuthCIDRs 다.
func renderPGHBAConf(tlsOn bool, cidrs []string) string {
	// TLS 면 클라이언트 줄은 hostssl(평문 접속 차단). replication 은 파드끼리라 host 그대로다.
	hostType := "host"
	if tlsOn {
		hostType = "hostssl"
	}
	ranges := hbaRanges(cidrs)

	var sb strings.Builder
	sb.WriteString("# Generated by keiailab-postgres-operator. Do not edit by hand.\n")
	sb.WriteString(hbaLine("# TYPE", "DATABASE", "USER", "ADDRESS", "METHOD"))
	sb.WriteString(hbaLine("local", "all", "all", "", hbaLocalMethod))
	for _, cidr := range ranges {
		sb.WriteString(hbaLine(hostType, "all", "all", cidr, hbaHostMethod))
	}
	for _, cidr := range ranges {
		sb.WriteString(hbaLine("host", "replication", superuserName, cidr, hbaHostMethod))
	}
	return sb.String()
}

// hbaLine 은 pg_hba.conf 한 줄이다(열 너비 고정).
func hbaLine(kind, database, user, address, method string) string {
	return fmt.Sprintf("%-7s %-15s %-15s %-23s %s\n", kind, database, user, address, method)
}

// hbaRanges 는 렌더할 클라이언트 대역이다 — 표준 표기로 고치고 못 읽는 항목은 뺀다(hba 파일 오류는 postgres
// 기동을 막는다). CRD 가 cidr 형식을 검증하므로 빼는 일은 없어야 한다. 남는 것이 없으면 사설 대역 셋이다.
func hbaRanges(cidrs []string) []string {
	out := make([]string, 0, len(cidrs))
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		out = append(out, network.String())
	}
	if len(out) == 0 {
		return defaultHostAuthCIDRs
	}
	return out
}

// hostAuthCIDRs 는 CR 의 TCP 클라이언트 대역이다(없으면 nil — 사설 대역 셋).
func hostAuthCIDRs(cluster *postgresv1alpha1.PostgresCluster) []string {
	if cluster.Spec.PostgreSQL == nil {
		return nil
	}
	return cluster.Spec.PostgreSQL.HostAuthCIDRs
}

// buildConfigMap은 shard/router 모두에서 동일 패턴으로 사용된다.
// 호출자가 name·role·shardOrdinal 을 정해 넘긴다 (router 의 경우 ordinal=-1).
//
// shard ConfigMap 에는 postgresql.conf + pg_hba.conf 둘 다 들어간다.
// router ConfigMap 은 router 가 PG runtime 이 아니므로 pg_hba 는 생략 가능하나,
// 동일 builder 사용 위해 포함 (router 가 무시).
func buildConfigMap(cluster *postgresv1alpha1.PostgresCluster, name, role string, shardOrdinal int32, reg *plugin.Registry) *corev1.ConfigMap {
	data := postgresConfigData(cluster, shardOrdinal, reg)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, role, shardOrdinal),
		},
		Data: data,
	}
}

func postgresConfigData(
	cluster *postgresv1alpha1.PostgresCluster,
	shardOrdinal int32,
	reg *plugin.Registry,
) map[string]string {
	return map[string]string{
		"postgresql.conf": renderPostgresConf(
			reg,
			cluster.Spec.Extensions,
			tlsEnabled(cluster),
			synchronousConfigForShard(cluster, shardOrdinal),
			archiveConfigForCluster(cluster),
			tuningForCluster(cluster),
		),
		"pg_hba.conf": renderPGHBAConf(tlsEnabled(cluster), hostAuthCIDRs(cluster)),
	}
}

func postgresConfigHash(data map[string]string) string {
	return sha256Hex(data["postgresql.conf"] + "\x00" + data["pg_hba.conf"])
}

// buildHeadlessService는 StatefulSet과 짝이 되는 ClusterIP=None Service를 만든다.
// 안정적 Pod DNS 제공이 목적이다 — shard 전용 (router 는 buildClientService 사용).
func buildHeadlessService(cluster *postgresv1alpha1.PostgresCluster, name, role string, shardOrdinal int32) *corev1.Service {
	labels := SelectorLabels(cluster.Name, role, shardOrdinal)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
			Ports: []corev1.ServicePort{{
				Name:       "postgres",
				Port:       pgPort,
				TargetPort: intstr.FromInt32(pgPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildClientService는 라우터의 진입점 Service(ClusterIP)다.
// router 는 shard 차원이 없으므로 SelectorLabels 에 ordinal=-1 을 전달한다.
func buildClientService(cluster *postgresv1alpha1.PostgresCluster, name, role string) *corev1.Service {
	labels := SelectorLabels(cluster.Name, role, -1)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "postgres",
				Port:       pgPort,
				TargetPort: intstr.FromInt32(pgPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildShardPrimaryService 는 shard 의 *현재 primary* 를 가리키는 ExternalName Service 를
// 만든다(§6 stable per-shard primary Service). externalHost 는 현재 primary Pod 의 안정
// DNS(host, 포트 제외)다. operator 가 failover 시 externalHost 를 갱신하면 이 이름을
// 참조하는 라우터/클라이언트가 새 primary 로 따라간다 — status polling 불요, DNS 만으로
// failover-follow.
//
// ExternalName 을 쓰는 이유: primary Pod 는 이미 shard headless Service 로 안정 per-pod
// DNS 를 가지므로, 그 DNS 로의 CNAME alias 만 operator 가 관리하면 된다(EndpointSlice/Pod
// IP 관리 불요 — 최소 surface). selector 가 없어 endpoint controller 와 경합하지 않는다.
func buildShardPrimaryService(cluster *postgresv1alpha1.PostgresCluster, name, externalHost string) *corev1.Service {
	labels := SelectorLabels(cluster.Name, "shard-primary", -1)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: externalHost,
		},
	}
}

// primaryEndpointHost 는 status 의 primary Endpoint("host:port")에서 host 만 뽑는다.
// 포트가 없으면 그대로 반환. 빈 문자열이면 빈 문자열.
func primaryEndpointHost(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		return endpoint[:i]
	}
	return endpoint
}

// buildInstanceServiceAccount 는 instance Pod 가 사용할 ServiceAccount 를 만든다.
// cluster 단위 단일 SA — 모든 shard Pod 가 공유 (namespace-scoped).
func buildInstanceServiceAccount(cluster *postgresv1alpha1.PostgresCluster) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InstanceServiceAccountName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "shard", -1),
		},
	}
}

// buildInstanceRole 는 instance manager 가 K8s API 호출에 필요한 최소 권한 Role.
//
// 권한 스펙 (RFC 0003 election + fencing 정확히 충족):
//   - coordination.k8s.io/leases: leaderelection (get/list/watch/create/update/patch/delete)
//   - core/persistentvolumeclaims: 자기 PVC 의 fence label patch (get/patch)
//   - core/events: instance 가 이벤트 송출 가능하도록 (create/patch — 선택적이나 운영 가시성)
func buildInstanceRole(cluster *postgresv1alpha1.PostgresCluster) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InstanceRoleName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "shard", -1),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch"},
			},
			// RFC 0006 R2 — instance manager 가 자기 Pod annotation 에
			// statusapi.Status 를 patch (status feedback channel).
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "patch"},
			},
		},
	}
}

// buildInstanceRoleBinding 는 ServiceAccount ↔ Role 결합 RoleBinding.
func buildInstanceRoleBinding(cluster *postgresv1alpha1.PostgresCluster) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InstanceRoleBindingName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "shard", -1),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      InstanceServiceAccountName(cluster.Name),
			Namespace: cluster.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     InstanceRoleName(cluster.Name),
		},
	}
}

// buildBootstrapContainer 는 PGDATA 가 비어 있을 때 initdb (first-cluster bootstrap)
// 또는 pg_basebackup (replica seeding from primary) 중 하나를 수행하는 init container.
//
// 결정 흐름:
//   - PG_VERSION 존재 → skip (재실행 안전)
//   - POD_ORDINAL=0 또는 PRIMARY_ENDPOINT 빈 값 → initdb
//   - 그 외 → pg_basebackup + standby.signal + primary_conninfo (postgresql.auto.conf)
//
// 분기 키는 *Pod ordinal* (StatefulSet 안에서 Pod 마다 다른 값) 이다. SHARD_ORDINAL
// 은 한 shard 의 모든 Pod 가 동일 PodTemplateSpec 을 공유하므로 같은 값을 받아
// pod 별 분기에 사용 불가 — RFC 0005 multi-shard 에서 lease 명명 등 다른 용도로
// 보존만 한다. POD_NAME 은 downward API (metadata.name) 로 주입되며 StatefulSet
// 의 ordinal-stable 명명 규약 (`<sts>-<ordinal>`) 에 따라 마지막 `-` 뒤가 ordinal.
//
// standby.signal 은 instance manager 가 leader election 결과에 따라 OnStartedLeading
// 에서 제거하고 OnStoppedLeading 에서 재생성한다 (RFC 0006 R3 Task A).
//
// PRIMARY_ENDPOINT · PROMOTION_EPOCH · CLUSTER_INITIALIZED · MEMBER_HOLD · RESEED_REQUESTS 는
// 런타임 ConfigMap(runtimeConfigMap) 에서 온다(ADR-0030·0031) — 승격이 파드 템플릿을 바꾸지 않는다.
func buildBootstrapContainer(
	image, pgMajor string,
	shardOrdinal int32,
	runtimeConfigMap string,
	members int32,
	replicaClusterEnabled bool,
	primaryUser string,
	primaryDBName string,
	primarySSLMode string,
	primaryCredentialConfig *replicaBootstrapConfig,
) corev1.Container {
	replicaClusterValue := "0"
	if replicaClusterEnabled {
		replicaClusterValue = "1"
	}
	env := slices.Concat(
		[]corev1.EnvVar{{Name: "SHARD_ORDINAL", Value: fmt.Sprintf("%d", shardOrdinal)}},
		primaryRuntimeEnv(runtimeConfigMap, primaryCredentialConfig, bootstrapRuntimeKeys...),
		[]corev1.EnvVar{
			{Name: "POSTGRES_MEMBER_COUNT", Value: fmt.Sprintf("%d", members)},
			{Name: "REPLICA_CLUSTER_ENABLED", Value: replicaClusterValue},
			{Name: "PRIMARY_USER", Value: primaryUser},
			{Name: "PRIMARY_DBNAME", Value: primaryDBName},
			{Name: "PRIMARY_SSLMODE", Value: primarySSLMode},
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
		},
		externalClusterCredentialEnv(primaryCredentialConfig),
	)
	return corev1.Container{
		Name:            bootstrapContainerName,
		Image:           image,
		Command:         []string{"sh", "-c"},
		Args:            []string{renderBootstrapScript(podBootstrapPaths(), pgBinDir(pgMajor))},
		Env:             env,
		SecurityContext: dataplaneContainerSecurityContext(),
		VolumeMounts: append(append([]corev1.VolumeMount{
			{Name: "data", MountPath: pgDataMountPath},
		}, dataplaneEphemeralVolumeMounts()...), externalClusterCredentialVolumeMounts(primaryCredentialConfig)...),
	}
}

// primaryRuntimeEnv 는 승격 상태 env 다.
//   - 복제 클러스터: 외부 원천 endpoint 인라인(로컬 승격 없음)
//   - 런타임 CM 있음: 같은 이름 키 참조(Optional=false)
//   - 그 밖(리샤드 타깃): PRIMARY_ENDPOINT 인라인 빈 문자열 — pod-0 initdb 로 빈 primary
func primaryRuntimeEnv(runtimeConfigMap string, replica *replicaBootstrapConfig, keys ...string) []corev1.EnvVar {
	if replica != nil {
		return []corev1.EnvVar{{Name: runtimeKeyPrimaryEndpoint, Value: replica.Endpoint}}
	}
	if runtimeConfigMap == "" {
		return []corev1.EnvVar{{Name: runtimeKeyPrimaryEndpoint, Value: ""}}
	}
	return runtimeEnvVars(runtimeConfigMap, keys...)
}

// passfileEnv 는 libpq passfile 경로 env 다 — 부트스트랩이 쓰는 superuser passfile.
func passfileEnv() corev1.EnvVar {
	return corev1.EnvVar{Name: pgPassFileEnv, Value: primaryPGPassFile}
}

// bootstrapAuthEnv 는 부트스트랩 init 의 superuser 자격 env 다 — Secret 비밀번호(없으면 파드가 뜨지 않는다)와
// passfile 경로. Secret 을 passfile 로 마운트하지 않는다(fsGroup 이 그룹 비트를 더해 libpq 가 무시한다).
func bootstrapAuthEnv(ref *corev1.LocalObjectReference) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 2)
	if ref != nil && ref.Name != "" {
		env = append(env, corev1.EnvVar{
			Name: superuserPasswordEnv,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: *ref,
				Key:                  superuserPasswordKey,
				Optional:             ptrBool(false), //nolint:modernize // typed-value pointer required
			}},
		})
	}
	return append(env, passfileEnv())
}

// bootstrapPaths 는 부트스트랩 스크립트가 만지는 경로다 — 시험은 임시 루트로 바꾼다.
type bootstrapPaths struct {
	Root       string // PVC 루트 — 재시드 토큰 파일
	Data       string
	PGPass     string
	ClientKey  string
	ClientCert string
	RootCert   string
}

// podBootstrapPaths 는 컨테이너 안 경로다.
func podBootstrapPaths() bootstrapPaths {
	return bootstrapPaths{
		Root:       pgDataMountPath,
		Data:       pgDataSubdir,
		PGPass:     primaryPGPassFile,
		ClientKey:  primaryClientKeyFile,
		ClientCert: primaryClientCertFile,
		RootCert:   primaryRootCertFile,
	}
}

// renderBootstrapScript 는 bootstrap init 스크립트다(설계 §3.6).
//
//	MEMBER_HOLD 에 자기    → exit 1 (파일 변화 0 — 재구축 엔진이 스냅숏을 뜨는 동안)
//	그 밖 모든 길          → 먼저 superuser passfile(*:*:*:postgres:<비밀번호>, 0600) — PGPASSFILE 이 가리킨다
//	재시드 토큰 ≠ applied  → 다른 primary 가 있을 때만 pending=token · PGDATA 비움, 없으면 exit 1
//	기존 PGDATA  standby.signal       → 승격 마커 rm + primary_conninfo 를 CM 값 한 줄로
//	             마커 epoch > CM epoch → primary 유지(#220)
//	             그 밖 다른 primary    → 마커 rm + standby.signal·conninfo·restart 마커
//	빈 PGDATA    다른 primary          → basebackup → standby.signal·conninfo → 마커 rm → applied=pending
//	             ord-0 ∧ 미초기화      → initdb (초기화된 클러스터·재시드 대기면 exit 1, I6)
//
// 복제 클러스터 분기는 그대로다. binDir 은 pg_basebackup·initdb 가 있는 곳이다.
func renderBootstrapScript(paths bootstrapPaths, binDir string) string {
	marker := `"$DATA/` + promotedPrimaryMarker + `"`
	return `set -eu
DATA="` + paths.Data + `"
PENDING="` + paths.Root + `/` + reseedPendingMarker + `"
APPLIED="` + paths.Root + `/` + reseedAppliedMarker + `"
PRIMARY_ENDPOINT="${PRIMARY_ENDPOINT:-}"
PROMOTION_EPOCH="$(printf '%s' "${PROMOTION_EPOCH:-}" | tr -cd '0-9')"
PROMOTION_EPOCH="${PROMOTION_EPOCH:-0}"
CLUSTER_INITIALIZED="${CLUSTER_INITIALIZED:-0}"
PRIMARY_USER="${PRIMARY_USER:-` + primaryConninfoUser + `}"
PRIMARY_DBNAME="${PRIMARY_DBNAME:-` + primaryConninfoDBName + `}"
PRIMARY_SSLMODE="${PRIMARY_SSLMODE:-` + primaryConninfoSSLMode + `}"
PRIMARY_PASSWORD="${PRIMARY_PASSWORD:-}"
PRIMARY_SSLKEY_FILE="${PRIMARY_SSLKEY_FILE:-}"
PRIMARY_SSLCERT_FILE="${PRIMARY_SSLCERT_FILE:-}"
PRIMARY_SSLROOTCERT_FILE="${PRIMARY_SSLROOTCERT_FILE:-}"
POD_ORDINAL="${POD_NAME##*-}"
MEMBER_COUNT="${POSTGRES_MEMBER_COUNT:-1}"
REPLICA_CLUSTER_ENABLED="${REPLICA_CLUSTER_ENABLED:-0}"
PRIMARY_HOST=""
PRIMARY_IS_SELF=0
if [ -n "$PRIMARY_ENDPOINT" ]; then
  PRIMARY_HOST="${PRIMARY_ENDPOINT%:*}"
  case "$PRIMARY_HOST" in
    "$POD_NAME"|"$POD_NAME".*) PRIMARY_IS_SELF=1 ;;
    *) PRIMARY_IS_SELF=0 ;;
  esac
fi

# A member on the hold list stops before touching any file (rebuild snapshot in progress).
case ",${MEMBER_HOLD:-}," in
  *",$POD_NAME,"*)
    echo "member held by operator" >&2
    exit 1
    ;;
esac

escape_pgpass() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/:/\\:/g'
}

# Superuser passfile for every libpq client of this pod (PGPASSFILE): replication, pg_rewind and
# pg_basebackup authenticate with scram — pg_hba trusts no network range. 0600 or libpq ignores it.
(
  umask 077
  printf '*:*:*:` + superuserName + `:%s\n' "$(escape_pgpass "${` + superuserPasswordEnv + `:-}")" > "` + paths.PGPass + `"
)
chmod 0600 "` + paths.PGPass + `"

prepare_primary_conninfo() {
  PRIMARY_PORT="${PRIMARY_ENDPOINT##*:}"
  PRIMARY_CONNINFO="host=$PRIMARY_HOST port=$PRIMARY_PORT user=$PRIMARY_USER dbname=$PRIMARY_DBNAME sslmode=$PRIMARY_SSLMODE application_name=$POD_NAME"
  if [ -n "$PRIMARY_PASSWORD" ]; then
    {
      printf '%s:' "$(escape_pgpass "$PRIMARY_HOST")"
      printf '%s:' "$(escape_pgpass "$PRIMARY_PORT")"
      printf '%s:' "$(escape_pgpass "$PRIMARY_DBNAME")"
      printf '%s:' "$(escape_pgpass "$PRIMARY_USER")"
      printf '%s\n' "$(escape_pgpass "$PRIMARY_PASSWORD")"
    } > "` + paths.PGPass + `"
    chmod 0600 "` + paths.PGPass + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO passfile=` + paths.PGPass + `"
  fi
  if [ -n "$PRIMARY_SSLKEY_FILE" ]; then
    cp "$PRIMARY_SSLKEY_FILE" "` + paths.ClientKey + `"
    chmod 0600 "` + paths.ClientKey + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslkey=` + paths.ClientKey + `"
  fi
  if [ -n "$PRIMARY_SSLCERT_FILE" ]; then
    cp "$PRIMARY_SSLCERT_FILE" "` + paths.ClientCert + `"
    chmod 0600 "` + paths.ClientCert + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslcert=` + paths.ClientCert + `"
  fi
  if [ -n "$PRIMARY_SSLROOTCERT_FILE" ]; then
    cp "$PRIMARY_SSLROOTCERT_FILE" "` + paths.RootCert + `"
    chmod 0600 "` + paths.RootCert + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslrootcert=` + paths.RootCert + `"
  fi
}

# Promotion epoch recorded in the promoted marker; an empty legacy marker or none is 0.
marker_epoch() {
  EPOCH="$(head -n 1 ` + marker + ` 2>/dev/null | tr -cd '0-9')"
  printf '%s' "${EPOCH:-0}"
}

# Rewrite every primary_conninfo line of postgresql.auto.conf as one line (no pile-up).
# Same-directory temp file then rename, so a crash never leaves a half-written file.
write_primary_conninfo() {
  AUTO="$DATA/postgresql.auto.conf"
  if [ -f "$AUTO" ]; then
    sed '/^[[:space:]]*primary_conninfo[[:space:]]*=/d' "$AUTO" > "$AUTO.keiailab-tmp"
  else
    : > "$AUTO.keiailab-tmp"
  fi
  printf "primary_conninfo = '%s'\n" "$PRIMARY_CONNINFO" >> "$AUTO.keiailab-tmp"
  chmod 0600 "$AUTO.keiailab-tmp"
  mv "$AUTO.keiailab-tmp" "$AUTO"
}

# Reseed token for this member (RESEED_REQUESTS = pod=token,...). A token not applied yet
# empties PGDATA for a fresh basebackup — only when another member is the primary.
RESEED_REQUESTS=",${RESEED_REQUESTS:-}"
RESEED_TOKEN=""
case "$RESEED_REQUESTS" in
  *",$POD_NAME="*)
    RESEED_TOKEN="${RESEED_REQUESTS#*,"$POD_NAME"=}"
    RESEED_TOKEN="${RESEED_TOKEN%%,*}"
    ;;
esac
if [ -n "$RESEED_TOKEN" ] && [ "$RESEED_TOKEN" != "$(cat "$APPLIED" 2>/dev/null || true)" ]; then
  if [ -z "$PRIMARY_HOST" ] || [ "$PRIMARY_IS_SELF" = "1" ]; then
    echo "reseed $RESEED_TOKEN refused: no other primary to copy from (PRIMARY_ENDPOINT='$PRIMARY_ENDPOINT')" >&2
    exit 1
  fi
  printf '%s\n' "$RESEED_TOKEN" > "$PENDING.tmp"
  mv "$PENDING.tmp" "$PENDING"
  rm -rf "$DATA"
  echo "reseed $RESEED_TOKEN: PGDATA emptied for a fresh basebackup"
fi

if [ -f "$DATA/PG_VERSION" ]; then
  chmod 0700 "$DATA"
  # iteration 35 fix (cluster postgres incident): empty postmaster.pid 정리.
  # postgres 의 graceful shutdown 실패 시 postmaster.pid 가 *0 byte* 로 남는
  # 흔적 (FATAL: lock file "postmaster.pid" is empty). 정상 running postgres
  # 의 postmaster.pid 는 non-empty (PID + epoch + ports) — -s 테스트로 *empty
  # 인 경우만* 제거하여 running instance 와 충돌 회피.
  if [ -f "$DATA/postmaster.pid" ] && [ ! -s "$DATA/postmaster.pid" ]; then
    rm -f "$DATA/postmaster.pid"
    echo "removed empty postmaster.pid (stale crash artifact)"
  fi
  # cycle 23 INC-0046 P19 ⑲ fix: non-empty stale postmaster.pid handling.
  # K3s ungraceful shutdown → postmaster.pid non-empty (PID + epoch + ports 보존)
  # → main postgres FATAL "lock file already exists" CrashLoop. /proc/$PID 검사로
  # non-alive 만 제거 (busybox 호환, kill -0 signal handling 차이 회피).
  if [ -f "$DATA/postmaster.pid" ] && [ -s "$DATA/postmaster.pid" ]; then
    STALE_PID=$(head -1 "$DATA/postmaster.pid" 2>/dev/null | tr -d "[:space:]")
    if [ -n "$STALE_PID" ] && [ ! -d "/proc/$STALE_PID" ]; then
      rm -f "$DATA/postmaster.pid"
      echo "removed stale postmaster.pid (PID $STALE_PID not alive in /proc)"
    fi
  fi
  if [ "$REPLICA_CLUSTER_ENABLED" = "1" ] && [ -n "$PRIMARY_HOST" ] && [ ! -f "$DATA/standby.signal" ]; then
    prepare_primary_conninfo
    touch "$DATA/standby.signal"
    printf "primary_conninfo = '%s'\n" "$PRIMARY_CONNINFO" >> "$DATA/postgresql.auto.conf"
    echo "existing PGDATA marked for standalone replica continuous recovery"
  elif [ "$REPLICA_CLUSTER_ENABLED" != "1" ] && [ -f "$DATA/standby.signal" ]; then
    # A standby never keeps the promoted marker (basebackup copies it). Its upstream is
    # the runtime ConfigMap primary, written as a single primary_conninfo line.
    rm -f ` + marker + `
    if [ -n "$PRIMARY_HOST" ] && [ "$PRIMARY_IS_SELF" = "0" ]; then
      prepare_primary_conninfo
      write_primary_conninfo
    fi
    echo "existing standby: promoted marker cleared; primary_conninfo follows the runtime ConfigMap"
  elif [ "$MEMBER_COUNT" -gt 1 ] && [ -n "$PRIMARY_HOST" ] && [ "$PRIMARY_IS_SELF" = "0" ] && [ ! -f "$DATA/standby.signal" ] && [ "$(marker_epoch)" -gt "$PROMOTION_EPOCH" ]; then
    # #220 failback: this pod was promoted after the epoch the runtime ConfigMap still
    # names (publish did not land yet). Restoring standby.signal here would demote the
    # real primary and pg_rewind it back to the old timeline, losing post-failover
    # writes. Keep it a primary; the operator fence stops an illegitimate primary.
    echo "promoted-primary marker epoch $(marker_epoch) > PROMOTION_EPOCH $PROMOTION_EPOCH; keeping primary (no standby.signal restore) — #220"
  elif [ "$MEMBER_COUNT" -gt 1 ] && [ -n "$PRIMARY_HOST" ] && [ "$PRIMARY_IS_SELF" = "0" ] && [ ! -f "$DATA/standby.signal" ]; then
    # split-brain fix (fix/ha-replica-standby-signal-restore): an HA replica whose
    # PGDATA is already initialized but has no standby.signal must boot as a *standby*,
    # not race the election as a Real elector. Restore standby.signal + primary_conninfo
    # *before* postgres starts so the T30 guard (cmd/instance: IsStandby → Follower)
    # observes a standby and never acquires the primary lease. The marker is still
    # emitted so the instance manager can pg_rewind on timeline divergence. Without
    # this both pods boot primary → split-brain (live RCA 2026-06-04, pg-e2e).
    # A promoted marker no newer than PROMOTION_EPOCH belongs to a superseded primary.
    rm -f ` + marker + `
    prepare_primary_conninfo
    touch "$DATA/standby.signal"
    write_primary_conninfo
    touch "$DATA/` + restartPrimaryAsStandbyMarker + `"
    echo "existing PGDATA in HA cluster has a different primary endpoint; standby.signal restored + marked for standby restart"
  fi
  echo "PGDATA already initialized at $DATA; permissions normalized; skipping bootstrap"
  exit 0
fi

# Replica cluster mode = ordinal zero is also seeded from external source and must
# stay in continuous recovery. Fail closed if the source endpoint is absent.
if [ "$REPLICA_CLUSTER_ENABLED" = "1" ]; then
  if [ -z "$PRIMARY_ENDPOINT" ]; then
    echo "replica cluster bootstrap requires PRIMARY_ENDPOINT" >&2
    exit 1
  fi
  prepare_primary_conninfo
  mkdir -p "$DATA"
  chmod 0700 "$DATA"
  ` + binDir + `/pg_basebackup -D "$DATA" -d "$PRIMARY_CONNINFO" --no-password --wal-method=stream --checkpoint=fast
  touch "$DATA/standby.signal"
  printf "primary_conninfo = '%s'\n" "$PRIMARY_CONNINFO" >> "$DATA/postgresql.auto.conf"
  echo "standalone replica pg_basebackup completed; standby.signal + primary_conninfo configured"
  exit 0
fi

# Bootstrap decision (deterministic, #221). PRIMARY_ENDPOINT is one shared value
# for every pod of the shard (runtime ConfigMap). A replica (ordinal != 0) must
# NEVER initdb — that produced an independent second primary with no
# standby.signal → split-brain (both pods read-write, no streaming). Decision:
#   1. live primary elsewhere (PRIMARY_ENDPOINT set, not self) → basebackup standby
#   2. ordinal 0 of a never-initialized cluster → initdb (the cluster seed)
#   3. ordinal 0 of an initialized cluster → fail closed (I6: never an empty primary)
#   4. replica with no usable primary yet → fail closed, let the StatefulSet retry
if [ -n "$PRIMARY_ENDPOINT" ] && [ "$PRIMARY_IS_SELF" = "0" ]; then
  prepare_primary_conninfo
  mkdir -p "$DATA"
  chmod 0700 "$DATA"
  ` + binDir + `/pg_basebackup -D "$DATA" -d "$PRIMARY_CONNINFO" --no-password --wal-method=stream --checkpoint=fast
  touch "$DATA/standby.signal"
  write_primary_conninfo
  rm -f ` + marker + `
  if [ -f "$PENDING" ]; then
    mv "$PENDING" "$APPLIED"
    echo "reseed $(cat "$APPLIED") applied"
  fi
  echo "pg_basebackup completed; standby.signal + primary_conninfo configured"
elif [ "$POD_ORDINAL" = "0" ] && [ "$CLUSTER_INITIALIZED" = "1" ]; then
  echo "cluster already initialized; $POD_NAME refuses initdb on empty PGDATA (PRIMARY_ENDPOINT='$PRIMARY_ENDPOINT')" >&2
  exit 1
elif [ "$POD_ORDINAL" = "0" ] && [ -f "$PENDING" ]; then
  echo "reseed $(cat "$PENDING") pending; $POD_NAME refuses initdb on empty PGDATA" >&2
  exit 1
elif [ "$POD_ORDINAL" = "0" ]; then
  mkdir -p "$DATA"
  chmod 0700 "$DATA"
  ` + binDir + `/initdb -D "$DATA" --auth-local=trust --auth-host=scram-sha-256 --username=postgres --encoding=UTF8 --locale=C
  echo "initdb completed at $DATA"
else
  echo "replica $POD_NAME has no usable primary endpoint yet (PRIMARY_ENDPOINT='$PRIMARY_ENDPOINT'); failing for StatefulSet retry to avoid split-brain initdb (#221)" >&2
  exit 1
fi
`
}

// buildInstanceEnv 는 instance manager (PID 1) 에 주입할 환경 변수 집합을 만든다.
// downward API + spec 매개변수 + current primary endpoint(런타임 CM 참조) + 고정 경로의 합산.
func buildInstanceEnv(
	clusterName string,
	serviceName string,
	shardOrdinal int32,
	pgMajor string,
	members int32,
	primaryEndpoint corev1.EnvVar,
	replicaClusterEnabled bool,
) []corev1.EnvVar {
	env := []corev1.EnvVar{
		// downward API — Pod / Namespace 식별자.
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{
			Name: "POD_UID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"},
			},
		},
		// spec 매개변수 — election lease 명명 + role 분기.
		{Name: "POSTGRES_CLUSTER", Value: clusterName},
		{Name: "POSTGRES_SERVICE_NAME", Value: serviceName},
		{Name: "POSTGRES_ROLE", Value: "shard"},
		{Name: "POSTGRES_SHARD_ORDINAL", Value: fmt.Sprintf("%d", shardOrdinal)},
		{Name: "POSTGRES_MEMBER_COUNT", Value: fmt.Sprintf("%d", members)},
		primaryEndpoint,
		// supervise.Config — image 안 표준 경로 + ConfigMap mount + Unix socket.
		{Name: "POSTGRES_BIN_DIR", Value: pgBinDir(pgMajor)},
		{Name: "POSTGRES_DATA_DIR", Value: pgDataSubdir},
		{Name: "POSTGRES_CONFIG_FILE", Value: pgConfigFile},
		{Name: "POSTGRES_HBA_FILE", Value: pgHbaFile},
		{Name: "POSTGRES_LOCAL_DSN", Value: "host=" + pgRunDir + " user=postgres dbname=postgres"},
	}
	if replicaClusterEnabled {
		env = append(env, corev1.EnvVar{Name: "POSTGRES_REPLICA_CLUSTER", Value: "standalone"})
	}
	return env
}

// buildPGStatefulSet은 단일 shard 의 StatefulSet desired state 를 만든다.
// RFC 0001 PostgresCluster CRD v2 모델에서 role 은 항상 "shard" 이며, shardOrdinal
// 은 0-based 값이다. members 는 primary 1 + async replica N 의 합산이다.
//
// 컨테이너 ENTRYPOINT 는 /usr/local/bin/instance (Dockerfile.pg). instance 가 PID 1
// 으로 동작하면서 buildInstanceEnv 의 env 를 읽어 postgres child 를 fork.
func buildPGStatefulSet(
	cluster *postgresv1alpha1.PostgresCluster,
	name, serviceName string,
	shardOrdinal int32,
	image, configMapName, pgMajor string,
	members int32,
	storage postgresv1alpha1.StorageSpec,
	resources corev1.ResourceRequirements,
	runtimeConfigMap string,
	configHash string,
	reshardTargetID string,
) *appsv1.StatefulSet {
	// reshardTargetID != "" → G3 online-resharding target shard (ADR-0027): ordinal
	// shard 모델과 *격리된* label 을 써서 aggregateShardStatus/failover 가 transient
	// target 을 라이브 shard 로 오인하지 않게 한다 (#220-class 차단). 빈 문자열이면
	// 기존 ordinal 경로와 byte-identical (모든 label 사용처에 동일 적용).
	labels := SelectorLabels(cluster.Name, "shard", shardOrdinal)
	if reshardTargetID != "" {
		labels = ReshardTargetSelectorLabels(cluster.Name, reshardTargetID)
	}
	// podLabels 는 셀렉터(labels)의 *superset* — ordinal shard 에 명명 식별 label `shard-id`
	// 를 부가한다(ADR-0029 P-A). 셀렉터(labels)에는 넣지 않아 기존 STS selector 불변 + 업그레이드
	// race 회피. reshard target 은 격리 유지(부가 안 함 — 승격 시 부여).
	podLabels := labels
	if reshardTargetID == "" {
		podLabels = make(map[string]string, len(labels)+1)
		maps.Copy(podLabels, labels)
		podLabels[ShardIDLabelKey] = ShardIDForOrdinal(shardOrdinal)
	}
	replicaConfig, _ := replicaBootstrapConfigForCluster(cluster)
	replicaClusterEnabled := replicaConfig != nil
	primaryUser := ""
	primaryDBName := ""
	primarySSLMode := ""
	if replicaConfig != nil {
		primaryUser = replicaConfig.User
		primaryDBName = replicaConfig.DBName
		primarySSLMode = replicaConfig.SSLMode
	}

	// QoS 기본값 — 사용자 spec.shards.resources 미지정 시 Burstable QoS 보장.
	// BestEffort 는 kube-scheduler eviction 1순위 — production 위험.
	// Limits 는 미설정 (Burstable). 사용자가 명시 시만 limit 적용.
	if len(resources.Requests) == 0 && len(resources.Limits) == 0 {
		resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	}

	pvcAccessModes := storage.AccessModes
	if len(pvcAccessModes) == 0 {
		pvcAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: pvcAccessModes,
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storage.Size,
			},
		},
		StorageClassName: storageClassPtr(storage.StorageClass),
	}

	// instance manager 환경 변수. reshard target 이면 POSTGRES_RESHARD_TARGET 를
	// 추가 주입 → cmd/instance 가 ordinal lease (PrimaryLeaseName) 대신 격리된
	// ReshardTargetLeaseName 을 사용해 실 shard election 침범을 차단한다 (ADR-0027).
	primaryEnv := primaryRuntimeEnv(runtimeConfigMap, replicaConfig, runtimeKeyPrimaryEndpoint)[0]
	instanceEnv := buildInstanceEnv(cluster.Name, serviceName, shardOrdinal, pgMajor, members, primaryEnv, replicaClusterEnabled)
	if reshardTargetID != "" {
		instanceEnv = append(instanceEnv, corev1.EnvVar{Name: "POSTGRES_RESHARD_TARGET", Value: reshardTargetID})
	}

	// superuser passfile(ADR-0034) — 부트스트랩이 쓰고 두 컨테이너의 libpq(walreceiver·pg_rewind·pg_basebackup)가
	// 읽는다. 복제 클러스터는 원천 자격(PRIMARY_PASSWORD)이라 무변이다.
	bootstrap := buildBootstrapContainer(image, pgMajor, shardOrdinal, runtimeConfigMap, members, replicaClusterEnabled, primaryUser, primaryDBName, primarySSLMode, replicaConfig)
	if !replicaClusterEnabled {
		bootstrap.Env = append(bootstrap.Env, bootstrapAuthEnv(cluster.Spec.SuperuserSecretRef)...)
		instanceEnv = append(instanceEnv, passfileEnv())
	}

	pvcLabels := make(map[string]string, len(labels)+1)
	maps.Copy(pvcLabels, labels)
	pvcLabels["postgres.keiailab.io/cluster"] = cluster.Name

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    podLabels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: serviceName,
			Replicas:    &members,
			// OnDelete — 템플릿이 바뀌어도 STS 컨트롤러는 멤버를 재시작하지 않는다. 교체는 단계화 롤아웃이
			// 한 번에 하나씩, primary 는 switchover 뒤에 한다(ADR-0033).
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			Selector:       &metav1.LabelSelector{MatchLabels: labels}, // 불변 — shard-id 미포함.
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
					Annotations: map[string]string{
						postgresConfigHashAnnotation:       configHash,
						postgresImageCatalogHashAnnotation: sha256Hex(image),
					},
				},
				Spec: corev1.PodSpec{
					SecurityContext:    dataplanePodSecurityContext(),
					ServiceAccountName: InstanceServiceAccountName(cluster.Name),
					InitContainers:     []corev1.Container{bootstrap},
					Containers: []corev1.Container{{
						Name:            pgContainerName,
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Env:             instanceEnv,
						Ports: []corev1.ContainerPort{
							{Name: "postgres", ContainerPort: pgPort, Protocol: corev1.ProtocolTCP},
							{Name: "probe", ContainerPort: instanceProbePort, Protocol: corev1.ProtocolTCP},
						},
						// readiness: instance manager 의 /readyz 가 election Status 반영.
						// initialDelaySeconds 5 — instance manager 의 waitSupReady 가 postgres
						// unix socket race 를 코드 레벨에서 처리 (RFC 0006 R3 prep) 하므로
						// probe 가 race 회피 임무를 중복 수행할 필요 없음. periodSeconds 3 으로
						// 첫 successful probe → Ready 전환 가속 (Pod Ready < 60s 목표).
						ReadinessProbe: probes.New().
							HTTP("/readyz", instanceProbePort).
							InitialDelay(5 * time.Second).
							Period(3 * time.Second).
							Timeout(3 * time.Second).
							FailureThreshold(3).
							Build(),
						LivenessProbe: probes.New().
							HTTP("/healthz", instanceProbePort).
							InitialDelay(60 * time.Second).
							Period(30 * time.Second).
							Timeout(5 * time.Second).
							FailureThreshold(3).
							Build(),
						VolumeMounts: append(append([]corev1.VolumeMount{
							{Name: "data", MountPath: pgDataMountPath},
							{Name: "config", MountPath: pgConfigMountPath, ReadOnly: true},
						}, dataplaneEphemeralVolumeMounts()...), tlsVolumeMounts(cluster)...),
					}},
					Volumes: append(append(append([]corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							},
						},
					}}, dataplaneEphemeralVolumes()...), tlsVolumes(cluster)...), externalClusterCredentialVolumes(replicaConfig)...),
					// production cluster cycle 21 stop hook 26차: modern HA 5-layer 활성.
					// Layer 2 TopologySpreadConstraints (multi-node 분산 SPOF 차단)
					// + Layer 3 PriorityClassName (evict 우선순위) — CR Spec.Shards
					// 의 신규 fields 사용. Affinity + Tolerations 도 동시 적용.
					Affinity:                  cluster.Spec.Shards.Affinity,
					Tolerations:               cluster.Spec.Shards.Tolerations,
					PriorityClassName:         cluster.Spec.Shards.PriorityClassName,
					TopologySpreadConstraints: commonstopology.Defaulted(cluster.Spec.Shards.TopologySpreadConstraints, cluster.Spec.Shards.Replicas, labels, commonstopology.WithMinReplicas(1)),
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "data",
					Labels: pvcLabels,
				},
				Spec: pvcSpec,
			}},
		},
	}
}

// buildTargetShardStatefulSet 은 G3 online-resharding 의 *target shard* (ADR-0027)
// StatefulSet 을 만든다. 라이브 ordinal shard 와 격리된 단일 fresh-primary 다:
//
//   - 이름/Service: `<cluster>-rsd-<shardID>` (names.go, ordinal `-shard-` 와 분리)
//   - label: ReshardTargetSelectorLabels (ordinal `shard` label 미부여 →
//     aggregateShardStatus/failover 가 blind, #220-class 차단)
//   - members=1 + 런타임 CM 없음(PRIMARY_ENDPOINT 인라인 빈 문자열) → pod-0 (`...-0`) 가
//     buildBootstrapContainer 의 `POD_ORDINAL=="0"` + 빈 endpoint 분기로 *initdb 빈
//     primary* 부팅 (잃을 데이터 0, #220 standby.signal 로직 무관)
//   - POSTGRES_RESHARD_TARGET env → cmd/instance 가 ReshardTargetLeaseName 사용
//     (충돌-불가 lease, 실 shard election 침범 차단)
//
// 본 함수는 buildPGStatefulSet 을 reshardTargetID 와 함께 재사용 — ordinal 경로
// 코드를 전혀 바꾸지 않는다 (빈 reshardTargetID 면 byte-identical).
func buildTargetShardStatefulSet(
	cluster *postgresv1alpha1.PostgresCluster,
	shardID string,
	image, pgMajor string,
	storage postgresv1alpha1.StorageSpec,
	resources corev1.ResourceRequirements,
	configMapName, configHash string,
) *appsv1.StatefulSet {
	return buildTargetShardStatefulSetWithMembers(
		cluster, shardID, image, pgMajor,
		1, "",
		storage, resources,
		configMapName, configHash,
	)
}

func buildTargetShardStatefulSetWithMembers(
	cluster *postgresv1alpha1.PostgresCluster,
	shardID string,
	image, pgMajor string,
	members int32,
	runtimeConfigMap string,
	storage postgresv1alpha1.StorageSpec,
	resources corev1.ResourceRequirements,
	configMapName, configHash string,
) *appsv1.StatefulSet {
	return buildPGStatefulSet(
		cluster,
		TargetShardStatefulSetName(cluster.Name, shardID),
		TargetShardServiceName(cluster.Name, shardID),
		0, // shardOrdinal: pod-0 initdb 경로용 (SHARD_ORDINAL env 는 정보용, 격리 label 은 reshardTargetID 가 결정)
		image, configMapName, pgMajor,
		members,
		storage, resources,
		runtimeConfigMap,
		configHash,
		shardID, // reshardTargetID → 격리 label + POSTGRES_RESHARD_TARGET env
	)
}

// buildTargetShardConfigMap 은 reshard target shard (ADR-0027) 의 postgresql.conf
// ConfigMap 을 만든다. 격리 label 사용 (ordinal CM 과 분리). 단일 fresh primary
// 이므로 synchronous config 는 shardOrdinal=0 기준 (members=1 → standby 없음).
func buildTargetShardConfigMap(cluster *postgresv1alpha1.PostgresCluster, shardID string, reg *plugin.Registry) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetShardConfigMapName(cluster.Name, shardID),
			Namespace: cluster.Namespace,
			Labels:    ReshardTargetSelectorLabels(cluster.Name, shardID),
		},
		Data: postgresConfigData(cluster, 0, reg),
	}
}

// buildTargetHeadlessService 은 reshard target shard 의 headless Service 를 만든다.
// selector 가 target STS pod 의 격리 label 과 일치해야 pod DNS 가 동작한다 (ADR-0027).
func buildTargetHeadlessService(cluster *postgresv1alpha1.PostgresCluster, shardID string) *corev1.Service {
	labels := ReshardTargetSelectorLabels(cluster.Name, shardID)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TargetShardServiceName(cluster.Name, shardID),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels,
			Ports: []corev1.ServicePort{{
				Name:       "postgres",
				Port:       pgPort,
				TargetPort: intstr.FromInt32(pgPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

func routerAutoscaleEnabled(cluster *postgresv1alpha1.PostgresCluster) bool {
	return cluster != nil &&
		cluster.Spec.Router != nil &&
		cluster.Spec.Router.Autoscale != nil &&
		cluster.Spec.Router.Autoscale.Enabled
}

func routerMinReplicas(cluster *postgresv1alpha1.PostgresCluster) int32 {
	if cluster == nil || cluster.Spec.Router == nil {
		return 1
	}
	if as := cluster.Spec.Router.Autoscale; as != nil && as.MinReplicas > 0 {
		return as.MinReplicas
	}
	if cluster.Spec.Router.Replicas > 0 {
		return cluster.Spec.Router.Replicas
	}
	return 1
}

func routerMaxReplicas(cluster *postgresv1alpha1.PostgresCluster) int32 {
	if cluster == nil || cluster.Spec.Router == nil || cluster.Spec.Router.Autoscale == nil {
		return routerMinReplicas(cluster)
	}
	if cluster.Spec.Router.Autoscale.MaxReplicas > 0 {
		return cluster.Spec.Router.Autoscale.MaxReplicas
	}
	return routerMinReplicas(cluster)
}

func routerTargetCPU(cluster *postgresv1alpha1.PostgresCluster) int32 {
	if cluster != nil && cluster.Spec.Router != nil && cluster.Spec.Router.Autoscale != nil &&
		cluster.Spec.Router.Autoscale.TargetCPU > 0 {
		return cluster.Spec.Router.Autoscale.TargetCPU
	}
	return 70
}

func routerScaleOnActiveConnections(cluster *postgresv1alpha1.PostgresCluster) bool {
	return cluster != nil && cluster.Spec.Router != nil && cluster.Spec.Router.Autoscale != nil &&
		cluster.Spec.Router.Autoscale.ScaleOnActiveConnections
}

func routerTargetActiveConnections(cluster *postgresv1alpha1.PostgresCluster) int32 {
	if cluster != nil && cluster.Spec.Router != nil && cluster.Spec.Router.Autoscale != nil &&
		cluster.Spec.Router.Autoscale.TargetActiveConnections > 0 {
		return cluster.Spec.Router.Autoscale.TargetActiveConnections
	}
	return 1000
}

func buildRouterHPA(cluster *postgresv1alpha1.PostgresCluster, deploymentName string) *autoscalingv2.HorizontalPodAutoscaler {
	minReplicas := routerMinReplicas(cluster)
	targetCPU := routerTargetCPU(cluster)
	metrics := []autoscalingv2.MetricSpec{{
		Type: autoscalingv2.ResourceMetricSourceType,
		Resource: &autoscalingv2.ResourceMetricSource{
			Name: corev1.ResourceCPU,
			Target: autoscalingv2.MetricTarget{
				Type:               autoscalingv2.UtilizationMetricType,
				AverageUtilization: &targetCPU,
			},
		},
	}}
	// opt-in: active-connection Pods 메트릭. pg-router 가 노출하는
	// RouterActiveConnectionsMetric 게이지를 custom-metrics adapter 가
	// custom.metrics.k8s.io 로 매핑한다는 전제. Pod 당 평균 active 커넥션이
	// target 을 넘으면 스케일 아웃. CPU 와 함께 있으면 HPA 는 둘 중 더 많은
	// replica 를 요구하는 쪽을 택한다(표준 HPA semantics).
	if routerScaleOnActiveConnections(cluster) {
		target := resource.NewQuantity(int64(routerTargetActiveConnections(cluster)), resource.DecimalSI)
		metrics = append(metrics, autoscalingv2.MetricSpec{
			Type: autoscalingv2.PodsMetricSourceType,
			Pods: &autoscalingv2.PodsMetricSource{
				Metric: autoscalingv2.MetricIdentifier{Name: postgresv1alpha1.RouterActiveConnectionsMetric},
				Target: autoscalingv2.MetricTarget{
					Type:         autoscalingv2.AverageValueMetricType,
					AverageValue: target,
				},
			},
		})
	}
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RouterHPAName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "router", -1),
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			},
			MinReplicas: &minReplicas,
			MaxReplicas: routerMaxReplicas(cluster),
			Metrics:     metrics,
		},
	}
}

// routerImage 는 QueryRouter Pod 가 실행할 pg-router 이미지다 (ROUTER_IMAGE 로 주입,
// 미설정 시 기본값 — 로컬 빌드 후 노드에 import 한 태그). reshardCopyImage() 와 동일 패턴:
// 이미지 경로는 배포 환경 관심사이므로 CRD 가 아니라 operator env 로 받는다.
func routerImage() string {
	if v := os.Getenv("ROUTER_IMAGE"); v != "" {
		return v
	}
	return "ghcr.io/keiailab/pg-router:dev"
}

// routerKeyspace 는 라우터가 조회할 ShardRange 의 keyspace 다. cmd/pg-router 의
// PGROUTER_KEYSPACE 기본값과 정합 — ShardRange.spec.keyspace 가 이 값이어야 라우팅된다.
const routerKeyspace = "default"

// routerEnv 는 cmd/pg-router 의 env 계약을 채운다. namespace/cluster 는 CR 에서,
// topology/backend 는 K8s API 기반 동적 모드로 고정한다(정적 env 토폴로지는 reshard 시
// 라우팅 테이블이 갱신되지 않아 본 오퍼레이터 모델과 맞지 않는다).
func routerEnv(cluster *postgresv1alpha1.PostgresCluster) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "PGROUTER_NAMESPACE", Value: cluster.Namespace},
		{Name: "PGROUTER_CLUSTER", Value: cluster.Name},
		{Name: "PGROUTER_KEYSPACE", Value: routerKeyspace},
		{Name: "PGROUTER_TOPOLOGY", Value: "crd"},
		{Name: "PGROUTER_BACKEND", Value: "status"},
		{Name: "PGROUTER_MODE", Value: routerMode()},
		{Name: "PGROUTER_LISTEN", Value: fmt.Sprintf(":%d", pgPort)},
		{Name: "PGROUTER_METRICS_ADDR", Value: fmt.Sprintf(":%d", routerMetricsPort)},
	}
}

// routerMode 는 pg-router 의 라우팅 모드다 (ROUTER_MODE 로 주입, 기본 query).
//
// query  = 쿼리를 파싱해 샤딩 키로 per-query 라우팅 + scatter-gather. 분산 SQL 의 본 모드.
// connection = 접속 시 *dbname* 을 키로 삼아 한 연결을 한 샤드에 고정하는 PoC 모드 —
// 실 dbname 과 샤딩 키가 충돌하므로 오퍼레이터 기본값으로는 부적합하다.
func routerMode() string {
	if v := os.Getenv("ROUTER_MODE"); v != "" {
		return v
	}
	return "query"
}

// buildRouterServiceAccount 는 router Pod 전용 ServiceAccount 다. instance SA 와 분리한다
// — router 는 PVC fence/lease 권한이 필요 없고(최소권한), instance 는 ShardRange 읽기가
// 필요 없다.
func buildRouterServiceAccount(cluster *postgresv1alpha1.PostgresCluster) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RouterServiceAccountName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "router", -1),
		},
	}
}

// buildRouterRole 는 pg-router 의 K8s 읽기 권한(최소)이다.
//
//   - shardranges: PGROUTER_TOPOLOGY=crd 의 키→샤드 매핑 소스 (watch 로 hot-reload)
//   - postgresclusters: PGROUTER_BACKEND=status 의 샤드 엔드포인트 소스
//     (failover 로 primary 가 바뀌면 .status 를 통해 인지)
//
// 쓰기 verb 는 없다 — 라우터는 CR 을 변경하지 않는다.
//
// `*/status` 서브리소스 규칙은 두지 않는다: `.status` 는 부모 리소스를 GET/LIST 할 때 함께
// 실려오므로 읽기에는 불필요하고(status 서브리소스 권한은 *쓰기* 용), operator 자신이 해당
// 권한을 보유하지 않아 RBAC escalation 방지에 걸려 Role 생성이 거부된다 (라이브 실측 2026-07-14:
// `roles ... is forbidden: ... attempting to grant RBAC permissions not currently held`).
func buildRouterRole(cluster *postgresv1alpha1.PostgresCluster) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RouterRoleName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "router", -1),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{postgresv1alpha1.GroupVersion.Group},
				Resources: []string{"shardranges", "postgresclusters"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

// buildRouterRoleBinding 은 router SA ↔ Role 결합이다.
func buildRouterRoleBinding(cluster *postgresv1alpha1.PostgresCluster) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RouterRoleBindingName(cluster.Name),
			Namespace: cluster.Namespace,
			Labels:    SelectorLabels(cluster.Name, "router", -1),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      RouterServiceAccountName(cluster.Name),
			Namespace: cluster.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     RouterRoleName(cluster.Name),
		},
	}
}

// buildRouterDeployment는 stateless QueryRouter(cmd/pg-router)의 Deployment를 만든다.
// ADR 0003 §강제 메커니즘에 의해 PVC를 절대 마운트하지 않는다(StatefulSet 사용 금지).
//
// image 는 pg-router 이미지여야 한다(routerImage() 가 결정). PG 베이스 이미지를 넘기면
// 그 엔트리포인트가 POD_NAME 등 instance 전용 env 를 요구해 CrashLoop 한다.
//
// env 는 cmd/pg-router 의 계약(PGROUTER_*)이다:
//   - TOPOLOGY=crd     — ShardRange CR 에서 키→샤드 매핑을 읽고 watch 로 hot-reload
//   - BACKEND=status   — PostgresCluster.status 에서 샤드 primary/replica 엔드포인트를
//     해석(failover 인지). 두 모드 모두 K8s API 를 읽으므로 전용 SA/Role 이 필요하다
//     (buildRouterServiceAccount/Role/RoleBinding).
func buildRouterDeployment(
	cluster *postgresv1alpha1.PostgresCluster,
	name, configMapName, image string,
	replicas int32,
	resources corev1.ResourceRequirements,
) *appsv1.Deployment {
	selectorLabels := SelectorLabels(cluster.Name, "router", -1)
	labels := maps.Clone(selectorLabels)
	if routerAutoscaleEnabled(cluster) {
		labels[RouterAutoscaleLabelKey] = "true"
	}

	// pg-router 는 /metrics(Prometheus 텍스트)로 active-connection 게이지를 노출한다.
	// scrape annotation 으로 Prometheus/custom-metrics adapter 가 수집한다(HPA
	// ScaleOnActiveConnections 결선의 metrics 소스). scrape 자체는 부작용 없으므로
	// autoscale 비활성이어도 노출을 켜둔다(관측성).
	podAnnotations := map[string]string{
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   strconv.Itoa(int(routerMetricsPort)),
		"prometheus.io/path":   "/metrics",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: podAnnotations},
				Spec: corev1.PodSpec{
					ServiceAccountName: RouterServiceAccountName(cluster.Name),
					SecurityContext:    dataplanePodSecurityContext(),
					Containers: []corev1.Container{{
						Name:            "router",
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Env:             routerEnv(cluster),
						Ports: []corev1.ContainerPort{{
							Name:          "postgres",
							ContainerPort: pgPort,
							Protocol:      corev1.ProtocolTCP,
						}, {
							Name:          "metrics",
							ContainerPort: routerMetricsPort,
							Protocol:      corev1.ProtocolTCP,
						}},
						// readiness = 라우팅 테이블(토폴로지) 확보 여부(/readyz). 확보 전엔
						// Service endpoint 에서 제외되어 라우팅 불가 Pod 로 트래픽이 안 감.
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/readyz",
									Port: intstr.FromInt32(routerMetricsPort),
								},
							},
							InitialDelaySeconds: 2,
							PeriodSeconds:       5,
						},
						VolumeMounts: append([]corev1.VolumeMount{
							{Name: "config", MountPath: pgConfigMountPath, ReadOnly: true},
						}, dataplaneEphemeralVolumeMounts()...),
					}},
					Volumes: append([]corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							},
						},
					}}, dataplaneEphemeralVolumes()...),
				},
			},
		},
	}
}
