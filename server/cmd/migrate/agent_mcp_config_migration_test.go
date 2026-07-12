package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAgentMCPConfigMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_mcp_config_object`); err != nil {
		t.Fatalf("restore unconstrained MCP config: %v", err)
	}

	var workspaceID, runtimeID, invalidAgentID, validAgentID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Agent MCP Migration', $1) RETURNING id`, "agent-mcp-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status) VALUES ($1, 'Agent MCP Migration', 'cloud', 'codex', 'online') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id, runtime_id, name, runtime_mode, runtime_config, mcp_config) VALUES ($1,$2,'Invalid MCP','cloud','{}','[]') RETURNING id`, workspaceID, runtimeID).Scan(&invalidAgentID); err != nil {
		t.Fatalf("insert invalid MCP agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id, runtime_id, name, runtime_mode, runtime_config, mcp_config) VALUES ($1,$2,'Valid MCP','cloud','{}','{"mcpServers":{}}') RETURNING id`, workspaceID, runtimeID).Scan(&validAgentID); err != nil {
		t.Fatalf("insert valid MCP agent: %v", err)
	}

	migration := readMigrationFile(t, "085_require_agent_mcp_config_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply MCP migration attempt %d: %v", attempt+1, err)
		}
	}
	var invalidIsNull bool
	var valid string
	if err := tx.QueryRow(ctx, `SELECT mcp_config IS NULL FROM agent WHERE id=$1`, invalidAgentID).Scan(&invalidIsNull); err != nil {
		t.Fatalf("read normalized MCP config: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT mcp_config::text FROM agent WHERE id=$1`, validAgentID).Scan(&valid); err != nil {
		t.Fatalf("read preserved MCP config: %v", err)
	}
	if !invalidIsNull || valid != `{"mcpServers": {}}` {
		t.Fatalf("migration result invalidNull=%v valid=%s", invalidIsNull, valid)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent SET mcp_config='[]'::jsonb WHERE id=$1`, validAgentID); err == nil {
		t.Fatal("MCP constraint accepted array")
	}
}
