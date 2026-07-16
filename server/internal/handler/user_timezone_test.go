package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTimezoneTestUser(t *testing.T, account string) string {
	t.Helper()
	ctx := context.Background()

	var userID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id`,
		"Timezone Test", account,
	).Scan(&userID); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

func newPatchMeRequest(userID, body string) *http.Request {
	req := httptest.NewRequest("PATCH", "/api/me", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	return req
}

func patchMe(t *testing.T, userID, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.UpdateMe(w, newPatchMeRequest(userID, body))
	if w.Code != wantStatus {
		t.Fatalf("UpdateMe: expected %d, got %d: %s", wantStatus, w.Code, w.Body.String())
	}
	return w
}

func storedUserTimezone(t *testing.T, userID string) *string {
	t.Helper()
	var stored *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT timezone FROM "user" WHERE id = $1`, userID,
	).Scan(&stored); err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	return stored
}

func TestUpdateMeAcceptsTimezone(t *testing.T) {
	userID := newTimezoneTestUser(t, "tz-set@multica.ai")

	w := patchMe(t, userID, `{"timezone":"Asia/Shanghai"}`, http.StatusOK)
	stored := storedUserTimezone(t, userID)
	if stored == nil || *stored != "Asia/Shanghai" {
		t.Fatalf("expected timezone=Asia/Shanghai, got %v", stored)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := resp["timezone"].(string); got != "Asia/Shanghai" {
		t.Fatalf("expected response timezone=Asia/Shanghai, got %v", resp["timezone"])
	}
}

func TestUpdateMeRejectsInvalidTimezone(t *testing.T) {
	userID := newTimezoneTestUser(t, "tz-reject@multica.ai")

	patchMe(t, userID, `{"timezone":"Not/A/Real/Zone"}`, http.StatusBadRequest)
	stored := storedUserTimezone(t, userID)
	if stored != nil {
		t.Fatalf("expected timezone unchanged (NULL), got %v", *stored)
	}
}

func TestUpdateMePreservesTimezoneWhenNotProvided(t *testing.T) {
	userID := newTimezoneTestUser(t, "tz-preserve@multica.ai")

	if _, err := testPool.Exec(context.Background(),
		`UPDATE "user" SET timezone = 'America/Los_Angeles' WHERE id = $1`, userID,
	); err != nil {
		t.Fatalf("preset timezone: %v", err)
	}

	patchMe(t, userID, `{"name":"Updated Name"}`, http.StatusOK)
	stored := storedUserTimezone(t, userID)
	if stored == nil || *stored != "America/Los_Angeles" {
		t.Fatalf("expected timezone preserved, got %v", stored)
	}
}

func TestUpdateMeClearsTimezoneOnEmptyString(t *testing.T) {
	userID := newTimezoneTestUser(t, "tz-clear@multica.ai")

	if _, err := testPool.Exec(context.Background(),
		`UPDATE "user" SET timezone = 'Asia/Shanghai' WHERE id = $1`, userID,
	); err != nil {
		t.Fatalf("preset timezone: %v", err)
	}

	w := patchMe(t, userID, `{"timezone":""}`, http.StatusOK)
	stored := storedUserTimezone(t, userID)
	if stored != nil {
		t.Fatalf("expected timezone cleared to NULL, got %v", *stored)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["timezone"] != nil {
		t.Fatalf("expected response timezone=null, got %v", resp["timezone"])
	}
}
