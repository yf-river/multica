package handler

import (
	"context"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestPRMergeLeavesDeliveryCompletionToProductionAcceptance(t *testing.T) {
	for _, phase := range []string{"testing", "awaiting_review", "publishing", "published", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			issueID, plan := coordinationFixture(t)
			coordinationDelivery(t, issueID, plan, phase)
			before, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
			if err != nil {
				t.Fatal(err)
			}
			testHandler.advanceIssueToDone(context.Background(), before, testWorkspaceID)
			after, err := testHandler.Queries.GetIssue(context.Background(), before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Status != after.Status || before.Revision != after.Revision {
				t.Fatalf("PR merge changed delivery-owned issue: %s -> %s", before.Status, after.Status)
			}
		})
	}
}

func TestPRMergeDoesNotCompleteWhenDeliveryLookupFails(t *testing.T) {
	issueID, _ := coordinationFixture(t)
	before, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	fault := &lookupFaultPool{DBTX: testPool, query: "IssueHasDelivery"}
	h := *testHandler
	h.Queries = db.New(fault)
	h.advanceIssueToDone(context.Background(), before, testWorkspaceID)
	if !fault.called {
		t.Fatal("delivery lookup fault was not exercised")
	}
	after, err := testHandler.Queries.GetIssue(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != after.Status || before.Revision != after.Revision {
		t.Fatal("lookup failure completed issue")
	}
}

func TestPRMergeLeavesRegisteredPlanAwaitingAcceptance(t *testing.T) {
	issueID, _ := coordinationFixture(t)
	before, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.advanceIssueToDone(context.Background(), before, testWorkspaceID)
	after, err := testHandler.Queries.GetIssue(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != after.Status || before.Revision != after.Revision {
		t.Fatal("PR merge completed registered plan before delivery submission")
	}
}
