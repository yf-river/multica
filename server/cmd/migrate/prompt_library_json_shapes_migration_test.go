package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPromptLibraryJSONShapesMigrationNormalizesAndConstrains(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, readMigrationFile(t, "042_require_prompt_library_json_shapes.down.sql")); err != nil {
		t.Fatal(err)
	}
	var workspaceID, promptID, versionID, runtimeID, agentID string
	slug := "prompt-library-shapes-" + uuid.NewString()
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Prompt Library Shapes',$1) RETURNING id`, slug).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider) VALUES ($1,$2,'cloud','codex') RETURNING id`, workspaceID, "shape-runtime-"+uuid.NewString()).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,runtime_mode,runtime_id,scope) VALUES ($1,$2,'cloud',$3,'workspace') RETURNING id`, workspaceID, "shape-agent-"+uuid.NewString(), runtimeID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_library_item (workspace_id,name,content,variables,tags) VALUES ($1,$2,'x','null','{}') RETURNING id`, workspaceID, "shape-prompt-"+uuid.NewString()).Scan(&promptID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prompt_library_version (prompt_id,workspace_id,version,name,content,variables,tags) VALUES ($1,$2,1,'shape','x','{}','null') RETURNING id`, promptID, workspaceID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO prompt_library_trial (workspace_id,prompt_id,version_id,agent_id,variables) VALUES ($1,$2,$3,$4,'[]')`, workspaceID, promptID, versionID, agentID); err != nil {
		t.Fatal(err)
	}

	migration := readMigrationFile(t, "042_require_prompt_library_json_shapes.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var itemVariables, itemTags, versionVariables, versionTags, trialVariables string
	if err := tx.QueryRow(ctx, `SELECT i.variables::text,i.tags::text,v.variables::text,v.tags::text,t.variables::text FROM prompt_library_item i JOIN prompt_library_version v ON v.prompt_id=i.id JOIN prompt_library_trial t ON t.prompt_id=i.id WHERE i.id=$1`, promptID).Scan(&itemVariables, &itemTags, &versionVariables, &versionTags, &trialVariables); err != nil {
		t.Fatal(err)
	}
	if itemVariables != "[]" || itemTags != "[]" || versionVariables != "[]" || versionTags != "[]" || trialVariables != "{}" {
		t.Fatalf("normalized shapes = %q %q %q %q %q", itemVariables, itemTags, versionVariables, versionTags, trialVariables)
	}

	for name, statement := range map[string]string{
		"item variables":    `UPDATE prompt_library_item SET variables='{}' WHERE id=$1`,
		"item tags":         `UPDATE prompt_library_item SET tags='{}' WHERE id=$1`,
		"version variables": `UPDATE prompt_library_version SET variables='{}' WHERE prompt_id=$1`,
		"version tags":      `UPDATE prompt_library_version SET tags='{}' WHERE prompt_id=$1`,
		"trial variables":   `UPDATE prompt_library_trial SET variables='[]' WHERE prompt_id=$1`,
	} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_shape`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, statement, promptID); err == nil {
			t.Fatalf("%s constraint accepted invalid shape", name)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_shape`); err != nil {
			t.Fatal(err)
		}
	}
}
