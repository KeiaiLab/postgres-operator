# 로컬 릴리스 절차

본 저장소는 ADR 0009에 따라 GitHub Actions를 사용하지 않는다. 릴리스 검증과 게시 작업은 로컬 Makefile target으로 수행한다.

## 성공 기준

[기능명] PostgreSQL Operator 릴리스

사용자 시나리오:
1. maintainer는 `VERSION=vX.Y.Z`를 지정해 preflight를 실행한다.
2. maintainer는 lint, unit/envtest, audit, manifest, Helm, install bundle 검증 결과를 확인한다.
3. maintainer는 같은 `VERSION`으로 release를 실행한다.
4. maintainer는 GHCR image, Git tag, GitHub Release, Helm repo index가 생성됐는지 확인한다.

기대 결과:
- `make gate`가 통과한다.
- `Chart.yaml`의 `version`과 `appVersion`이 `VERSION`에서 `v`를 제거한 값과 일치한다.
- `CHANGELOG.md`에 동일 버전 항목이 있다.
- `dist/install.yaml`과 Helm `--include-crds` 렌더 결과에 CRD 2개가 포함된다.
- 릴리스 전 worktree가 clean 상태다.

## Step → verify

1. 산출물 갱신
   - 실행: `make manifests generate build-installer`
   - verify: `git diff -- charts/postgresql-operator/crds dist/install.yaml config/crd/bases`

2. 로컬 검증
   - 실행: `make gate`
   - verify: lint, test, audit, validate가 모두 성공한다.

3. push 없는 릴리스 검증
   - 실행: `make release-preflight VERSION=v0.1.1-alpha`
   - verify: Helm package가 `/tmp/postgres-operator-release` 아래에서 생성 후 삭제되고, worktree clean 검사가 통과한다.

4. 실제 릴리스
   - 실행: `make release VERSION=v0.1.1-alpha`
   - verify: GHCR image push, Git tag push, GitHub Release 생성, `gh-pages` Helm index 갱신이 모두 성공한다.

## 수동 검증 명령

```bash
go test $(go list ./... | grep -v /test/e2e)
make lint-config && make lint
make validate
helm lint --strict charts/postgresql-operator
helm template --include-crds gate charts/postgresql-operator
helm package charts/postgresql-operator -d /tmp/postgres-operator-release
kubectl apply --dry-run=client --validate=false -f dist/install.yaml
rm -rf /tmp/postgres-operator-release
```

## L3 e2e

Kind 기반 e2e는 명시 수동 게이트다. 실 dev/prod 클러스터가 아니라 전용 Kind cluster만 사용한다.

```bash
make test-e2e PILLAR=p1
```

테스트 cluster 이름은 `postgresql-operator-test-e2e`이며 target 종료 시 삭제된다.
