package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func getTenantAdminStatus(t *testing.T, h *Handler, workspaceID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+workspaceID+"/initial-admin-status", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("workspaceId", workspaceID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	h.GetTenantInitialAdminStatus(recorder, req)
	return recorder
}

func decodeTenantAdminStatus(t *testing.T, recorder *httptest.ResponseRecorder) getTenantInitialAdminStatusResponse {
	t.Helper()
	var response getTenantInitialAdminStatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func TestGetTenantInitialAdminStatus(t *testing.T) {
	t.Run("initial owner", func(t *testing.T) {
		var account, name string
		if err := testPool.QueryRow(context.Background(), `
			SELECT u.account, u.name
			FROM member m JOIN "user" u ON u.id = m.user_id
			WHERE m.workspace_id = $1 AND m.role = 'owner'
			ORDER BY m.created_at, m.id LIMIT 1
		`, testWorkspaceID).Scan(&account, &name); err != nil {
			t.Fatalf("load expected initial owner: %v", err)
		}
		recorder := getTenantAdminStatus(t, testHandler, testWorkspaceID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
		}
		response := decodeTenantAdminStatus(t, recorder)
		if !response.Exists || response.WorkspaceID != testWorkspaceID || response.UserName == nil || *response.UserName != account || response.NickName == nil || *response.NickName != name {
			t.Fatalf("response = %+v, want owner %s/%s", response, account, name)
		}
	})

	t.Run("workspace without owner", func(t *testing.T) {
		var workspaceID string
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO workspace (name, slug, description, issue_prefix)
			VALUES ('No Owner', $1, '', 'NOA') RETURNING id
		`, "no-owner-"+uuid.NewString()).Scan(&workspaceID); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
		t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })
		recorder := getTenantAdminStatus(t, testHandler, workspaceID)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
		}
		response := decodeTenantAdminStatus(t, recorder)
		if response.Exists || response.WorkspaceID != workspaceID || response.UserName != nil || response.NickName != nil {
			t.Fatalf("response = %+v, want no owner", response)
		}
	})

	for _, testCase := range []struct {
		name, workspaceID string
		want              int
	}{
		{name: "malformed id", workspaceID: "not-a-uuid", want: http.StatusBadRequest},
		{name: "oversized id", workspaceID: strings.Repeat("a", 51), want: http.StatusBadRequest},
		{name: "missing workspace", workspaceID: uuid.NewString(), want: http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := getTenantAdminStatus(t, testHandler, testCase.workspaceID)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}

func TestGetTenantInitialAdminStatusDatabaseFailure(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	queries := db.New(pool)
	pool.Close()
	recorder := getTenantAdminStatus(t, &Handler{Queries: queries}, testWorkspaceID)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetTenantInitialAdminStatusRequiresAuthentication(t *testing.T) {
	router := chi.NewRouter()
	router.Use(middleware.Auth(nil, nil, nil))
	router.Get("/api/workspaces/{workspaceId}/initial-admin-status", testHandler.GetTenantInitialAdminStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/initial-admin-status", nil))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "missing authorization") {
		t.Fatalf("response = %d %s, want missing-authorization 401", recorder.Code, recorder.Body.String())
	}
}
