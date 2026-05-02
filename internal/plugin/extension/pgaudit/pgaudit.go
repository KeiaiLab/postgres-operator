/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package pgaudit는 pgAudit extension의 ExtensionPlugin 구현이다.
//
// SharedPreloadOrder=100. pgvector 와 동률 (사전 정렬에서 앞).
// PreInstall/PostInstall 은 P10-T3 에서 audit 정책 설정으로 채워진다.
package pgaudit

import (
	"context"
	"database/sql"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	Name         = "pgaudit"
	PreloadOrder = 100
)

type Plugin struct{}

var _ plugin.ExtensionPlugin = (*Plugin)(nil)

func (Plugin) Name() string                                   { return Name }
func (Plugin) SharedPreloadOrder() int                        { return PreloadOrder }
func (Plugin) PreInstall(_ context.Context, _ *sql.DB) error  { return nil }
func (Plugin) PostInstall(_ context.Context, _ *sql.DB) error { return nil }
func (Plugin) Validate(_ string) error                        { return nil }

func Register(r *plugin.Registry) { r.RegisterExtension(Plugin{}) }
