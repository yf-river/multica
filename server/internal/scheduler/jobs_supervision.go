package scheduler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/supervision"
	"time"
)

func QualitySupervisionJob(s *supervision.Service) JobSpec {
	return JobSpec{Name: "quality_supervision", Cadence: supervision.PollInterval,
		CatchUpMode: CatchUpLatestOnly, CatchUpWindow: time.Hour,
		// 1. Preparation has a separate 120-second HTTP budget; allow time to persist its evidence.
		RunTimeout: 140 * time.Second, StaleTimeout: 3 * time.Minute, HeartbeatInterval: 15 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 3, RetryBackoff: []time.Duration{time.Minute, 5 * time.Minute},
		Scopes: StaticScopes(ScopeGlobal), Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			err := s.Tick(ctx)
			return HandlerResult{}, err
		}}
}
