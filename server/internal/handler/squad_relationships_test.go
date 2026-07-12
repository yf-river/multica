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

type squadRelationshipReaderStub struct{ err error }

func (s squadRelationshipReaderStub) GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error) {
	return db.Agent{}, s.err
}

func (s squadRelationshipReaderStub) GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	return db.Member{}, s.err
}

func TestSquadRelationshipLoadersDistinguishMissingFromStorageFailure(t *testing.T) {
	loaders := map[string]func(http.ResponseWriter, *http.Request, squadRelationshipReaderStub) bool{
		"agent": func(w http.ResponseWriter, r *http.Request, reader squadRelationshipReaderStub) bool {
			_, ok := loadSquadAgent(w, r, reader, parseUUID("00000000-0000-0000-0000-000000000001"), parseUUID("00000000-0000-0000-0000-000000000002"), "agent not found in this workspace")
			return ok
		},
		"member": func(w http.ResponseWriter, r *http.Request, reader squadRelationshipReaderStub) bool {
			_, ok := loadSquadMember(w, r, reader, parseUUID("00000000-0000-0000-0000-000000000001"), parseUUID("00000000-0000-0000-0000-000000000002"), "member not found in this workspace")
			return ok
		},
	}
	for name, load := range loaders {
		for _, tt := range []struct {
			name     string
			err      error
			wantCode int
			wantBody string
		}{
			{name: "missing", err: pgx.ErrNoRows, wantCode: http.StatusBadRequest, wantBody: "not found in this workspace"},
			{name: "storage", err: errors.New("storage unavailable"), wantCode: http.StatusInternalServerError, wantBody: "failed to validate squad " + name},
		} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "/api/squads", nil)
				if load(w, r, squadRelationshipReaderStub{err: tt.err}) {
					t.Fatal("expected lookup failure")
				}
				if w.Code != tt.wantCode || !strings.Contains(w.Body.String(), tt.wantBody) {
					t.Fatalf("response = %d %s, want %d containing %q", w.Code, w.Body.String(), tt.wantCode, tt.wantBody)
				}
			})
		}
	}
}
