/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// kind 훈련장(test/e2e/loopdrill/budgets.go)의 창이 오퍼레이터 상수를 덮는지 교차 확인한다 —
// 창이 짧으면 수정판이 정상 동작해도 판정이 붉거나, 기점 결함이 창 밖으로 빠져 RED 가 초록이 된다.
//
// import 하지 않고 원본을 타입 검사한다: 이미지 test 단계 컨텍스트(api·cmd·internal)에 test/ 가
// 없어서다. 그 자리(원본 없음)에서만 건너뛰고, 맥 land 검사는 저장소 전체라 늘 돈다.
const loopdrillBudgetsPath = "../../test/e2e/loopdrill/budgets.go"

func TestLoopDrillBudgetsCoverOperator(t *testing.T) {
	budgets := loadLoopDrillBudgets(t)

	// 창 ≥ 오퍼레이터 상수(수정판에서 파생될 값은 설계의 파생 근거 상수로)
	atLeast := []struct {
		window string
		floor  time.Duration
		why    string
	}{
		{"StatusPollInterval", statusPollInterval, "statusPollInterval"},
		{"StatusStaleThresh", statusStaleThresh, "statusStaleThresh"},
		{"DebounceBound", failoverDebounceThreshold, "failoverDebounceThreshold"},
		{"DebounceBound", statusStaleThresh, "설계 §3.3 디바운스 = statusStaleThresh"},
		{"RetargetBound", statusStaleThresh, "설계 §3.4 memberRetargetAfter = statusStaleThresh"},
		{"StaleBound", staleStandbyReseedTimeout, "staleStandbyReseedTimeout"},
		{"SnapshotReadyBound", staleStandbyReseedTimeout, "설계 부록 A snapshotReadyTimeout = staleStandbyReseedTimeout"},
		{"CooldownBound", reseedCooldown, "reseedCooldown"},
		{"StuckTerminatingBound", stuckTerminatingForceDeleteThreshold, "stuckTerminatingForceDeleteThreshold"},
		{"ConvergeBound", 2*staleStandbyReseedTimeout + reseedCooldown, "Stale 판정 + snapshotReadyTimeout + reseedCooldown"},
		{"LoopObservationWindow", reseedCooldown, "관측창 = reseedCooldown"},
		{"StallStopDuration", stuckTerminatingForceDeleteThreshold + staleStandbyReseedTimeout, "설계 S5 하한"},
		{"StallStopDuration", statusStaleThresh + reseedCooldown, "기점 정지 = 끊김 판정 + 두 번째 재시드"},
		{"StandbyAgeFloor", staleStandbyReseedTimeout, "기점 재시드 파드 나이 조건"},
		{"StandbyFreeze", staleStandbyReseedTimeout, "S6 끊김 > Stale 판정"},
	}
	for _, c := range atLeast {
		got, ok := budgets[c.window]
		if !ok {
			t.Fatalf("budgets.go 에 %s 없음", c.window)
		}
		if got < c.floor {
			t.Errorf("%s = %s < %s(%s)", c.window, got, c.floor, c.why)
		}
	}

	// S3·S4·S6 정지는 하트비트 끊김 문턱을 넘어야 관측자가 본다
	if budgets["FaultPause"] <= statusStaleThresh {
		t.Errorf("FaultPause = %s ≤ statusStaleThresh %s", budgets["FaultPause"], statusStaleThresh)
	}
}

// loadLoopDrillBudgets 는 budgets.go 의 내보낸 Duration 상수를 평가한다(원본이 없으면 건너뛴다).
func loadLoopDrillBudgets(t *testing.T) map[string]time.Duration {
	t.Helper()
	src, err := os.ReadFile(filepath.FromSlash(loopdrillBudgetsPath))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("훈련장 원본이 이 빌드 컨텍스트에 없다(이미지 test 단계)")
	}
	if err != nil {
		t.Fatalf("budgets.go 읽기: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, loopdrillBudgetsPath, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("budgets.go 파싱: %v", err)
	}
	conf := types.Config{Importer: timeOnlyImporter{}}
	pkg, err := conf.Check("loopdrill", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatalf("budgets.go 타입 검사(time 만 import 해야 한다): %v", err)
	}

	budgets := map[string]time.Duration{}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !c.Exported() {
			continue
		}
		ns, exact := constant.Int64Val(constant.ToInt(c.Val()))
		if !exact {
			t.Fatalf("%s 가 정수 Duration 이 아니다", name)
		}
		budgets[name] = time.Duration(ns)
	}
	return budgets
}

// timeOnlyImporter 는 "time" 을 Duration 과 단위 상수만 가진 합성 패키지로 준다 —
// 툴체인 export data 없이 상수식을 평가하고, budgets.go 가 다른 것을 들이면 실패한다.
type timeOnlyImporter struct{}

func (timeOnlyImporter) Import(path string) (*types.Package, error) {
	if path != "time" {
		return nil, fmt.Errorf("budgets.go 는 time 만 import 한다: %q", path)
	}
	pkg := types.NewPackage("time", "time")
	duration := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Duration", nil), types.Typ[types.Int64], nil)
	pkg.Scope().Insert(duration.Obj())
	units := map[string]time.Duration{
		"Nanosecond": time.Nanosecond, "Microsecond": time.Microsecond, "Millisecond": time.Millisecond,
		"Second": time.Second, "Minute": time.Minute, "Hour": time.Hour,
	}
	for name, unit := range units {
		pkg.Scope().Insert(types.NewConst(token.NoPos, pkg, name, duration, constant.MakeInt64(int64(unit))))
	}
	pkg.MarkComplete()
	return pkg, nil
}
