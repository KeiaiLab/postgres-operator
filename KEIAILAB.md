# keiailab 포크 운영 규칙

이 레포 = GitHub `KeiaiLab/postgres-operator` 의 keiailab 포크(Forgejo `keiailab-platform/postgres-operator`).
설계: `/Users/phil/dev/.claude/specs/2026-09-25-postgres-operator-loop-fix-design.md` §3.11.

## 기점

- GitHub main `c7608427b7c059df900ac1003c30316711ae4d17` = `v0.4.0-beta.11` + 차트 주석 1줄(`internal/ cmd/ api/` diff 0).
- 포크 브랜치 `upstream-main` = 마지막으로 받은 상류 main. `stable` = 기점 + keiailab 커밋(기본 브랜치).

## 동기

- 포크 `stable` 이 첫 자리다. 상류로는 GitHub 브랜치 `keiailab/loop-fix` 의 PR(merge commit — 해시 보존)로 보낸다.
  병합 뒤 포크는 GitHub main 을 ff 로 받는다.
- 상류 main 의 새 커밋: `upstream-main` 을 올리고 `stable` 에 병합 커밋 하나로 land-direct 착지.
- 상류 `Dockerfile`·`Makefile`·`.github/`·`.dockerignore` 는 고치지 않는다(재기반 대조용). 포크 빌드는 `Dockerfile.keiailab` 하나.

## 이미지

- 굽는 자리 = 이 맥 image-ship(등록부 `/Users/phil/dev/.claude/image-ship.toml` 절 `keiailab-platform/postgres-operator`).
  Forgejo Actions 는 끈다 — `.github/workflows` 폴백이 없는 러너 라벨을 영원히 기다린다.
- 저장소 `harbor.keiailab.com/keiailab/platform/postgres-operator` · 태그 `<sha8>`·`<epoch>-<sha8>` · linux/amd64.
- `Dockerfile.keiailab` 기본 타깃 `image` = deps → build → test 사슬 — 발행된 `<sha8>` = 그 트리의 시험 초록.
  build·test 는 `--network=none`, 의존은 Nexus Go 프록시, 베이스는 디지스트 핀 + 빌더 mirrors(Harbor). `# syntax=` 줄 금지.
- 핀: 소비자(data 차트)는 `<sha8>@sha256:<digest>` · `IfNotPresent`. `latest`·ImagePolicy·Renovate 는 쓰지 않는다.

## 게이트

- 착지 = land-direct. 검사는 등록부 land 표 `check`: `go vet` · `go test`(api·cmd·internal) ·
  `make manifests generate` 뒤 생성물 diff 0 · `go test ./test/chart/...`.
- envtest 스위트 `TestController`·`TestAPIs` 는 apiserver·etcd 가 필요해 맥·이미지에서 빼고 GitHub `test.yml`(상류 PR)이 돈다.

## 비상 정지

- data values `postgres-operator.replicas: 0` 커밋을 land-direct 로 착지한다(GitOps 라 drift 가 되살리지 않는다).
- `v0.4.0-beta.11` 로 되돌리지 않는다 — nodes 권한이 있으면 failover 루프, 없으면 컨트롤러 정지.
