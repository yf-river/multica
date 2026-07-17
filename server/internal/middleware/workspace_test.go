package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/requestctx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const testResolverSlug = "middleware-resolver-test"

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("skipping: could not connect to database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("skipping: database not reachable: %v", err)
	}
	return pool
}

func setupResolverFixture(t *testing.T, pool *pgxpool.Pool) (workspaceID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, testResolverSlug)

	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1, $2, '', 'MRT') RETURNING id`,
		"Middleware Resolver Test", testResolverSlug,
	).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, testResolverSlug)
	})
	return workspaceID
}

func TestResolveWorkspaceIDFromRequest(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)

	workspaceID := setupResolverFixture(t, pool)

	const (
		uuidA = "00000000-0000-0000-0000-000000000001"
		uuidB = "00000000-0000-0000-0000-000000000002"
	)

	cases := []struct {
		name      string
		setup     func(r *http.Request)
		want      string
		wantEmpty bool
	}{
		{
			name: "context UUID wins over everything else",
			setup: func(r *http.Request) {
				ctx := requestctx.WithWorkspace(r.Context(), uuidA, db.Member{})
				*r = *r.WithContext(ctx)
				r.Header.Set("X-Workspace-Slug", testResolverSlug)
				r.Header.Set("X-Workspace-ID", uuidB)
			},
			want: uuidA,
		},
		{
			name: "X-Workspace-Slug header resolves to UUID via DB lookup",
			setup: func(r *http.Request) {
				r.Header.Set("X-Workspace-Slug", testResolverSlug)
			},
			want: workspaceID,
		},
		{
			name: "X-Workspace-Slug wins over X-Workspace-ID",
			setup: func(r *http.Request) {
				r.Header.Set("X-Workspace-Slug", testResolverSlug)
				r.Header.Set("X-Workspace-ID", uuidB)
			},
			want: workspaceID,
		},
		{
			name: "unknown X-Workspace-Slug does not fall through to another identity",
			setup: func(r *http.Request) {
				r.Header.Set("X-Workspace-Slug", "does-not-exist")
				r.Header.Set("X-Workspace-ID", uuidB)
			},
			wantEmpty: true,
		},
		{
			name: "X-Workspace-ID header is returned when no slug provided",
			setup: func(r *http.Request) {
				r.Header.Set("X-Workspace-ID", uuidA)
			},
			want: uuidA,
		},
		{
			name: "query parameters cannot select a workspace",
			setup: func(r *http.Request) {
				q := r.URL.Query()
				q.Set("workspace", testResolverSlug)
				q.Set("tenant_id", uuidA)
				r.URL.RawQuery = q.Encode()
			},
			wantEmpty: true,
		},
		{
			name:      "no identifier at all returns empty",
			setup:     func(r *http.Request) {},
			wantEmpty: true,
		},
		{
			name: "unknown slug with no UUID fallback returns empty",
			setup: func(r *http.Request) {
				r.Header.Set("X-Workspace-Slug", "does-not-exist")
			},
			wantEmpty: true,
		},
		{
			name: "task_token actor: client-supplied slug/id cannot override token-bound workspace",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor-Source", "task_token")
				r.Header.Set("X-Workspace-ID", uuidA)
				r.Header.Set("X-Workspace-Slug", testResolverSlug)
			},
			want: uuidA,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/anything", nil)
			tc.setup(req)

			got := ResolveWorkspaceIDFromRequest(req, queries)

			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestResolveWorkspaceUUIDUsesCurrentHeaders(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)
	workspaceID := setupResolverFixture(t, pool)
	resolve := resolveWorkspaceUUID(queries)

	tests := []struct {
		name    string
		url     string
		slug    string
		id      string
		want    string
		wantErr error
	}{
		{name: "query identity is ignored", url: "/api/anything?workspace=" + testResolverSlug + "&tenant_id=00000000-0000-0000-0000-000000000001"},
		{name: "unknown slug does not fall through", url: "/api/anything", slug: "does-not-exist", id: "00000000-0000-0000-0000-000000000001", wantErr: errWorkspaceNotFound},
		{name: "current slug resolves", url: "/api/anything", slug: testResolverSlug, id: "00000000-0000-0000-0000-000000000001", want: workspaceID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.url, nil)
			request.Header.Set("X-Workspace-Slug", test.slug)
			request.Header.Set("X-Workspace-ID", test.id)
			got, err := resolve(request)
			if !errors.Is(err, test.wantErr) || got != test.want {
				t.Fatalf("resolve = %q, err=%v; want %q, err=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}
