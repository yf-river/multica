package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestRuntimeProfileFixedArgsMigrationNormalizesAndConstrainsStrings(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_fixed_args_string_array`); err != nil {
		t.Fatal(err)
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Runtime Profile Args Migration',$1) RETURNING id`, "runtime-profile-args-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	var profileID string
	if err := tx.QueryRow(ctx, `INSERT INTO runtime_profile (workspace_id,display_name,protocol_family,command_name,fixed_args) VALUES ($1,'Invalid Args','codex','codex','["ok",1]'::jsonb) RETURNING id`, workspaceID).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "040_require_runtime_profile_fixed_args_array.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var args string
	if err := tx.QueryRow(ctx, `SELECT fixed_args::text FROM runtime_profile WHERE id=$1`, profileID).Scan(&args); err != nil {
		t.Fatal(err)
	}
	if args != "[]" {
		t.Fatalf("migrated fixed_args=%s, want []", args)
	}
	for _, invalid := range []string{`null`, `{}`, `[1]`, `[""]`, `["   "]`} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_fixed_args`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime_profile SET fixed_args=$2::jsonb WHERE id=$1`, profileID, invalid); err == nil {
			t.Fatalf("constraint accepted %s", invalid)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_fixed_args`); err != nil {
			t.Fatal(err)
		}
	}
}
