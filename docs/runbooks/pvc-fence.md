# PVC Fence Runbook

> ROADMAP G1 §76 PVC fencing (split-brain fail-fast). 본 runbook 은
> `internal/controller/failover/pvc_fence_runbook.go` (`DecidePVCFence`)
> 의 결정 분기와 *1:1 대응*한다. 코드 변경 시 본 문서도 함께 갱신.

## 1. 목적과 범위

operator-driven only 정책 (T30, `executeClusterPromotion` 단일 경로) 으로
*control-plane* 차원의 split-brain 은 차단된다. 그러나 *data-plane* 차원
(volume attach / CSI driver / lease propagation) 의 잔존 위험을 차단하기
위해 PVC level fencing 이 추가 layer 로 필요하다.

본 runbook 의 범위:

- 어떤 신호로 fencing 을 trigger 하는가
- 어떤 절차로 fencing 을 적용·해제하는가
- 각 사유별 사후 분석 (post-mortem) 절차

## 2. Fencing trigger 분류

| 사유 (`PVCFenceReason`) | 신호 | 위험도 | 권장 대응 |
|---|---|---|---|
| `MultiAttach` | CSI controller 가 RWO PVC 의 multi-attach 보고 | **CRITICAL** | 즉시 fence 모든 Pod, storage class 점검 |
| `SplitBrain` | 동일 cluster 의 2+ Pod 가 `instance-role=primary` + ready=true | **HIGH** | lease holder 만 보존, 나머지 fence |
| `StaleLease` (PromotionRace) | lease renewTime + duration 초과 + 관찰 primary ≠ holder | **MEDIUM** | promotion 잔재 fence + lease 강제 갱신 검토 |
| (정상) | 1 primary + holder identity 일치 | — | 동작 없음, audit trail 만 기록 |

## 3. Fence 적용 절차

operator 가 `DecidePVCFence` 결과 `ShouldFence=true` 항목에 대해 자동으로
다음을 수행한다:

1. **라벨 부착**: `kubectl label pvc <name> postgres.keiailab.io/fenced=true`
2. **Pod evict**: 해당 PVC 를 마운트한 Pod 를 `kubectl delete pod <name> --grace-period=0 --force`
   - StatefulSet 가 자동 재생성하지만, PVC fenced 라벨로 Pod predicate 가 *재마운트 차단* (별 admission webhook)
3. **상태 기록**: `PostgresCluster.status.conditions` 에 `type=Fenced` + reason 추가
4. **이벤트**: `kubectl events --for postgrescluster/<name>` 에 `Warning Fenced ...` 발행

수동 검증 명령:

```bash
kubectl get pvc -l postgres.keiailab.io/fenced=true
kubectl describe postgrescluster <name> | grep -A5 Fenced
kubectl events --for postgrescluster/<name> --types=Warning | grep Fence
```

## 4. Fence 해제 절차

fencing 사유가 해결된 후 운영자가 명시적으로 해제한다. *자동 해제 금지* —
사후 분석 + 인적 검증 필수.

```bash
# 1. 데이터 무결성 검증 (해당 PVC 데이터 손상 여부)
kubectl exec <surviving-primary> -- pg_checksums --check
kubectl exec <surviving-primary> -- pg_dump -Fc -f /tmp/snapshot.dump <db>

# 2. 사후 분석 작성 (docs/kb/incident/INC-NNNN-...md)
#    + 5 Whys + Timeline + Resolution + Prevention

# 3. fencing 라벨 제거
kubectl label pvc <name> postgres.keiailab.io/fenced-

# 4. PVC 가 재사용 가능한지 (orphan 아닌지) 확인 후 StatefulSet rolling restart
kubectl rollout restart statefulset/<cluster>-shard-0
```

## 5. 사유별 사후 분석 가이드

### 5.1 MultiAttach

**근본 원인 후보**:

- StorageClass 의 access mode 설정 오류 (RWX 인데 RWO 가정)
- CSI driver 버그 (attach detach race)
- 노드 장애 + force delete pod 후 stale attachment

**조치**:

1. StorageClass 의 `accessModes` 검증 — `kubectl get sc <name> -o yaml | yq '.accessModes'`
2. CSI driver 버전 확인 — operator 호환 매트릭스 (ADR-0006 참조)
3. force delete 절차 점검 — `kubectl delete pod --force` 사용 빈도 audit

### 5.2 SplitBrain

**근본 원인 후보**:

- operator manager 다중 leader (election lease 의도치 않은 다중 보유)
- promotion 잔재 (이전 primary 의 `instance-role` 라벨 미정리)
- 시계 skew (lease renewTime 계산 오류)

**조치**:

1. operator manager Pod 의 leader-election lease 검사:
   ```bash
   kubectl get lease -n <operator-ns> postgres-operator-failover-leader -o yaml
   ```
2. 모든 PostgresCluster Pod 의 `instance-role` 라벨 점검
3. NTP / 시계 sync 검증 (`chronyc tracking` on each node)

### 5.3 StaleLease (PromotionRace)

**근본 원인 후보**:

- operator restart 직후 election 미완료 상태에서 primary 잔재
- Lease 갱신 실패 (API server 일시 장애)
- network partition (operator manager ↔ apiserver)

**조치**:

1. operator manager 로그에서 `lost leader` / `started leader` 이벤트 시계열 확인
2. apiserver audit log 에서 lease PATCH 실패 검색
3. partition 가능성 → CNI / network plugin 상태 점검

## 6. 자동 테스트 + 회귀 가드

본 runbook 의 결정 분기는 `internal/controller/failover/pvc_fence_runbook_test.go`
의 `TestPVCFenceRunbook` 가 4 분기 + helper 1 = 5 sub-test 로 cover.

실행:

```bash
go test ./internal/controller/failover -run TestPVCFenceRunbook -v
```

새 fencing 사유 추가 시:

1. `PVCFenceReason` 상수 추가
2. `DecidePVCFence` 분기 추가
3. 본 §2 표 + §5 사유별 가이드 추가
4. `TestPVCFenceRunbook` 에 sub-test 추가

## 7. 메트릭 + 알람

operator 가 노출하는 메트릭:

- `postgres_operator_pvc_fence_decisions_total{reason=...}` — counter
- `postgres_operator_pvc_fenced_count` — gauge (현재 fenced PVC 수)

Helm 의 `PrometheusRule` 권장 (G2 참조):

```yaml
- alert: PostgresPVCFenced
  expr: postgres_operator_pvc_fenced_count > 0
  for: 1m
  labels: { severity: critical }
  annotations:
    summary: "PVC fencing 활성 — split-brain / multi-attach 위험"
```

## 8. References

- ROADMAP.md G1 L76 (PVC fencing)
- `internal/controller/failover/pvc_fence_runbook.go` — `DecidePVCFence`
- `internal/controller/failover/pvc_fence_runbook_test.go` — `TestPVCFenceRunbook`
- ADR-0006 — Repmgr/PgBouncer/Barman 통합 (storage class 호환성)
- T30 — HA bootstrap fence race (operator-driven only 결정 근거)
- INC 작성 시 — `standards/incident-kb.md` 의 Postmortem-lite 형식
