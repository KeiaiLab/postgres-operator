/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package postgres provides pure SQL DSL helpers for the operator.
//
// 본 패키지는 K8s reconciler 와 분리된 *순수 SQL 생성기* — Go 단위
// 테스트로 SQL 결정성 / quoting / 권한 매핑을 검증한다. 실 SQL 실행
// 은 별 layer (`internal/controller/postgresdatabase_controller.go` 등).
package postgres

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// D.5.8 Role/permission database-object privilege (ROADMAP G2 L133).
//
// PostgresUser 의 `inRoles` + `passwordSecretRef` 등 *role-level* 권한은
// 이미 구현되어 있다 (`postgresuser_controller.go`). 본 파일은 *database
// object 단위* GRANT/REVOKE DSL — schema / table / sequence / function /
// database 4 object class 에 대한 declarative 권한 관리.

// ErrInvalidGrant 는 grant spec validation 실패 시 반환.
var ErrInvalidGrant = errors.New("postgres: invalid grant spec")

// ObjectClass 는 GRANT 대상 객체 종류이다.
type ObjectClass string

const (
	ObjectClassDatabase ObjectClass = "DATABASE"
	ObjectClassSchema   ObjectClass = "SCHEMA"
	ObjectClassTable    ObjectClass = "TABLE"
	ObjectClassSequence ObjectClass = "SEQUENCE"
	ObjectClassFunction ObjectClass = "FUNCTION"
)

// 각 ObjectClass 별 *허용 privilege* 집합 (PG 18 문서 정합).
//
// 출처: https://www.postgresql.org/docs/current/sql-grant.html
var allowedPrivileges = map[ObjectClass]map[string]bool{
	ObjectClassDatabase: {
		"CONNECT": true, "CREATE": true, "TEMPORARY": true, "TEMP": true,
	},
	ObjectClassSchema: {
		"CREATE": true, "USAGE": true,
	},
	ObjectClassTable: {
		"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
		"TRUNCATE": true, "REFERENCES": true, "TRIGGER": true,
	},
	ObjectClassSequence: {
		"USAGE": true, "SELECT": true, "UPDATE": true,
	},
	ObjectClassFunction: {
		"EXECUTE": true,
	},
}

// GrantSpec 는 단일 GRANT 문의 declarative 사용자 의도이다.
type GrantSpec struct {
	// Object 는 대상 객체 종류 (DATABASE/SCHEMA/TABLE/SEQUENCE/FUNCTION).
	Object ObjectClass
	// Privileges 는 부여할 권한 (대문자, 중복은 무시). "ALL" 한 항목이면 ALL PRIVILEGES.
	Privileges []string
	// Names 는 대상 객체 이름 목록 (schema-qualified 권장).
	// schema 자체 grant 면 schema 이름, table 이면 "schema.table" 권장.
	Names []string
	// Grantee 는 권한을 받을 role. 빈 문자열 금지.
	Grantee string
	// WithGrantOption 은 GRANT OPTION 추가 여부.
	WithGrantOption bool
}

// BuildGrantSQL 는 GrantSpec 으로부터 *결정적 SQL* 을 생성한다.
//
// 결정성 보장:
//   - Privileges 는 알파벳 정렬 후 join
//   - Names 는 입력 순서 보존 (사용자 의도 — 정렬하지 않음)
//   - 식별자 quoting: `pg_quoteIdentifier` 와 동등한 double-quote 처리
//
// 검증:
//   - Grantee 비어 있으면 ErrInvalidGrant
//   - Names 비어 있으면 ErrInvalidGrant
//   - Privileges 가 허용 set 외이면 ErrInvalidGrant
//   - Object 미지원 시 ErrInvalidGrant
func BuildGrantSQL(spec GrantSpec) (string, error) {
	if err := validateGrant(spec); err != nil {
		return "", err
	}
	privs := normalizePrivileges(spec.Object, spec.Privileges)
	names := joinIdentifiers(spec.Object, spec.Names)
	grantee := quoteIdentifier(spec.Grantee)

	stmt := fmt.Sprintf("GRANT %s ON %s %s TO %s",
		privs, objectClause(spec.Object), names, grantee)
	if spec.WithGrantOption {
		stmt += " WITH GRANT OPTION"
	}
	return stmt, nil
}

// BuildRevokeSQL 는 동일 spec 으로부터 REVOKE 문을 생성한다.
//
// WithGrantOption=true 면 `REVOKE GRANT OPTION FOR ...` — 권한 본체 보존.
func BuildRevokeSQL(spec GrantSpec) (string, error) {
	if err := validateGrant(spec); err != nil {
		return "", err
	}
	privs := normalizePrivileges(spec.Object, spec.Privileges)
	names := joinIdentifiers(spec.Object, spec.Names)
	grantee := quoteIdentifier(spec.Grantee)

	prefix := "REVOKE "
	if spec.WithGrantOption {
		prefix += "GRANT OPTION FOR "
	}
	return fmt.Sprintf("%s%s ON %s %s FROM %s",
		prefix, privs, objectClause(spec.Object), names, grantee), nil
}

// BuildDefaultPrivilegesSQL 는 ALTER DEFAULT PRIVILEGES 문을 생성한다.
//
// 미래 생성될 모든 TABLE / SEQUENCE / FUNCTION 에 자동 권한 부여 — 운영
// 자가 매번 GRANT 하지 않아도 새 객체에 권한 적용. schema 단위 또는 owner-role
// 단위로 지정 가능.
func BuildDefaultPrivilegesSQL(spec GrantSpec, schema string) (string, error) {
	if err := validateGrant(spec); err != nil {
		return "", err
	}
	if schema == "" {
		return "", fmt.Errorf("%w: schema must not be empty for default privileges", ErrInvalidGrant)
	}
	if spec.Object == ObjectClassDatabase || spec.Object == ObjectClassSchema {
		return "", fmt.Errorf("%w: default privileges only for TABLE/SEQUENCE/FUNCTION, got %s",
			ErrInvalidGrant, spec.Object)
	}
	privs := normalizePrivileges(spec.Object, spec.Privileges)
	grantee := quoteIdentifier(spec.Grantee)
	return fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT %s ON %sS TO %s",
		quoteIdentifier(schema), privs, spec.Object, grantee), nil
}

func validateGrant(spec GrantSpec) error {
	if spec.Grantee == "" {
		return fmt.Errorf("%w: empty Grantee", ErrInvalidGrant)
	}
	if len(spec.Names) == 0 {
		return fmt.Errorf("%w: empty Names", ErrInvalidGrant)
	}
	allowed, ok := allowedPrivileges[spec.Object]
	if !ok {
		return fmt.Errorf("%w: unsupported Object class %q", ErrInvalidGrant, spec.Object)
	}
	if len(spec.Privileges) == 0 {
		return fmt.Errorf("%w: empty Privileges", ErrInvalidGrant)
	}
	for _, p := range spec.Privileges {
		up := strings.ToUpper(strings.TrimSpace(p))
		if up == "ALL" {
			continue
		}
		if !allowed[up] {
			return fmt.Errorf("%w: privilege %q not allowed for %s", ErrInvalidGrant, p, spec.Object)
		}
	}
	return nil
}

func normalizePrivileges(_ ObjectClass, privs []string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range privs {
		up := strings.ToUpper(strings.TrimSpace(p))
		if up == "ALL" {
			return "ALL PRIVILEGES"
		}
		if !seen[up] {
			seen[up] = true
			out = append(out, up)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func joinIdentifiers(class ObjectClass, names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, quoteName(class, n))
	}
	return strings.Join(out, ", ")
}

// quoteName 은 schema-qualified 식별자를 분리하여 각 컴포넌트를 quote 한다.
//
// table / sequence / function 의 "schema.name" 형식 지원. 1 component
// 이면 unqualified (현재 search_path 기준).
func quoteName(class ObjectClass, name string) string {
	if class == ObjectClassDatabase || class == ObjectClassSchema {
		return quoteIdentifier(name)
	}
	parts := strings.SplitN(name, ".", 2)
	for i := range parts {
		parts[i] = quoteIdentifier(parts[i])
	}
	return strings.Join(parts, ".")
}

// quoteIdentifier 는 PG SQL 식별자 quoting — `pg_quoteIdentifier` 동등.
//
// double-quote 자체는 두 개 (`"`→`""`). 본 구현은 항상 quote 처리 →
// reserved keyword 또는 mixed-case 도 안전.
func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func objectClause(class ObjectClass) string {
	// SCHEMA / DATABASE 는 단일 이름이지만 TABLE / SEQUENCE / FUNCTION 도 동일 형식.
	// PostgreSQL 문법: GRANT <priv> ON <CLASS> <names> TO <role>
	return string(class)
}
