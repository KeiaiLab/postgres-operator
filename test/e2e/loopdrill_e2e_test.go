//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// kind 훈련장 진입점 — make test-e2e-loopdrill(설계 2026-09-25 §3.12).
//
//	kind 기동(KEEP=1 재사용) ─ 저장·CRD ─ 탐침 P1·P2 ─ 시험 이미지 싣기
//	  └ 시나리오마다: (RED 는 새로 설치) 수렴 ─ 표식 행 ─ before ─ 유발 ─ 창 ─ after ─ Judge ─ 체크포인트
//	  └ 판정문(LOOPDRILL_REPORT) ─ RED: S4 J1∨J2∨J3∨J5 ∧ S5 J4 붉음 · GREEN: 전부 초록

const (
	envExpect       = "LOOPDRILL_EXPECT"
	envChartDir     = "LOOPDRILL_CHART_DIR"
	envBaseChartDir = "LOOPDRILL_BASE_CHART_DIR"
	envOperator     = "LOOPDRILL_OPERATOR_IMAGE"
	envReport       = "LOOPDRILL_REPORT"
	envScenarios    = "LOOPDRILL_SCENARIOS"
	envStateDir     = "LOOPDRILL_STATE_DIR"
	envKeep         = "KEEP"

	// S0 의 기점 = 운영이 도는 ghcr v0.4.0-beta.11(e997c262) 인덱스, Harbor ghcr-proxy.
	baselineOperatorImage = "harbor.keiailab.com/ghcr-proxy/keiailab/postgres-operator:v0.4.0-beta.11@sha256:29c503ffc16c3347d9b6912a9994d17c069667c89efe0a738e6e472ae551869d"
	baseChartRef          = "origin/stable"
	verdictDir            = "test/e2e/loopdrill/verdicts"
	drillDir              = "test/e2e/loopdrill"
	judgeDir              = "internal/loopdrill"
	drillDriverGlob       = "test/e2e/loopdrill*_test.go"
	drillHelpers          = "test/e2e/helpers.go"
	kindConfig            = "kind-config.yaml"
	storageNote           = "csi-driver-nfs v4.13.4 · nfs-retain(Retain) · VolumeSnapshotClass nfs-snapshot(Delete)"

	// preHoldDataCommit 은 data ST0(fe7c2ba) 의 부모 — RED 차트 트리 핀.
	// ST0 뒤 data stable 은 오퍼레이터 0 복제라 기점 루프·정지가 재현되지 않는다.
	preHoldDataCommit = "56d79387a723284b99f3899420a5daf86161113b"
)

// 판정문 결과.
const (
	resultRedAsExpected  = "red-as-expected"
	resultRedMissing     = "red-not-reproduced"
	resultGreen          = "green"
	resultNotGreen       = "not-green"
	resultProbeFailed    = "p1-false"
	resultScenarioMissed = "incomplete"
)

var (
	redScenarios   = []loopdrill.Scenario{loopdrill.ScenarioS4, loopdrill.ScenarioS5}
	greenScenarios = []loopdrill.Scenario{loopdrill.ScenarioS0, loopdrill.ScenarioS1, loopdrill.ScenarioS2, loopdrill.ScenarioS3, loopdrill.ScenarioS4, loopdrill.ScenarioS5, loopdrill.ScenarioS5b, loopdrill.ScenarioS6, loopdrill.ScenarioS7}
)

// Fidelity 는 시험 이미지가 운영과 얼마나 같은가.
type Fidelity string

const (
	FidelityPublished Fidelity = "published-amd64" // 발행 digest 그대로, 노드는 amd64 에뮬레이션
	FidelityLocal     Fidelity = "local-arm64"     // 같은 sha 를 이 맥에서 arm64 로 구움(push 0)
)

// fidelityNotes 는 kind 가 운영과 다른 자리(설계 §7 위험 9).
var fidelityNotes = []string{
	"amd64 이미지는 arm64 노드에서 binfmt 에뮬레이션",
	"저장은 NFS CSI(운영 Ceph RBD 아님)",
	"CiliumNetworkPolicy 는 CRD 만 — 집행 0",
	"kubeadm kind(운영 k3s 아님) · control-plane 1대",
}

type drillConfig struct {
	mode         loopdrill.Mode
	chartDir     string
	baseChartDir string
	operator     imageRef
	report       string
	scenarios    []loopdrill.Scenario
	keep         bool
	stateDir     string
}

func loadConfig() (drillConfig, error) {
	cfg := drillConfig{mode: loopdrill.Mode(os.Getenv(envExpect)), keep: os.Getenv(envKeep) == "1"}
	if cfg.mode != loopdrill.ModeRed && cfg.mode != loopdrill.ModeGreen {
		return cfg, fmt.Errorf("%s 는 red 또는 green: %q", envExpect, cfg.mode)
	}
	cfg.chartDir = os.Getenv(envChartDir)
	if cfg.chartDir == "" {
		return cfg, fmt.Errorf("%s(data 체크아웃의 postgres-operator/) 가 필요하다", envChartDir)
	}
	var err error
	if cfg.operator, err = parseImageRef(os.Getenv(envOperator)); err != nil {
		return cfg, fmt.Errorf("%s: %w", envOperator, err)
	}
	cfg.scenarios = redScenarios
	if cfg.mode == loopdrill.ModeGreen {
		cfg.scenarios = greenScenarios
	}
	if raw := os.Getenv(envScenarios); raw != "" {
		cfg.scenarios = nil
		for _, s := range strings.Split(raw, ",") {
			cfg.scenarios = append(cfg.scenarios, loopdrill.Scenario(strings.TrimSpace(s)))
		}
	}
	cfg.baseChartDir = os.Getenv(envBaseChartDir)
	cfg.report = os.Getenv(envReport)
	cfg.stateDir = os.Getenv(envStateDir)
	if cfg.stateDir == "" {
		cfg.stateDir = filepath.Join(os.TempDir(), "loopdrill-"+clusterName)
	}
	return cfg, nil
}

// Inputs 는 판정문이 결속하는 입력(설계 §3.12 — digest · pg-drill 과 인스턴스 매니저 기점 · fidelity · data 커밋 · 트리 · kind 노드 판 · 저장).
type Inputs struct {
	OperatorImage    string   `json:"operatorImage"`
	OperatorRun      string   `json:"operatorRun"`
	PGImage          string   `json:"pgImage"` // pg-drill 이미지 ID
	PGRun            string   `json:"pgRun"`
	PGInstanceCommit string   `json:"pgInstanceCommit"` // pg-drill 인스턴스 매니저를 구운 기점
	Fidelity         Fidelity `json:"fidelity"`
	DataCommit       string   `json:"dataCommit"`
	DataTree         string   `json:"dataTree"`
	BaseDataTree     string   `json:"baseDataTree,omitempty"`
	KindNode         string   `json:"kindNode"`
	KindVersion      string   `json:"kindVersion"`
	Storage          string   `json:"storage"`
	DrillDigest      string   `json:"drillDigest"`
	Notes            []string `json:"notes"`
}

// ScenarioRun 은 시나리오 하나의 기록.
type ScenarioRun struct {
	loopdrill.Verdict
	Expect loopdrill.Expect      `json:"expect"`
	Before loopdrill.ClusterSnap `json:"before"`
	After  loopdrill.ClusterSnap `json:"after"`
}

// DrillReport 는 판정문.
type DrillReport struct {
	Expect     loopdrill.Mode `json:"expect"`
	Result     string         `json:"result"`
	Inputs     Inputs         `json:"inputs"`
	Probes     ProbeResult    `json:"probes"`
	Scenarios  []ScenarioRun  `json:"scenarios"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt time.Time      `json:"finishedAt"`
}

// checkpoint 는 KEEP=1 재개용 — 같은 입력일 때만 이어 쓴다.
type checkpoint struct {
	Key    string        `json:"key"`
	Probes *ProbeResult  `json:"probes,omitempty"`
	Runs   []ScenarioRun `json:"runs"`
}

type drill struct {
	t          *testing.T
	cfg        drillConfig
	kubeconfig string
	inputs     Inputs
	operatorOn imageRef // 노드에 실은 시험 오퍼레이터
	pgOn       imageRef // 노드에 실은 시험 pg
	apiCIDR    string
	operator   operatorRef
	gauge      *gaugeScraper
}

func TestLoopDrill(t *testing.T) {
	if os.Getenv(envExpect) == "" {
		t.Skip(envExpect + " 없음 — make test-e2e-loopdrill 로 돈다")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	d := &drill{t: t, cfg: cfg}
	report, err := d.run()
	if report != nil {
		if werr := d.writeReport(report); werr != nil {
			t.Errorf("판정문 쓰기: %v", werr)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	want := resultRedAsExpected
	if cfg.mode == loopdrill.ModeGreen {
		want = resultGreen
	}
	if report.Result != want {
		t.Fatalf("판정문 결과 %s(기대 %s) — %s", report.Result, want, d.reportPath(report))
	}
}

func (d *drill) run() (*DrillReport, error) {
	started := time.Now()
	if err := d.prepareState(); err != nil {
		return nil, err
	}
	if err := d.ensureCluster(); err != nil {
		return nil, err
	}
	if !d.cfg.keep {
		d.t.Cleanup(func() { _ = teardownCluster(d.kubeconfig) })
	}
	if err := d.bindInputs(); err != nil {
		return nil, err
	}
	cp := d.loadCheckpoint()

	// 탐침 — P1 거짓이면 설계 재검토(판정문에 남기고 멈춘다)
	if cp.Probes == nil {
		probes, err := runProbes()
		if err != nil {
			return nil, fmt.Errorf("탐침: %w", err)
		}
		cp.Probes = &probes
		d.saveCheckpoint(cp)
	}
	report := &DrillReport{Expect: d.cfg.mode, Inputs: d.inputs, Probes: *cp.Probes, StartedAt: started}
	if !cp.Probes.NewPodLatest {
		report.Result, report.FinishedAt = resultProbeFailed, time.Now()
		return report, errors.New("P1 거짓 — 새 파드가 ConfigMap 최신 값을 env 로 받지 않는다(설계 재검토)")
	}

	if err := d.loadImages(*cp.Probes); err != nil {
		return nil, err
	}
	report.Inputs = d.inputs

	for _, scn := range d.cfg.scenarios {
		if slices.ContainsFunc(cp.Runs, func(r ScenarioRun) bool { return r.Scenario == scn }) {
			continue
		}
		run, err := d.scenario(scn)
		if err != nil {
			report.Scenarios, report.FinishedAt = cp.Runs, time.Now()
			return report, fmt.Errorf("%s: %w", scn, err)
		}
		cp.Runs = append(cp.Runs, run)
		d.saveCheckpoint(cp)
		d.t.Logf("%s 판정 %s", scn, summarize(run.Verdict))
	}
	report.Scenarios = cp.Runs
	report.Result = d.result(cp.Runs)
	report.FinishedAt = time.Now()
	return report, nil
}

// prepareState 는 저장소 루트로 옮기고 드라이버 전용 kubeconfig·kind 이름을 환경에 둔다(사용자 kubeconfig 무변).
func (d *drill) prepareState() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	if err := os.MkdirAll(d.cfg.stateDir, 0o755); err != nil {
		return err
	}
	d.kubeconfig = filepath.Join(d.cfg.stateDir, "kubeconfig")
	d.t.Setenv("KUBECONFIG", d.kubeconfig)
	d.t.Setenv("KIND_CLUSTER", clusterName)
	return nil
}

// repoRoot 는 go.mod 가 있는 곳까지 올라간다.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod 를 못 찾았다")
		}
		dir = parent
	}
}

// ensureCluster 는 kind 를 새로 띄우거나(KEEP=1 이면) 있는 것을 이어 쓴다.
func (d *drill) ensureCluster() error {
	exists, err := clusterExists()
	if err != nil {
		return err
	}
	switch {
	case exists && d.cfg.keep:
		err = exportKubeconfig(d.kubeconfig)
	case exists:
		// 지난 판이 끊겨 남긴 클러스터 — NFS 순서를 지켜 걷고 새로 띄운다
		d.t.Logf("남은 kind 클러스터 %q 를 걷고 새로 띄운다", clusterName)
		if err = exportKubeconfig(d.kubeconfig); err == nil {
			err = teardownCluster(d.kubeconfig)
		}
		if err == nil {
			err = createCluster(testdataPath(kindConfig), d.kubeconfig)
		}
	default:
		err = createCluster(testdataPath(kindConfig), d.kubeconfig)
	}
	if err != nil {
		return err
	}
	if d.apiCIDR, err = kindSubnetV4(); err != nil {
		return err
	}
	return installInfra()
}

// bindInputs 는 차트 트리·kind 판·드라이버 digest 를 판정문 입력에 묶는다(차트 디렉터리가 더러우면 멈춘다).
func (d *drill) bindInputs() error {
	commit, tree, err := d.dataTree()
	if err != nil {
		return err
	}
	version, err := kind("version")
	if err != nil {
		return err
	}
	node, err := kindNodeImage()
	if err != nil {
		return err
	}
	digest, err := drillDigest()
	if err != nil {
		return err
	}
	d.inputs = Inputs{
		OperatorImage:    d.cfg.operator.String(),
		PGInstanceCommit: pgDrillBase,
		DataCommit:       commit,
		DataTree:         tree,
		KindNode:         node,
		KindVersion:      strings.TrimSpace(version),
		Storage:          storageNote,
		DrillDigest:      digest,
		Notes:            fidelityNotes,
	}
	return nil
}

// dataTree 는 판정문이 결속할 data 커밋·트리 — RED 는 차트 저장소에서 ST0 부모 트리를 풀어 쓴다.
func (d *drill) dataTree() (string, string, error) {
	if d.cfg.mode != loopdrill.ModeRed {
		return chartTree(d.cfg.chartDir)
	}
	dir, tree, err := d.archiveChart(preHoldDataCommit, "red-chart")
	if err != nil {
		return "", "", err
	}
	d.t.Logf("RED 차트 = data %s(ST0 부모) 트리 %s", shortHash(preHoldDataCommit), shortHash(tree))
	d.cfg.chartDir = dir
	return preHoldDataCommit, tree, nil
}

// chartTree 는 차트 디렉터리의 커밋과 git 트리 해시 — 판정문이 이것에 결속된다.
func chartTree(dir string) (commit, tree string, err error) {
	if out, err := run(commandTimeout, "git", "-C", dir, "status", "--porcelain", "--", "."); err != nil || strings.TrimSpace(out) != "" {
		return "", "", fmt.Errorf("차트 디렉터리 %s 가 git 체크아웃이 아니거나 커밋 안 된 변경이 있다: %v %s", dir, err, out)
	}
	out, err := run(commandTimeout, "git", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	commit = strings.TrimSpace(out)
	prefix, err := run(commandTimeout, "git", "-C", dir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	out, err = run(commandTimeout, "git", "-C", dir, "rev-parse", "HEAD:"+strings.TrimSuffix(strings.TrimSpace(prefix), "/"))
	if err != nil {
		return "", "", err
	}
	return commit, strings.TrimSpace(out), nil
}

func kindNodeImage() (string, error) {
	raw, err := os.ReadFile(testdataPath(kindConfig))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "image: "); ok {
			return value, nil
		}
	}
	return "", errors.New("kind-config 에 노드 이미지 없음")
}

// drillDigest 는 훈련장 원본(판정문 제외)의 내용 해시 — 어떤 드라이버가 판정했는지.
func drillDigest() (string, error) {
	files, err := filepath.Glob(drillDriverGlob)
	if err != nil {
		return "", err
	}
	files = append(files, drillHelpers)
	for _, root := range []string{judgeDir, drillDir} {
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && path == verdictDir {
				return filepath.SkipDir
			}
			if !e.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	slices.Sort(files)
	sum := sha256.New()
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(sum, "%s\x00%d\x00", f, len(raw))
		sum.Write(raw)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

// loadImages 는 P2 에 따라 시험 이미지를 싣는다.
func (d *drill) loadImages(probes ProbeResult) error {
	fidelity := FidelityLocal
	if probes.Amd64 {
		fidelity = FidelityPublished
	}
	op, pg, pgID, err := prepareImages(d.cfg.operator, fidelity)
	if err != nil {
		return err
	}
	d.operatorOn, d.pgOn = op, pg
	d.inputs.OperatorRun, d.inputs.PGRun, d.inputs.Fidelity = op.String(), pg.String(), fidelity
	d.inputs.PGImage = pgID
	return nil
}

func (d *drill) target() release {
	return release{ChartDir: d.cfg.chartDir, Operator: d.operatorOn, PG: d.pgOn, APICIDR: d.apiCIDR}
}

// install 은 새로 설치하고 수렴까지 기다린 뒤 멤버 수를 낸다.
func (d *drill) install(r release) (observation, int, error) {
	if err := resetRelease(); err != nil {
		return observation{}, 0, err
	}
	if err := installRelease(r, d.cfg.stateDir); err != nil {
		return observation{}, 0, err
	}
	return d.settle()
}

// settle 은 수렴을 기다리고 오퍼레이터 참조를 새로 읽는다.
func (d *drill) settle() (observation, int, error) {
	replicas, err := waitReplicas()
	if err != nil {
		return observation{}, 0, err
	}
	members := replicas + 1
	obs, err := waitConverged(members, installBound)
	if err != nil {
		return obs, members, err
	}
	if d.operator, err = readOperator(drillOperatorDeploy); err != nil {
		return obs, members, err
	}
	if d.cfg.mode == loopdrill.ModeGreen && d.gauge == nil {
		if d.gauge, err = newGaugeScraper(d.operator); err != nil {
			return obs, members, err
		}
	}
	return obs, members, nil
}

func waitReplicas() (int, error) {
	deadline := time.Now().Add(installBound)
	for {
		replicas, err := clusterReplicas()
		if err == nil {
			return replicas, nil
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
}

// scenario 는 시나리오 하나를 돈다 — RED 는 매번 새로 설치(루프·정지는 수렴하지 않는다), GREEN 은 이어서.
func (d *drill) scenario(scn loopdrill.Scenario) (ScenarioRun, error) {
	if scn == loopdrill.ScenarioS0 {
		return d.transition()
	}
	var obs observation
	var members int
	var err error
	if d.cfg.mode == loopdrill.ModeRed || d.operator.Deployment == "" {
		obs, members, err = d.install(d.target())
	} else {
		obs, members, err = d.settle()
	}
	if err != nil {
		return ScenarioRun{}, err
	}
	f, err := faultFor(scn, members-1)
	if err != nil {
		return ScenarioRun{}, err
	}
	if f.prepare != nil {
		if err := f.prepare(obs); err != nil {
			return ScenarioRun{}, err
		}
	}
	return d.observeScenario(scn, f, members, nil)
}

// observeScenario 는 표식 행 · before · 창 · after · 판정.
func (d *drill) observeScenario(scn loopdrill.Scenario, f fault, members int, oldPods []string) (ScenarioRun, error) {
	obs, err := probeCluster()
	if err != nil {
		return ScenarioRun{}, err
	}
	primary, ok := obs.primary()
	if !ok {
		return ScenarioRun{}, errors.New("유발 전 primary 가 하나가 아니다")
	}
	if err := ensureSchema(primary.Pod); err != nil {
		return ScenarioRun{}, err
	}
	marker, err := insertMarker(primary.Pod, scn)
	if err != nil {
		return ScenarioRun{}, err
	}
	if err := confirmMarker(obs.standbys(), marker, time.Now().Add(markerBound)); err != nil {
		return ScenarioRun{}, err
	}

	// before — 유발 직전 한 표본
	seed := newRecorder(0, members)
	seed.sample()
	before, err := d.collect(seed, oldPods)
	if err != nil {
		return ScenarioRun{}, err
	}

	rec := newRecorder(lastTimeline(before), members)
	rec.timeline = slices.Clone(before.Timeline)
	if err := d.window(scn, f, obs, before, rec); err != nil {
		return ScenarioRun{}, err
	}
	after, err := d.collect(rec, oldPods)
	if err != nil {
		return ScenarioRun{}, err
	}
	after.Writes = checkMarker(marker)
	after.Writes.WritableAt = rec.writableAt

	exp := loopdrill.ExpectFor(scn, d.cfg.mode, members-1)
	return ScenarioRun{Verdict: loopdrill.Judge(scn, before, after, exp), Expect: exp, Before: before, After: after}, nil
}

// lastTimeline 은 스냅숏이 마지막으로 본 타임라인(0 = 못 봄).
func lastTimeline(s loopdrill.ClusterSnap) int64 {
	if len(s.Timeline) == 0 {
		return 0
	}
	return s.Timeline[len(s.Timeline)-1].Timeline
}

// transition 은 S0 — 기점(beta.11 + 운영 차트) 설치 → S5 로 정지 재현 확인 → 수정판으로 helm upgrade.
func (d *drill) transition() (ScenarioRun, error) {
	if d.inputs.Fidelity != FidelityPublished {
		return ScenarioRun{}, errors.New("S0 기점 이미지는 발행 amd64 뿐이다(P2 거짓이면 S0 를 못 돈다)")
	}
	baseChart, err := d.baseChart()
	if err != nil {
		return ScenarioRun{}, err
	}
	baseRef, err := parseImageRef(baselineOperatorImage)
	if err != nil {
		return ScenarioRun{}, err
	}
	baseOp, err := loadImage(baseRef, nodePlatformAmd)
	if err != nil {
		return ScenarioRun{}, err
	}
	base := release{ChartDir: baseChart, Operator: baseOp, PG: d.pgOn, APICIDR: d.apiCIDR}
	obs, members, err := d.install(base)
	if err != nil {
		return ScenarioRun{}, err
	}

	// 정지 재현 — 기점은 S5 에서 멈춰야 한다(아니면 S0 가 옛 정지에서 출발하지 않는다)
	if err := d.reproduceStall(obs); err != nil {
		return ScenarioRun{}, err
	}
	oldPods, err := d.operator.pods()
	if err != nil {
		return ScenarioRun{}, err
	}
	upgrade := fault{rounds: 1, inject: func(observation, int) (injection, error) {
		return injection{}, installRelease(d.target(), d.cfg.stateDir)
	}}
	run, err := d.observeScenario(loopdrill.ScenarioS0, upgrade, members, oldPods)
	if err != nil {
		return ScenarioRun{}, err
	}
	if d.operator, err = readOperator(drillOperatorDeploy); err != nil {
		return ScenarioRun{}, err
	}
	return run, nil
}

// reproduceStall 은 기점 오퍼레이터를 S5 로 세우고 'Failed to watch' 가 섰는지 본다.
func (d *drill) reproduceStall(obs observation) error {
	if err := waitStandbyAge(obs); err != nil {
		return err
	}
	since := time.Now()
	inj, err := stopStandbyNode(nodeRBACAsIs)(obs, 0)
	if err != nil {
		return err
	}
	time.Sleep(loopdrill.StallStopDuration)
	if err := inj.revert(); err != nil {
		return err
	}
	lines, err := d.operator.failedWatchLines(since)
	if err != nil {
		return err
	}
	if lines == 0 {
		return errors.New("기점 오퍼레이터가 S5 에서 멈추지 않았다('Failed to watch' 0)")
	}
	return nil
}

// baseChart 는 S0 기점 차트 — 주어지면 그것, 아니면 차트 저장소의 origin/stable 트리를 풀어 쓴다.
func (d *drill) baseChart() (string, error) {
	if d.cfg.baseChartDir != "" {
		return d.cfg.baseChartDir, nil
	}
	dir, tree, err := d.archiveChart(baseChartRef, "base-chart")
	if err != nil {
		return "", err
	}
	d.inputs.BaseDataTree = tree
	return dir, nil
}

// archiveChart 는 차트 저장소의 ref 트리를 상태 디렉터리 name 에 풀고 차트 경로와 트리 해시를 낸다.
func (d *drill) archiveChart(ref, name string) (string, string, error) {
	prefix, err := run(commandTimeout, "git", "-C", d.cfg.chartDir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	path := strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	top, err := run(commandTimeout, "git", "-C", d.cfg.chartDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(d.cfg.stateDir, name)
	archive := filepath.Join(d.cfg.stateDir, name+".tar")
	if err := os.RemoveAll(dest); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", "", err
	}
	if _, err := run(commandTimeout, "git", "-C", strings.TrimSpace(top), "archive", "-o", archive, ref, path); err != nil {
		return "", "", err
	}
	if _, err := run(commandTimeout, "tar", "-xf", archive, "-C", dest); err != nil {
		return "", "", err
	}
	tree, err := run(commandTimeout, "git", "-C", strings.TrimSpace(top), "rev-parse", ref+":"+path)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dest, path), strings.TrimSpace(tree), nil
}

// result 는 모드의 통과 조건으로 판정문 결과를 낸다.
func (d *drill) result(runs []ScenarioRun) string {
	byScenario := map[loopdrill.Scenario]loopdrill.Verdict{}
	for _, r := range runs {
		byScenario[r.Scenario] = r.Verdict
	}
	if d.cfg.mode == loopdrill.ModeRed {
		s4, ok4 := byScenario[loopdrill.ScenarioS4]
		s5, ok5 := byScenario[loopdrill.ScenarioS5]
		if !ok4 || !ok5 {
			return resultScenarioMissed
		}
		if loopdrill.RedReproduced(s4, s5) {
			return resultRedAsExpected
		}
		return resultRedMissing
	}
	for _, scn := range greenScenarios {
		v, ok := byScenario[scn]
		if !ok {
			return resultScenarioMissed
		}
		if !v.Green() {
			return resultNotGreen
		}
	}
	return resultGreen
}

func summarize(v loopdrill.Verdict) string {
	var parts []string
	for _, c := range v.Checks {
		mark := "✓"
		if !c.Pass {
			mark = "✗"
		}
		parts = append(parts, fmt.Sprintf("%s%s(%s)", c.ID, mark, c.Detail))
	}
	return strings.Join(parts, " · ")
}

// ── 판정문 · 체크포인트 ──

func (d *drill) reportPath(r *DrillReport) string {
	if d.cfg.report != "" {
		return d.cfg.report
	}
	label := d.cfg.operator.Tag
	if label == "" {
		label = drillTag(d.cfg.operator.Digest)
	}
	return filepath.Join(verdictDir, fmt.Sprintf("%s-%s.json", label, shortHash(r.Inputs.DataTree)))
}

func shortHash(h string) string {
	return h[:min(shortDigestLen, len(h))]
}

func (d *drill) writeReport(r *DrillReport) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := d.reportPath(r)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	d.t.Logf("판정문 %s — %s", path, r.Result)
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func (d *drill) checkpointKey() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(d.cfg.mode), d.inputs.OperatorImage, d.inputs.PGInstanceCommit, d.inputs.DataTree, d.inputs.DrillDigest,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (d *drill) checkpointPath() string {
	return filepath.Join(d.cfg.stateDir, "checkpoint.json")
}

func (d *drill) loadCheckpoint() checkpoint {
	fresh := checkpoint{Key: d.checkpointKey()}
	if !d.cfg.keep {
		return fresh
	}
	raw, err := os.ReadFile(d.checkpointPath())
	if err != nil {
		return fresh
	}
	var cp checkpoint
	if json.Unmarshal(raw, &cp) != nil || cp.Key != fresh.Key {
		return fresh
	}
	return cp
}

func (d *drill) saveCheckpoint(cp checkpoint) {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		d.t.Logf("체크포인트 직렬화: %v", err)
		return
	}
	if err := os.WriteFile(d.checkpointPath(), raw, 0o644); err != nil {
		d.t.Logf("체크포인트 쓰기: %v", err)
	}
}
