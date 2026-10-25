// 包 middleware 是 chi 路由的一圈 HTTP 中间件集合：鉴权（Auth / DaemonAuth）、
// 工作区成员/角色门禁、客户端元信息、请求日志、CSP 头、限流等。它们以
// func(http.Handler) http.Handler 的形式组装在 router.go 的路由树上，
// 位于「接到请求」与「业务 handler」之间。
//
// 本文件实现用户侧主鉴权中间件 Auth：解析凭证（任务 token / 云节点 PAT / 个人
// PAT / JWT / Cookie 会话），校验其有效性并把它翻译成一组标准请求头
// （X-User-ID、X-Agent-ID 等）交给下游。所有跨中间件传递的身份信息都以服务端
// 设的头为准，客户端伪造的同名头一律先剥除。
package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// uuidToString 只是 pgtype.UUID 转字符串的本地别名，避免各处写 util.UUIDToString。
func uuidToString(u pgtype.UUID) string { return util.UUIDToString(u) }

// rejectTemporarilyDisabledUser 若 userID/email 命中「临时禁用名单」就回 403 并返回
// true（调用方应立即 return）；没命中返回 false 继续正常鉴权。authPath 记录走的是哪条
// 鉴权路径（task_token/pat/jwt……），便于日志排查。
func rejectTemporarilyDisabledUser(w http.ResponseWriter, r *http.Request, userID, email, authPath string) bool {
	if !auth.IsTemporarilyDisabledUser(userID, email) {
		return false
	}
	slog.Warn(
		"auth: temporarily disabled user rejected",
		"path", r.URL.Path,
		"user_id", userID,
		"auth_path", authPath,
	)
	writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
	return true
}

// Auth 是用户侧鉴权中间件：校验 JWT 或个人访问令牌（PAT）。
// token 来源（按优先级）：
//  1. Authorization: Bearer <token> 请求头（PAT 或 JWT）
//  2. multica_auth HttpOnly cookie（JWT）——改状态类请求还要求带合法 CSRF token
//
// 校验通过后，把 X-User-ID 与 X-User-Email 头写到请求上供下游 handler 使用。
//
// patCache 可选：非 nil 时 PAT 查询带短 TTL 缓存（auth.AuthCacheTTL）。缓存命中时
// 中间件会跳过 DB SELECT 和 last_used_at 的 UPDATE——因此 last_used_at 每个 TTL
// 窗口最多刷一次，而不是每请求一次。
//
// cloudPAT 可选：非 nil 时，mcn_ 前缀的 token 通过调用 Multica Cloud Fleet 服务校验，
// 而不是查本地库。为 nil（未配置 Fleet URL）时 mcn_ 在前缀分支就直接被拒——不会落到
// mul_/JWT 路径，因为 mcn_ 串按构造就不可能是合法的 mul_ PAT 或 JWT。
func Auth(queries *db.Queries, patCache *auth.PATCache, cloudPAT *auth.CloudPATVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// X-Actor-Source 只允许服务端设置——客户端带来的任何值都不可信，在下面
			// 各鉴权分支开始前先删掉。只有 mat_ 分支会重新设置它。这样才能防止客户端
			// 拿一个普通 mul_ PAT 再伪造 `X-Actor-Source: member`（或任何值），骗下游
			// handler 以为请求来自非 task-token 路径。
			r.Header.Del("X-Actor-Source")

			tokenString, fromCookie := extractToken(r)
			if tokenString == "" {
				slog.Debug("auth: no token found", "path", r.URL.Path)
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}

			// Cookie 鉴权需要 CSRF：改状态类方法（非 GET/HEAD/OPTIONS）必须带合法
			// CSRF token，否则返回 403。
			if fromCookie && !auth.ValidateCSRF(r) {
				slog.Debug("auth: CSRF validation failed", "path", r.URL.Path)
				http.Error(w, `{"error":"CSRF validation failed"}`, http.StatusForbidden)
				return
			}

			// Agent 任务 token：前缀 "mat_"。服务端在任务被认领时铸造，由 daemon 注入
			// agent 进程。它对「当前操作者是谁」有最终发言权——token 绑定的四个 ID
			// (user_id, agent_id, task_id, workspace_id) 在这里被写进请求头，覆盖客户端
			// 发来的任何同名值，这样下游的 actor 解析器就不会被「剥掉或伪造 X-Agent-ID /
			// X-Task-ID」的客户端骗到。仅限真人使用的端点（如 agent 环境管理）会拒绝这种
			// 鉴权方式；见 `actorSourceFromRequest`。MUL-2600。
			if strings.HasPrefix(tokenString, "mat_") {
				if queries == nil {
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				hash := auth.HashToken(tokenString)
				tt, err := queries.GetTaskTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("auth: invalid task token", "path", r.URL.Path, "error", err)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				userID := uuidToString(tt.UserID)
				if rejectTemporarilyDisabledUser(w, r, userID, "", "task_token") {
					return
				}
				boundWorkspaceID := uuidToString(tt.WorkspaceID)
				// An explicit workspace selector that disagrees with the
				// token's binding is rejected here, BEFORE the overwrite
				// below erases what the client asked for. Silently
				// swapping the target let a request for workspace B run
				// against A (GitHub #9403).
				if checkTaskTokenWorkspaceTarget(w, r, queries, boundWorkspaceID) {
					return
				}
				r.Header.Set("X-User-ID", userID)
				r.Header.Set("X-Agent-ID", uuidToString(tt.AgentID))
				r.Header.Set("X-Task-ID", uuidToString(tt.TaskID))
				r.Header.Set("X-Workspace-ID", boundWorkspaceID)
				// X-Actor-Source flags the auth path so resolveActor and
				// any owner-only handler can deny without re-querying the
				// token table. The value "task_token" is the only signal
				// this header is allowed to carry — strip anything else a
				// client tried to send.
				r.Header.Set("X-Actor-Source", "task_token")
				next.ServeHTTP(w, r)
				return
			}

			// Cloud Node PAT：前缀 "mcn_"。由调用 Multica Cloud Fleet 服务来校验——
			// token 的状态与 owner_id 绑定关系的权威归属是 Cloud（不是我们）。我们绝不
			// 为这个前缀查本地 personal_access_tokens 表：mcn_ 串不可能是合法的 mul_ 值，
			// 落下去只会是多余的 DB miss。当验证器未配置（没有 MULTICA_CLOUD_URL）时，
			// 在这个分支直接拒绝，而不是把 token 当 JWT/PAT 继续走——fail closed 避免
			// 一台配错的机器静默降级鉴权。
			//
			// Cloud 确认 token 后，还会再确认它返回的 owner_id 映射到本地真实用户。
			// Cloud 的 `owner_id` 与我们的 `users.id` 按契约共享同一 UUID 空间，这一步
			// 是纵深防御：查无此人说明本地行在节点仍活跃时被删了，或有人在伪造
			// owner_id——无论哪种，都不能放一个幽灵 X-User-ID 通过。
			if strings.HasPrefix(tokenString, auth.CloudPATPrefix) {
				if cloudPAT == nil {
					slog.Warn("auth: mcn_ token presented but cloud verifier not configured", "path", r.URL.Path)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				identity, err := cloudPAT.Verify(r.Context(), tokenString, ownerLookupFor(queries))
				if err != nil {
					if errors.Is(err, auth.ErrCloudPATInvalid) {
						slog.Warn("auth: cloud rejected mcn_ token", "path", r.URL.Path, "error", err)
						http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
						return
					}
					// Cloud 不可达 / 5xx / 解码错误。这里回 503，好让调用方（CLI/daemon）
					// 能重试——回 401 会让他们把明明有效的 token 丢掉。
					slog.Warn("auth: cloud pat verify unavailable", "path", r.URL.Path, "error", err)
					http.Error(w, `{"error":"cloud pat verifier unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				if rejectTemporarilyDisabledUser(w, r, identity.OwnerID, "", "cloud_pat") {
					return
				}
				r.Header.Set("X-User-ID", identity.OwnerID)
				// 给鉴权路径打标，使账户级门禁（如 restricted account endpoints 上的
				// handler.RequireHumanActor）能区分云节点机器凭证与人类 PAT/JWT。
				// 与 mat_ 分支盖的 "task_token" 同一套路——都是服务端设置、权威、
				// 且在中间件顶部已剥掉任何客户端自带的同名值。理由同 MUL-2600：
				// 机器凭证（运行中的 agent 或云节点）不能被当成「owner 亲自批准了
				// 账户级操作」。
				r.Header.Set("X-Actor-Source", "cloud_pat")
				next.ServeHTTP(w, r)
				return
			}

			// PAT：以 "mul_" 开头的 token
			if strings.HasPrefix(tokenString, "mul_") {
				hash := auth.HashToken(tokenString)

				// 缓存命中：TTL 未过期、上次查它时有效、期间没有任何失效操作。
				// 跳过 DB SELECT 与 last_used_at UPDATE——last_used_at 每个 TTL 窗口
				// 才刷一次。
				if userID, ok := patCache.Get(r.Context(), hash); ok {
					if rejectTemporarilyDisabledUser(w, r, userID, "", "pat_cache") {
						return
					}
					r.Header.Set("X-User-ID", userID)
					next.ServeHTTP(w, r)
					return
				}

				if queries == nil {
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}
				pat, err := queries.GetPersonalAccessTokenByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("auth: invalid PAT", "path", r.URL.Path, "error", err)
					http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
					return
				}

				userID := uuidToString(pat.UserID)
				if rejectTemporarilyDisabledUser(w, r, userID, "", "pat") {
					return
				}
				r.Header.Set("X-User-ID", userID)

				// 把缓存 TTL 夹到 token 剩余有效期内：快过期（剩余 < AuthCacheTTL）的
				// PAT，不能靠缓存命中在 expires_at 之后继续通过鉴权。
				var expiresAt time.Time
				if pat.ExpiresAt.Valid {
					expiresAt = pat.ExpiresAt.Time
				}
				patCache.Set(r.Context(), hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

				// 缓存 miss = TTL 过期（或吊销后/进程重启后首次使用）。刷新 last_used_at；
				// 之后 TTL 窗口内的命中完全跳过这次写。
				go queries.UpdatePersonalAccessTokenLastUsed(context.Background(), pat.ID)

				next.ServeHTTP(w, r)
				return
			}

			// JWT：解析 HMAC 签名并校验有效性
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, jwt.ErrSignatureInvalid
				}
				return auth.JWTSecret(), nil
			})
			if err != nil || !token.Valid {
				slog.Warn("auth: invalid token", "path", r.URL.Path, "error", err)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				slog.Warn("auth: invalid claims", "path", r.URL.Path)
				http.Error(w, `{"error":"invalid claims"}`, http.StatusUnauthorized)
				return
			}

			// sub 就是用户 ID；缺失或空都按非法 claims 拒绝。
			sub, ok := claims["sub"].(string)
			if !ok || strings.TrimSpace(sub) == "" {
				slog.Warn("auth: invalid claims", "path", r.URL.Path)
				http.Error(w, `{"error":"invalid claims"}`, http.StatusUnauthorized)
				return
			}
			email, _ := claims["email"].(string)
			if rejectTemporarilyDisabledUser(w, r, sub, email, "jwt") {
				return
			}
			r.Header.Set("X-User-ID", sub)
			if email != "" {
				r.Header.Set("X-User-Email", email)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractToken 取出 bearer token，并告知它是否来自 cookie。
// 优先级：Authorization 请求头 > multica_auth cookie。
func extractToken(r *http.Request) (token string, fromCookie bool) {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString != authHeader {
			return tokenString, false
		}
	}

	if cookie, err := r.Cookie(auth.CookieName(auth.AuthCookieName)); err == nil && cookie.Value != "" {
		return cookie.Value, true
	}

	return "", false
}
