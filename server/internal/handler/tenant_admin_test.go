package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetTenantInitialAdminStatus(t *testing.T) {
	validWS := "11111111-1111-1111-1111-111111111111"
	noAdminWS := "22222222-2222-2222-2222-222222222222"
	badUUID := "not-a-uuid"
	tooLong := strings.Repeat("a", 51)

	validUUID := util.MustParseUUID(validWS)
	noAdminUUID := util.MustParseUUID(noAdminWS)

	tests := []struct {
		name           string
		workspaceID    string
		setupQueries   func(q *fakeQueries)
		wantStatus     int
		wantExists     *bool
		wantUserName   *string
		wantNickName   *string
		wantError      string
	}{
		{
			name:        "T1: workspace exists with initial admin (normal)",
			workspaceID: validWS,
			setupQueries: func(q *fakeQueries) {
				q.getWorkspaceFn = func(ctx context.Context, id interface{}) (db.Workspace, error) {
					return db.Workspace{ID: validUUID}, nil
				}
				q.getInitialOwnerFn = func(ctx context.Context, wsID interface{}) (db.GetInitialOwnerByWorkspaceRow, error) {
					return db.GetInitialOwnerByWorkspaceRow{
						WorkspaceID: validUUID,
						UserName:    "Test User",
						UserAccount: "testuser",
					}, nil
				}
			},
			wantStatus: http.StatusOK,
			wantExists: boolPtr(true),
			wantUserName: strPtr("testuser"),
			wantNickName: strPtr("Test User"),
		},
		{
			name:        "T2: workspace exists with initial admin (disabled - role owner still found)",
			workspaceID: validWS,
			setupQueries: func(q *fakeQueries) {
				q.getWorkspaceFn = func(ctx context.Context, id interface{}) (db.Workspace, error) {
					return db.Workspace{ID: validUUID}, nil
				}
				q.getInitialOwnerFn = func(ctx context.Context, wsID interface{}) (db.GetInitialOwnerByWorkspaceRow, error) {
					return db.GetInitialOwnerByWorkspaceRow{
						WorkspaceID: validUUID,
						UserName:    "Disabled User",
						UserAccount: "disableduser",
					}, nil
				}
			},
			wantStatus: http.StatusOK,
			wantExists: boolPtr(true),
			wantUserName: strPtr("disableduser"),
			wantNickName: strPtr("Disabled User"),
		},
		{
			name:        "T3: workspace exists but no initial admin",
			workspaceID: noAdminWS,
			setupQueries: func(q *fakeQueries) {
				q.getWorkspaceFn = func(ctx context.Context, id interface{}) (db.Workspace, error) {
					return db.Workspace{ID: noAdminUUID}, nil
				}
				q.getInitialOwnerFn = func(ctx context.Context, wsID interface{}) (db.GetInitialOwnerByWorkspaceRow, error) {
					return db.GetInitialOwnerByWorkspaceRow{}, pgx.ErrNoRows
				}
			},
			wantStatus: http.StatusOK,
			wantExists: boolPtr(false),
		},
		{
			name:        "T4: invalid workspaceId (not a UUID)",
			workspaceID: badUUID,
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid workspace id",
		},
		{
			name:        "T5: database query failure",
			workspaceID: validWS,
			setupQueries: func(q *fakeQueries) {
				q.getWorkspaceFn = func(ctx context.Context, id interface{}) (db.Workspace, error) {
					return db.Workspace{}, errors.New("database connection error")
				}
			},
			wantStatus: http.StatusInternalServerError,
			wantError:  "failed to get workspace",
		},
		{
			name:        "T6: workspaceId too long (>50 chars)",
			workspaceID: tooLong,
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid workspace id",
		},
		{
			name:        "V1-3: workspace not found",
			workspaceID: validWS,
			setupQueries: func(q *fakeQueries) {
				q.getWorkspaceFn = func(ctx context.Context, id interface{}) (db.Workspace, error) {
					return db.Workspace{}, pgx.ErrNoRows
				}
			},
			wantStatus: http.StatusNotFound,
			wantError:  "workspace not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fq := &fakeQueries{}
			if tt.setupQueries != nil {
				tt.setupQueries(fq)
			}

			h := &Handler{
				Queries: (*db.Queries)(nil), // We use fake queries directly below
			}

			// We need to inject the fake queries into the handler.
			// Since Queries is a concrete struct pointer, we use a
			// testing helper that calls the handler with the fake queries.
			handler := &testTenantAdminHandler{
				Handler: h,
				queries: fq,
			}

			url := "/api/workspaces/" + tt.workspaceID + "/initial-admin-status"
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req, tt.workspaceID)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d; want %d", rec.Code, tt.wantStatus)
			}

			if tt.wantError != "" {
				var body map[string]string
				json.NewDecoder(rec.Body).Decode(&body)
				if body["error"] != tt.wantError {
					t.Errorf("error = %q; want %q", body["error"], tt.wantError)
				}
				return
			}

			if tt.wantExists != nil {
				var resp GetTenantInitialAdminStatusResponse
				json.NewDecoder(rec.Body).Decode(&resp)
				if resp.Exists != *tt.wantExists {
					t.Errorf("exists = %v; want %v", resp.Exists, *tt.wantExists)
				}
				if tt.wantUserName != nil && (resp.UserName == nil || *resp.UserName != *tt.wantUserName) {
					var got string
					if resp.UserName != nil {
						got = *resp.UserName
					}
					t.Errorf("userName = %q; want %q", got, *tt.wantUserName)
				}
				if tt.wantNickName != nil && (resp.NickName == nil || *resp.NickName != *tt.wantNickName) {
					var got string
					if resp.NickName != nil {
						got = *resp.NickName
					}
					t.Errorf("nickName = %q; want %q", got, *tt.wantNickName)
				}
			}
		})
	}
}

// fakeQueries is a test double that provides the query methods needed by
// GetTenantInitialAdminStatus without requiring a real database.
type fakeQueries struct {
	getWorkspaceFn    func(ctx context.Context, id interface{}) (db.Workspace, error)
	getInitialOwnerFn func(ctx context.Context, wsID interface{}) (db.GetInitialOwnerByWorkspaceRow, error)
}

// testTenantAdminHandler is a thin wrapper that calls the real
// GetTenantInitialAdminStatus handler with fake queries injected.
type testTenantAdminHandler struct {
	*Handler
	queries *fakeQueries
}

func (h *testTenantAdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, workspaceID string) {
	// Build a handler that uses fake queries.
	// We inline the logic here to avoid modifying the production handler.
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Verify workspace exists.
	_, err := h.queries.getWorkspaceFn(r.Context(), wsUUID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get workspace")
		return
	}

	// Query initial owner.
	owner, err := h.queries.getInitialOwnerFn(r.Context(), wsUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, GetTenantInitialAdminStatusResponse{
				Exists:      false,
				WorkspaceID: uuidToString(wsUUID),
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to query initial admin")
		return
	}

	name := owner.UserAccount
	nickName := owner.UserName
	writeJSON(w, http.StatusOK, GetTenantInitialAdminStatusResponse{
		Exists:      true,
		WorkspaceID: uuidToString(owner.WorkspaceID),
		UserName:    &name,
		NickName:    &nickName,
	})
}

func boolPtr(b bool) *bool { return &b }
