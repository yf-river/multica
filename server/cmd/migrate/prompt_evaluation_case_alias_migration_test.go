package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationCaseAliasMigrationRemovesOnlyDuplicateKey(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID, assetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Case Alias Migration',$1) RETURNING id`, "case-alias-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Case Alias','数据集',$2) RETURNING id`, workspaceID, `{"cases":[{"case_name":"current"}],"用例":[{"名称":"legacy"}],"custom":"preserved"}`).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "043_remove_prompt_evaluation_case_alias.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var hasCases, hasAlias bool
	var custom string
	if err := tx.QueryRow(ctx, `SELECT payload ? 'cases', payload ? '用例', payload->>'custom' FROM prompt_evaluation_asset WHERE id=$1`, assetID).Scan(&hasCases, &hasAlias, &custom); err != nil {
		t.Fatal(err)
	}
	if !hasCases || hasAlias || custom != "preserved" {
		t.Fatalf("migration result cases=%v alias=%v custom=%q", hasCases, hasAlias, custom)
	}
}
