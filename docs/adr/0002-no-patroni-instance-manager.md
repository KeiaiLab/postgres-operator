# ADR 0002 — Patroni 미사용, Instance Manager + K8s API as DCS

- **상태**: Accepted
- **날짜**: 2026-04-26
- **결정자**: @keiailab/maintainers
- **관련**: ADR 0001, ADR 0003

## 컨텍스트

PostgreSQL HA의 사실상 표준은 Patroni(Python)이며, etcd/Consul/ZooKeeper 같은 외부 DCS(Distributed Consensus Store)를 합의 저장소로 사용한다. 그러나:

- **외부 DCS 의존**: etcd 클러스터의 운영 부담, 장애 시 PG HA 전체 실패
- **Python 런타임**: Go 기반 오퍼레이터에서 Patroni를 sidecar로 띄울 때 이미지 크기/보안 표면적 증가
- **CloudNativePG의 검증**: K8s API 자체를 DCS로 사용하고 instance manager(Pod 내부 PID 1 Go agent)가 PG를 supervise하는 모델이 production에서 충분히 동작함을 입증

## 결정

본 오퍼레이터는 Patroni를 사용하지 않는다. 대신:

1. **Instance Manager**: 각 PG Pod의 PID 1로 Go 바이너리(`cmd/instance`)를 실행해 postgres 자식 프로세스를 감독.
2. **K8s API as DCS**: Pod의 role(primary/standby) 결정은 K8s lease 객체(`coordination.k8s.io/v1`) 기반 leader election.
3. **CRD Status가 토폴로지 권위**: `PostgresCluster.status.topology`가 현재 RS primary 명단을 보유.

## 근거

1. **운영 단순화**: etcd 의존 제거. K8s control plane이 이미 합의를 보장.
2. **이미지/보안**: Go static 바이너리 단일, distroless 베이스, 외부 런타임 0.
3. **CRD 권위**: PG 상태와 K8s 상태의 분기 가능성을 줄임 (Patroni가 etcd에 쓰는 상태 vs operator가 K8s에 쓰는 상태의 이중화 제거).
4. **CNPG 선례**: 동일 모델로 production-grade 운영 실적.
5. **Citus 통합 자연스러움**: instance manager가 직접 `citus_update_node` 호출 → 새 primary IP 전파를 단일 책임자가 처리.

## 트레이드오프

- **K8s API server 가용성 의존**: API server 장애 시 election 차단 가능.
  - **완화**: K8s control plane은 이미 클러스터 운영의 전제. PG와 동일 가용성 클래스 가정. 추가로 PVC fencing으로 split-brain 차단.
- **Patroni 생태계 도구(patronictl 등) 미적용**: 운영자가 친숙한 CLI를 사용 불가.
  - **완화**: `kubectl pgo` 또는 자체 CLI를 Phase 13에서 제공. 일반 운영은 `kubectl` + CR로 충분.
- **장기 라이센스/유지보수 위험**: 자체 instance manager 코드를 영구 유지보수.
  - **완화**: CNPG가 Apache 2.0으로 공개한 코드 패턴을 참고(라이선스 호환). 핵심 로직은 ~수백 줄 수준.

## 대안 (검토 후 기각)

- **Patroni sidecar**: 외부 DCS 운영 부담, 이중 권위(K8s vs etcd) 문제
- **pg_auto_failover**: 별도 monitor 노드 운영 필요, K8s 통합 미성숙
- **Stolon**: 활발한 유지보수 부재

## 결과

- `cmd/instance/main.go` 별도 바이너리, distroless 이미지로 패키징
- 모든 RS(CSS, SS)에서 동일한 instance manager 사용
- K8s lease 명명 규칙: `<cluster>-<rs-name>-primary` (예: `orders-css-primary`, `orders-shard-a-primary`)
- 본 ADR 변경은 RFC 필수
