package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTriggerToResponseRejectsMalformedEventFilters(t *testing.T) {
	h := &Handler{}
	for _, raw := range [][]byte{[]byte(`{}`), []byte(`[{}]`), []byte(`[{"event":"push","actions":[""]}]`)} {
		if _, err := h.triggerToResponse(db.AutopilotTrigger{
			Kind:         "webhook",
			WebhookToken: pgtype.Text{String: "token", Valid: true},
			EventFilters: raw,
		}); err == nil {
			t.Fatalf("triggerToResponse event_filters=%s expected an error", raw)
		}
	}
}
