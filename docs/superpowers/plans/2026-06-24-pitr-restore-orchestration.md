# PITR Restore Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `BackupJob.spec.type=restore` perform a real pgBackRest PITR restore instead of execing restore against a running primary.

**Architecture:** Filesystem pgBackRest repositories must survive Pod restarts, so the shard StatefulSet mounts the repo path from the data PVC instead of `EmptyDir`. A restore BackupJob stops the shard Pod by scaling the shard StatefulSet to zero, runs a Kubernetes restore Job that mounts the same PVC, then scales the StatefulSet back up and marks the BackupJob succeeded only after the restore Job completes.

**Tech Stack:** Go, controller-runtime fake client/envtest patterns, Kubernetes StatefulSet/Job/PVC, pgBackRest command plugin, Ginkgo E2E.

---

### Task 1: Persistent Filesystem pgBackRest Repo

**Files:**
- Modify: `internal/controller/builders.go`
- Test: `internal/controller/builders_test.go`

- [ ] **Step 1: Write the failing test**

Add a test that builds a shard StatefulSet and asserts the postgres container mounts the `data` PVC at `/var/lib/pgbackrest` with `SubPath: "pgbackrest"`, and that no `pgbackrest-repo` `EmptyDir` remains.

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
docker run --rm -v "${PWD}:/workspace" -v go-mod-cache:/go/pkg/mod -v go-build-cache:/root/.cache/go-build -w /workspace golang:1.26 bash -lc 'export PATH=/usr/local/go/bin:$PATH; go test -count=1 ./internal/controller -run TestBuildPGStatefulSet_PgBackRestRepoUsesDataPVC -v'
```

Expected: FAIL because the repo is currently mounted from `pgbackrest-repo` `EmptyDir`.

- [ ] **Step 3: Implement minimal production change**

Remove the pgBackRest repo from `dataplaneEphemeralVolumeMounts()` / `dataplaneEphemeralVolumes()` and add a StatefulSet-only mount:

```go
{Name: "data", MountPath: backupRepoMountPath, SubPath: "pgbackrest"}
```

- [ ] **Step 4: Run test to verify it passes**

Run the same `go test` command. Expected: PASS.

### Task 2: Restore Runner Job Builder

**Files:**
- Modify: `internal/controller/backupjob_controller.go`
- Test: `internal/controller/backupjob_controller_test.go`

- [ ] **Step 1: Write the failing test**

Add a test for a running sidecar restore BackupJob where the shard StatefulSet already has replicas `0`. The reconciler should create an owned restore Job that mounts PVC `data-<cluster>-shard-0-0` at both `/var/lib/postgresql/data` and `/var/lib/pgbackrest`.

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
docker run --rm -v "${PWD}:/workspace" -v go-mod-cache:/go/pkg/mod -v go-build-cache:/root/.cache/go-build -w /workspace golang:1.26 bash -lc 'export PATH=/usr/local/go/bin:$PATH; go test -count=1 ./internal/controller -run TestBackupJobReconcile_RunningSidecarRestoreCreatesRestoreJob -v'
```

Expected: FAIL because current `reconcileSidecar` execs into the primary Pod and does not create a Job.

- [ ] **Step 3: Implement minimal production change**

Add a restore-specific branch before the existing sidecar backup exec path. The branch should create a `batch/v1.Job` named with `backupRunnerJobName(bj.Name)`, use the source StatefulSet postgres image, mount the data PVC, and run `BackupCommandPlugin.RestoreCommand`.

- [ ] **Step 4: Run test to verify it passes**

Run the same `go test` command. Expected: PASS.

### Task 3: Restore StatefulSet Stop/Start State Machine

**Files:**
- Modify: `internal/controller/backupjob_controller.go`
- Test: `internal/controller/backupjob_controller_test.go`

- [ ] **Step 1: Write failing tests**

Add tests for three observable states:

1. StatefulSet replicas `1` -> reconciler scales to `0`, condition reason `RestoreClusterStopping`, no Job yet.
2. restore Job `Complete=True` -> reconciler scales StatefulSet back to `1`, condition reason `RestoreSucceeded`.
3. restore Job `Failed=True` -> BackupJob phase `Failed`, condition reason `RestoreFailed`.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
docker run --rm -v "${PWD}:/workspace" -v go-mod-cache:/go/pkg/mod -v go-build-cache:/root/.cache/go-build -w /workspace golang:1.26 bash -lc 'export PATH=/usr/local/go/bin:$PATH; go test -count=1 ./internal/controller -run TestBackupJobReconcile_RunningSidecarRestore -v'
```

Expected: FAIL until the state machine handles all three states.

- [ ] **Step 3: Implement minimal production change**

Implement restore orchestration by inspecting Kubernetes objects instead of adding new CRD status fields:

1. Scale shard-0 StatefulSet to `0` if needed.
2. Wait until shard-0 Pods are gone.
3. Create or observe the restore Job.
4. On Job completion, scale StatefulSet back to original single-shard member count.
5. Mark BackupJob terminal only after Job completion.

- [ ] **Step 4: Run tests to verify they pass**

Run the same `go test` command. Expected: PASS.

### Task 4: E2E PITR Drill

**Files:**
- Modify: `test/e2e/pitr_restore_e2e_test.go`
- Modify: `docs/E2E_TEST_REPORT.ko.md`

- [ ] **Step 1: Update the E2E fixture**

Enable `spec.backup.enabled=true` in the PITR fixture so WAL archiving is active before the full backup and target-time restore drill.

- [ ] **Step 2: Flip PITR restore `PContext` to `Context`**

Only flip after unit tests for the restore state machine pass.

- [ ] **Step 3: Run targeted live E2E**

Run:

```bash
docker run --rm --privileged --network host -v /var/run/docker.sock:/var/run/docker.sock -v "${PWD}:/workspace" -v go-mod-cache:/go/pkg/mod -v go-build-cache:/root/.cache/go-build -w /workspace golang:1.26 bash -c 'set -euo pipefail; export PATH=/usr/local/go/bin:$PATH; export DEBIAN_FRONTEND=noninteractive; apt-get update >/tmp/apt-update.log; apt-get install -y docker.io >/tmp/apt-install.log; bash .devcontainer/post-install.sh >/tmp/post-install.log; export KIND_CLUSTER=postgres-operator-e2e-pitr-codex; export CERT_MANAGER_INSTALL_SKIP=true; make setup-test-e2e; kind export kubeconfig --name "$KIND_CLUSTER"; go test -tags=e2e ./test/e2e -timeout 35m -v -ginkgo.v -ginkgo.label-filter=p1 -ginkgo.focus="PITR restore"'
```

Expected: restore/checksum specs pass and only unrelated Pending remains.

### Self-Review

- Spec coverage: covers repo persistence, restore execution away from running primary, stop/start orchestration, and E2E Pending release.
- Placeholder scan: no TBD/TODO steps remain.
- Type consistency: uses existing `BackupJobPhase`, `RunnerJobName`, StatefulSet, Job, and condition reason patterns without adding new CRD fields.
