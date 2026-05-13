/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"fmt"
	"sort"
	"sync"
)

// Registry는 in-process(compile-time) 플러그인 등록 메커니즘이다.
//
// out-of-process gRPC 모드(HashiCorp go-plugin 패턴)는 Pillar P13-T4 시점에
// 본 Registry를 어댑터로 감싸 추가된다. 그 시점에도 본 Registry의 공개
// 시그니처는 유지된다(외부 사용자가 in-process로 등록한 플러그인은 무관).
//
// 동시성:
//   - 등록(Register*)은 init() 또는 main() 시점에서 단일 goroutine이 호출한다고
//     가정하나, 안전을 위해 sync.RWMutex로 보호한다.
//   - 조회(Backup, Exporter, ...)는 reconcile 루프에서 다중 goroutine이 동시에
//     호출 가능하므로 RLock을 사용한다.
//
// 중복 등록 정책:
//   - 동일 Name()으로 두 번 등록 시 panic. 이는 init() 시점 결정성을 강제하는
//     선택이며, dynamic swap이 필요한 시점은 P13 후속 task에서 별도 메서드로
//     제공한다.
type Registry struct {
	mu         sync.RWMutex
	backups    map[string]BackupPlugin
	exporters  map[string]ExporterPlugin
	extensions map[string]ExtensionPlugin
	routers    map[string]RouterPlugin
	auths      map[string]AuthPlugin
}

// NewRegistry는 빈 Registry를 반환한다. 단위 테스트와 컨트롤러 부트스트랩 양쪽에서
// 사용된다. 본 오퍼레이터는 컨트롤러 부트스트랩 시 단일 인스턴스(default)를
// 만들어 reconciler에 주입한다.
func NewRegistry() *Registry {
	return &Registry{
		backups:    make(map[string]BackupPlugin),
		exporters:  make(map[string]ExporterPlugin),
		extensions: make(map[string]ExtensionPlugin),
		routers:    make(map[string]RouterPlugin),
		auths:      make(map[string]AuthPlugin),
	}
}

// RegisterBackup은 BackupPlugin을 등록한다. 중복 시 panic.
func (r *Registry) RegisterBackup(p BackupPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.backups[p.Name()]; dup {
		panic(fmt.Sprintf("plugin: duplicate BackupPlugin registration: %q", p.Name()))
	}
	r.backups[p.Name()] = p
}

// RegisterExporter는 ExporterPlugin을 등록한다. 중복 시 panic.
func (r *Registry) RegisterExporter(p ExporterPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.exporters[p.Name()]; dup {
		panic(fmt.Sprintf("plugin: duplicate ExporterPlugin registration: %q", p.Name()))
	}
	r.exporters[p.Name()] = p
}

// RegisterExtension은 ExtensionPlugin을 등록한다. 중복 시 panic.
func (r *Registry) RegisterExtension(p ExtensionPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.extensions[p.Name()]; dup {
		panic(fmt.Sprintf("plugin: duplicate ExtensionPlugin registration: %q", p.Name()))
	}
	r.extensions[p.Name()] = p
}

// RegisterRouter는 RouterPlugin을 등록한다. 중복 시 panic.
func (r *Registry) RegisterRouter(p RouterPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.routers[p.Name()]; dup {
		panic(fmt.Sprintf("plugin: duplicate RouterPlugin registration: %q", p.Name()))
	}
	r.routers[p.Name()] = p
}

// RegisterAuth는 AuthPlugin을 등록한다. 중복 시 panic.
func (r *Registry) RegisterAuth(p AuthPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.auths[p.Name()]; dup {
		panic(fmt.Sprintf("plugin: duplicate AuthPlugin registration: %q", p.Name()))
	}
	r.auths[p.Name()] = p
}

// Backup은 등록된 BackupPlugin을 이름으로 조회한다.
func (r *Registry) Backup(name string) (BackupPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.backups[name]
	return p, ok
}

// Exporter는 등록된 ExporterPlugin을 이름으로 조회한다.
func (r *Registry) Exporter(name string) (ExporterPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.exporters[name]
	return p, ok
}

// Extension은 등록된 ExtensionPlugin을 이름으로 조회한다.
func (r *Registry) Extension(name string) (ExtensionPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.extensions[name]
	return p, ok
}

// Router는 등록된 RouterPlugin을 이름으로 조회한다.
func (r *Registry) Router(name string) (RouterPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.routers[name]
	return p, ok
}

// Auth는 등록된 AuthPlugin을 이름으로 조회한다.
func (r *Registry) Auth(name string) (AuthPlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.auths[name]
	return p, ok
}

// EnabledExtensions 는 등록된 ExtensionPlugin 중 names 에 포함된 것만 반환한다
// (RFC 0006 R1 — per-cluster extension lifecycle).
//
// names 가 nil/빈 slice 면 빈 결과 (vanilla PG). 등록되지 않은 이름은 ok=false 와
// 함께 missing slice 로 보고 — webhook 이 이를 admission 차단으로 사용.
//
// 정렬 규약은 Extensions() 와 동일 (SharedPreloadOrder 오름차순 + 사전순).
func (r *Registry) EnabledExtensions(names []string) (enabled []ExtensionPlugin, missing []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range names {
		p, ok := r.extensions[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		enabled = append(enabled, p)
	}
	sort.Slice(enabled, func(i, j int) bool {
		oi, oj := enabled[i].SharedPreloadOrder(), enabled[j].SharedPreloadOrder()
		if oi != oj {
			return oi < oj
		}
		return enabled[i].Name() < enabled[j].Name()
	})
	return enabled, missing
}

// Extensions는 등록된 모든 ExtensionPlugin을 SharedPreloadOrder() 오름차순,
// 동률 시 Name() 사전순으로 정렬해 반환한다.
//
// 본 정렬 규약은 ExtensionPlugin 별 SharedPreloadOrder 가 강제하는 우선순위를
// 그대로 직렬화한다. P10 reconciler 가 본 메서드 결과를 strings.Join 하면 우선순위가
// 보장된다.
//
// Deprecated: per-cluster filtering 은 EnabledExtensions(names) 사용. 본 메서드는
// 등록된 *모든* extension 을 반환 — 모든 cluster 에 강제 효과로 cross-validation
// bug 2 의 원인. 신규 코드는 EnabledExtensions 우선.
func (r *Registry) Extensions() []ExtensionPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExtensionPlugin, 0, len(r.extensions))
	for _, p := range r.extensions {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		oi, oj := out[i].SharedPreloadOrder(), out[j].SharedPreloadOrder()
		if oi != oj {
			return oi < oj
		}
		return out[i].Name() < out[j].Name()
	})
	return out
}
