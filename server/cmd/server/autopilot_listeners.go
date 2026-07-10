package main

import (
	"context"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerAutopilotAnalyticsListener keeps best-effort analytics outside the
// durable projection transaction. The run_done event is emitted only after the
// run state and delivery receipt commit.
func registerAutopilotAnalyticsListener(bus *events.Bus, svc *service.AutopilotService) {
	bus.Subscribe(protocol.EventAutopilotRunDone, func(event events.Event) {
		svc.CaptureAutopilotRunDone(context.Background(), event)
	})
}
