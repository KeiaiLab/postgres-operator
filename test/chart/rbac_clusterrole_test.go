/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// 본 파일은 Helm 차트가 렌더하는 manager ClusterRole 이 config/rbac/role.yaml
// (controller-gen 정본) 과 정합함을 라이브 클러스터 없이 검증한다.
//
// 왜 Go 테스트인가: 차트 RBAC 템플릿은 controller-gen 결과를 사람이 손으로 옮긴
// 사본이라 CRD 추가 시 누락되기 쉽다. shardsplitjobs / shardranges 규칙이
// 차트에서 빠지면 operator 가 해당 리소스 watch 에서 forbidden 루프에 빠진다
// (issue #260, 0.4.0-beta.2). `helm template > /dev/null` 만으로는 *규칙 부재*
// 를 못 잡으므로, 렌더된 ClusterRole 을 실제로 파싱하여 규칙 존재를 단언한다.
package chart

import (
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// generatedRoles 는 차트 manager-role 이 담아야 할 controller-gen 생성물이다
// (마커 → role.yaml · 메트릭 인증 → metrics_auth_role.yaml).
var generatedRoles = []string{
	"../../config/rbac/role.yaml",
	"../../config/rbac/metrics_auth_role.yaml",
}

// rbacGrant 는 권한 한 칸(apiGroup, resource, verb)이다.
type rbacGrant struct {
	group, resource, verb string
}

// clusterRole 는 helm 렌더 결과에서 ClusterRole 규칙만 최소 파싱하기 위한 형태다.
type clusterRole struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Rules []struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	} `json:"rules"`
}

// findManagerClusterRole 는 렌더된 문서들에서 manager-role ClusterRole 을 찾는다.
func findManagerClusterRole(t *testing.T, docs []string) (clusterRole, bool) {
	t.Helper()
	for _, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var cr clusterRole
		// 한 문서가 ClusterRole 이 아니면 무시(파싱 실패/불일치 허용).
		if err := yaml.Unmarshal([]byte(doc), &cr); err != nil {
			continue
		}
		if cr.Kind == "ClusterRole" && strings.HasSuffix(cr.Metadata.Name, "-manager-role") {
			return cr, true
		}
	}
	return clusterRole{}, false
}

// hasRule 는 ClusterRole 에 (apiGroup, resource) 를 정확히 wantVerbs 집합으로
// 커버하는 규칙이 존재하는지 검사한다 (verb 순서 무관, 정확 일치).
func hasRule(cr clusterRole, apiGroup, resource string, wantVerbs ...string) bool {
	for _, rule := range cr.Rules {
		if !slices.Contains(rule.APIGroups, apiGroup) || !slices.Contains(rule.Resources, resource) {
			continue
		}
		if verbSetEqual(rule.Verbs, wantVerbs) {
			return true
		}
	}
	return false
}

// verbSetEqual 는 두 verb 슬라이스가 같은 집합인지 검사한다 (순서 무관).
func verbSetEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// TestManagerClusterRole_CoversShardSplitResources 는 차트가 렌더하는 manager
// ClusterRole 이 샤딩 리소스(shardranges / shardsplitjobs / shardsplitjobs/status)
// RBAC 을 config/rbac/role.yaml 과 동일한 verb 집합으로 포함하는지 보장한다.
// issue #260 (차트 RBAC 누락 → watch forbidden 루프) 회귀를 직접 차단한다.
func TestManagerClusterRole_CoversShardSplitResources(t *testing.T) {
	docs := helmTemplate(t)
	cr, found := findManagerClusterRole(t, docs)
	if !found {
		t.Fatal("렌더 결과에 manager-role ClusterRole 이 없음")
	}

	// config/rbac/role.yaml 정본: shardranges 는 create/delete 없이
	// get,list,watch,update,patch. shardsplitjobs 는 AutoSplit 마커가 create·delete 를
	// 더해 메인 CRD 규칙과 같은 전 verb 다.
	if !hasRule(cr, "postgres.keiailab.io", "shardranges",
		"get", "list", "watch", "update", "patch") {
		t.Error("manager-role 에 shardranges RBAC 규칙 부재 (get,list,watch,update,patch)")
	}
	if !hasRule(cr, "postgres.keiailab.io", "shardsplitjobs",
		"get", "list", "watch", "create", "update", "patch", "delete") {
		t.Error("manager-role 에 shardsplitjobs RBAC 규칙 부재 (get,list,watch,create,update,patch,delete)")
	}

	// status subresource 는 status 규칙 묶음(get,update,patch)에 포함되어야 한다.
	if !hasRule(cr, "postgres.keiailab.io", "shardsplitjobs/status",
		"get", "update", "patch") {
		t.Error("manager-role 에 shardsplitjobs/status RBAC 규칙 부재 (get,update,patch)")
	}
}

// grantSet 은 ClusterRole 규칙을 권한 칸 집합으로 편다.
func grantSet(cr clusterRole) map[rbacGrant]bool {
	set := map[rbacGrant]bool{}
	for _, rule := range cr.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					set[rbacGrant{group, resource, verb}] = true
				}
			}
		}
	}
	return set
}

// loadClusterRole 은 생성물 ClusterRole YAML 한 파일을 읽는다.
func loadClusterRole(t *testing.T, path string) clusterRole {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s 읽기 실패: %v", path, err)
	}
	var cr clusterRole
	if err := yaml.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("%s 파싱 실패: %v", path, err)
	}
	return cr
}

// TestChartRoleCoversRBAC 는 차트 manager-role ⊇ role.yaml ∪ metrics_auth_role.yaml 을
// 강제한다. 차트는 생성물을 손으로 옮긴 사본이라, 빠진 권한은 운영에서 forbidden
// 정지(nodes 선례)나 메트릭 인증 실패(tokenreviews)로만 드러난다.
func TestChartRoleCoversRBAC(t *testing.T) {
	cr, found := findManagerClusterRole(t, helmTemplate(t))
	if !found {
		t.Fatal("렌더 결과에 manager-role ClusterRole 이 없음")
	}
	have := grantSet(cr)

	for _, path := range generatedRoles {
		for g := range grantSet(loadClusterRole(t, path)) {
			if have[g] {
				continue
			}
			t.Errorf("차트 manager-role 에 %s 권한 없음: group=%q resource=%q verb=%q",
				path, g.group, g.resource, g.verb)
		}
	}
}

// pvcDelete 는 멤버 PVC 를 지우는 권한 칸이다 — 어떤 판에도 없어야 한다(I2, 2026-09-24 PV 누수).
var pvcDelete = rbacGrant{"", "persistentvolumeclaims", "delete"}

// TestRoleYAMLNoPVCDelete 는 마커 정본(role.yaml)이 PVC 를 지우지 못하고 재구축 엔진의 스냅숏·죽은 노드
// 판정 권한을 갖는지 본다(설계 §3.10) — 멤버 데이터를 버리는 길은 readyToUse 스냅숏 뒤 파드 재생성뿐이다.
func TestRoleYAMLNoPVCDelete(t *testing.T) {
	role := loadClusterRole(t, generatedRoles[0])
	if grantSet(role)[pvcDelete] {
		t.Error("role.yaml 이 persistentvolumeclaims delete 를 준다 — 재시드는 PVC 를 지우지 않는다")
	}
	rules := []struct {
		group, resource string
		verbs           []string
	}{
		{"", "persistentvolumeclaims", []string{"get", "list", "watch", "patch"}},
		{"snapshot.storage.k8s.io", "volumesnapshots", []string{"get", "list", "create", "delete"}},
		{"", "nodes", []string{"get", "list", "watch"}},
	}
	for _, want := range rules {
		if !hasRule(role, want.group, want.resource, want.verbs...) {
			t.Errorf("role.yaml 에 %s/%s 규칙 %v 없음", want.group, want.resource, want.verbs)
		}
	}
}

// TestChartRoleNoPVCDelete 는 차트 manager-role 에서도 PVC delete 를 뺐는지 본다 — ⊇ 시험(TestChartRoleCoversRBAC)은
// 손으로 옮긴 사본에 남은 권한을 못 잡는다.
func TestChartRoleNoPVCDelete(t *testing.T) {
	cr, found := findManagerClusterRole(t, helmTemplate(t))
	if !found {
		t.Fatal("렌더 결과에 manager-role ClusterRole 이 없음")
	}
	if grantSet(cr)[pvcDelete] {
		t.Error("차트 manager-role 이 persistentvolumeclaims delete 를 준다 — 마커에서 뺀 권한이 차트에 남았다")
	}
}
