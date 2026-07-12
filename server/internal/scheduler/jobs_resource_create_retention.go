package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const JobNamePruneResourceCreateRequests = "prune_resource_create_requests"

const (
	resourceCreateRetentionInterval = "31 days"
	resourceCreateBatchSize         = 1000
	resourceCreateMaxBatches        = 20
)

// ResourceCreateRequestRetentionJob bounds durable response storage while
// preserving one day beyond the first-party 30-day recovery window. Incomplete
// records are evidence of an interrupted operation and are reported, not
// deleted automatically.
func ResourceCreateRequestRetentionJob(pool *pgxpool.Pool) JobSpec {
	return JobSpec{
		Name:              JobNamePruneResourceCreateRequests,
		Cadence:           time.Hour,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        5 * time.Minute,
		StaleTimeout:      10 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{time.Minute, 5 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           makeResourceCreateRequestRetentionHandler(pool),
	}
}

func makeResourceCreateRequestRetentionHandler(pool *pgxpool.Pool) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		var deleted int64
		batchLimitReached := true
		for range resourceCreateMaxBatches {
			command, err := pool.Exec(ctx, `
				WITH expired AS (
					SELECT ctid
					FROM resource_create_request
					WHERE completed_at < now() - $1::interval
					ORDER BY completed_at
					LIMIT $2
					FOR UPDATE SKIP LOCKED
				)
				DELETE FROM resource_create_request AS request
				USING expired
				WHERE request.ctid = expired.ctid
			`, resourceCreateRetentionInterval, resourceCreateBatchSize)
			if err != nil {
				return HandlerResult{}, fmt.Errorf("delete completed resource create requests: %w", err)
			}
			rows := command.RowsAffected()
			deleted += rows
			if rows < resourceCreateBatchSize {
				batchLimitReached = false
				break
			}
			if in.Heartbeat != nil {
				if err := in.Heartbeat(ctx); err != nil {
					return HandlerResult{}, err
				}
			}
		}

		var staleIncomplete int64
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM resource_create_request
			WHERE completed_at IS NULL
			  AND created_at < now() - $1::interval
		`, resourceCreateRetentionInterval).Scan(&staleIncomplete); err != nil {
			return HandlerResult{}, fmt.Errorf("count stale incomplete resource create requests: %w", err)
		}
		if staleIncomplete > 0 {
			slog.Warn("resource create retention found stale incomplete requests", "count", staleIncomplete)
		}
		if batchLimitReached {
			slog.Warn("resource create retention reached per-run delete limit", "deleted", deleted)
		}
		return HandlerResult{
			RowsAffected: deleted,
			Result: map[string]any{
				"retention_days":      31,
				"stale_incomplete":    staleIncomplete,
				"batch_limit_reached": batchLimitReached,
			},
		}, nil
	}
}
