// Package plugin은 본 오퍼레이터의 확장 지점(Plugin SDK)을 정의한다.
//
// 본 패키지는 Pillar P13(Plugin SDK)의 인터페이스 동결 산출물이며, 모든 다른
// Pillar(P1~P12, P14)가 컨트롤러 코드를 작성할 때 구체 구현이 아닌 본 패키지의
// 인터페이스를 호출하도록 강제한다. 이 강제는 두 가지 가치를 보장한다.
//
//  1. 새 백업 도구·exporter·extension·router·auth 메커니즘 추가 = 인터페이스
//     구현 1개 + Registry 등록 1줄. 핵심 reconciler 변경 0.
//  2. 외부 컨트리뷰터가 향후 gRPC over UDS 모드(HashiCorp go-plugin 패턴)로
//     Apache-2.0 외 라이선스의 폐쇄 플러그인을 분리 배포 가능. 본 인터페이스가
//     wire format의 단일 출처(SOT).
//
// 인터페이스 동결 정책:
//   - 본 패키지의 5개 인터페이스 시그니처는 ADR 0005 "Plugin SDK 인터페이스
//     모델"의 합의에 따라 변경되며, alpha 단계에서는 추가 메서드 도입(non-
//     breaking)만 허용한다. 메서드 제거·시그니처 변경은 RFC 0012("Plugin SDK
//     안정화")에서 일괄 처리한다.
//   - 핵심 reconciler가 본 패키지 외부의 구체 구현(예: pgbackrest 실행 바이너리)을
//     직접 import 하면 golangci-lint custom 규칙으로 reject된다(P13-T2).
//
// 의존성 최소화:
//   - 본 패키지는 stdlib + k8s.io/api/core/v1 외 외부 의존을 갖지 않는다.
//   - 모니터링 매니페스트(예: prometheus-operator의 PrometheusRule)는 raw JSON/
//     YAML []byte로 노출하여 본 SDK가 특정 모니터링 스택 버전에 종속되지 않게
//     한다.
package plugin

import (
	"context"
	"database/sql"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// ClusterTarget은 플러그인이 동작 대상으로 삼는 PostgresCluster 인스턴스를 식별한다.
//
// 본 구조체는 Pillar P1의 PostgresCluster CRD 스키마가 확정되는 시점에 cluster
// reference 의미를 보강한다. 현재는 namespace/name 식별자와 역할(coordinator/
// worker pool/router) 정보만 보유하여 인터페이스 동결을 가능하게 한다.
type ClusterTarget struct {
	// Namespace는 PostgresCluster CR이 위치한 K8s namespace.
	Namespace string
	// Name은 PostgresCluster CR의 metadata.name.
	Name string
	// Role은 동작 대상 계층("coordinator" | "worker" | "router")을 표시한다.
	// 빈 문자열이면 클러스터 전체에 대한 동작.
	Role string
	// PoolName은 Role="worker"일 때 worker pool 식별자.
	PoolName string
}

// ----------------------------------------------------------------------------
// BackupPlugin
// ----------------------------------------------------------------------------

// BackupPlugin은 백업 도구(pgBackRest, WAL-G, Barman, custom S3 mover 등)를
// 추상화한다. Pillar P4(Backup/PITR) reconciler가 본 인터페이스만을 호출하며,
// 구체 도구 선택은 BackupSpec.Tool 필드와 Registry 등록 결과에 의해 정해진다.
type BackupPlugin interface {
	// Name은 본 플러그인의 고유 식별자. BackupSpec.Tool과 일치해야 한다.
	Name() string

	// PerformBackup은 target에 대한 백업을 opts에 따라 수행한다.
	// 멱등성은 백업 도구의 책임이며, 본 인터페이스는 도구의 단일 호출만 보장한다.
	PerformBackup(ctx context.Context, target ClusterTarget, opts BackupOptions) (BackupResult, error)

	// RestorePIT은 target을 ts 시점으로 복구한다(Point-In-Time Recovery).
	// 분산 PITR 시 본 메서드는 단일 PG 인스턴스에 대해서만 호출되며, 분산 named
	// restore point 강제(2PC 조정)는 RFC 0005 distributed transactions 후속에서
	// 별도 reconciler가 책임진다.
	RestorePIT(ctx context.Context, target ClusterTarget, ts time.Time) error

	// Validate는 사용자가 작성한 BackupSpec이 본 플러그인 관점에서 합법인지
	// 검사한다. webhook 단계에서 호출된다.
	Validate(spec *BackupSpec) error
}

// BackupCommandPlugin 은 operator manager 밖에서 실행할 백업 도구 command 를
// 계획한다. executionMode=sidecar 처럼 controller 가 K8s pod exec 로 명령을
// 전달해야 하는 경우 BackupPlugin 이 본 확장 인터페이스를 함께 구현한다.
type BackupCommandPlugin interface {
	// BackupCommand 는 target 백업을 수행할 argv 를 반환한다.
	BackupCommand(target ClusterTarget, opts BackupOptions) ([]string, error)

	// RestoreCommand 는 target PITR 복구를 수행할 argv 를 반환한다.
	RestoreCommand(target ClusterTarget, ts time.Time) ([]string, error)

	// ParseBackupResult 는 백업 command 출력에서 BackupResult 를 추출한다.
	ParseBackupResult(output []byte, opts BackupOptions) BackupResult
}

// BackupSpec은 BackupJob CRD의 Spec.Backup 서브필드를 본 SDK 관점에서 본 형태다.
// 본 구조체는 BackupJob CRD 시그니처와 1:1 매핑은 아니며, CRD에서 본 SDK로
// 넘기기 위한 어댑터 역할이다.
type BackupSpec struct {
	// Tool은 사용할 백업 도구 이름(예: "pgbackrest" | "walg" | "barman").
	// Registry에 등록된 BackupPlugin.Name()과 일치해야 한다.
	Tool string
	// Repo는 다중 저장소 환경에서 어느 저장소를 쓸지 식별한다.
	Repo string
	// Settings는 도구별 자유형식 키-값. webhook 검증 시 도구별 schema로 추가
	// 검사된다.
	Settings map[string]string
}

// BackupOptions는 단일 백업 호출의 동작 모드를 지정한다.
type BackupOptions struct {
	// Type은 백업 종류("full" | "incremental" | "differential").
	Type string
	// Repo는 BackupSpec.Repo와 동일하나 호출 시점에 override 가능.
	Repo string
	// Labels는 백업 결과 메타데이터에 첨부될 K8s 스타일 레이블.
	Labels map[string]string

	// ExecutionMode는 plugin이 백업을 *어디서 실행*할지 결정한다.
	// (P1-6, RFC 0004 §실행 모델, 2026-04-30 추가)
	//
	// 값:
	//   - "sidecar": pgBackRest 같이 PG Pod에 동거하는 long-running tool에 명령
	//                전달. operator는 sidecar 컨테이너의 lifecycle만 관리.
	//   - "job":     WAL-G 같이 standalone process로 실행. operator가 K8s Job을
	//                생성하여 plugin binary를 호출.
	//   - "":        plugin default 사용 (각 plugin이 정의).
	//
	// 추가 모드는 ADR 0005 §변경 정책 "alpha 단계 추가만 허용"에 따라 *값*만
	// 추가 가능 (시그니처 변경 없음). RFC 0004 §실행 모델 참조.
	ExecutionMode string
}

// BackupResult는 단일 백업 호출의 결과다. PITR 색인 및 retention 정책의 입력.
type BackupResult struct {
	// BackupID는 도구가 부여한 고유 식별자(예: pgBackRest의 backup label).
	BackupID string
	// StartedAt/EndedAt은 호출 시작·종료 시각(UTC).
	StartedAt time.Time
	EndedAt   time.Time
	// Bytes는 백업 크기(바이트). 0 허용(스트리밍 도구).
	Bytes int64
	// Repo는 실제로 사용된 저장소 식별자.
	Repo string
}

// ----------------------------------------------------------------------------
// ExporterPlugin
// ----------------------------------------------------------------------------

// ExporterPlugin은 Prometheus exporter(postgres_exporter, pgwatch2, custom)를
// 추상화한다. Pillar P6(Observability) reconciler가 본 인터페이스를 통해
// 사이드카 spec, Grafana 대시보드, alert rule을 결합한다.
//
// 모니터링 스택 버전 종속을 피하기 위해 대시보드/룰은 raw 바이트로 노출한다.
type ExporterPlugin interface {
	Name() string

	// SidecarSpec은 PostgresCluster Pod에 주입할 exporter 사이드카 컨테이너
	// 스펙을 반환한다. controller-runtime이 이 spec을 PodSpec.Containers에
	// 추가하며, 사용자는 본 SDK 외부에서 PodSpec을 추가 수정할 수 없다.
	SidecarSpec() corev1.Container

	// DashboardJSON은 Grafana 대시보드 JSON 정의를 반환한다.
	// 빈 슬라이스 허용(대시보드 미제공 플러그인).
	DashboardJSON() ([]byte, error)

	// AlertRulesYAML은 prometheus-operator PrometheusRule 또는 호환 형식의
	// YAML 매니페스트를 반환한다. 빈 슬라이스 허용.
	AlertRulesYAML() ([]byte, error)
}

// ----------------------------------------------------------------------------
// ExtensionPlugin
// ----------------------------------------------------------------------------

// ExtensionPlugin은 PostgreSQL extension의 install/preload/post-init 훅을
// 추상화한다. Pillar P10(Extensions) reconciler가 본 인터페이스를 통해
// shared_preload_libraries 우선순위를 결정한다 (낮은 숫자가 앞쪽 — pgaudit=100,
// pg_cron=200 등).
//
// 정렬 규약 (낮은 숫자가 먼저 로드) 의 정확성은 본 SDK 의 회귀 테스트로 보장한다.
type ExtensionPlugin interface {
	Name() string

	// SharedPreloadOrder는 shared_preload_libraries 문자열 내 등장 순서를
	// 결정하는 정수 키다. 작을수록 앞쪽. pgaudit=100, pg_cron=200 등.
	// 동률은 Name() 사전순.
	SharedPreloadOrder() int

	// PreInstall은 CREATE EXTENSION 호출 전에 실행되는 SQL 훅이다.
	// (예: 의존 schema 생성, role 권한 부여)
	PreInstall(ctx context.Context, conn *sql.DB) error

	// PostInstall은 CREATE EXTENSION 호출 후 실행되는 SQL 훅이다.
	// (예: extension 별 초기 설정 SQL)
	PostInstall(ctx context.Context, conn *sql.DB) error

	// Validate는 사용자가 지정한 version 문자열이 본 extension에서 지원되는지
	// 검사한다. webhook 및 reconciler 양쪽에서 호출.
	Validate(version string) error
}

// ----------------------------------------------------------------------------
// RouterPlugin
// ----------------------------------------------------------------------------

// RouterPlugin은 QueryRouter 라우팅 정책을 추상화한다. 0.3.0-alpha 디폴트는
// 자체 stateless QueryRouter (RFC 0004) 이며, 향후 사용자 정의 라우터 또는
// 외부 Apache-2.0 라우터(예: Vitess-style)를 추가할 수 있다.
//
// PostgresCluster CR(또는 RFC 0004 결정에 따라 별도 QueryRouter CR)의
// Spec.Router 필드가 어느 RouterPlugin을 사용할지 결정한다.
type RouterPlugin interface {
	Name() string

	// BuildRouterPodSpec은 라우터 Pod의 PodSpec을 반환한다.
	// ADR 0003 §강제 메커니즘에 의해 본 함수가 반환하는 PodSpec은 PVC 마운트,
	// streaming replication, K8s lease 보유 중 어느 것도 포함해서는 안 된다.
	// 위반 시 webhook이 거절한다.
	BuildRouterPodSpec(target ClusterTarget) (corev1.PodSpec, error)

	// HealthProbe는 라우터 Pod에 대한 도메인-특화 readiness 검사다.
	// 디폴트 RouterPlugin은 router_metadata_lag_seconds 임계 초과 시 unready
	// 반환을 구현한다.
	HealthProbe(ctx context.Context, podName, podNamespace string) error
}

// ----------------------------------------------------------------------------
// AuthPlugin
// ----------------------------------------------------------------------------

// AuthPlugin은 인증 메커니즘(SCRAM-SHA-256 디폴트, mTLS, OIDC bridge 등)을
// 추상화한다. Pillar P7(Security/TLS) reconciler가 본 인터페이스를 통해
// pg_hba.conf, 인증서 회전, secret 스키마를 결정한다.
type AuthPlugin interface {
	Name() string

	// Configure는 target 클러스터에 본 auth 메커니즘을 적용한다.
	// 예: pg_hba.conf 갱신, role 생성, 인증서 마운트 보장.
	Configure(ctx context.Context, target ClusterTarget) error

	// SecretSchemaJSON은 본 auth 메커니즘이 요구하는 K8s Secret의 JSON Schema를
	// 반환한다. webhook이 사용자가 작성한 Secret을 검증할 때 사용한다.
	// 빈 슬라이스는 "Secret 불요" 의미.
	SecretSchemaJSON() ([]byte, error)

	// RotateSecret은 oldRef의 인증 자격(password/cert/token 등)을 새로 발급하여
	// newRef로 반환한다. 운영 자동화(Bitnami 의 update-password CronJob 패턴을
	// operator 내재화)의 SDK 토대다.
	//
	// 의미론:
	//   - oldRef == nil: 초기 생성(첫 부트스트랩). plugin이 새 Secret을 만들고
	//     newRef로 가리킨다.
	//   - oldRef != nil: 회전. plugin이 새 자격을 발급하고 *기존 oldRef는 caller
	//     가 책임지고 cleanup* 한다(즉시 삭제 / grace period 보존 등 정책은 본
	//     SDK 외부의 P7 reconciler 결정).
	//
	// 멱등성은 plugin의 책임이다. 본 인터페이스는 단일 호출만 보장한다.
	//
	// 본 메서드는 ADR 0005 §변경 정책의 "alpha 단계 추가 메서드(non-breaking) 허용"
	// 에 부합하도록 후속 추가됐다(P0-5 권장, 2026-04-30). 첫 구현은 P7 reconciler
	// 가 SCRAM-SHA-256 회전을 위해 추가하며, 인터페이스는 본 시점에 동결된다.
	RotateSecret(ctx context.Context, target ClusterTarget, oldRef *corev1.SecretReference) (newRef *corev1.SecretReference, err error)
}
