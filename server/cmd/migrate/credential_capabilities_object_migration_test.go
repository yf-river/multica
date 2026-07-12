package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestCredentialCapabilitiesMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		ALTER TABLE external_credential_profile
		DROP CONSTRAINT IF EXISTS external_credential_capabilities_object
	`); err != nil {
		t.Fatalf("restore unconstrained capabilities: %v", err)
	}

	var userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('Credential Capabilities Migration', $1)
		RETURNING id
	`, "credential-capabilities-migration-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}

	var profileID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO external_credential_profile (
			user_id, provider, name, secret_ref, capabilities
		) VALUES ($1, 'tapd', 'Credential Capabilities Migration', 'env:TAPD_TOKEN', 'null'::jsonb)
		RETURNING id
	`, userID).Scan(&profileID); err != nil {
		t.Fatalf("insert null capabilities: %v", err)
	}

	migration := readMigrationFile(t, "038_require_credential_capabilities_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply credential capabilities migration attempt %d: %v", attempt+1, err)
		}
	}

	var capabilities string
	if err := tx.QueryRow(ctx, `
		SELECT capabilities::text FROM external_credential_profile WHERE id = $1
	`, profileID).Scan(&capabilities); err != nil {
		t.Fatalf("read normalized capabilities: %v", err)
	}
	if capabilities != "{}" {
		t.Fatalf("migrated capabilities = %s, want {}", capabilities)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_constraint
		WHERE conrelid = 'external_credential_profile'::regclass
		  AND conname = 'external_credential_capabilities_object'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read capabilities constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("capabilities constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_credential_capabilities`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE external_credential_profile SET capabilities = '[]'::jsonb WHERE id = $1
	`, profileID); err == nil {
		t.Fatal("credential capabilities constraint accepted an array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_credential_capabilities`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
