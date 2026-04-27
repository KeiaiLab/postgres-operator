/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"sort"
)

// Op는 단일 pg_dist_node 변경 종류다(RFC 0002 §5).
type Op string

const (
	OpAdd    Op = "add"
	OpUpdate Op = "update"
	OpRemove Op = "remove"
)

// Action은 단일 pg_dist_node 변경 1건이다.
//
// 호출자(SQLExecutor)는 Op에 따라 다음 SQL로 변환한다:
//   - add    → SELECT citus_add_node('<Name>', <Port>, groupid => <Group>, ...)
//   - update → SELECT citus_update_node(<old_id>, '<Name>', <Port>)  (nodeid lookup 후)
//   - remove → SELECT citus_remove_node('<Name>', <Port>)
type Action struct {
	Op   Op
	Node Node
}

// nodeKey는 (group, name, port) 3-tuple로 노드 식별자다.
type nodeKey struct {
	group int32
	name  string
	port  int32
}

func keyOf(n Node) nodeKey {
	return nodeKey{group: n.Group, name: n.Name, port: n.Port}
}

// ComputeActions는 current·desired 두 토폴로지의 차이를 적용 순서가 정해진
// Action 리스트로 변환한다(RFC 0002 §5).
//
// 결정성: 입력 슬라이스의 순서와 무관하게 동일 결과. 결과 정렬:
//
//  1. remove (분산 테이블 가용성 보전을 위해 add 전에)
//  2. update
//  3. add
//
// 같은 Op 내 정렬: (Group, Name, Port) 사전식.
func ComputeActions(current, desired []Node) []Action {
	cur := make(map[nodeKey]Node, len(current))
	for _, n := range current {
		cur[keyOf(n)] = n
	}
	des := make(map[nodeKey]Node, len(desired))
	for _, n := range desired {
		des[keyOf(n)] = n
	}

	var adds, updates, removes []Action

	for k, dn := range des {
		cn, ok := cur[k]
		if !ok {
			adds = append(adds, Action{Op: OpAdd, Node: dn})
			continue
		}
		// 같은 키지만 ShouldHaveShards나 Role/Pool/Index 같은 부가 필드가
		// 다르면 update. Group/Name/Port는 키 일치이므로 동일.
		if cn != dn {
			updates = append(updates, Action{Op: OpUpdate, Node: dn})
		}
	}
	for k, cn := range cur {
		if _, ok := des[k]; !ok {
			removes = append(removes, Action{Op: OpRemove, Node: cn})
		}
	}

	sortActions(adds)
	sortActions(updates)
	sortActions(removes)

	out := make([]Action, 0, len(removes)+len(updates)+len(adds))
	out = append(out, removes...)
	out = append(out, updates...)
	out = append(out, adds...)
	return out
}

func sortActions(a []Action) {
	sort.Slice(a, func(i, j int) bool {
		ki, kj := keyOf(a[i].Node), keyOf(a[j].Node)
		if ki.group != kj.group {
			return ki.group < kj.group
		}
		if ki.name != kj.name {
			return ki.name < kj.name
		}
		return ki.port < kj.port
	})
}
