package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSkillClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	skillID := "11111111-1111-4111-8111-111111111111"
	req := newRequest(http.MethodGet, "/api/skills/"+skillID, nil).WithContext(ctx)
	req = withURLParam(req, "id", skillID)
	w := httptest.NewRecorder()

	testHandler.GetSkill(w, req)

	if w.Code != 499 {
		t.Fatalf("GetSkill canceled request: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
