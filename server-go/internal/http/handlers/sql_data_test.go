package handlers

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildSelect(t *testing.T) {
	t.Parallel()
	// No ORDER BY.
	q, args := buildSelect("public", "users", "", "", 100, 0)
	if q != `SELECT * FROM "public"."users" LIMIT $1 OFFSET $2` {
		t.Errorf("no-order SQL = %q", q)
	}
	if !reflect.DeepEqual(args, []any{100, 0}) {
		t.Errorf("args = %v", args)
	}
	// With ORDER BY + DESC. orderBy is quoted (injection-safe even though
	// the caller validates it against the real column set first).
	q2, _ := buildSelect("public", "users", "created_at", "desc", 50, 50)
	if q2 != `SELECT * FROM "public"."users" ORDER BY "created_at" DESC LIMIT $1 OFFSET $2` {
		t.Errorf("ordered SQL = %q", q2)
	}
	// A would-be-injection column name is simply quoted (defence in depth);
	// the handler rejects it earlier, but quoting means even if it slipped
	// through it's an identifier, not executable SQL.
	q3, _ := buildSelect("public", "t", `x"; DROP TABLE u; --`, "asc", 10, 0)
	if want := `ORDER BY "x""; DROP TABLE u; --" ASC`; !strings.Contains(q3, want) {
		t.Errorf("injection col not safely quoted: %q", q3)
	}
}

func TestBuildInsert(t *testing.T) {
	t.Parallel()
	q, args, err := buildInsert("public", "users", map[string]cellValue{
		"email": {Value: "a@b.co"},
		"name":  {Value: "A", IsNull: false},
		"bio":   {IsNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Columns sorted: bio, email, name.
	want := `INSERT INTO "public"."users" ("bio", "email", "name") VALUES ($1, $2, $3) RETURNING *`
	if q != want {
		t.Errorf("SQL = %q want %q", q, want)
	}
	// bio binds NULL, email/name bind their values.
	if args[0] != nil {
		t.Errorf("bio should bind NULL, got %v", args[0])
	}
	if args[1] != "a@b.co" || args[2] != "A" {
		t.Errorf("value binds wrong: %v", args)
	}

	if _, _, err := buildInsert("public", "users", nil); err == nil {
		t.Error("empty insert should error")
	}
}

func TestBuildUpdate(t *testing.T) {
	t.Parallel()
	q, args, err := buildUpdate("public", "users",
		map[string]cellValue{"email": {Value: "new@b.co"}, "active": {Value: true}},
		map[string]cellValue{"id": {Value: 7}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// SET cols sorted (active,email) → $1,$2; then WHERE id → $3.
	want := `UPDATE "public"."users" SET "active" = $1, "email" = $2 WHERE "id" = $3 RETURNING *`
	if q != want {
		t.Errorf("SQL = %q want %q", q, want)
	}
	if !reflect.DeepEqual(args, []any{true, "new@b.co", 7}) {
		t.Errorf("args = %v", args)
	}

	// NULL in SET binds nil.
	q2, args2, _ := buildUpdate("public", "t",
		map[string]cellValue{"note": {IsNull: true}},
		map[string]cellValue{"id": {Value: 1}})
	if !strings.Contains(q2, `SET "note" = $1 WHERE "id" = $2`) {
		t.Errorf("null-set SQL = %q", q2)
	}
	if args2[0] != nil {
		t.Errorf("null SET should bind nil, got %v", args2[0])
	}

	if _, _, err := buildUpdate("public", "t", nil, map[string]cellValue{"id": {Value: 1}}); err == nil {
		t.Error("empty SET should error")
	}
}

func TestBuildDelete(t *testing.T) {
	t.Parallel()
	q, args, err := buildDelete("public", "users", map[string]cellValue{"id": {Value: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if q != `DELETE FROM "public"."users" WHERE "id" = $1` {
		t.Errorf("SQL = %q", q)
	}
	if !reflect.DeepEqual(args, []any{9}) {
		t.Errorf("args = %v", args)
	}
	// Composite PK.
	q2, _, _ := buildDelete("public", "memberships",
		map[string]cellValue{"user_id": {Value: 1}, "org_id": {Value: 2}})
	if !strings.Contains(q2, `WHERE "org_id" = $1 AND "user_id" = $2`) {
		t.Errorf("composite delete SQL = %q", q2)
	}
	// Never an unbounded DELETE.
	if _, _, err := buildDelete("public", "t", nil); err == nil {
		t.Error("empty-key delete must error (no unbounded DELETE)")
	}
}

func TestPKComplete(t *testing.T) {
	t.Parallel()
	cs := newColSet([]string{"id", "email", "name"}, []string{"id"})

	if !cs.pkComplete(map[string]cellValue{"id": {Value: 1}}) {
		t.Error("exact PK should be complete")
	}
	// Missing PK col.
	if cs.pkComplete(map[string]cellValue{"email": {Value: "x"}}) {
		t.Error("non-PK key must not be complete")
	}
	// Extra non-PK col alongside PK → not exactly the PK.
	if cs.pkComplete(map[string]cellValue{"id": {Value: 1}, "email": {Value: "x"}}) {
		t.Error("PK + extra col must not be complete")
	}

	// Composite PK.
	cc := newColSet([]string{"user_id", "org_id", "role"}, []string{"user_id", "org_id"})
	if !cc.pkComplete(map[string]cellValue{"user_id": {Value: 1}, "org_id": {Value: 2}}) {
		t.Error("full composite PK should be complete")
	}
	if cc.pkComplete(map[string]cellValue{"user_id": {Value: 1}}) {
		t.Error("partial composite PK must not be complete")
	}

	// No PK at all → never complete, hasPK false.
	noPK := newColSet([]string{"a", "b"}, nil)
	if noPK.hasPK() {
		t.Error("table without PK should report hasPK=false")
	}
	if noPK.pkComplete(map[string]cellValue{"a": {Value: 1}}) {
		t.Error("no-PK table must never be pkComplete")
	}
}

func TestValidateWriteIdentifiers(t *testing.T) {
	t.Parallel()
	cs := newColSet([]string{"id", "email"}, []string{"id"})

	if bad := validateWriteIdentifiers(cs,
		map[string]cellValue{"email": {Value: "x"}},
		map[string]cellValue{"id": {Value: 1}}); bad != "" {
		t.Errorf("valid columns flagged: %q", bad)
	}
	// An unknown column is reported.
	bad := validateWriteIdentifiers(cs, map[string]cellValue{"id": {Value: 1}, "evil": {Value: "x"}})
	if bad != "evil" {
		t.Errorf("expected 'evil' flagged, got %q", bad)
	}
}
