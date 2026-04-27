/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package postgis는 PostGIS extension의 ExtensionPlugin 구현이다.
//
// SharedPreloadOrder=300. preload는 필수 아님(extension load on first use)이나
// 일관성을 위해 동일 SDK로 등록한다.
package postgis

import (
	"context"
	"database/sql"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	Name         = "postgis"
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
