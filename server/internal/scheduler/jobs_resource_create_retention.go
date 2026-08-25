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

// ResourceCreateRequestRetentionJob bounds durable mutation-response storage while
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
		tables := []string{
			"resource_create_request",
			"skill_import_request",
			"autopilot_trigger_rotation_request",
		}
		deletedByTable := make(map[string]int64, len(tables))
		staleByTable := make(map[string]int64, len(tables))
		var totalDeleted int64
		batchLimitReached := false
		for _, table := range tables {
			tableLimitReached := true
			for range resourceCreateMaxBatches {
				command, err := pool.Exec(ctx, fmt.Sprintf(`
				WITH expired AS (
					SELECT ctid
					FROM %s
					WHERE completed_at < now() - $1::interval
					ORDER BY completed_at
					LIMIT $2
					FOR UPDATE SKIP LOCKED
				)
				DELETE FROM %s AS request
				USING expired
				WHERE request.ctid = expired.ctid
			`, table, table), resourceCreateRetentionInterval, resourceCreateBatchSize)
				if err != nil {
					return HandlerResult{}, fmt.Errorf("delete completed requests from %s: %w", table, err)
				}
				rows := command.RowsAffected()
				deletedByTable[table] += rows
				totalDeleted += rows
				if rows < resourceCreateBatchSize {
					tableLimitReached = false
					break
				}
				if in.Heartbeat != nil {
					if err := in.Heartbeat(ctx); err != nil {
						return HandlerResult{}, err
					}
				}
			}
			if tableLimitReached {
				batchLimitReached = true
				slog.Warn("request retention reached per-table delete limit", "table", table, "deleted", deletedByTable[table])
			}

			var staleIncomplete int64
			if err := pool.QueryRow(ctx, fmt.Sprintf(`
			SELECT count(*)
			FROM %s
			WHERE completed_at IS NULL
			  AND created_at < now() - $1::interval
		`, table), resourceCreateRetentionInterval).Scan(&staleIncomplete); err != nil {
				return HandlerResult{}, fmt.Errorf("count stale incomplete requests in %s: %w", table, err)
			}
			staleByTable[table] = staleIncomplete
			if staleIncomplete > 0 {
				slog.Warn("request retention found stale incomplete requests", "table", table, "count", staleIncomplete)
			}
		}
		return HandlerResult{
			RowsAffected: totalDeleted,
			Result: map[string]any{
				"retention_days":   31,
				"deleted_by_table": deletedByTable,
				"stale_by_table":   staleByTable,
				"stale_incomplete": staleByTable["resource_create_request"] +
					staleByTable["skill_import_request"] +
					staleByTable["autopilot_trigger_rotation_request"],
				"batch_limit_reached": batchLimitReached,
			},
		}, nil
	}
}
