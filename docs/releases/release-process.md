# 로컬 릴리스 절차

본 저장소는 ADR 0009에 따라 GitHub Actions를 사용하지 않는다. 릴리스 검증과 게시 작업은 로컬 Makefile target으로 수행한다.

## 성공 기준

[기능명] Postgres Operator 릴리스

사용자 시나리오:
1. maintainer는 `VERSION=vX.Y.Z`를 지정해 preflight를 실행한다.
2. maintainer는 lint, unit/envtest, audit, manifest, Helm, install bundle 검증 결과를 확인한다.
3. maintainer는 같은 `VERSION`으로 release를 실행한다.
4. maintainer는 GHCR image, Git tag, GitHub Release, Helm repo index가 생성됐는지 확인한다.

기대 결과:
- `make gate`가 통과한다.
- `Chart.yaml`의 `version`과 `appVersion`이 `VERSION`에서 `v`를 제거한 값과 일치한다.
- `CHANGELOG.md`에 동일 버전 항목이 있다.
- `dist/install.yaml`과 Helm `--include-crds` 렌더 결과에 CRD **8개**(`postgresclusters`, `backupjobs`, `scheduledbackups`, `poolers`, `postgresdatabases`, `postgresusers`, `imagecatalogs`, `clusterimagecatalogs`)가 포함된다.
- `bundle/manifests/postgres.keiailab.io_*.yaml` 카운트 ≥ 8 + `operator-sdk bundle validate --select-optional suite=operatorframework` 통과.
- `kube-linter lint dist/install.yaml` + helm template 모두 0 lint errors.
- Chart appVersion ↔ kustomize newTag ↔ dist image tag drift 0.
- `.github/workflows/` 비어 있음 (ADR-0009 영구 금지 enforce).
- 릴리스 전 worktree가 clean 상태다.

## Step → verify

1. 산출물 갱신
   - 실행: `make manifests generate build-installer`
   - verify: `git diff -- charts/postgres-operator/crds dist/install.yaml config/crd/bases`

2. 로컬 검증
   - 실행: `make gate`
   - verify: lint, test, audit, validate가 모두 성공한다.

3. push 없는 릴리스 검증
   - 실행: `make release-preflight VERSION=v0.1.1-alpha`
   - verify: Helm package가 `/tmp/postgres-operator-release` 아래에서 생성 후 삭제되고, worktree clean 검사가 통과한다.

4. 실제 릴리스
   - 실행: `make release VERSION=v0.1.1-alpha`
   - verify: GHCR image push, Git tag push, GitHub Release 생성, `gh-pages` Helm index 갱신이 모두 성공한다.

5. OLM bundle 재생성 + community-operators PR (선택, alpha tag 이후)
   - 실행: `make bundle VERSION=0.3.0-alpha.N` + `make bundle-build VERSION=0.3.0-alpha.N`
   - verify: `operator-sdk bundle validate ./bundle --select-optional suite=operatorframework` 통과, `docker push ghcr.io/keiailab/postgres-operator-bundle:0.3.0-alpha.N` 성공.
   - 절차 상세: [docs/operator-guide/community-operators-onboarding.md](../operator-guide/community-operators-onboarding.md).

6. Artifact Hub 등록/검색 검증
   - 전제: Artifact Hub control panel 에 Helm repository 를 `keiailab-postgres-operator` 이름으로 추가한다.
   - repository URL: `https://keiailab.github.io/postgres-operator`
   - package URL: `https://artifacthub.io/packages/helm/keiailab-postgres-operator/postgres-operator`
   - API 등록: `ARTIFACTHUB_API_KEY_ID=... ARTIFACTHUB_API_KEY_SECRET=... make artifacthub-register`
   - verify: `make artifacthub-smoke`
   - 실패 해석: Helm repository reachability 단계가 통과하고 Artifact Hub package registration 단계만 404이면, chart package 문제가 아니라 Artifact Hub 쪽 repository 미등록 또는 아직 미처리 상태다. `charts/artifacthub-repo.yml` 의 `repositoryID` 는 Artifact Hub repository card 에 표시되는 ID 와 일치해야 Verified publisher 가 붙는다.
   - 현재 상태 확인: `make artifacthub-smoke` 가 `Artifact Hub repository is not registered` 로 실패하면 `https://artifacthub.io/api/v1/repositories/search?org=keiailab&kind=0` 결과에 `https://keiailab.github.io/postgres-operator` 가 없는 상태다. 등록 후 Artifact Hub tracker 는 보통 30분 주기로 재처리하므로 새 chart version 게시 또는 tracker 대기 뒤 재검증한다.

## 수동 검증 명령

```bash
go test $(go list ./... | grep -v /test/e2e)
make lint-config && make lint
make validate
helm lint --strict charts/postgres-operator
helm template --include-crds gate charts/postgres-operator
helm package charts/postgres-operator -d /tmp/postgres-operator-release
make artifacthub-smoke
kubectl create --dry-run=client --validate=false -f dist/install.yaml
rm -rf /tmp/postgres-operator-release
```

## L3 e2e

Kind 기반 e2e는 명시 수동 게이트다. 실 dev/prod 클러스터가 아니라 전용 Kind cluster만 사용한다.

```bash
make test-e2e PILLAR=p1
```

테스트 cluster 이름은 `postgres-operator-test-e2e`이며 target 종료 시 삭제된다.
