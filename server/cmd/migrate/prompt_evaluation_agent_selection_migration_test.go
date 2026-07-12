package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationAgentSelectionMigrationNormalizesAliases(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var workspaceID, topAssetID, nestedAssetID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Agent Selection Migration',$1) RETURNING id`, "agent-selection-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	topPayload := `{"custom":"preserved","执行智能体":{"id":"11111111-1111-4111-8111-111111111111"}}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Top Agent','测试套件',$2) RETURNING id`, workspaceID, topPayload).Scan(&topAssetID); err != nil {
		t.Fatal(err)
	}
	nestedPayload := `{"运行环境":{"provider":"codex","目标智能体标识":"22222222-2222-4222-8222-222222222222"},"历史调试载荷":{"trace":"kept","execution_agent_id":"ignored-by-priority"}}`
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Nested Agent','测试套件',$2) RETURNING id`, workspaceID, nestedPayload).Scan(&nestedAssetID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "048_normalize_prompt_evaluation_agent_selection.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var topAgent, custom, nestedAgent, provider, trace string
	var hasTopAliases, hasNestedAliases bool
	if err := tx.QueryRow(ctx, `
		SELECT payload->>'agent_id', payload->>'custom',
		       payload ?| ARRAY['执行智能体','execution_agent_id','target_agent_id','执行智能体标识','目标智能体标识','execution_agent','target_agent']
		FROM prompt_evaluation_asset WHERE id=$1
	`, topAssetID).Scan(&topAgent, &custom, &hasTopAliases); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT payload->>'agent_id', payload->'运行环境'->>'provider', payload->'历史调试载荷'->>'trace',
		       (payload->'运行环境') ?| ARRAY['执行智能体','execution_agent','agent_id','execution_agent_id','target_agent_id','执行智能体标识','目标智能体标识']
		       OR (payload->'历史调试载荷') ?| ARRAY['执行智能体','execution_agent','agent_id','execution_agent_id','target_agent_id','执行智能体标识','目标智能体标识']
		FROM prompt_evaluation_asset WHERE id=$1
	`, nestedAssetID).Scan(&nestedAgent, &provider, &trace, &hasNestedAliases); err != nil {
		t.Fatal(err)
	}
	if topAgent != "11111111-1111-4111-8111-111111111111" || custom != "preserved" || hasTopAliases {
		t.Fatalf("top agent=%q custom=%q aliases=%v", topAgent, custom, hasTopAliases)
	}
	if nestedAgent != "ignored-by-priority" || provider != "codex" || trace != "kept" || hasNestedAliases {
		t.Fatalf("nested agent=%q provider=%q trace=%q aliases=%v", nestedAgent, provider, trace, hasNestedAliases)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_agent_selection`); err != nil {
		t.Fatal(err)
	}
	_, insertErr := tx.Exec(ctx, `INSERT INTO prompt_evaluation_asset (workspace_id,name,asset_type,payload) VALUES ($1,'Invalid Agent','测试套件','{"agent_id":{}}')`, workspaceID)
	if insertErr == nil {
		t.Fatal("invalid agent_id was accepted")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_agent_selection`); err != nil {
		t.Fatal(err)
	}
}
