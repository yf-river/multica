package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestResourceCreateRequestMigrationPreservesCurrentOperations(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DROP TABLE resource_create_request`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, readMigrationFile(t, "011_project_create_idempotency.up.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, readMigrationFile(t, "012_squad_create_idempotency.up.sql")); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	var workspaceID, projectID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ('Resource Request Migration', $1) RETURNING id
	`, "resource-request-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, 'Migrated Project') RETURNING id
	`, workspaceID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.NewString()
	requestKey := uuid.NewString()
	requestHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := tx.Exec(ctx, `
		INSERT INTO project_create_request (
			workspace_id, actor_id, idempotency_key, request_hash,
			project_id, response_body, completed_at
		) VALUES ($1, $2, $3, $4, $5, '{"id":"project-response"}', now())
	`, workspaceID, actorID, requestKey, requestHash, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO squad_create_request (
			workspace_id, actor_id, idempotency_key, request_hash
		) VALUES ($1, $2, $3, $4)
	`, workspaceID, actorID, requestKey, requestHash); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.Exec(ctx, readMigrationFile(t, "049_unify_resource_create_requests.up.sql")); err != nil {
		t.Fatal(err)
	}

	var rows, completed int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (
			WHERE resource_type = 'project'
			  AND resource_id = $1
			  AND response_body = '{"id":"project-response"}'::jsonb
			  AND completed_at IS NOT NULL
		)
		FROM resource_create_request
		WHERE workspace_id = $2 AND actor_id = $3 AND idempotency_key = $4
	`, projectID, workspaceID, actorID, requestKey).Scan(&rows, &completed); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || completed != 1 {
		t.Fatalf("migrated rows=%d completed project rows=%d, want 2/1", rows, completed)
	}

	var oldProjectTable, oldSquadTable *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('project_create_request')::text, to_regclass('squad_create_request')::text`).Scan(&oldProjectTable, &oldSquadTable); err != nil {
		t.Fatal(err)
	}
	if oldProjectTable != nil || oldSquadTable != nil {
		t.Fatalf("old request tables remain: project=%v squad=%v", oldProjectTable, oldSquadTable)
	}
}

func TestAttachmentCreateRequestMigrationExtendsCurrentResourceContract(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, readMigrationFile(t, "050_add_attachment_create_requests.up.sql")); err != nil {
		t.Fatal(err)
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ('Attachment Request Migration', $1) RETURNING id
	`, "attachment-request-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_create_request (
			workspace_id, actor_id, resource_type, idempotency_key, request_hash
		) VALUES ($1, $2, 'attachment', $3, $4)
	`, workspaceID, uuid.NewString(), uuid.NewString(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil {
		t.Fatalf("attachment request type rejected: %v", err)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_resource_type`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_create_request (
			workspace_id, actor_id, resource_type, idempotency_key, request_hash
		) VALUES ($1, $2, 'unknown', $3, $4)
	`, workspaceID, uuid.NewString(), uuid.NewString(), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); err == nil {
		t.Fatal("unknown resource type unexpectedly accepted")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_resource_type`); err != nil {
		t.Fatal(err)
	}
}

func TestQuickCreateIdentityMigrationEnforcesBothRequestAndIssueIdentity(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, readMigrationFile(t, "051_require_unique_quick_create_identity.up.sql")); err != nil {
		t.Fatal(err)
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ('Quick Create Identity', $1) RETURNING id
	`, "quick-create-identity-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	originID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, number, origin_type, origin_id)
		VALUES ($1, 'Quick Create Identity', 'member', $2, 1, 'quick_create', $3)
	`, workspaceID, uuid.NewString(), originID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT duplicate_origin`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, number, origin_type, origin_id)
		VALUES ($1, 'Duplicate Quick Create', 'member', $2, 2, 'quick_create', $3)
	`, workspaceID, uuid.NewString(), originID); err == nil {
		t.Fatal("duplicate quick-create origin unexpectedly accepted")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT duplicate_origin`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_create_request (
			workspace_id, actor_id, resource_type, idempotency_key, request_hash
		) VALUES ($1, $2, 'quick_create', $3, $4)
	`, workspaceID, uuid.NewString(), uuid.NewString(), "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatalf("quick-create request type rejected: %v", err)
	}
}
