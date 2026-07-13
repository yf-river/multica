package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationAssetPayloadMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE prompt_evaluation_asset DROP CONSTRAINT IF EXISTS prompt_evaluation_asset_payload_is_object`); err != nil {
		t.Fatalf("restore unconstrained asset payload: %v", err)
	}
	var workspaceID, assetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Asset Payload Migration', $1) RETURNING id`, "asset-payload-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Asset Payload Migration','数据集','[]') RETURNING id`, workspaceID).Scan(&assetID); err != nil {
		t.Fatalf("insert invalid asset payload: %v", err)
	}

	up := readMigrationFile(t, "093_require_prompt_evaluation_asset_payload_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply asset payload migration attempt %d: %v", attempt+1, err)
		}
	}
	var payload string
	if err := tx.QueryRow(ctx, `SELECT payload::text FROM prompt_evaluation_asset WHERE id=$1`, assetID).Scan(&payload); err != nil {
		t.Fatalf("read normalized asset payload: %v", err)
	}
	if payload != `{}` {
		t.Fatalf("normalized asset payload = %s, want {}", payload)
	}
	if _, err := tx.Exec(ctx, `UPDATE prompt_evaluation_asset SET payload='[]'::jsonb WHERE id=$1`, assetID); err == nil {
		t.Fatal("asset payload constraint accepted array")
	}
}
