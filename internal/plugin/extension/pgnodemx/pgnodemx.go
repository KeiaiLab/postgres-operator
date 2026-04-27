/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package pgnodemx는 pgnodemx extension의 ExtensionPlugin 구현이다.
//
// SharedPreloadOrder=300. cgroup/host 메트릭을 PG 함수로 노출 — pgMonitor
// (Pillar P6)의 핵심 의존성이다.
package pgnodemx

import (
	"context"
	"database/sql"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	Name         = "pgnodemx"
	PreloadOrder = 300
)

type Plugin struct{}

var _ plugin.ExtensionPlugin = (*Plugin)(nil)

func (Plugin) Name() string                                   { return Name }
func (Plugin) SharedPreloadOrder() int                        { return PreloadOrder }
func (Plugin) PreInstall(_ context.Context, _ *sql.DB) error  { return nil }
func (Plugin) PostInstall(_ context.Context, _ *sql.DB) error { return nil }
func (Plugin) Validate(_ string) error                        { return nil }

func Register(r *plugin.Registry) { r.RegisterExtension(Plugin{}) }
