package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestExternalCredentialCreateRequestMigrationAddsAccountScopedIdentity(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		DROP INDEX IF EXISTS external_credential_profile_create_request_unique;
		ALTER TABLE external_credential_profile
			DROP COLUMN IF EXISTS idempotency_key,
			DROP COLUMN IF EXISTS request_hash;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, readMigrationFile(t, "058_add_external_credential_create_request.up.sql")); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('Credential Request Migration', $1) RETURNING id`, "credential-request-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	hash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := tx.Exec(ctx, `
		INSERT INTO external_credential_profile
			(user_id, provider, name, secret_ref, idempotency_key, request_hash)
		VALUES ($1, 'tapd', 'Current Credential', 'env:TAPD_TOKEN', $2, $3)
	`, userID, key, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT duplicate_credential_request`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO external_credential_profile
			(user_id, provider, name, secret_ref, idempotency_key, request_hash)
		VALUES ($1, 'gongfeng', 'Changed Credential', 'env:GONGFENG_TOKEN', $2, $3)
	`, userID, key, hash); err == nil {
		t.Fatal("duplicate account request identity was accepted")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT duplicate_credential_request`); err != nil {
		t.Fatal(err)
	}
}
