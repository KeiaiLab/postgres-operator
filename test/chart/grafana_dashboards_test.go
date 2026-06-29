/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package chart 는 Helm 차트 렌더링 결과를 라이브 클러스터 없이 검증한다.
//
// 왜 Go 테스트인가: grafana-dashboards.yaml 템플릿은 완전한 Grafana 대시보드
// JSON 문서를 Helm YAML 안에 ConfigMap.data 로 임베드한다. legendFormat 의
// 리터럴 중괄호({{name}})를 내보내기 위해 `{{ "{{" }}name{{ "}}" }}` 형태의
// Helm 이스케이프를 쓰는데, 이스케이프가 한 곳이라도 깨지면 임베드 JSON 이
// 조용히 깨진 채 렌더링된다(`helm template > /dev/null` 만으로는 못 잡음).
// 본 테스트는 렌더 결과를 실제로 JSON 파싱하여 회귀를 차단한다.
package chart

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// chartPath 는 본 테스트 파일(test/chart/) 기준 차트 디렉터리의 상대 경로다.
const chartPath = "../../charts/postgres-operator"

// configMap 는 helm 렌더 결과에서 ConfigMap 만 최소 파싱하기 위한 형태다.
type configMap struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// grafanaDashboard 는 임베드 JSON 의 검증 대상 필드만 추출한다.
type grafanaDashboard struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Panels []struct {
		Title   string `json:"title"`
		Targets []struct {
			Expr string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
}

// helmTemplate 는 helm 이 PATH 에 없으면 스킵하고, 있으면 차트를 렌더해
// `---` 로 분리된 매니페스트 문서들을 반환한다.
func helmTemplate(t *testing.T, extraArgs ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skipf("helm 바이너리 부재 — 차트 렌더 검증 스킵 (%v)", err)
	}
	abs, err := filepath.Abs(chartPath)
	if err != nil {
		t.Fatalf("차트 경로 절대화 실패: %v", err)
	}
	args := append([]string{"template", "rt", abs}, extraArgs...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template 실패: %v\n%s", err, out)
	}
	return strings.Split(string(out), "\n---\n")
}

// findDashboardConfigMap 는 렌더된 문서들에서 grafana-dashboards ConfigMap 을 찾는다.
// 없으면 (found=false) 반환 — 호출자가 "토글 off 시 부재" 검증에 사용.
func findDashboardConfigMap(t *testing.T, docs []string) (configMap, bool) {
	t.Helper()
	for _, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var cm configMap
		// 한 문서가 ConfigMap 이 아니면 무시(파싱 실패 허용).
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			continue
		}
		if cm.Kind == "ConfigMap" && strings.HasSuffix(cm.Metadata.Name, "-grafana-dashboards") {
			return cm, true
		}
	}
	return configMap{}, false
}

// TestGrafanaDashboards_DisabledByDefault 는 기본값(enabled=false)에서 대시보드
// ConfigMap 이 렌더되지 않아야 함을 보장한다(opt-in 보장 + 무관 환경 오염 방지).
func TestGrafanaDashboards_DisabledByDefault(t *testing.T) {
	docs := helmTemplate(t)
	if _, found := findDashboardConfigMap(t, docs); found {
		t.Fatal("기본값에서 grafana-dashboards ConfigMap 이 렌더됨 — opt-in 위반")
	}
}

// TestGrafanaDashboards_EmbeddedJSONValid 는 enabled=true 일 때 ConfigMap.data 의
// 모든 .json 키가 유효한 JSON 으로 파싱되는지 검증한다 — Helm 이스케이프 깨짐
// (legendFormat 리터럴 중괄호)으로 인한 임베드 JSON 손상 회귀를 직접 차단한다.
func TestGrafanaDashboards_EmbeddedJSONValid(t *testing.T) {
	docs := helmTemplate(t, "--set", "metrics.grafanaDashboards.enabled=true")
	cm, found := findDashboardConfigMap(t, docs)
	if !found {
		t.Fatal("enabled=true 인데 grafana-dashboards ConfigMap 이 렌더되지 않음")
	}

	jsonKeys := 0
	for key, body := range cm.Data {
		if !strings.HasSuffix(key, ".json") {
			continue
		}
		jsonKeys++
		var probe map[string]any
		if err := json.Unmarshal([]byte(body), &probe); err != nil {
			t.Errorf("data[%q] 가 유효한 JSON 이 아님 (Helm 이스케이프 손상 의심): %v", key, err)
		}
	}
	if jsonKeys == 0 {
		t.Fatal("ConfigMap.data 에 .json 대시보드 키가 하나도 없음")
	}
}

// TestGrafanaDashboards_ExpectedDashboardsAndUIDs 는 두 대시보드(cluster-overview,
// pooler)가 존재하고 각 임베드 JSON 의 uid 가 키 prefix 와 일치하는지 보장한다.
// uid 불일치는 Grafana sidecar import 시 대시보드 덮어쓰기/중복을 유발한다.
func TestGrafanaDashboards_ExpectedDashboardsAndUIDs(t *testing.T) {
	docs := helmTemplate(t, "--set", "metrics.grafanaDashboards.enabled=true")
	cm, found := findDashboardConfigMap(t, docs)
	if !found {
		t.Fatal("enabled=true 인데 grafana-dashboards ConfigMap 이 렌더되지 않음")
	}

	// sidecar 자동 import label 이 있어야 kube-prometheus-stack 이 인식한다.
	if cm.Metadata.Labels["grafana_dashboard"] != "1" {
		t.Errorf("grafana_dashboard sidecar label = %q, want \"1\"",
			cm.Metadata.Labels["grafana_dashboard"])
	}

	wantUID := map[string]string{
		"postgres-operator-cluster-overview.json": "postgres-operator-cluster-overview",
		"postgres-operator-pooler.json":           "postgres-operator-pooler",
	}
	for key, uid := range wantUID {
		body, ok := cm.Data[key]
		if !ok {
			t.Errorf("대시보드 키 %q 부재", key)
			continue
		}
		var dash grafanaDashboard
		if err := json.Unmarshal([]byte(body), &dash); err != nil {
			t.Errorf("data[%q] JSON 파싱 실패: %v", key, err)
			continue
		}
		if dash.UID != uid {
			t.Errorf("data[%q] uid = %q, want %q", key, dash.UID, uid)
		}
		if len(dash.Panels) == 0 {
			t.Errorf("data[%q] 에 패널이 하나도 없음", key)
		}
	}
}

// TestGrafanaDashboards_PoolerExposesExporterMetrics 는 pooler 대시보드가 PgBouncer
// exporter 메트릭(cnpg_pgbouncer_* prefix)을 실제 패널 target 으로 노출하는지
// 보장한다. ROADMAP G2 의 "PgBouncer exporter 메트릭 기반 collection" 주장을
// 렌더 레벨에서 회귀 차단한다(exporter prefix 변경 시 즉시 적발).
func TestGrafanaDashboards_PoolerExposesExporterMetrics(t *testing.T) {
	docs := helmTemplate(t, "--set", "metrics.grafanaDashboards.enabled=true")
	cm, found := findDashboardConfigMap(t, docs)
	if !found {
		t.Fatal("enabled=true 인데 grafana-dashboards ConfigMap 이 렌더되지 않음")
	}

	body, ok := cm.Data["postgres-operator-pooler.json"]
	if !ok {
		t.Fatal("pooler 대시보드 키 부재")
	}
	var dash grafanaDashboard
	if err := json.Unmarshal([]byte(body), &dash); err != nil {
		t.Fatalf("pooler 대시보드 JSON 파싱 실패: %v", err)
	}

	const exporterPrefix = "cnpg_pgbouncer_"
	exporterPanels := 0
	for _, panel := range dash.Panels {
		for _, target := range panel.Targets {
			if strings.Contains(target.Expr, exporterPrefix) {
				exporterPanels++
				break
			}
		}
	}
	if exporterPanels == 0 {
		t.Errorf("pooler 대시보드에 %q exporter 메트릭을 쓰는 패널이 하나도 없음", exporterPrefix)
	}
}
