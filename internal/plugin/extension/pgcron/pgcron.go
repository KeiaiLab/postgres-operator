/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package pgcron은 pg_cron extension의 ExtensionPlugin 구현이다.
//
// SharedPreloadOrder=200. pgaudit (100) 뒤. 분산 환경에서 cron job 은 coordinator
// 에서만 실행되도록 RFC 0002 ShardRange 도입 후 추가 가드를 둔다.
package pgcron

import (
	"context"
	"database/sql"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	Name         = "pg_cron"
	PreloadOrder = 200
)

type Plugin struct{}

var _ plugin.ExtensionPlugin = (*Plugin)(nil)

func (Plugin) Name() string                                   { return Name }
func (Plugin) SharedPreloadOrder() int                        { return PreloadOrder }
func (Plugin) PreInstall(_ context.Context, _ *sql.DB) error  { return nil }
func (Plugin) PostInstall(_ context.Context, _ *sql.DB) error { return nil }
func (Plugin) Validate(_ string) error                        { return nil }

func Register(r *plugin.Registry) { r.RegisterExtension(Plugin{}) }
