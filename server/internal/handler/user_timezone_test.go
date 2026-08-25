package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestUpdateMeTimezoneContract(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	for _, tc := range []struct {
		name              string
		initial           *string
		body              string
		wantStatus        int
		wantStored        *string
		checkResponse     bool
		wantResponseValue any
	}{
		{
			name: "set", body: `{"timezone":"Asia/Shanghai"}`,
			wantStatus: http.StatusOK, wantStored: stringPtr("Asia/Shanghai"),
			checkResponse: true, wantResponseValue: "Asia/Shanghai",
		},
		{
			name: "reject invalid", body: `{"timezone":"Not/A/Real/Zone"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "preserve when omitted", initial: stringPtr("America/Los_Angeles"),
			body: `{"name":"Updated Name"}`, wantStatus: http.StatusOK,
			wantStored: stringPtr("America/Los_Angeles"),
		},
		{
			name: "clear with empty string", initial: stringPtr("Asia/Shanghai"),
			body: `{"timezone":""}`, wantStatus: http.StatusOK,
			checkResponse: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := newTimezoneTestUser(t, fmt.Sprintf("tz-%d@multica.ai", time.Now().UnixNano()))
			if tc.initial != nil {
				if _, err := testPool.Exec(context.Background(),
					`UPDATE "user" SET timezone = $1 WHERE id = $2`, *tc.initial, userID,
				); err != nil {
					t.Fatalf("preset timezone: %v", err)
				}
			}

			response := patchMe(t, userID, tc.body, tc.wantStatus)
			stored := storedUserTimezone(t, userID)
			if (stored == nil) != (tc.wantStored == nil) ||
				(stored != nil && *stored != *tc.wantStored) {
				t.Fatalf("stored timezone = %v, want %v", stored, tc.wantStored)
			}
			if !tc.checkResponse {
				return
			}
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload["timezone"] != tc.wantResponseValue {
				t.Fatalf("response timezone = %v, want %v", payload["timezone"], tc.wantResponseValue)
			}
		})
	}
}
