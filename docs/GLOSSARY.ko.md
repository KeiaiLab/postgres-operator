# 용어집 (Glossary · 적요)

> ⚖️ **이 문서가 용어 정의의 단일 출처(SSOT)다.** 다른 문서 마지막 장의 "용어집" 절은 여기 정의를 **그대로 발췌**해 어디서 보든 설명이 동일하게 유지한다. 정의를 고칠 때는 이 문서를 먼저 고치고, 발췌 측을 맞춘다.
>
> 작성 지침: [`AGENTS.md`](../AGENTS.md) "문서 작성 (docs/)" · [`DOCS_MAP.ko.md`](DOCS_MAP.ko.md) §3.

---

## HA · Failover

| 용어 | 정의 |
|---|---|
| Operator | Kubernetes 위에서 애플리케이션(여기선 PostgreSQL)의 생성·운영·복구를 CRD + 컨트롤러로 자동화하는 소프트웨어. |
| Failover (장애 조치) | Primary 장애 감지 후 Replica 하나를 새 Primary로 자동 승격해 서비스를 잇는 동작. |
| Switchover (계획 전환) | 장애가 아닌 의도된 상황에서 Primary를 다른 인스턴스로 무중단 전환하는 동작. |
| Promotion (승격) | Replica를 Primary로 올리는 행위. 본 operator는 `pg_promote()`(SQL)로 수행. |
| Failback guard | 막 강등/격리된 옛 Primary가 곧바로 다시 승격 후보가 되는 것을 막는 가드. |
| Fencing (PVC Fencing) | 옛/이상 Primary가 데이터에 쓰지 못하도록 PVC 접근을 차단해 split-brain을 막는 격리. |
| Debounce (디바운스) | 일시적 신호로 인한 오탐 failover를 막기 위해 장애를 일정 시간(기본 8초) 유지될 때만 인정하는 대기. |
| Lease (임대) | Kubernetes Lease 오브젝트. 본 operator는 외부 HA 에이전트 없이 이를 DCS로 써서 Primary 선출을 한다. |
| DCS (Distributed Configuration Store) | HA 상태 합의를 저장하는 분산 저장소. 본 operator는 K8s API(Lease)를 DCS로 사용. |
| Rogue Primary | 정상 승격 절차 밖에서 자신이 Primary라 여기는 이상 인스턴스. 감지 시 re-seed로 정리. |
| Stale Replica | WAL 수신이 뒤처져 최신 상태가 아닌 복제본. |
| Re-seed (재시드) | 뒤처지거나 이상한 인스턴스의 데이터를 새 Primary 기준으로 다시 복제해 정상화. |
| Split-brain | Primary가 둘 이상 존재해 데이터가 갈라지는 장애. fencing으로 예방. |
| RTO (Recovery Time Objective) | 장애에서 서비스 복구까지 허용되는 목표 시간. 본 프로젝트 failover 드릴 기준 30초. |
| Pod readiness | Pod/컨테이너가 트래픽을 받을 준비가 됐는지의 K8s 상태. 승격 후보 검증에 사용. |

## 백업 · 복구 (PITR)

| 용어 | 정의 |
|---|---|
| PITR (Point-In-Time Recovery) | WAL을 재생해 데이터베이스를 **특정 과거 시점**으로 복원하는 기법. |
| WAL (Write-Ahead Log) | 변경을 먼저 기록하는 PostgreSQL의 로그. 복제·PITR의 기반. |
| WAL archiving | WAL 세그먼트를 백업 저장소로 지속 보관하는 것(`archive-push`). |
| pgBackRest | 본 operator의 기본 백업 도구(플러그인). WAL-G·Barman은 대체 플러그인. |
| restore_command | 복구 시 아카이브된 WAL을 가져오는 PostgreSQL 설정 명령. |
| targetTime | PITR가 복원할 목표 시각. 본 구현은 microsecond 정밀도 보존. |
| ScheduledBackup | 크론 주기로 BackupJob을 자동 생성하는 CRD. |
| Repo (백업 저장소) | pgBackRest가 백업/WAL을 두는 저장소. 본 구현은 data PVC 내부 경로 사용. |
| 오프라인 복원 | STS를 scale-0으로 내려 Pod 정지 후 data PVC를 마운트해 복원하는 방식. |

## Kubernetes · 운영

| 용어 | 정의 |
|---|---|
| CRD (Custom Resource Definition) | K8s API를 확장하는 사용자 정의 리소스 타입(PostgresCluster 등). |
| Reconcile / Reconciler | 실제 상태를 원하는 상태(spec)로 수렴시키는 controller-runtime 루프. |
| controller-runtime | Kubebuilder 기반 컨트롤러 작성 라이브러리. |
| StatefulSet (STS) | 안정적 식별자·스토리지를 갖는 Pod 집합. PostgreSQL 인스턴스를 띄우는 워크로드. |
| PVC (PersistentVolumeClaim) | Pod에 영속 스토리지를 붙이는 K8s 요청 오브젝트. |
| Admission Webhook | 리소스 생성/수정 시 검증(validating)·기본값(defaulting)을 수행하는 진입점. |
| Finalizer | 리소스 삭제 전에 정리 로직을 보장하기 위한 K8s 메커니즘. |
| Annotation | 오브젝트에 붙이는 비식별 메타데이터. 본 operator는 restore-in-progress 락 등에 사용. |
| Hibernation | 클러스터를 STS scale-0으로 내려 PVC는 보존한 채 휴면시키는 기능. |
| CEL validation | CRD 스키마에서 표현식으로 값 제약을 거는 검증(예: 보호된 이름 차단). |
| Helm chart | K8s 매니페스트를 패키징·배포하는 단위. operator 배포에 사용. |
| envtest | 실제 클러스터 없이 API 서버/etcd만 띄워 컨트롤러를 통합 테스트하는 도구. |
| kind | Docker 컨테이너 안에 K8s 클러스터를 띄우는 도구. e2e 테스트에 사용. |
| DinD (Docker-in-Docker) | 컨테이너 안에서 또 Docker 데몬을 돌리는 방식. 중첩 시 cgroup 한계 발생 가능. |
| DooD (Docker-out-of-Docker) | 컨테이너가 호스트의 Docker 데몬(소켓)을 공유해 쓰는 방식. |

## 분산 SQL · 샤딩 (로드맵)

| 용어 | 정의 |
|---|---|
| ShardRange | 샤드 범위를 정의하는 CRD. 분산 메타데이터의 source of truth. |
| ShardSplitJob | 온라인으로 샤드를 분할하는 작업 CRD. |
| AutoSplit | 임계치 도달 시 샤드 분할을 자동 트리거. |
| vindex | 라우팅 키를 샤드로 해석하는 인덱스(쿼리 라우터가 소비). |
| pg-router | PG wire-protocol v3 기반 쿼리 라우터 프록시(PoC). |
| 2PC / saga | 크로스 샤드 분산 트랜잭션을 위한 자체 구현 방식(외부 확장 비의존). |
| Replica Cluster | 외부 클러스터를 streaming standby로 복제하는 구성. |

## 프로젝트 · 문서

| 용어 | 정의 |
|---|---|
| G0~G6 게이트 | 로드맵 단계 게이트(G1 단일 샤드 HA, G2 운영 품질, G3 샤딩 기반 등). |
| SSOT (Single Source of Truth) | 한 사실을 한 곳에만 두고 나머지는 링크/발췌하는 단일 출처 원칙. |
| ADR (Architecture Decision Record) | 설계 결정과 근거를 남기는 문서. |
| RFC (Request for Comments) | 단계별 설계 제안 문서. |
| RCA (Root Cause Analysis) | 장애·실패의 근본 원인 분석. |
| 게이트(make gate) | lint + test + audit + validate를 묶은 릴리스 품질 게이트. |

---

> 새 용어가 문서에 등장하면 여기에 한 줄 정의로 추가하고, 그 문서 마지막 장 "용어집" 절에 발췌한다.
