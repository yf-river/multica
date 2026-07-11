package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestQuickCreateInboxTitleMigrationNormalizesRetiredFormat(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Quick Create Title Migration', $1, 'QCM')
		RETURNING id
	`, "quick-create-title-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	insert := func(title, details string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_item (
				workspace_id, recipient_type, recipient_id, type, title, details
			) VALUES ($1, 'member', gen_random_uuid(), 'quick_create_done', $2, $3::jsonb)
			RETURNING id
		`, workspaceID, title, details).Scan(&id); err != nil {
			t.Fatalf("insert inbox item %q: %v", title, err)
		}
		return id
	}

	legacyID := insert(
		"Created QCM-42: Improve notification titles",
		`{"identifier":"QCM-42","original_prompt":"ignored"}`,
	)
	emptyLegacyID := insert(
		"Created QCM-43:",
		`{"identifier":"QCM-43","original_prompt":"  Restore\n  the prompt  "}`,
	)
	currentID := insert(
		"Already current",
		`{"identifier":"QCM-44","original_prompt":"must not replace title"}`,
	)

	migration := readMigrationFile(t, "025_normalize_quick_create_inbox_titles.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	for _, tc := range []struct {
		id   string
		want string
	}{
		{id: legacyID, want: "Improve notification titles"},
		{id: emptyLegacyID, want: "Restore the prompt"},
		{id: currentID, want: "Already current"},
	} {
		var got string
		if err := tx.QueryRow(ctx, `SELECT title FROM inbox_item WHERE id = $1`, tc.id).Scan(&got); err != nil {
			t.Fatalf("read migrated inbox item: %v", err)
		}
		if got != tc.want {
			t.Fatalf("migrated title = %q, want %q", got, tc.want)
		}
	}
}
