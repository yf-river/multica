package service

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestResolveAutopilotTriggerTimezonePreservesMissingTrigger(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	defer pool.Close()

	service := &AutopilotService{Queries: db.New(pool)}
	missing := uuid.MustParse(uuid.NewString())
	triggerID := pgtype.UUID{Bytes: missing, Valid: true}
	if timezone, err := service.resolveAutopilotTriggerTimezone(context.Background(), triggerID); err == nil {
		t.Fatalf("missing trigger returned timezone %q without an error", timezone)
	}
}
