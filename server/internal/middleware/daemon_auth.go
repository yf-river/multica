// 本文件实现 daemon（本机常驻代理进程）访问 API 的鉴权中间件 DaemonAuth：
// 主路径认 "mdt_" 前缀的 daemon token，并为老 daemon 保留了用用户级凭证
// （PAT/JWT/mcn_ 云节点 PAT）鉴权的回退。与用户侧 Auth 中间件的差异在于：
// 这里把身份写进 context（工作区/daemon ID/走了哪条鉴权路径），供慢日志归因。
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Daemon 维度请求数据的 context 键。
type daemonContextKey int

const (
	ctxKeyDaemonWorkspaceID daemonContextKey = iota
	ctxKeyDaemonID
	ctxKeyDaemonAuthPath
)

// Daemon 鉴权路径标签，经 context 暴露给慢日志做归因。
const (
	DaemonAuthPathDaemonToken = "daemon_token"
	DaemonAuthPathPAT         = "pat"
	DaemonAuthPathCloudPAT    = "cloud_pat"
	DaemonAuthPathJWT         = "jwt"
)

// DaemonWorkspaceIDFromContext 返回 DaemonAuth 中间件设定的工作区 ID。
func DaemonWorkspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonWorkspaceID).(string)
	return id
}

// DaemonIDFromContext 返回 DaemonAuth 中间件设定的 daemon ID。
func DaemonIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonID).(string)
	return id
}

// DaemonAuthPathFromContext 返回这次请求经哪种 token 鉴权通过——
// "daemon_token"、"pat"、"cloud_pat" 或 "jwt"，供遥测用。
// 请求没走 DaemonAuth 时返回空串。
func DaemonAuthPathFromContext(ctx context.Context) string {
	p, _ := ctx.Value(ctxKeyDaemonAuthPath).(string)
	return p
}

// WithDaemonContext 返回设置了 daemon 工作区 ID 与 daemon ID 的新 context。
// 供测试模拟「daemon token 已鉴权」的状态。
func WithDaemonContext(ctx context.Context, workspaceID, daemonID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyDaemonWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxKeyDaemonID, daemonID)
	ctx = context.WithValue(ctx, ctxKeyDaemonAuthPath, DaemonAuthPathDaemonToken)
	return ctx
}

// DaemonAuth 校验 daemon 鉴权 token（mdt_ 前缀），并为「仍用用户 token 鉴权的老
// daemon」回退到 JWT/PAT 校验。
//
// 两个缓存都可选。非 nil 时：
//   - daemonCache 在 mdt_ 路径上跳过 daemon_token 的 DB 查询；
//   - patCache 在 mul_ 回退路径上同时跳过 PAT 的 DB 查询与 last_used_at 更新。
//     该缓存与常规 Auth 中间件共用，所以「人类 CLI 与 daemon 共用同一个热门 PAT」时，
//     每个 AuthCacheTTL 窗口只产生一次 DB 往返。
//
// cloudPAT 可选：非 nil 时 mcn_ 前缀 token 通过调用 Multica Cloud Fleet 服务校验
// （X-User-ID 拿到返回的 owner_id）。为 nil 时 mcn_ 在前缀分支直接被拒——与常规 Auth
// 中间件一致的 fail-closed 契约。
//
// 缓存 miss 一律落回原始 DB 查询路径。
func DaemonAuth(queries *db.Queries, patCache *auth.PATCache, daemonCache *auth.DaemonTokenCache, cloudPAT *auth.CloudPATVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// X-Actor-Source 只允许服务端设置——在任何分支重新盖印前，先剥掉客户端
			// 自带的任何值。这与 Auth 中间件的做法一致（见 auth.go 里
			// "X-Actor-Source is server-set only..." 注释），让两个中间件对外契约统一：
			// 下游门禁（如 handler.RequireHumanActor）不管请求走哪条鉴权路径，都可以
			// 信任这个头。
			r.Header.Del("X-Actor-Source")

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				slog.Debug("daemon_auth: missing authorization header", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			tokenString := strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				slog.Debug("daemon_auth: invalid format", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "invalid authorization format")
				return
			}

			// Daemon token：前缀 "mdt_"。
			if strings.HasPrefix(tokenString, "mdt_") {
				hash := auth.HashToken(tokenString)

				if id, ok := daemonCache.Get(r.Context(), hash); ok {
					ctx := context.WithValue(r.Context(), ctxKeyDaemonWorkspaceID, id.WorkspaceID)
					ctx = context.WithValue(ctx, ctxKeyDaemonID, id.DaemonID)
					ctx = context.WithValue(ctx, ctxKeyDaemonAuthPath, DaemonAuthPathDaemonToken)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}

				if queries == nil {
					writeError(w, http.StatusUnauthorized, "invalid daemon token")
					return
				}
				dt, err := queries.GetDaemonTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("daemon_auth: invalid daemon token", "path", r.URL.Path, "error", err)
					writeError(w, http.StatusUnauthorized, "invalid daemon token")
					return
				}

				identity := auth.DaemonTokenIdentity{
					WorkspaceID: uuidToString(dt.WorkspaceID),
					DaemonID:    dt.DaemonID,
				}
				// daemon_token.expires_at 非空（NOT NULL）；正常情况下 pgtype Valid 为
				// true，但这里防御一下零值，防止算出负 TTL。
				var expiresAt time.Time
				if dt.ExpiresAt.Valid {
					expiresAt = dt.ExpiresAt.Time
				}
				daemonCache.Set(r.Context(), hash, identity, auth.TTLForExpiry(time.Now(), expiresAt))

				ctx := context.WithValue(r.Context(), ctxKeyDaemonWorkspaceID, identity.WorkspaceID)
				ctx = context.WithValue(ctx, ctxKeyDaemonID, identity.DaemonID)
				ctx = context.WithValue(ctx, ctxKeyDaemonAuthPath, DaemonAuthPathDaemonToken)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Cloud Node PAT：前缀 "mcn_"。镜像 Auth 里的 mcn_ 分支——Multica Cloud
			// Fleet 是权威，我们只把解析出的 owner_id 以 X-User-ID 呈现给下游 daemon
			// handler（它们随后按常规方式校验工作区成员身份）。同为 fail-closed 语义：
			// 没配 Fleet URL → 401；Fleet 不可达 → 503。另外还要求 owner_id 映射到本地
			// 真实用户——理由见 Auth 里的注释。
			if strings.HasPrefix(tokenString, auth.CloudPATPrefix) {
				if cloudPAT == nil {
					slog.Warn("daemon_auth: mcn_ token presented but cloud verifier not configured", "path", r.URL.Path)
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				identity, err := cloudPAT.Verify(r.Context(), tokenString, ownerLookupFor(queries))
				if err != nil {
					if errors.Is(err, auth.ErrCloudPATInvalid) {
						slog.Warn("daemon_auth: cloud rejected mcn_ token", "path", r.URL.Path, "error", err)
						writeError(w, http.StatusUnauthorized, "invalid token")
						return
					}
					slog.Warn("daemon_auth: cloud pat verify unavailable", "path", r.URL.Path, "error", err)
					writeError(w, http.StatusServiceUnavailable, "cloud pat verifier unavailable")
					return
				}
				if rejectTemporarilyDisabledUser(w, r, identity.OwnerID, "", DaemonAuthPathCloudPAT) {
					return
				}
				r.Header.Set("X-User-ID", identity.OwnerID)
				// 镜像常规 Auth 中间件：给鉴权路径打标，让任何下游门禁（handler.
				// RequireHumanActor 等）都能认出这是机器凭证而不是人类 PAT。daemon 路由
				// 当前用不到这些门禁，但把盖印保持与 Auth 一致，可避免将来某个端点挪到或
				// 被两个中间件共享时，行为取决于路由到了哪个中间件。
				r.Header.Set("X-Actor-Source", "cloud_pat")
				ctx := context.WithValue(r.Context(), ctxKeyDaemonAuthPath, DaemonAuthPathCloudPAT)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 回退：PAT token（"mul_" 前缀）。
			if strings.HasPrefix(tokenString, "mul_") {
				hash := auth.HashToken(tokenString)

				if userID, ok := patCache.Get(r.Context(), hash); ok {
					if rejectTemporarilyDisabledUser(w, r, userID, "", DaemonAuthPathPAT) {
						return
					}
					r.Header.Set("X-User-ID", userID)
					ctx := context.WithValue(r.Context(), ctxKeyDaemonAuthPath, DaemonAuthPathPAT)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}

				if queries == nil {
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				pat, err := queries.GetPersonalAccessTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("daemon_auth: invalid PAT", "path", r.URL.Path, "error", err)
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}

				userID := uuidToString(pat.UserID)
				if rejectTemporarilyDisabledUser(w, r, userID, "", DaemonAuthPathPAT) {
					return
				}
				r.Header.Set("X-User-ID", userID)

				var expiresAt time.Time
				if pat.ExpiresAt.Valid {
					expiresAt = pat.ExpiresAt.Time
				}
				patCache.Set(r.Context(), hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

				// 缓存 miss = 这个 TTL 窗口里的第一次请求。刷新 last_used_at；
				// 后续命中完全跳过这次写。
				go queries.UpdatePersonalAccessTokenLastUsed(context.Background(), pat.ID)

				ctx := context.WithValue(r.Context(), ctxKeyDaemonAuthPath, DaemonAuthPathPAT)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// 回退：JWT token。
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return auth.JWTSecret(), nil
			})
			if err != nil || !token.Valid {
				slog.Warn("daemon_auth: invalid token", "path", r.URL.Path, "error", err)
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid claims")
				return
			}
			sub, ok := claims["sub"].(string)
			if !ok || strings.TrimSpace(sub) == "" {
				writeError(w, http.StatusUnauthorized, "invalid claims")
				return
			}
			email, _ := claims["email"].(string)
			if rejectTemporarilyDisabledUser(w, r, sub, email, DaemonAuthPathJWT) {
				return
			}
			r.Header.Set("X-User-ID", sub)
			ctx := context.WithValue(r.Context(), ctxKeyDaemonAuthPath, DaemonAuthPathJWT)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
