# CNPG → Keiailab Postgres Operator: Gap 분석

> **언어**: [English](GAP-ANALYSIS.md) | [한국어](GAP-ANALYSIS.ko.md) | [日本語](GAP-ANALYSIS.ja.md) | [中文](GAP-ANALYSIS.zh.md)

## 요약

본 문서는 keiailab 프로덕션 클러스터에서 현재 운영 중인 CloudNativePG (CNPG) v1.29과
keiailab/postgres-operator v0.3.0-alpha.18의 기능을 비교합니다.
목표: CNPG를 교체하기 전에 해결해야 할 기능 격차를 식별합니다.

**주요 결과:**
- 16개 핵심 컴포넌트 중 12개가 완전 구현됨
- 5개 P0 (필수) 격차가 프로덕션 교체를 차단
- 3개 추가 P1 격차가 운영 신뢰성에 필요
- 예상 소요: 8 스프린트
- 배포 준비도: 7.5/10 (개발 즉시 가능, 프로덕션은 강화 필요)

---

## Gap 매트릭스

### P0 — 프로덕션 차단 (교체 불가)

| # | 격차 | CNPG 해당 기능 | 소요 | 스프린트 |
|---|------|---------------|------|----------|
| 1 | WAL 아카이빙 + 오브젝트 스토어 | barmanObjectStore + CNPG-I | ✅ Done (#127) | S3 |
| 2 | 오브젝트 스토어 PITR | spec.backup.recovery | 1주 | S4 |
| 3 | TLS Phase 3 (마운트 + ssl=on) | 기본 동작 | 3일 | S1 |
| 4 | postgresql.conf 핫 리로드 | pg_reload_conf() | ✅ Done (#126) | S2 |
| 5 | 백업 보존 정리 | retentionPolicy | ✅ Done (#130) | S5 |

### P1 — 운영 신뢰성

| # | 격차 | CNPG 해당 기능 | 소요 | 스프린트 |
|---|------|---------------|------|----------|
| 6 | Switchover | cnpg promote | ✅ Done (#130) | S5 |
| 7 | Fencing | cnpg fencing on/off | 3일 | S6 |
| 8 | 동기 복제 | syncReplicas | 2일 | S6 |
| 9 | pg_hba.conf 리로드 | Config 리로드 | ✅ Done (#126) | S2 |
| 10 | 커스텀 PG 파라미터 | spec.postgresql.parameters | ✅ Done (#126) | S2 |

### P2 — 개선 (교체 비차단)

| # | 격차 | 비고 |
|---|------|------|
| 11 | 볼륨 스냅샷 | 백업 가속 |
| 12 | 메이저 업그레이드 | pg_upgrade |
| 13 | 지연 복제 | recovery_min_apply_delay |
| 14 | 테이블스페이스 PVC | 다중 PVC |
| 15 | 커스텀 모니터링 | Exporter 확장 |

---

## MVP 정의

**CNPG 교체 최소 조건:** P0 (5개) + P1의 6, 7, 10 = **총 8개 기능**

---

## 마이그레이션 로드맵

| 스프린트 | 초점 | E2E 검증 |
|----------|------|----------|
| S1 | TLS Phase 3 | psql sslmode=verify-full 성공 |
| S2 | Config 핫 리로드 | SHOW 결과 spec 변경 반영 |
| S3 | WAL 아카이브 + 백업 | WAL 세그먼트 오브젝트 스토어 도착 |
| S4 | PITR 복원 | 시점 복원 + 데이터 검증 |
| S5 | 보존 + Switchover | 정리 수행 + primary 전환 |
| S6 | Fencing + 동기 복제 | Split-brain 시뮬레이션 통과 |
| S7 | 통합 테스트 | keiailab 클러스터 전체 e2e |
| S8 | CNPG 교체 | 무중단 마이그레이션 |
