// 本文件实现「工作区上下文」相关中间件：从请求里解析请求要操作哪个工作区
// （slug 优先、UUID 兜底，task token 已绑定工作区时以绑定为准），校验当前用户在
// 该工作区的成员身份（可加角色要求），并把 member 与 workspace ID 注入请求 context，
// 供下游 handler 使用。
package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 工作区维度请求数据的 context 键。
type contextKey int

const (
	ctxKeyWorkspaceID contextKey = iota
	ctxKeyMember
)

// MemberFromContext 返回工作区中间件注入的成员（db.Member）；没注入过则 ok=false。
func MemberFromContext(ctx context.Context) (db.Member, bool) {
	m, ok := ctx.Value(ctxKeyMember).(db.Member)
	return m, ok
}

// WorkspaceIDFromContext 返回工作区中间件注入的工作区 ID；没有则返回空串。
func WorkspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyWorkspaceID).(string)
	return id
}

// SetMemberContext 把工作区 ID 与 member 注入 context。适合这样的 handler：
// 自己通过实体查找解析出了工作区，又想把这个 member 共享给下游代码。
func SetMemberContext(ctx context.Context, workspaceID string, member db.Member) context.Context {
	ctx = context.WithValue(ctx, ctxKeyWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxKeyMember, member)
	return ctx
}

// errWorkspaceNotFound 表示「给了 slug 但对不上任何工作区」。有了它中间件才能区分
// 「没给任何标识」（400）与「给了标识但非法/不存在」（404）。
var errWorkspaceNotFound = errors.New("workspace not found")

// ResolveWorkspaceIDFromRequest 按与工作区中间件相同的优先级，从 HTTP 请求里解析出
// 工作区 UUID。它是「这个请求针对哪个工作区」这一问题的唯一权威答案，被两类路由共用：
// 走中间件保护的路由（经 context 快路径）与不走中间件、必须自己解析 slug 的路由
// （如 /api/upload-file）。
//
// 优先级（从高到低）：
//  1. task-token 绑定（X-Actor-Source == "task_token"）——权威、服务端设定，
//     客户端无法重新协商（MUL-2600）；
//  2. 中间件注入的 context（中间件保护路由的快路径）；
//  3. X-Workspace-Slug 请求头 → GetWorkspaceBySlug → UUID（重构后的前端）；
//  4. ?workspace_slug 查询参数 → GetWorkspaceBySlug → UUID；
//  5. X-Workspace-ID 请求头（CLI/daemon 兼容）；
//  6. ?workspace_id 查询参数（CLI/daemon 兼容）。
//
// 没有提供任何标识、或给了 slug 却解析不到工作区，都返回 ""。需要区分「没给标识」
// （400）与「slug 无效」（404）的调用方应使用中间件内部的 resolver——本辅助函数把
// 两种情形都折叠成 ""，方便 handler 侧做简单判断。
func ResolveWorkspaceIDFromRequest(r *http.Request, queries *db.Queries) string {
	// mat_ 任务 token 在 token 行上绑定唯一工作区。Auth 中间件在剥掉客户端自带的
	// X-Actor-Source 后，把该工作区写进 X-Workspace-ID。请求上任何其它工作区标识
	// （slug 头/参数、ID 参数、URL 路径参数）都可能是 agent 在试图扩大它的影响半径
	// ——一律忽略。
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return r.Header.Get("X-Workspace-ID")
	}
	if id := WorkspaceIDFromContext(r.Context()); id != "" {
		return id
	}
	if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
		if ws, err := queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return util.UUIDToString(ws.ID)
		}
	}
	if slug := r.URL.Query().Get("workspace_slug"); slug != "" {
		if ws, err := queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return util.UUIDToString(ws.ID)
		}
	}
	if id := r.Header.Get("X-Workspace-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("workspace_id")
}

// workspaceResolver 从请求中提取工作区 UUID 的函数类型。三种返回：
//   - ("", nil)：根本没提供任何工作区标识；
//   - ("", errWorkspaceNotFound)：给了 slug 但不存在；
//   - (uuid, nil)：解析成功。
type workspaceResolver func(r *http.Request) (string, error)

// resolveWorkspaceUUID 构造一个「slug 优先」的 resolver。
//
// 优先级（从高到低）：
//  1. task-token 绑定（X-Actor-Source == "task_token"）——权威、服务端设定；
//     agent 无法靠传不同的 slug/id 扩大工作区范围（MUL-2600）；
//  2. X-Workspace-Slug 头 / ?workspace_slug 参数 → GetWorkspaceBySlug → UUID；
//  3. X-Workspace-ID 头 / ?workspace_id 参数 → 直接用 UUID（CLI/daemon 兼容）。
//
// TODO: 缓存 slug→UUID 的查询（slug 不可变，短 TTL 缓存是安全的）。
func resolveWorkspaceUUID(queries *db.Queries) workspaceResolver {
	return func(r *http.Request) (string, error) {
		// task-token 鉴权的请求必须操作 token 绑定的工作区。auth 中间件已把该 ID 写进
		// X-Workspace-ID；agent 在线上塞的任何东西（slug 头/参数、id 参数、URL 参数）
		// 都无法覆盖它。
		if r.Header.Get("X-Actor-Source") == "task_token" {
			id := r.Header.Get("X-Workspace-ID")
			if id == "" {
				return "", errWorkspaceNotFound
			}
			return id, nil
		}
		// slug 路径（首选——URL 重构后前端发这个）
		if slug := r.URL.Query().Get("workspace_slug"); slug != "" {
			ws, err := queries.GetWorkspaceBySlug(r.Context(), slug)
			if err != nil {
				return "", errWorkspaceNotFound
			}
			return util.UUIDToString(ws.ID), nil
		}
		if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
			ws, err := queries.GetWorkspaceBySlug(r.Context(), slug)
			if err != nil {
				return "", errWorkspaceNotFound
			}
			return util.UUIDToString(ws.ID), nil
		}
		// UUID 兜底（CLI、daemon、老客户端）
		if id := r.URL.Query().Get("workspace_id"); id != "" {
			return id, nil
		}
		if id := r.Header.Get("X-Workspace-ID"); id != "" {
			return id, nil
		}
		return "", nil
	}
}

// writeError 写一个极简 JSON 错误响应（Content-Type + 状态码 + {"error": msg}）。
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}

// RequireWorkspaceMember 解析工作区（slug 优先、UUID 兜底），校验当前用户是成员，
// 并把 member 与 workspace ID 注入请求 context。不是成员返回 404（对外不暴露
// 工作区是否存在）。
func RequireWorkspaceMember(queries *db.Queries) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), nil)
}

// RequireWorkspaceRole 同 RequireWorkspaceMember，但额外要求成员角色命中 roles 之一。
func RequireWorkspaceRole(queries *db.Queries, roles ...string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, resolveWorkspaceUUID(queries), roles)
}

// RequireWorkspaceMemberFromURL 从 chi URL 路径参数取工作区 ID，校验成员身份并注入
// context。供「路径里直接带工作区 ID」的路由使用。
func RequireWorkspaceMemberFromURL(queries *db.Queries, param string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, nil)
}

// RequireWorkspaceRoleFromURL 同 RequireWorkspaceMemberFromURL，但额外要求成员角色
// 命中 roles 之一。
func RequireWorkspaceRoleFromURL(queries *db.Queries, param string, roles ...string) func(http.Handler) http.Handler {
	return buildMiddleware(queries, func(r *http.Request) (string, error) {
		id := chi.URLParam(r, param)
		if id == "" {
			return "", nil
		}
		return id, nil
	}, roles)
}

// buildMiddleware 是上面四个公开中间件的公共实现：解析工作区 → 校验 task-token 绑定 →
// 取用户 → 查成员 → 按需校验角色 → 注入 context。
//
// 流程中的错误映射：slug 不存在 → 404；没提供标识 → 400；没带用户头/用户 ID 非法 →
// 401；查不到成员 → 404；角色不符 → 403。
func buildMiddleware(queries *db.Queries, resolve workspaceResolver, roles []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			workspaceID, resolveErr := resolve(r)
			if resolveErr != nil {
				writeError(w, http.StatusNotFound, "workspace not found")
				return
			}
			if workspaceID == "" {
				writeError(w, http.StatusBadRequest, "workspace_id or workspace_slug is required")
				return
			}

			// task-token 绑定的兜底校验：即使工作区是从 chi URL 路径参数解析出来的
			// （RequireWorkspaceMemberFromURL），agent 也不许操作它 task token 里盖戳
			// 之外的工作区。这是 resolveWorkspaceUUID 早先检查背后的收口逻辑。MUL-2600。
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

			ctx := SetMemberContext(r.Context(), workspaceID, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
