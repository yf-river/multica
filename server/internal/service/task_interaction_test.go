package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestInteractionContinuationNeverAutomaticallyReplaysApproval(t *testing.T) {
	normal := db.AgentTaskQueue{Attempt: 1, MaxAttempts: 2, IssueID: pgtype.UUID{Valid: true}}
	if !retryEligible(string(taskfailure.ReasonAgentProviderNetwork), normal) {
		t.Fatal("ordinary network retry fixture is invalid")
	}
	continuation := normal
	continuation.Context = []byte(`{"dsh_interaction_continuation":true}`)
	for _, reason := range []string{string(taskfailure.ReasonAgentProviderNetwork), "runtime_offline", "timeout"} {
		if retryEligible(reason, continuation) {
			t.Fatalf("%s would automatically replay a human decision", reason)
		}
	}
}
