/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package postgres

import (
	"errors"
	"strings"
	"testing"
)

func TestObjectGrants(t *testing.T) {
	t.Run("TABLE SELECT/INSERT 결정성", func(t *testing.T) {
		spec := GrantSpec{
			Object:     ObjectClassTable,
			Privileges: []string{"INSERT", "SELECT"}, // 입력 순서 reverse
			Names:      []string{"public.orders", "public.users"},
			Grantee:    "appuser",
		}
		got, err := BuildGrantSQL(spec)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		// 정렬: INSERT, SELECT (알파벳).
		want := `GRANT INSERT, SELECT ON TABLE "public"."orders", "public"."users" TO "appuser"`
		if got != want {
			t.Fatalf("want=%q\ngot =%q", want, got)
		}
	})

	t.Run("ALL → ALL PRIVILEGES", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassDatabase, Privileges: []string{"ALL"},
			Names: []string{"appdb"}, Grantee: "appuser",
		})
		if !strings.Contains(got, "ALL PRIVILEGES") {
			t.Fatalf("ALL must expand to ALL PRIVILEGES, got %q", got)
		}
	})

	t.Run("WITH GRANT OPTION", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassSchema, Privileges: []string{"USAGE"},
			Names: []string{"app"}, Grantee: "appuser", WithGrantOption: true,
		})
		if !strings.HasSuffix(got, "WITH GRANT OPTION") {
			t.Fatalf("WITH GRANT OPTION suffix missing: %q", got)
		}
	})

	t.Run("REVOKE 일반", func(t *testing.T) {
		got, _ := BuildRevokeSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"SELECT"},
			Names: []string{"public.users"}, Grantee: "appuser",
		})
		want := `REVOKE SELECT ON TABLE "public"."users" FROM "appuser"`
		if got != want {
			t.Fatalf("want=%q\ngot =%q", want, got)
		}
	})

	t.Run("REVOKE GRANT OPTION FOR", func(t *testing.T) {
		got, _ := BuildRevokeSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"UPDATE"},
			Names: []string{"public.users"}, Grantee: "appuser",
			WithGrantOption: true,
		})
		if !strings.HasPrefix(got, "REVOKE GRANT OPTION FOR ") {
			t.Fatalf("REVOKE GRANT OPTION FOR prefix missing: %q", got)
		}
	})

	t.Run("DEFAULT PRIVILEGES TABLE", func(t *testing.T) {
		got, err := BuildDefaultPrivilegesSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"SELECT"},
			Names:   []string{"placeholder"}, // Names 는 default privileges 에서는 무시 (validation 통과용)
			Grantee: "appuser",
		}, "public")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		want := `ALTER DEFAULT PRIVILEGES IN SCHEMA "public" GRANT SELECT ON TABLES TO "appuser"`
		if got != want {
			t.Fatalf("want=%q\ngot =%q", want, got)
		}
	})

	t.Run("DEFAULT PRIVILEGES DATABASE/SCHEMA 거부", func(t *testing.T) {
		for _, oc := range []ObjectClass{ObjectClassDatabase, ObjectClassSchema} {
			_, err := BuildDefaultPrivilegesSQL(GrantSpec{
				Object: oc, Privileges: []string{"USAGE"},
				Names: []string{"x"}, Grantee: "u",
			}, "public")
			if !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("object=%s want ErrInvalidGrant, got %v", oc, err)
			}
		}
	})

	t.Run("validation: empty grantee/names/privileges", func(t *testing.T) {
		base := GrantSpec{Object: ObjectClassTable, Privileges: []string{"SELECT"},
			Names: []string{"t"}, Grantee: "u"}
		cases := []GrantSpec{
			{Object: base.Object, Privileges: base.Privileges, Names: base.Names},                     // empty grantee
			{Object: base.Object, Privileges: base.Privileges, Grantee: base.Grantee},                 // empty names
			{Object: base.Object, Names: base.Names, Grantee: base.Grantee},                           // empty privs
			{Object: "UNKNOWN", Privileges: []string{"x"}, Names: base.Names, Grantee: base.Grantee},  // bad class
			{Object: ObjectClassTable, Privileges: []string{"NOPE"}, Names: base.Names, Grantee: "u"}, // bad priv
		}
		for i, c := range cases {
			if _, err := BuildGrantSQL(c); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("case[%d]: want ErrInvalidGrant, got %v", i, err)
			}
		}
	})

	t.Run("quoting schema.table 분리 quote", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"SELECT"},
			Names:   []string{`schema_a.table_b`},
			Grantee: "u",
		})
		if !strings.Contains(got, `"schema_a"."table_b"`) {
			t.Fatalf("schema.table 분리 quote 실패: %q", got)
		}
	})

	t.Run("quoting double-quote 자체 escape", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"SELECT"},
			Names:   []string{"plain_name"},
			Grantee: `role"with-dq`,
		})
		// grantee 의 inner `"` 는 `""` 로 escape.
		if !strings.Contains(got, `"role""with-dq"`) {
			t.Fatalf(`grantee 의 " escape 실패: %q`, got)
		}
	})

	t.Run("중복 privilege 제거", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassTable, Privileges: []string{"SELECT", "select", "SELECT"},
			Names: []string{"t"}, Grantee: "u",
		})
		if strings.Count(got, "SELECT") != 1 {
			t.Fatalf("dedupe 실패: %q", got)
		}
	})

	t.Run("결정성: 동일 입력 → 동일 출력", func(t *testing.T) {
		spec := GrantSpec{Object: ObjectClassTable, Privileges: []string{"SELECT", "INSERT"},
			Names: []string{"a", "b"}, Grantee: "u"}
		a, _ := BuildGrantSQL(spec)
		b, _ := BuildGrantSQL(spec)
		if a != b {
			t.Fatalf("non-deterministic: %q vs %q", a, b)
		}
	})

	t.Run("FUNCTION EXECUTE", func(t *testing.T) {
		got, err := BuildGrantSQL(GrantSpec{
			Object: ObjectClassFunction, Privileges: []string{"EXECUTE"},
			Names: []string{"public.calc"}, Grantee: "u",
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != `GRANT EXECUTE ON FUNCTION "public"."calc" TO "u"` {
			t.Fatalf("FUNCTION grant mismatch: %q", got)
		}
	})

	t.Run("SEQUENCE USAGE+SELECT 정렬", func(t *testing.T) {
		got, _ := BuildGrantSQL(GrantSpec{
			Object: ObjectClassSequence, Privileges: []string{"USAGE", "SELECT"},
			Names: []string{"public.seq_id"}, Grantee: "u",
		})
		if !strings.HasPrefix(got, "GRANT SELECT, USAGE ON SEQUENCE") {
			t.Fatalf("정렬 위반: %q", got)
		}
	})
}
