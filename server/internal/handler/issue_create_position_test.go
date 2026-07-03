package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func createIssueAndGetPosition(t *testing.T, title string) (string, float64) {
	t.Helper()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    title,
		"status":   "todo",
		"priority": "low",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue %q: expected 201, got %d: %s", title, w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue %q: %v", title, err)
	}
	return issue.ID, issue.Position
}

// TestCreateIssuePositionTopOfColumn verifies that a newly created issue is
// placed above all existing issues in the same status column (manual sort order).
//
// Before the fix, new issues were always assigned position=0. After drag-reorder
// activity, existing issues accumulate negative positions at the top of the
// column, so a fresh issue at 0 would land in the middle of a long list.
//
// The fix queries MIN(position) for the workspace+status pair and assigns
// newPosition = minPos - 1, so the new ticket always appears first.
func TestCreateIssuePositionTopOfColumn(t *testing.T) {
	// Create two issues via the API. The first lands at COALESCE(MIN,0)-1 = -1,
	// the second at -2, and so on — each successive issue ends up above the
	// previous one, which is exactly the desired behavior.
	id1, pos1 := createIssueAndGetPosition(t, "position-test first issue")
	t.Cleanup(func() { deleteTestIssue(t, id1) })

	id2, pos2 := createIssueAndGetPosition(t, "position-test second issue")
	t.Cleanup(func() { deleteTestIssue(t, id2) })

	id3, pos3 := createIssueAndGetPosition(t, "position-test third issue")
	t.Cleanup(func() { deleteTestIssue(t, id3) })

	// Each new issue must have a strictly lower position than the previous one,
	// ensuring it sorts to the top of the column in manual order.
	if pos2 >= pos1 {
		t.Errorf("second issue position (%v) should be less than first (%v)", pos2, pos1)
	}
	if pos3 >= pos2 {
		t.Errorf("third issue position (%v) should be less than second (%v)", pos3, pos2)
	}
}

// TestCreateIssuePositionBelowExplicitMinimum verifies the fix against a
// realistic drag-reordered column: after manually setting a low position
// directly in the DB (simulating drag-and-drop), a new issue created via the
// API should land below the explicit minimum, not at 0.
func TestCreateIssuePositionBelowExplicitMinimum(t *testing.T) {
	// Create a seed issue via the API.
	seedID, _ := createIssueAndGetPosition(t, "position-seed issue")
	t.Cleanup(func() { deleteTestIssue(t, seedID) })

	// Simulate drag-and-drop: overwrite the seed's position to a large negative
	// value (-9999), as if the user dragged it to the very top of a long list.
	const simulatedMinPos = -9999.0
	if _, err := testPool.Exec(t.Context(),
		`UPDATE issue SET position = $1 WHERE id = $2`,
		simulatedMinPos, seedID,
	); err != nil {
		t.Fatalf("failed to set explicit position: %v", err)
	}

	// Now create a new issue. It must land below -9999, not at 0.
	newIssueID, newPosition := createIssueAndGetPosition(t, "position-new issue")
	t.Cleanup(func() { deleteTestIssue(t, newIssueID) })

	if newPosition >= simulatedMinPos {
		t.Errorf("new issue position (%v) should be less than simulated min (%v); got position 0 (unfixed behavior)?",
			newPosition, simulatedMinPos)
	}
}

func TestAutopilotCreateIssuePositionBelowCurrentMinimum(t *testing.T) {
	ctx := t.Context()
	seedTitle := fmt.Sprintf("position-autopilot seed %d", time.Now().UnixNano())
	autopilotIssueTitle := fmt.Sprintf("position-autopilot issue %d", time.Now().UnixNano())

	seedID, _ := createIssueAndGetPosition(t, seedTitle)
	t.Cleanup(func() { deleteTestIssue(t, seedID) })

	const simulatedMinPos = -9999.0
	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET position = $1 WHERE id = $2`,
		simulatedMinPos, seedID,
	); err != nil {
		t.Fatalf("failed to set explicit position: %v", err)
	}

	var minBefore float64
	if err := testPool.QueryRow(ctx,
		`SELECT MIN(position) FROM issue WHERE workspace_id = $1 AND status = 'todo'`,
		testWorkspaceID,
	).Scan(&minBefore); err != nil {
		t.Fatalf("load min position: %v", err)
	}

	fixture := createDispatchedAutopilotIssue(t, ctx, "Position autopilot", autopilotIssueTitle, nil)

	var createdPos float64
	if err := testPool.QueryRow(ctx, `SELECT position FROM issue WHERE id = $1`, fixture.issueID).Scan(&createdPos); err != nil {
		t.Fatalf("load autopilot-created issue position: %v", err)
	}
	if createdPos >= minBefore {
		t.Errorf("autopilot-created issue position (%v) should be less than current min (%v); fixed position 0 would sort in the middle",
			createdPos, minBefore)
	}
}
