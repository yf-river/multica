package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWorkspaceSecretSettingMigrationUsesOneCurrentKey(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	insert := func(name string, settings string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO workspace (name, slug, issue_prefix, settings)
			VALUES ($1, $2, 'WSR', $3::jsonb) RETURNING id
		`, name, "secret-setting-"+uuid.NewString(), settings).Scan(&id); err != nil {
			t.Fatalf("insert workspace %s: %v", name, err)
		}
		return id
	}

	legacyID := insert("Legacy secret setting", `{"theme":"dark","always_redact_env":true}`)
	precedenceID := insert("Current secret setting", `{"always_redact_env":true,"always_redact_secrets":false}`)

	up := readMigrationFile(t, "023_rename_workspace_secret_setting.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	var legacyRemoved, currentValue, themePreserved bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT (settings ? 'always_redact_env'),
		       settings ->> 'always_redact_secrets' = 'true',
		       settings ->> 'theme' = 'dark'
		FROM workspace WHERE id = $1
	`, legacyID).Scan(&legacyRemoved, &currentValue, &themePreserved); err != nil {
		t.Fatalf("read migrated legacy workspace: %v", err)
	}
	if !legacyRemoved || !currentValue || !themePreserved {
		t.Fatalf("legacy migration result removed=%v current=%v theme=%v", legacyRemoved, currentValue, themePreserved)
	}

	var precedencePreserved bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT (settings ? 'always_redact_env')
		   AND settings ->> 'always_redact_secrets' = 'false'
		FROM workspace WHERE id = $1
	`, precedenceID).Scan(&precedencePreserved); err != nil {
		t.Fatalf("read current-key workspace: %v", err)
	}
	if !precedencePreserved {
		t.Fatal("existing always_redact_secrets value did not win over retired alias")
	}
}
