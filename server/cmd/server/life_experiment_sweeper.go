package main

import (
	"context"
	"log/slog"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const lifeExperimentSweepInterval = 30 * time.Second

func runLifeExperimentSweeper(ctx context.Context, queries *db.Queries) {
	ticker := time.NewTicker(lifeExperimentSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stopExpiredLifeExperimentRounds(ctx, queries)
		}
	}
}

func stopExpiredLifeExperimentRounds(ctx context.Context, queries *db.Queries) {
	rounds, err := queries.StopAllExpiredLifeExperimentRounds(ctx)
	if err != nil {
		slog.Warn("life experiment sweeper: failed to stop expired rounds", "error", err)
		return
	}
	if len(rounds) > 0 {
		slog.Info("life experiment sweeper: stopped expired rounds", "count", len(rounds))
	}
}
