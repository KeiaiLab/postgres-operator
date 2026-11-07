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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// kind 훈련장 진입점 — make test-e2e-loopdrill(설계 2026-09-25 §3.12).
//
//	kind 기동(KEEP=1 재사용) ─ 오조준 차단 ─ 저장·CRD·적격 라벨 ─ 탐침 P1·P2 ─ 시험 이미지 싣기 ─ 선풀
//	  └ 시나리오마다: (RED 는 새로 설치) 수렴 ─ 쓰기 탐침 ─ 표식 행 ─ before ─ 유발 ─ 창 ─ after ─ Judge ─ 체크포인트
//	  └ 판정문(LOOPDRILL_REPORT) ─ RED: S3·S5 각 2판 모두 붉음 · GREEN: S0–S10 전부 초록
//
// S0 전환(GREEN 첫 시나리오):
//
//	사전(ST0 부모 트리 + beta.11 · 0-0·0-1 은 W4, 0-2 는 적격) ─ 정지 재현(J4 붉음) ─ 운영 누적물 ─ before
//	  ─ ST0 트리(C1 부모, 오퍼레이터 0) ─ C1(CRD CreateReplace · helm upgrade · 확인) ─ C2(오퍼레이터 2) ─ 단계화 ─ after

const (
	envExpect       = "LOOPDRILL_EXPECT"
	envChartDir     = "LOOPDRILL_CHART_DIR"
	envBaseChartDir = "LOOPDRILL_BASE_CHART_DIR"
	envC1Ref        = "LOOPDRILL_C1_REF"
	envOperator     = "LOOPDRILL_OPERATOR_IMAGE"
	envReport       = "LOOPDRILL_REPORT"
	envScenarios    = "LOOPDRILL_SCENARIOS"
	envStateDir     = "LOOPDRILL_STATE_DIR"
	envKeep         = "KEEP"

	// S0 의 기점 = 운영이 도는 ghcr v0.4.0-beta.11(e997c262) 인덱스, Harbor ghcr-proxy.
	baselineOperatorImage = "harbor.keiailab.com/ghcr-proxy/keiailab/postgres-operator:v0.4.0-beta.11@sha256:29c503ffc16c3347d9b6912a9994d17c069667c89efe0a738e6e472ae551869d"
	verdictDir            = "test/e2e/loopdrill/verdicts"
	drillDir              = "test/e2e/loopdrill"
	judgeDir              = "internal/loopdrill"
	drillDriverGlob       = "test/e2e/loopdrill*_test.go"
	drillHelpers          = "test/e2e/helpers.go"
	kindConfig            = "kind-config.yaml"
	storageNote           = "csi-driver-nfs v4.13.4 · nfs-retain(Retain) · VolumeSnapshotClass nfs-snapshot(Delete)"

	// defaultC1Ref 는 C1(D1) — 차트 저장소 HEAD(= C2, D1s)의 부모. D1s 는 D1 위 커밋 하나다(설계 §6).
	defaultC1Ref = "HEAD~1"

	// preHoldDataCommit 은 data ST0(fe7c2ba) 의 부모 — RED·S0 사전 차트 트리 핀.
	// ST0 뒤 data stable 은 오퍼레이터 0 복제라 기점 루프·정지가 재현되지 않는다.
	preHoldDataCommit = "56d79387a723284b99f3899420a5daf86161113b"

	// 운영 누적물(S0) — auto.conf primary_conninfo 줄 수(운영 "수백 줄") · wal_keep_size(메인 루프 5GB).
	conninfoPadding     = 300
	alterWalKeep        = "ALTER SYSTEM SET wal_keep_size = '5GB'"
	alterLogConnections = "ALTER SYSTEM SET log_connections = 'on'"
	reloadConf          = "SELECT pg_reload_conf()"
)

// 판정문 결과.
const (
	resultRedAsExpected = loopdrill.ResultRedAsExpected
	resultGreen         = loopdrill.ResultGreen
	resultGreenStalls   = loopdrill.ResultGreenHostStalls
	resultProbeFailed   = "p1-false"
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
	"CiliumNetworkPolicy 는 CRD 만 — 집행 0 · NetworkPolicy 는 kindnet",
	"kubeadm kind(운영 k3s 아님) · control-plane 1대",
	"-rw Service 는 testdata 흉내(운영은 오퍼레이터·GitOps 밖)",
}

type drillConfig struct {
	mode         loopdrill.Mode
	chartDir     string
	baseChartDir string
	c1Ref        string
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
	cfg.scenarios = loopdrill.RedScenarios()
	if cfg.mode == loopdrill.ModeGreen {
		cfg.scenarios = loopdrill.GreenScenarios()
	}
	if raw := os.Getenv(envScenarios); raw != "" {
		cfg.scenarios = nil
		for _, s := range strings.Split(raw, ",") {
			cfg.scenarios = append(cfg.scenarios, loopdrill.Scenario(strings.TrimSpace(s)))
		}
	}
	cfg.c1Ref = os.Getenv(envC1Ref)
	if cfg.c1Ref == "" {
		cfg.c1Ref = defaultC1Ref
	}
	cfg.baseChartDir = os.Getenv(envBaseChartDir)
	cfg.report = os.Getenv(envReport)
	cfg.stateDir = os.Getenv(envStateDir)
	if cfg.stateDir == "" {
		cfg.stateDir = filepath.Join(os.TempDir(), "loopdrill-"+clusterName)
	}
	return cfg, nil
}

// Inputs 는 판정문이 결속하는 입력(설계 §3.12 — S · D · 실제 돈 imageID · P2 · pg-drill 과 인스턴스 매니저 기점 ·
// data C1/C2/T1/T2/사전 트리 · kind 판·라벨 · 저장 · 탐침 간격).
type Inputs struct {
	OperatorImage    string   `json:"operatorImage"`              // 입력 참조
	OperatorRevision string   `json:"operatorRevision,omitempty"` // S — revision 라벨
	OperatorDigest   string   `json:"operatorDigest,omitempty"`   // D — 입력 참조의 digest
	OperatorRun      string   `json:"operatorRun"`                // 노드에 실은 태그
	OperatorImageID  string   `json:"operatorImageID,omitempty"`  // 오퍼레이터 파드가 실제 돈 imageID
	PGImage          string   `json:"pgImage"`                    // pg-drill 이미지 ID
	PGRun            string   `json:"pgRun"`
	PGInstanceCommit string   `json:"pgInstanceCommit"` // pg-drill 인스턴스 매니저를 구운 기점
	Fidelity         Fidelity `json:"fidelity"`
	Amd64            bool     `json:"amd64"`                    // P2
	DataCommit       string   `json:"dataCommit"`               // RED = 사전 · GREEN = C2
	DataTree         string   `json:"dataTree"`                 // RED = 사전 · GREEN = T2
	C1Commit         string   `json:"c1Commit,omitempty"`       // GREEN C1(D1)
	C1Tree           string   `json:"c1Tree,omitempty"`         // T1
	HoldCommit       string   `json:"holdCommit,omitempty"`     // ST0 트리 = C1 부모
	HoldTree         string   `json:"holdTree,omitempty"`       //
	BaseDataCommit   string   `json:"baseDataCommit,omitempty"` // S0 사전 = ST0 부모 핀
	BaseDataTree     string   `json:"baseDataTree,omitempty"`   //
	KindNode         string   `json:"kindNode"`
	KindVersion      string   `json:"kindVersion"`
	NodeLabels       []string `json:"nodeLabels"` // 적격·W4 노드 분류
	Storage          string   `json:"storage"`
	ProbeInterval    string   `json:"probeInterval"`
	DrillDigest      string   `json:"drillDigest"`
	Notes            []string `json:"notes"`
}

// ScenarioRun 은 시나리오 하나의 기록.
type ScenarioRun struct {
	loopdrill.Verdict
	Round  int                   `json:"round"`
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

// checkpoint 는 KEEP=1 재개용 — 같은 입력일 때만 이어 쓴다. Runs[i] 는 계획의 i 번째 시나리오다(RED 는 되풀이).
type checkpoint struct {
	Key    string        `json:"key"`
	Probes *ProbeResult  `json:"probes,omitempty"`
	Runs   []ScenarioRun `json:"runs"`
}

type drill struct {
	t          testing.TB
	cfg        drillConfig
	kubeconfig string
	inputs     Inputs
	operatorOn imageRef // 노드에 실은 시험 오퍼레이터
	pgOn       imageRef // 노드에 실은 시험 pg
	apiCIDR    string
	operator   operatorRef
	gauge      *gaugeScraper
	secrets    secrets
	eligible   []string // J11 적격 노드
	avoid      []string // J11 W4
	c1Faults   []string // S0 C1 확인 실패
	emergency  loopdrill.EmergencyRecord
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
	// V1 통과 — green-host-stalls 도 통과다(예산 그대로, 두 계측기가 함께 멈춘 시간만 뺐다 · ADR-0039)
	want := []string{resultRedAsExpected}
	if cfg.mode == loopdrill.ModeGreen {
		want = []string{resultGreen, resultGreenStalls}
	}
	if !slices.Contains(want, report.Result) {
		t.Fatalf("판정문 결과 %s(기대 %s) — %s", report.Result, strings.Join(want, "|"), d.reportPath(report))
	}
}

// mustTarget 은 훈련장 kubeconfig 가 kind 루프백이 아니면 곧바로 멈춘다(훈련 시작 · 유발 직전).
func (d *drill) mustTarget() {
	if err := guardTarget(); err != nil {
		d.t.Fatalf("훈련장 오조준 — 멈춘다: %v", err)
	}
}

func (d *drill) run() (*DrillReport, error) {
	started := time.Now()
	if err := d.prepareState(); err != nil {
		return nil, err
	}
	if err := d.startStallClock(); err != nil {
		return nil, fmt.Errorf("호스트 정지 시계: %w", err)
	}
	if err := d.ensureCluster(); err != nil {
		return nil, err
	}
	d.mustTarget()
	if !d.cfg.keep {
		d.t.Cleanup(func() { _ = teardownCluster(d.kubeconfig) })
	}
	if err := d.prepareInfra(); err != nil {
		return nil, err
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

	if err := d.loadImages(cp.Probes); err != nil {
		return nil, err
	}
	report.Probes = *cp.Probes

	for i, scn := range d.cfg.scenarios {
		if i < len(cp.Runs) {
			continue
		}
		run, err := d.scenario(scn)
		if err != nil {
			report.Inputs, report.Scenarios, report.FinishedAt = d.inputs, cp.Runs, time.Now()
			return report, fmt.Errorf("%s: %w", scn, err)
		}
		run.Round = rounds(cp.Runs, scn) + 1
		cp.Runs = append(cp.Runs, run)
		d.saveCheckpoint(cp)
		d.t.Logf("%s(%d판) 판정 %s", scn, run.Round, summarize(run.Verdict))
	}
	report.Inputs, report.Scenarios = d.inputs, cp.Runs
	report.Result = d.result(cp.Runs)
	report.FinishedAt = time.Now()
	return report, nil
}

// rounds 는 이미 돈 scn 판 수다.
func rounds(runs []ScenarioRun, scn loopdrill.Scenario) int {
	n := 0
	for _, r := range runs {
		if r.Scenario == scn {
			n++
		}
	}
	return n
}

// prepareState 는 저장소 루트로 옮기고 드라이버 전용 kubeconfig·kind 이름을 정한다(사용자 kubeconfig 무변).
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
	drillKubeconfig = d.kubeconfig
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
	d.apiCIDR, err = kindSubnetV4()
	return err
}

// prepareInfra 는 저장·CRD·적격 라벨을 올리고 J11 노드 분류를 읽는다.
func (d *drill) prepareInfra() error {
	if err := installInfra(); err != nil {
		return err
	}
	var err error
	d.eligible, d.avoid, err = nodeSets()
	return err
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
		OperatorDigest:   d.cfg.operator.Digest,
		PGInstanceCommit: pgDrillBase,
		DataCommit:       commit,
		DataTree:         tree,
		KindNode:         node,
		KindVersion:      strings.TrimSpace(version),
		NodeLabels:       []string{"eligible=" + strings.Join(d.eligible, ","), "w4=" + strings.Join(d.avoid, ",")},
		Storage:          storageNote,
		ProbeInterval:    loopdrill.WriteProbeInterval.String(),
		DrillDigest:      digest,
		Notes:            fidelityNotes,
	}
	if d.cfg.mode == loopdrill.ModeGreen {
		return d.bindTransition()
	}
	return nil
}

// bindTransition 은 GREEN S0 의 C1·ST0·사전 트리를 결속한다(판정문 T1 · 보류 · 사전 트리).
func (d *drill) bindTransition() error {
	var err error
	if d.inputs.C1Commit, d.inputs.C1Tree, err = d.chartRef(d.cfg.c1Ref); err != nil {
		return fmt.Errorf("C1 %s: %w", d.cfg.c1Ref, err)
	}
	if d.inputs.HoldCommit, d.inputs.HoldTree, err = d.chartRef(d.inputs.C1Commit + "~1"); err != nil {
		return fmt.Errorf("ST0 트리(C1 부모): %w", err)
	}
	d.inputs.BaseDataCommit, d.inputs.BaseDataTree, err = d.chartRef(preHoldDataCommit)
	return err
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
	return chartRefIn(dir, "HEAD")
}

// chartRef 는 차트 저장소의 ref 커밋과 차트 경로 트리다.
func (d *drill) chartRef(ref string) (string, string, error) {
	return chartRefIn(d.cfg.chartDir, ref)
}

func chartRefIn(dir, ref string) (string, string, error) {
	out, err := run(commandTimeout, "git", "-C", dir, "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", "", err
	}
	commit := strings.TrimSpace(out)
	prefix, err := run(commandTimeout, "git", "-C", dir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	path := strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	out, err = run(commandTimeout, "git", "-C", dir, "rev-parse", commit+":"+path)
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

// loadImages 는 P2 에 따라 시험 이미지를 싣고 S(revision 라벨)·선풀을 기록한다.
func (d *drill) loadImages(probes *ProbeResult) error {
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
	d.inputs.PGImage, d.inputs.Amd64 = pgID, probes.Amd64
	d.inputs.OperatorRevision = imageRevision(op)

	// 선풀 — kubelet 은 막 실은 이미지를 노드 status 에 늦게 올린다(상한 probeBound)
	deadline := time.Now().Add(probeBound)
	for {
		if probes.Unpulled, err = prepulled(op, pg); err != nil {
			return err
		}
		if len(probes.Unpulled) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("선풀 안 됨: %v", probes.Unpulled)
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
}

func (d *drill) target() release {
	r := release{ChartDir: d.cfg.chartDir, Operator: d.operatorOn, PG: d.pgOn, APICIDR: d.apiCIDR}
	if d.cfg.mode == loopdrill.ModeRed {
		r.Overlays = []string{redValuesFile}
	}
	return r
}

// install 은 새로 설치하고 수렴까지 기다린 뒤 멤버 수를 낸다.
func (d *drill) install(r release) (observation, int, error) {
	if err := resetRelease(); err != nil {
		return observation{}, 0, err
	}
	var err error
	if d.secrets, err = ensureSecrets(); err != nil {
		return observation{}, 0, err
	}
	if err := installRelease(r, d.cfg.stateDir); err != nil {
		return observation{}, 0, err
	}
	return d.settle()
}

// settle 은 수렴을 기다리고 오퍼레이터 참조·실제 돈 imageID 를 새로 읽는다.
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
	if d.secrets.probe == "" {
		if d.secrets, err = ensureSecrets(); err != nil {
			return obs, members, err
		}
	}
	d.noteImageID()
	if d.cfg.mode == loopdrill.ModeGreen && d.gauge == nil {
		if d.gauge, err = newGaugeScraper(d.operator); err != nil {
			return obs, members, err
		}
	}
	return obs, members, nil
}

// noteImageID 는 오퍼레이터 파드가 실제 돈 imageID 를 판정문 입력에 적는다(마지막 값 — GREEN 은 S0 C2 뒤 시험 이미지).
func (d *drill) noteImageID() {
	pods, err := d.operator.running()
	if err != nil {
		return
	}
	for _, p := range pods {
		i := slices.IndexFunc(p.Status.ContainerStatuses, func(c containerStatus) bool { return c.Name == managerContainer })
		if i >= 0 && p.Status.ContainerStatuses[i].ImageID != "" {
			d.inputs.OperatorImageID = p.Status.ContainerStatuses[i].ImageID
			return
		}
	}
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
	d.emergency = loopdrill.EmergencyRecord{}
	f, err := d.faultFor(scn, members-1)
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

// observeScenario 는 쓰기 탐침 · 표식 행 · before · 창 · after · 판정.
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
	warm, err := ensureWriteProbe(primary.Pod, d.secrets)
	if err != nil {
		return ScenarioRun{}, err
	}
	if !warm {
		d.t.Logf("%s 쓰기 탐침이 연속 %d 개에 못 이르렀다 — J10·J15 가 붉게 남긴다", scn, loopdrill.WriteProbeWarmup)
	}
	marker, err := insertMarker(primary.Pod, scn)
	if err != nil {
		return ScenarioRun{}, err
	}
	if err := confirmMarker(obs.standbys(), marker, time.Now().Add(markerBound)); err != nil {
		return ScenarioRun{}, err
	}

	// before — 유발 직전 한 표본(S0 는 사전 trust 양성 대조 · 운영 누적물 기준)
	seed := newRecorder(0, members, primary)
	seed.sample()
	seed.sampleSlow(nil)
	before, err := d.collect(scn, seed, oldPods, loopdrill.ClusterSnap{}, collectBefore)
	if err != nil {
		return ScenarioRun{}, err
	}
	if scn == loopdrill.ScenarioS0 {
		before.HBA.PreTrust = connectAs(connectNoPassword, "")
		before.Settings = settingsOf(obs)
	}

	d.mustTarget()
	rec := newRecorder(lastTimeline(before), members, primary)
	rec.timeline = slices.Clone(before.Timeline)
	exp := loopdrill.ExpectFor(scn, d.cfg.mode, members-1)
	rec.follow = exp.Promotion == loopdrill.PromotionSwitchover || exp.Promotion == loopdrill.PromotionEmergency
	if err := d.window(scn, f, obs, before, rec); err != nil {
		return ScenarioRun{}, err
	}
	after, err := d.collect(scn, rec, oldPods, before, collectAfter)
	if err != nil {
		return ScenarioRun{}, err
	}
	after.Writes = checkMarker(marker)
	return ScenarioRun{Verdict: loopdrill.Judge(scn, before, after, exp), Expect: exp, Before: before, After: after}, nil
}

// lastTimeline 은 스냅숏이 마지막으로 본 타임라인(0 = 못 봄).
func lastTimeline(s loopdrill.ClusterSnap) int64 {
	if len(s.Timeline) == 0 {
		return 0
	}
	return s.Timeline[len(s.Timeline)-1].Timeline
}

// transitionCharts 는 S0 이 차례로 설치하는 차트 디렉터리다 — C2 는 LOOPDRILL_CHART_DIR 그대로.
type transitionCharts struct {
	pre  string // ST0 부모(사전)
	hold string // ST0 트리 = C1 부모(오퍼레이터 0)
	c1   string // D1
}

// transition 은 S0 — 사전(beta.11 + ST0 부모 트리) → 정지 재현 → 운영 누적물 → ST0 트리 → C1 → C1 확인 → C2 → 단계화.
func (d *drill) transition() (ScenarioRun, error) {
	if d.inputs.Fidelity != FidelityPublished {
		return ScenarioRun{}, errors.New("S0 기점 이미지는 발행 amd64 뿐이다(P2 거짓이면 S0 를 못 돈다)")
	}
	charts, err := d.transitionCharts()
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
	pre := release{ChartDir: charts.pre, Operator: baseOp, PG: d.pgOn, APICIDR: d.apiCIDR}
	obs, members, err := d.installPlaced(pre)
	if err != nil {
		return ScenarioRun{}, err
	}

	// 정지 재현 — 기점은 S5 에서 멈춰야 한다(아니면 S0 가 옛 정지에서 출발하지 않는다)
	if err := d.reproduceStall(obs); err != nil {
		return ScenarioRun{}, err
	}
	if obs, err = waitConverged(members, installBound); err != nil {
		return ScenarioRun{}, fmt.Errorf("정지 재현 뒤 수렴: %w", err)
	}
	if err := accumulate(obs); err != nil {
		return ScenarioRun{}, fmt.Errorf("운영 누적물: %w", err)
	}
	oldPods, err := d.operator.pods()
	if err != nil {
		return ScenarioRun{}, err
	}
	stsGen := obs.Generation
	cutover := fault{rounds: 1, inject: func(observation, int) (injection, error) {
		return injection{}, d.cutover(charts, baseOp, stsGen)
	}}
	run, err := d.observeScenario(loopdrill.ScenarioS0, cutover, members, oldPods)
	if err != nil {
		return ScenarioRun{}, err
	}
	if d.operator, err = readOperator(drillOperatorDeploy); err != nil {
		return ScenarioRun{}, err
	}
	d.noteImageID()
	return run, nil
}

// transitionCharts 는 사전·ST0·C1 트리를 상태 디렉터리에 푼다(LOOPDRILL_BASE_CHART_DIR 가 있으면 사전은 그것).
func (d *drill) transitionCharts() (transitionCharts, error) {
	var ch transitionCharts
	var err error
	if ch.pre = d.cfg.baseChartDir; ch.pre == "" {
		if ch.pre, _, err = d.archiveChart(preHoldDataCommit, "pre-chart"); err != nil {
			return ch, err
		}
	}
	if ch.hold, _, err = d.archiveChart(d.inputs.HoldCommit, "hold-chart"); err != nil {
		return ch, err
	}
	ch.c1, _, err = d.archiveChart(d.inputs.C1Commit, "c1-chart")
	return ch, err
}

// installPlaced 는 사전 배치로 설치한다 — 0-0·0-1 은 W4, 0-2 는 적격 워커(운영 e21 에 두 멤버가 몰린 모양).
// 적격 워커를 cordon 해 둘을 W4 에 세우고, 0-1 이 자리 잡으면 W4 를 cordon 해 0-2 를 적격 워커로 보낸다.
func (d *drill) installPlaced(r release) (observation, int, error) {
	eligible, err := nodesWithRole(nodeRolePG)
	if err != nil {
		return observation{}, 0, err
	}
	w4, err := nodesWithRole(nodeRoleW4)
	if err != nil || len(w4) == 0 {
		return observation{}, 0, fmt.Errorf("W4 노드 없음: %v", err)
	}
	all := append(slices.Clone(eligible), w4...)
	defer func() {
		for _, n := range all {
			_ = uncordonNode(n)
		}
	}()
	if err := resetRelease(); err != nil {
		return observation{}, 0, err
	}
	if d.secrets, err = ensureSecrets(); err != nil {
		return observation{}, 0, err
	}
	for _, n := range eligible {
		if err := cordonNode(n); err != nil {
			return observation{}, 0, err
		}
	}
	if err := installRelease(r, d.cfg.stateDir); err != nil {
		return observation{}, 0, err
	}
	if err := waitScheduled(memberName(1)); err != nil {
		return observation{}, 0, err
	}
	for _, n := range eligible {
		if err := uncordonNode(n); err != nil {
			return observation{}, 0, err
		}
	}
	if err := cordonNode(w4[0]); err != nil {
		return observation{}, 0, err
	}
	if err := waitScheduled(memberName(2)); err != nil {
		return observation{}, 0, err
	}
	return d.settle()
}

// memberName 은 샤드 멤버 파드 이름.
func memberName(ordinal int) string {
	return shardSTS + "-" + strconv.Itoa(ordinal)
}

// waitScheduled 는 파드가 노드에 배정될 때까지 기다린다.
func waitScheduled(pod string) error {
	deadline := time.Now().Add(installBound)
	for time.Now().Before(deadline) {
		out, err := kubectl("get", "pod", pod, "-n", dataNamespace, "-o", "jsonpath={.spec.nodeName}")
		if err == nil && strings.TrimSpace(out) != "" {
			return nil
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
	return fmt.Errorf("%s 가 %s 안에 배정되지 않았다", pod, installBound)
}

// reproduceStall 은 기점 오퍼레이터를 S5 로 세우고 'Failed to watch' 가 섰는지 본다.
func (d *drill) reproduceStall(obs observation) error {
	if err := waitStandbyAge(obs); err != nil {
		return err
	}
	since := time.Now()
	d.mustTarget()
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

// accumulate 는 운영 누적물을 사전 상태에 얹는다(설계 §3.12 S0) — 세 멤버 wal_keep_size·log_connections ALTER SYSTEM,
// auto.conf primary_conninfo 수백 줄, 복제본에 복사된 승격 마커.
func accumulate(obs observation) error {
	for _, m := range obs.Members {
		if _, err := psql(m.Pod, alterWalKeep, alterLogConnections, reloadConf); err != nil {
			return err
		}
		dir, err := psql(m.Pod, "SHOW data_directory")
		if err != nil {
			return err
		}
		if err := padConninfo(m.Pod, firstLine(dir)); err != nil {
			return err
		}
		if m.Role != roleStandby {
			continue
		}
		touch := `touch "$1/` + promotedMarker + `"`
		args := []string{"exec", "-n", dataNamespace, m.Pod, "-c", pgContainer, "--", "sh", "-c", touch, "sh", firstLine(dir)}
		if _, err := kubectl(args...); err != nil {
			return err
		}
	}
	return nil
}

// padConninfo 는 auto.conf 의 마지막 primary_conninfo 줄(없으면 빈 값)을 conninfoPadding 번 덧붙인다.
func padConninfo(pod, dataDir string) error {
	script := `f="$1/postgresql.auto.conf"; line=$(grep '^primary_conninfo' "$f" | tail -n 1); ` +
		`[ -n "$line" ] || line="primary_conninfo = ''"; i=0; ` +
		`while [ "$i" -lt "$2" ]; do printf '%s\n' "$line" >> "$f"; i=$((i+1)); done`
	_, err := kubectl("exec", "-n", dataNamespace, pod, "-c", pgContainer, "--",
		"sh", "-c", script, "sh", dataDir, strconv.Itoa(conninfoPadding))
	return err
}

// cutover 는 ST0 트리(오퍼레이터 0) → C1(CRD CreateReplace · helm upgrade · 확인) → C2(helm upgrade)다.
func (d *drill) cutover(ch transitionCharts, baseOp imageRef, stsGen int64) error {
	if err := guardTarget(); err != nil {
		return err
	}
	hold := release{ChartDir: ch.hold, Operator: baseOp, PG: d.pgOn, APICIDR: d.apiCIDR}
	if err := installRelease(hold, d.cfg.stateDir); err != nil {
		return fmt.Errorf("ST0 트리: %w", err)
	}
	if err := waitOperatorGone(d.operator); err != nil {
		return err
	}
	c1 := release{ChartDir: ch.c1, Operator: d.operatorOn, PG: d.pgOn, APICIDR: d.apiCIDR}
	if err := installRelease(c1, d.cfg.stateDir); err != nil {
		return fmt.Errorf("C1: %w", err)
	}
	d.c1Faults = verifyC1(stsGen, d.operatorOn)
	if err := installRelease(d.target(), d.cfg.stateDir); err != nil {
		return fmt.Errorf("C2: %w", err)
	}
	return nil
}

// waitOperatorGone 은 오퍼레이터 파드가 0 이 될 때까지 기다린다(ST0 — 옛 리더 없이 새 파드가 리스를 잡는다).
func waitOperatorGone(op operatorRef) error {
	deadline := time.Now().Add(probeBound)
	for time.Now().Before(deadline) {
		if pods, err := op.pods(); err == nil && len(pods) == 0 {
			return nil
		}
		time.Sleep(loopdrill.StatusPollInterval)
	}
	return fmt.Errorf("ST0 트리 뒤 %s 안에 오퍼레이터 파드가 0 이 되지 않았다", probeBound)
}

// result 는 모드의 통과 조건으로 판정문 결과를 낸다.
func (d *drill) result(runs []ScenarioRun) string {
	verdicts := make([]loopdrill.Verdict, 0, len(runs))
	for _, r := range runs {
		verdicts = append(verdicts, r.Verdict)
	}
	return loopdrill.Outcome(d.cfg.mode, len(d.cfg.scenarios), verdicts)
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

	// 관측(H1 등)은 판정이 아니라 표식 없이 붙인다
	for _, o := range v.Observations {
		parts = append(parts, fmt.Sprintf("%s(%s)", o.ID, o.Detail))
	}
	return strings.Join(parts, " · ")
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
	repo := strings.TrimSpace(top)
	if _, err := run(commandTimeout, "git", "-C", repo, "archive", "-o", archive, ref, path); err != nil {
		return "", "", err
	}
	if _, err := run(commandTimeout, "tar", "-xf", archive, "-C", dest); err != nil {
		return "", "", err
	}
	tree, err := run(commandTimeout, "git", "-C", repo, "rev-parse", ref+":"+path)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dest, path), strings.TrimSpace(tree), nil
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
		string(d.cfg.mode), d.inputs.OperatorImage, d.inputs.PGInstanceCommit,
		d.inputs.DataTree, d.inputs.C1Tree, d.inputs.DrillDigest,
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
