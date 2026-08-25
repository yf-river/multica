package middleware

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/requestctx"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// errWorkspaceNotFound is returned when a slug was provided but doesn't match
// any workspace. This lets the middleware distinguish "no identifier provided"
// (400) from "identifier provided but invalid" (404).
var errWorkspaceNotFound = errors.New("workspace not found")

// ResolveWorkspaceIDFromRequest returns the workspace UUID for an HTTP
// request using the same priority order as the workspace middleware. This is
// the single source of truth for "which workspace is this request targeting?",
// shared by middleware-protected routes (via context fast path) and
// middleware-less routes (e.g. /api/upload-file) that must resolve the slug
// themselves.
//
// Priority:
//  1. task-token binding (X-Actor-Source == "task_token") — authoritative,
//     server-set, cannot be re-negotiated by the client (MUL-2600)
//  2. middleware-injected context (fast path for middleware-protected routes)
//  3. X-Workspace-Slug header → GetWorkspaceBySlug → UUID (Web/Desktop)
//  4. X-Workspace-ID header (CLI/daemon)
//
// Returns "" when no identifier was provided OR a slug was provided but
// doesn't resolve to any workspace. Callers that need to distinguish "no
// identifier" (400) from "invalid slug" (404) should use the middleware's
// internal resolver instead — this helper collapses both cases to "" for
// simpler handler-level checks.
func ResolveWorkspaceIDFromRequest(r *http.Request, queries *db.Queries) string {
	id, _ := resolveWorkspaceTarget(r, queries, true)
	return id
}

// workspaceResolver extracts a workspace UUID from the request.
// Returns ("", nil) if no workspace identifier was provided at all.
// Returns ("", errWorkspaceNotFound) if a slug was provided but doesn't exist.
// Returns (uuid, nil) on success.
type workspaceResolver func(r *http.Request) (string, error)

// resolveWorkspaceUUID builds a resolver that accepts slug-first identification.
//
// Priority:
//  1. task-token binding (X-Actor-Source == "task_token") — authoritative,
//     server-set; the agent cannot widen its workspace scope by passing a
//     different slug/id (MUL-2600)
//  2. X-Workspace-Slug header → GetWorkspaceBySlug → UUID (Web/Desktop)
//  3. X-Workspace-ID header → UUID directly (CLI/daemon)
//
// TODO: cache slug→UUID lookup (slug is immutable, safe to cache with short TTL)
func resolveWorkspaceUUID(queries *db.Queries) workspaceResolver {
	return func(r *http.Request) (string, error) {
		return resolveWorkspaceTarget(r, queries, false)
	}
}

func resolveWorkspaceTarget(r *http.Request, queries *db.Queries, useContext bool) (string, error) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		id := r.Header.Get("X-Workspace-ID")
		if id == "" {
			return "", errWorkspaceNotFound
		}
		return id, nil
	}
	if useContext {
		if id := requestctx.WorkspaceID(r.Context()); id != "" {
			return id, nil
		}
	}
	if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
		workspace, err := queries.GetWorkspaceBySlug(r.Context(), slug)
		if err != nil {
			return "", errWorkspaceNotFound
		}
		return util.UUIDToString(workspace.ID), nil
	}
	return r.Header.Get("X-Workspace-ID"), nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

// RequireWorkspaceMember resolves the workspace from the current slug or UUID
// header, validates membership, and injects the member and workspace ID
// into the request context.
func RequireWorkspaceMember(queries *db.Queries) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), nil)
}

// RequireWorkspaceMemberFromURL resolves the workspace ID from a chi URL
// parameter, validates membership, and injects into context.
func RequireWorkspaceMemberFromURL(queries *db.Queries, param string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, nil)
}

// RequireWorkspaceRoleFromURL is like RequireWorkspaceMemberFromURL but
// additionally checks that the member has one of the specified roles.
func RequireWorkspaceRoleFromURL(queries *db.Queries, param string, roles ...string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, roles)
}

func buildMiddleware(queries *db.Queries, resolve workspaceResolver, roles []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			workspaceID, resolveErr := resolve(r)
			if resolveErr != nil {
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}
			if workspaceID == "" {
				writeError(w, http.StatusBadRequest, "workspace header is required")
				return
			}

			// Final task-token binding check: even when the workspace
			// was resolved from a chi URL parameter
			// (RequireWorkspaceMemberFromURL), the agent must not be
			// allowed to operate on a workspace other than the one
			// stamped into its task token. This is the catch-all
			// behind resolveWorkspaceUUID's earlier check. MUL-2600.
			if r.Header.Get("X-Actor-Source") == "task_token" {
				bound := r.Header.Get("X-Workspace-ID")
				if bound == "" || workspaceID != bound {
					writeError(w, http.StatusForbidden, "task token is bound to a different workspace")
					return
				}
			}

			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeError(w, http.StatusUnauthorized, "user not authenticated")
				return
			}

			userUUID, err := util.ParseUUID(userID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "user not authenticated")
				return
			}
			wsUUID, err := util.ParseUUID(workspaceID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid workspace_id")
				return
			}
			member, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      userUUID,
				WorkspaceID: wsUUID,
			})
			if err != nil {
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}

			if len(roles) > 0 {
				allowed := false
				for _, role := range roles {
					if member.Role == role {
						allowed = true
						break
					}
				}
				if !allowed {
					writeError(w, http.StatusForbidden, "insufficient permissions")
					return
				}
			}

			ctx := requestctx.WithWorkspace(r.Context(), workspaceID, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
