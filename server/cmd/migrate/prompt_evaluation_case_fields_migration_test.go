package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationCaseFieldsMigrationNormalizesAliases(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID, assetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Case Fields Migration',$1) RETURNING id`, "case-fields-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	legacy := `{"custom":"preserved","cases":[{"名称":"中文名称","变量":{"title":"登录"},"期望":"验收","标签":"legacy"},{"name":"old name","输入变量":{"repo":"api"},"期望包含":["trace"],"tags":["current"]},"invalid"]}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Case Fields','数据集',$2) RETURNING id`, workspaceID, legacy).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "044_normalize_prompt_evaluation_case_fields.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var payload string
	if err := tx.QueryRow(ctx, `SELECT payload::text FROM prompt_evaluation_asset WHERE id=$1`, assetID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	want := `{"cases": [{"tags": ["legacy"], "case_name": "中文名称", "variables": {"title": "登录"}, "expected_contains": ["验收"]}, {"tags": ["current"], "case_name": "old name", "variables": {"repo": "api"}, "expected_contains": ["trace"]}], "custom": "preserved"}`
	if payload != want {
		t.Fatalf("payload=%s\nwant=%s", payload, want)
	}
}
