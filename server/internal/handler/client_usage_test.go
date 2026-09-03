package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
)

func TestNormalizeClientUsageOS(t *testing.T) {
	if got := normalizeClientUsageOS(" MacOS "); got != "macos" {
		t.Fatalf("normalizeClientUsageOS() = %q, want macos", got)
	}
	if got := normalizeClientUsageOS("Darwin 24.4"); got != "unknown" {
		t.Fatalf("normalizeClientUsageOS() = %q, want unknown", got)
	}
}

func TestUpsertClientUsageRefreshesTheDailyWebRow(t *testing.T) {
	const installID = "8d98d7db-4d40-4505-bc49-16b76db32721"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM client_usage_daily WHERE user_id = $1 AND install_id = $2`, testUserID, installID)
	})

	report := func(body any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/client-usage", body)
		req.Header.Set("X-Client-Platform", "web")
		req.Header.Set("X-Client-Version", "0.1.0")
		req.Header.Set("X-Client-OS", "macos")
		req = req.WithContext(middleware.SetClientMetadata(req.Context(), "web", "0.1.0", "macos"))
		testHandler.UpsertClientUsage(w, req)
		return w
	}

	w := report(map[string]any{"install_id": installID})
	if w.Code != http.StatusNoContent {
		t.Fatalf("first report status = %d: %s", w.Code, w.Body.String())
	}
	if w = report(map[string]any{"install_id": installID}); w.Code != http.StatusNoContent {
		t.Fatalf("activity refresh status = %d: %s", w.Code, w.Body.String())
	}

	var rowCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM client_usage_daily
		WHERE user_id = $1 AND client_type = 'web' AND install_id = $2
	`, testUserID, installID).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("daily row count = %d", rowCount)
	}
}
