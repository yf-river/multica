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

func TestCreateIssuePositionTopOfColumn(t *testing.T) {
	id1, pos1 := createIssueAndGetPosition(t, "position-test first issue")
	t.Cleanup(func() { deleteTestIssue(t, id1) })

	id2, pos2 := createIssueAndGetPosition(t, "position-test second issue")
	t.Cleanup(func() { deleteTestIssue(t, id2) })

	id3, pos3 := createIssueAndGetPosition(t, "position-test third issue")
	t.Cleanup(func() { deleteTestIssue(t, id3) })

	if pos2 >= pos1 {
		t.Errorf("second issue position (%v) should be less than first (%v)", pos2, pos1)
	}
	if pos3 >= pos2 {
		t.Errorf("third issue position (%v) should be less than second (%v)", pos3, pos2)
	}
}

func TestCreateIssuePositionBelowExplicitMinimum(t *testing.T) {
	seedID, _ := createIssueAndGetPosition(t, "position-seed issue")
	t.Cleanup(func() { deleteTestIssue(t, seedID) })

	const simulatedMinPos = -9999.0
	if _, err := testPool.Exec(t.Context(),
		`UPDATE issue SET position = $1 WHERE id = $2`,
		simulatedMinPos, seedID,
	); err != nil {
		t.Fatalf("failed to set explicit position: %v", err)
	}

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
