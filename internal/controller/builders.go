/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/keiailab/operator-commons/pkg/probes"
	"github.com/keiailab/operator-commons/pkg/security"
	commonstopology "github.com/keiailab/operator-commons/pkg/topology"

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

	// backupRepoMountPath 는 filesystem pgBackRest repo (#209) 가 마운트되는 위치다.
	backupRepoMountPath   = "/var/lib/pgbackrest"
	primaryPGPassFile     = "/tmp/primary.pgpass"
	primaryClientKeyFile  = "/tmp/primary-client.key"
	primaryClientCertFile = "/tmp/primary-client.crt"
	primaryRootCertFile   = "/tmp/primary-root.crt"

	// postgresUserUID는 PostgreSQL 표준 postgres user의 UID/GID다.
	// ADR 0006에 의해 동결된 데이터플레인 Pod의 runAsUser/runAsGroup/fsGroup 기본값.
	postgresUserUID int64 = 70

	restartPrimaryAsStandbyMarker = ".keiailab-restart-primary-as-standby"
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
// iteration 8 (2026-05-07): operator-commons/pkg/security 위임 — 3 operator 공통
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
		// #209: filesystem pgBackRest repo (WAL archive-push + full backup land here).
		{Name: "pgbackrest-repo", MountPath: backupRepoMountPath},
	}
}

// dataplaneEphemeralVolumes는 dataplaneEphemeralVolumeMounts와 짝이 되는
// emptyDir Volume 정의를 반환한다.
func dataplaneEphemeralVolumes() []corev1.Volume {
	return []corev1.Volume{
		{Name: "ephemeral-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "ephemeral-pg-run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "pgbackrest-repo", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
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
// extension list).
func renderPostgresConf(
	reg *plugin.Registry,
	enabledExtensions []string,
	tlsOn bool,
	syncConfig *synchronousPostgresConfig,
	archiveConfig *archivePostgresConfig,
) string {
	var sb strings.Builder
	sb.WriteString("# Generated by keiailab-postgres-operator. Do not edit by hand.\n")
	sb.WriteString("listen_addresses = '*'\n")
	sb.WriteString("port = 5432\n")
	// Unix socket 위치 — instance manager 의 LocalDSN 이 본 경로에 의존.
	fmt.Fprintf(&sb, "unix_socket_directories = '%s'\n", pgRunDir)
	// WAL + replication 기본값 — replicas>0 일 때 streaming replication 전제.
	sb.WriteString("wal_level = replica\n")
	// pg_rewind 전제. data checksums 없는 기존 스토리지에서도 failover 후
	// former primary 를 current primary timeline 으로 되감을 수 있게 한다.
	sb.WriteString("wal_log_hints = on\n")
	sb.WriteString("max_wal_senders = 10\n")
	sb.WriteString("max_replication_slots = 10\n")
	sb.WriteString("hot_standby = on\n")
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
	// pgbackrest 공통 옵션 (plugin.go pgbackrestCommonArgs 미러, 라이브 검증 2026-06-04):
	// readOnlyRootFilesystem + uid 70 + deb /etc/pgbackrest.conf(640) 환경 회피
	// (--config=/dev/null + --log-level-file=off + --pg1-path + --pg1-user/database).
	commonArgs := "--config=/dev/null --log-level-file=off --pg1-path=" + pgDataSubdir +
		" --pg1-user=postgres --pg1-database=postgres"
	cmd := fmt.Sprintf(
		`sh -c "env %s pgbackrest %s --stanza=%s stanza-create 2>/dev/null || true; exec env %s pgbackrest %s --stanza=%s archive-push \"$1\"" -- %%p`,
		repoEnv, commonArgs, stanza, repoEnv, commonArgs, stanza)
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

// renderPGHBAConf 는 pg_hba.conf 본문을 생성한다.
//
// 인증 정책 (alpha 단계 — production 은 추후 ADR + secret 기반 강화):
//   - local Unix socket: trust (instance manager 가 peer auth 로 LocalDSN 사용)
//   - pg_rewind source connection: cluster 내부 postgres normal connection trust
//   - host (cluster 내부 10.0.0.0/8 + 172.16.0.0/12 + 192.168.0.0/16): scram-sha-256
//   - replication: cluster 내부 trust (alpha — secret rotation 후속)
func renderPGHBAConf(tlsOn bool) string {
	// Pillar P7 §7 Phase 3b: TLS 활성 시 host → hostssl 강제 (외부 client 의
	// plaintext connection 차단). replication 은 동일 cluster pod-to-pod 라
	// 내부 신뢰 boundary — host 그대로 (cert chain 별도 issuance 회피).
	hostType := "host"
	if tlsOn {
		hostType = "hostssl"
	}
	return fmt.Sprintf(`# Generated by keiailab-postgres-operator. Do not edit by hand.
# TYPE  DATABASE        USER            ADDRESS                 METHOD
local   all             all                                     trust
%-7s all             postgres        10.0.0.0/8              trust
%-7s all             postgres        172.16.0.0/12           trust
%-7s all             postgres        192.168.0.0/16          trust
%-7s all             all             10.0.0.0/8              scram-sha-256
%-7s all             all             172.16.0.0/12           scram-sha-256
%-7s all             all             192.168.0.0/16          scram-sha-256
host    replication     all             10.0.0.0/8              trust
host    replication     all             172.16.0.0/12           trust
host    replication     all             192.168.0.0/16          trust
`, hostType, hostType, hostType, hostType, hostType, hostType)
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
		),
		"pg_hba.conf": renderPGHBAConf(tlsEnabled(cluster)),
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
func buildBootstrapContainer(
	image, pgMajor string,
	shardOrdinal int32,
	primaryEndpoint string,
	members int32,
	replicaClusterEnabled bool,
	primaryUser string,
	primaryDBName string,
	primarySSLMode string,
	primaryCredentialConfig *replicaBootstrapConfig,
) corev1.Container {
	binDir := pgBinDir(pgMajor)
	replicaClusterValue := "0"
	if replicaClusterEnabled {
		replicaClusterValue = "1"
	}
	script := `set -eu
DATA="` + pgDataSubdir + `"
PRIMARY_ENDPOINT="${PRIMARY_ENDPOINT:-}"
PRIMARY_USER="${PRIMARY_USER:-postgres}"
PRIMARY_DBNAME="${PRIMARY_DBNAME:-postgres}"
PRIMARY_SSLMODE="${PRIMARY_SSLMODE:-prefer}"
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

escape_pgpass() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/:/\\:/g'
}

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
    } > "` + primaryPGPassFile + `"
    chmod 0600 "` + primaryPGPassFile + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO passfile=` + primaryPGPassFile + `"
  fi
  if [ -n "$PRIMARY_SSLKEY_FILE" ]; then
    cp "$PRIMARY_SSLKEY_FILE" "` + primaryClientKeyFile + `"
    chmod 0600 "` + primaryClientKeyFile + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslkey=` + primaryClientKeyFile + `"
  fi
  if [ -n "$PRIMARY_SSLCERT_FILE" ]; then
    cp "$PRIMARY_SSLCERT_FILE" "` + primaryClientCertFile + `"
    chmod 0600 "` + primaryClientCertFile + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslcert=` + primaryClientCertFile + `"
  fi
  if [ -n "$PRIMARY_SSLROOTCERT_FILE" ]; then
    cp "$PRIMARY_SSLROOTCERT_FILE" "` + primaryRootCertFile + `"
    chmod 0600 "` + primaryRootCertFile + `"
    PRIMARY_CONNINFO="$PRIMARY_CONNINFO sslrootcert=` + primaryRootCertFile + `"
  fi
}

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
  elif [ "$MEMBER_COUNT" -gt 1 ] && [ -n "$PRIMARY_HOST" ] && [ "$PRIMARY_IS_SELF" = "0" ] && [ ! -f "$DATA/standby.signal" ]; then
    # split-brain fix (fix/ha-replica-standby-signal-restore): an HA replica whose
    # PGDATA is already initialized but has no standby.signal must boot as a *standby*,
    # not race the election as a Real elector. Restore standby.signal + primary_conninfo
    # *before* postgres starts so the T30 guard (cmd/instance: IsStandby → Follower)
    # observes a standby and never acquires the primary lease. The marker is still
    # emitted so the instance manager can pg_rewind on timeline divergence. Without
    # this both pods boot primary → split-brain (live RCA 2026-06-04, pg-e2e).
    prepare_primary_conninfo
    touch "$DATA/standby.signal"
    printf "primary_conninfo = '%s'\n" "$PRIMARY_CONNINFO" >> "$DATA/postgresql.auto.conf"
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

# pod ordinal 0 = primary slot (initdb on first cluster boot, primary thereafter via election).
# pod ordinal != 0 = standby slot (basebackup from current primary if available).
# PRIMARY_ENDPOINT empty = no live primary observed yet → fallback to initdb (cold-start of cluster).
if [ "$POD_ORDINAL" = "0" ] || [ -z "$PRIMARY_ENDPOINT" ]; then
  mkdir -p "$DATA"
  chmod 0700 "$DATA"
  ` + binDir + `/initdb -D "$DATA" --auth-local=trust --auth-host=scram-sha-256 --username=postgres --encoding=UTF8 --locale=C
  echo "initdb completed at $DATA"
else
  prepare_primary_conninfo
  mkdir -p "$DATA"
  chmod 0700 "$DATA"
  ` + binDir + `/pg_basebackup -D "$DATA" -d "$PRIMARY_CONNINFO" --no-password --wal-method=stream --checkpoint=fast
  touch "$DATA/standby.signal"
  printf "primary_conninfo = '%s'\n" "$PRIMARY_CONNINFO" >> "$DATA/postgresql.auto.conf"
  echo "pg_basebackup completed; standby.signal + primary_conninfo configured"
fi
`
	return corev1.Container{
		Name:    bootstrapContainerName,
		Image:   image,
		Command: []string{"sh", "-c"},
		Args:    []string{script},
		Env: append([]corev1.EnvVar{
			{Name: "SHARD_ORDINAL", Value: fmt.Sprintf("%d", shardOrdinal)},
			{Name: "PRIMARY_ENDPOINT", Value: primaryEndpoint},
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
		}, externalClusterCredentialEnv(primaryCredentialConfig)...),
		SecurityContext: dataplaneContainerSecurityContext(),
		VolumeMounts: append(append([]corev1.VolumeMount{
			{Name: "data", MountPath: pgDataMountPath},
		}, dataplaneEphemeralVolumeMounts()...), externalClusterCredentialVolumeMounts(primaryCredentialConfig)...),
	}
}

// buildInstanceEnv 는 instance manager (PID 1) 에 주입할 환경 변수 집합을 만든다.
// downward API + spec 매개변수 + current primary endpoint + 고정 경로의 합산.
func buildInstanceEnv(
	clusterName string,
	shardOrdinal int32,
	pgMajor string,
	members int32,
	primaryEndpoint string,
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
		{Name: "POSTGRES_ROLE", Value: "shard"},
		{Name: "POSTGRES_SHARD_ORDINAL", Value: fmt.Sprintf("%d", shardOrdinal)},
		{Name: "POSTGRES_MEMBER_COUNT", Value: fmt.Sprintf("%d", members)},
		{Name: "PRIMARY_ENDPOINT", Value: primaryEndpoint},
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
	primaryEndpoint string,
	configHash string,
) *appsv1.StatefulSet {
	labels := SelectorLabels(cluster.Name, "shard", shardOrdinal)
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

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: serviceName,
			Replicas:    &members,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						postgresConfigHashAnnotation:       configHash,
						postgresImageCatalogHashAnnotation: sha256Hex(image),
					},
				},
				Spec: corev1.PodSpec{
					SecurityContext:    dataplanePodSecurityContext(),
					ServiceAccountName: InstanceServiceAccountName(cluster.Name),
					InitContainers:     []corev1.Container{buildBootstrapContainer(image, pgMajor, shardOrdinal, primaryEndpoint, members, replicaClusterEnabled, primaryUser, primaryDBName, primarySSLMode, replicaConfig)},
					Containers: []corev1.Container{{
						Name:            pgContainerName,
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Env:             buildInstanceEnv(cluster.Name, shardOrdinal, pgMajor, members, primaryEndpoint, replicaClusterEnabled),
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
					Labels: labels,
				},
				Spec: pvcSpec,
			}},
		},
	}
}

// buildRouterDeployment는 stateless QueryRouter의 Deployment를 만든다.
// ADR 0003 §강제 메커니즘에 의해 PVC를 절대 마운트하지 않는다(StatefulSet 사용
// 금지). 본 함수는 P12-T2 시점에 cmd/router 바이너리 이미지로 교체된다. 현재는
// PG 베이스 이미지를 그대로 사용하는 placeholder.
func buildRouterDeployment(
	cluster *postgresv1alpha1.PostgresCluster,
	name, configMapName, image string,
	replicas int32,
	resources corev1.ResourceRequirements,
) *appsv1.Deployment {
	labels := SelectorLabels(cluster.Name, "router", -1)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: dataplanePodSecurityContext(),
					Containers: []corev1.Container{{
						Name:            "router",
						Image:           image,
						Resources:       resources,
						SecurityContext: dataplaneContainerSecurityContext(),
						Ports: []corev1.ContainerPort{{
							Name:          "postgres",
							ContainerPort: pgPort,
							Protocol:      corev1.ProtocolTCP,
						}},
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
