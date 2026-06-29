# Windows Test Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide a Windows PowerShell test wrapper that keeps Go test temp/cache outputs out of the repository and reduces Windows security scanning friction.

**Architecture:** Add one focused script under `scripts/` that normalizes `GOTMPDIR`, `GOCACHE`, Go binary discovery, optional envtest asset discovery, and common package presets. Add a lightweight PowerShell self-test mode so script behavior can be validated without running expensive Go tests.

**Tech Stack:** PowerShell 5+/7, Go test, controller-runtime envtest assets, existing Makefile/package layout.

---

### Task 1: Add Ignore Rule For Windows Go Test Binaries

**Files:**
- Modify: `.gitignore`

- [x] **Step 1: Write the failing check**

Run:

```powershell
Select-String -Path .gitignore -Pattern '^\*\.test\.exe$'
```

Expected: no match.

- [x] **Step 2: Add ignore rule**

Add:

```gitignore
*.test.exe
```

near the existing Go test binary ignore section.

- [x] **Step 3: Verify**

Run:

```powershell
Select-String -Path .gitignore -Pattern '^\*\.test\.exe$'
```

Expected: one match.

### Task 2: Add Windows Test Wrapper Script

**Files:**
- Create: `scripts/test-windows.ps1`

- [x] **Step 1: Write initial script with self-test support**

Create `scripts/test-windows.ps1` with:

- parameters: `-Preset`, `-Package`, `-Run`, `-GinkgoFocus`, `-Fresh`, `-Race`, `-Timeout`, `-DryRun`, `-SelfTest`
- repo-root detection from the script location
- Windows Go discovery from `go` on PATH or `%LOCALAPPDATA%\Programs\go1.26.4\go\bin\go.exe`
- repo-external defaults:
  - `GOTMPDIR=%LOCALAPPDATA%\keiailab\postgres-operator\go-tmp`
  - `GOCACHE=%LOCALAPPDATA%\keiailab\postgres-operator\go-cache`
- optional `KUBEBUILDER_ASSETS` fallback from `bin\k8s`
- presets:
  - `controller`: `./internal/controller`
  - `sharding`: `./cmd/instance ./internal/router ./cmd/pg-router ./cmd/reshard-copy-poc ./api/v1alpha1 ./internal/controller`
  - `unit`: `./api/... ./internal/version/... ./internal/plugin/... ./internal/instance/fencing/... ./internal/instance/supervise/...`
  - `all`: `./...`
- `-Fresh` adds `-count=1`; default omits it so Go cache can work.
- `-DryRun` prints the resolved command and environment paths without running tests.
- `-SelfTest` validates presets and path placement without invoking `go test`.

- [x] **Step 2: RED check**

Before creating the script, run:

```powershell
Test-Path scripts\test-windows.ps1
```

Expected: `False`.

- [x] **Step 3: GREEN check**

After creating the script, run:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -SelfTest
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -GinkgoFocus "Promote phase" -DryRun
```

Expected: both exit 0. Dry-run output shows `go test ./internal/controller --ginkgo.focus=Promote phase` and temp/cache paths outside the repository.

### Task 3: Document Usage In Handoff

**Files:**
- Modify: `docs/WORK_HANDOFF.ko.md`

- [x] **Step 1: Add Windows wrapper usage note**

Add a short note near the verification section:

```markdown
- Windows 로컬 테스트는 `scripts/test-windows.ps1` 를 우선 사용한다. 기본은 Go test cache 를 살리고,
  `-Fresh` 를 줄 때만 `-count=1` 을 붙인다. `GOTMPDIR`/`GOCACHE` 는 repo 밖
  `%LOCALAPPDATA%\keiailab\postgres-operator\...` 로 고정해 `*.test.exe` 가 workspace 에 남지 않게 한다.
```

- [x] **Step 2: Verify docs mention wrapper**

Run:

```powershell
Select-String -Path docs\WORK_HANDOFF.ko.md -Pattern 'test-windows.ps1'
```

Expected: one or more matches.

### Task 4: Final Verification And Commit

**Files:**
- `.gitignore`
- `scripts/test-windows.ps1`
- `docs/WORK_HANDOFF.ko.md`
- `docs/superpowers/plans/2026-06-29-windows-test-wrapper.md`

- [x] **Step 1: Run script validation**

Run:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -SelfTest
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset sharding -DryRun
```

Expected: exit 0.

- [x] **Step 2: Run a real focused test through wrapper**

Run:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-windows.ps1 -Preset controller -GinkgoFocus "Promote phase"
```

Expected: `ok github.com/keiailab/postgres-operator/internal/controller`.

- [x] **Step 3: Check repository cleanliness rules**

Run:

```powershell
git diff --check
git status --short --branch
```

Expected: no whitespace errors; only intended files modified.

- [x] **Step 4: Commit**

Run:

```powershell
git add .gitignore scripts/test-windows.ps1 docs/WORK_HANDOFF.ko.md docs/superpowers/plans/2026-06-29-windows-test-wrapper.md
git commit -m "chore(test): add windows go test wrapper"
```
