package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type promptEvaluationCandidateReaderStub struct {
	candidate db.PromptEvaluationOptimizationCandidate
	err       error
	params    db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams
}

func (s *promptEvaluationCandidateReaderStub) GetPromptEvaluationOptimizationCandidateInWorkspace(_ context.Context, params db.GetPromptEvaluationOptimizationCandidateInWorkspaceParams) (db.PromptEvaluationOptimizationCandidate, error) {
	s.params = params
	return s.candidate, s.err
}

func TestLoadPromptEvaluationOptimizationCandidate(t *testing.T) {
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	candidateID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}

	tests := []struct {
		name     string
		reader   *promptEvaluationCandidateReaderStub
		wantOK   bool
		wantCode int
	}{
		{name: "success", reader: &promptEvaluationCandidateReaderStub{candidate: db.PromptEvaluationOptimizationCandidate{ID: candidateID}}, wantOK: true, wantCode: http.StatusOK},
		{name: "missing", reader: &promptEvaluationCandidateReaderStub{err: pgx.ErrNoRows}, wantCode: http.StatusNotFound},
		{name: "storage failure", reader: &promptEvaluationCandidateReaderStub{err: errors.New("storage unavailable")}, wantCode: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			candidate, ok := loadPromptEvaluationOptimizationCandidate(w, req, tt.reader, workspaceID, candidateID)
			if ok != tt.wantOK || w.Code != tt.wantCode {
				t.Fatalf("loadPromptEvaluationOptimizationCandidate() ok=%t code=%d, want ok=%t code=%d body=%s", ok, w.Code, tt.wantOK, tt.wantCode, w.Body.String())
			}
			if tt.reader.params.WorkspaceID != workspaceID || tt.reader.params.ID != candidateID {
				t.Fatalf("query params = %+v, want workspace=%v candidate=%v", tt.reader.params, workspaceID, candidateID)
			}
			if tt.wantOK && candidate.ID != candidateID {
				t.Fatalf("candidate ID = %v, want %v", candidate.ID, candidateID)
			}
		})
	}
}
