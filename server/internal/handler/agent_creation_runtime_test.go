package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentCreationRuntimeReaderStub struct{ err error }

func (s agentCreationRuntimeReaderStub) GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	return db.AgentRuntime{}, s.err
}

func TestLoadAgentCreationRuntimeDistinguishesMissingFromStorageFailure(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{name: "missing", err: pgx.ErrNoRows, wantCode: http.StatusBadRequest, wantBody: "invalid runtime_id"},
		{name: "storage failure", err: errors.New("storage unavailable"), wantCode: http.StatusInternalServerError, wantBody: "failed to validate agent creation runtime"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
			if _, ok := loadAgentCreationRuntime(w, r, agentCreationRuntimeReaderStub{err: tt.err}, parseUUID("00000000-0000-0000-0000-000000000001"), parseUUID("00000000-0000-0000-0000-000000000002")); ok {
				t.Fatal("expected lookup failure")
			}
			if w.Code != tt.wantCode || !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Fatalf("response = %d %s, want %d containing %q", w.Code, w.Body.String(), tt.wantCode, tt.wantBody)
			}
		})
	}
}
