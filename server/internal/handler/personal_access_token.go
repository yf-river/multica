// 本文件实现个人访问令牌（PAT，multica CLI/daemon 用来以用户身份调 API 的凭据）的
// CRUD 端点：创建（明文 token 仅此一次返回）、列表、撤销，以及 daemon 轮询的「原地
// 续期」端点 RenewCurrentPersonalAccessToken。
//
// 设计要点：库中只存 token 的哈希与 12 字符前缀，明文绝不落库、也绝不再次回传。
// 续期采用 CAS（把续期窗口当谓词）——并发续期只有一个赢家，其余拿到 ErrNoRows 后
// 重读当前行即可，保证「成功返回后 expires_at 新鲜到能撑到下一次轮询」。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PATRenewThreshold 是 PAT 进入「可原地续期」窗口的剩余有效期。daemon 大约每 3 天
// 轮询一次，7 天阈值保证在 token 还剩 ≥4 天有效时至少有一次续期机会——足够吸收一次
// 瞬时网络故障，用户不至于被迫重跑 `multica login`。
const PATRenewThreshold = 7 * 24 * time.Hour

// PATRenewExtension 是续期后 expires_at 往后推的时长。与 CreatePersonalAccessToken
// 的初始签发窗口（90 天）一致，让续期后的 token 与新建 token 拥有相同的生命周期——
// 不搞二等公民。
const PATRenewExtension = 90 * 24 * time.Hour

// PersonalAccessTokenResponse 是单条 PAT 的对外结构（含 token_prefix 便于用户辨认，
// 不含明文）。
type PersonalAccessTokenResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"token_prefix"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// CreatePATResponse 在 PAT 基础上多带一条仅创建时可见的明文 token。
type CreatePATResponse struct {
	PersonalAccessTokenResponse
	Token string `json:"token"`
}

// patToResponse 把 PAT 行转成对外结构。
func patToResponse(pat db.PersonalAccessToken) PersonalAccessTokenResponse {
	return PersonalAccessTokenResponse{
		ID:         uuidToString(pat.ID),
		Name:       pat.Name,
		Prefix:     pat.TokenPrefix,
		ExpiresAt:  timestampToPtr(pat.ExpiresAt),
		LastUsedAt: timestampToPtr(pat.LastUsedAt),
		CreatedAt:  timestampToString(pat.CreatedAt),
	}
}

// CreatePATRequest 是创建 PAT 的请求体：名字必填，ExpiresInDays 可选（不传/非正数
// 表示永不过期）。
type CreatePATRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

// CreatePersonalAccessToken 创建一条 PAT：生成原始 token，落库只存哈希 + 前缀，
// 明文 token 仅在本次响应的 CreatePATResponse.Token 里返回。
func (h *Handler) CreatePersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreatePATRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	rawToken, err := auth.GeneratePATToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	var expiresAt pgtype.Timestamptz
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt = pgtype.Timestamptz{
			Time:  time.Now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour),
			Valid: true,
		}
	}

	prefix := rawToken
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}

	pat, err := h.Queries.CreatePersonalAccessToken(r.Context(), db.CreatePersonalAccessTokenParams{
		UserID:      parseUUID(userID),
		Name:        req.Name,
		TokenHash:   auth.HashToken(rawToken),
		TokenPrefix: prefix,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	writeJSON(w, http.StatusCreated, CreatePATResponse{
		PersonalAccessTokenResponse: patToResponse(pat),
		Token:                       rawToken,
	})
}

// ListPersonalAccessTokens 列出当前用户的全部 PAT（不含明文）。
func (h *Handler) ListPersonalAccessTokens(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	pats, err := h.Queries.ListPersonalAccessTokensByUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}

	resp := make([]PersonalAccessTokenResponse, len(pats))
	for i, pat := range pats {
		resp[i] = patToResponse(pat)
	}
	writeJSON(w, http.StatusOK, resp)
}

// RenewPATResponse 是 RenewCurrentPersonalAccessToken 的返回体。
//
// Renewed=false 不是错误，只是说明调用方在 token 进入续期窗口之前就来轮询了。
// 调用方应始终读 ExpiresAt 作为权威过期时间，而不是假定旧值仍然有效。
type RenewPATResponse struct {
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

// RenewCurrentPersonalAccessToken 在「本次请求所用的 PAT」进入续期窗口时，原地把它的
// expires_at 往后推。
//
// 本端点刻意不铸新 token——那要么得轮换明文（会破坏 CLI/daemon 的多进程模型：同一份
// CLI 配置启动的所有进程共享同一条 PAT），要么每次轮询都得把明文 token 在线上回传
// 一遍（无谓的暴露，daemon 本来就拿着它）。取而代之是原子地把行的 expires_at 后移；
// 缓存里 PAT 条目的 TTL 足够短（auth.AuthCacheTTL ≤ 10m），下次缓存未命中就会追上新
// 过期时间，无需显式失效。
//
// 只有 mul_ 前缀的 PAT 可续期：cookie/JWT 会话没有可续的 PAT 行；mat_ 任务 token 是
// 单次用途且短命。mcn_ 云端节点 PAT 归 Multica Cloud Fleet 所有——我们本地连它的
// 过期时间都看不到。
func (h *Handler) RenewCurrentPersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 从 Authorization 头重读原始 token——上游 Auth 中间件把它解析成 userID，但不把
	// 哈希传下来，而我们要的是行而不只是用户。
	authHeader := r.Header.Get("Authorization")
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	if rawToken == "" || rawToken == authHeader || !strings.HasPrefix(rawToken, "mul_") {
		writeError(w, http.StatusBadRequest, "only personal access tokens can be renewed")
		return
	}

	hash := auth.HashToken(rawToken)
	pat, err := h.Queries.GetPersonalAccessTokenByHash(r.Context(), hash)
	if err != nil {
		// Auth 中间件已校验过 token，所以走到这里却没有行，说明 PAT 是在「中间件缓存
		// 命中」与「这次 DB 读」之间的空隙里被撤销或过期的。返回 401，让 daemon 的
		// 401 分支像其它鉴权失败一样触发「请重新登录」提示，而不是一个泛泛的 500。
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "token is no longer valid")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up token")
		return
	}

	// 纵深防御：中间件已按同一条 PAT 行设了 X-User-ID，因此这个不匹配本不该发生。
	// 一旦发生，说明有头绕过了中间件伪造——绝不能在别人名下续期，必须大声失败。
	if uuidToString(pat.UserID) != userID {
		writeError(w, http.StatusUnauthorized, "token does not belong to caller")
		return
	}

	// 这段代码出现前铸造的 PAT 可能带着 NULL expires_at（「永不过期」情形）。没有
	// 可后推的东西——返回当前（缺省）过期时间，让调用方当它是永久 token。
	if !pat.ExpiresAt.Valid {
		writeJSON(w, http.StatusOK, RenewPATResponse{ExpiresAt: "", Renewed: false})
		return
	}

	now := time.Now()
	remaining := pat.ExpiresAt.Time.Sub(now)
	if remaining > PATRenewThreshold {
		writeJSON(w, http.StatusOK, RenewPATResponse{
			ExpiresAt: timestampToString(pat.ExpiresAt),
			Renewed:   false,
		})
		return
	}

	newExpiresAt := pgtype.Timestamptz{Time: now.Add(PATRenewExtension), Valid: true}
	// 把续期阈值当作 CAS 谓词：仅当行的现有 expires_at 仍在窗口内才更新。第一个写入者
	// 成功后行落在 now+90d，远在 now+7d 之外，所以并发续期者会撞上 WHERE 条件得到
	// ErrNoRows。
	renewThreshold := pgtype.Timestamptz{Time: now.Add(PATRenewThreshold), Valid: true}
	updated, err := h.Queries.ExtendPersonalAccessTokenExpiry(r.Context(), db.ExtendPersonalAccessTokenExpiryParams{
		ID:               pat.ID,
		NewExpiresAt:     newExpiresAt,
		RenewThresholdAt: renewThreshold,
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, RenewPATResponse{
			ExpiresAt: timestampToString(updated),
			Renewed:   true,
		})
	case errors.Is(err, pgx.ErrNoRows):
		// 并发的续期（或撤销）赢了竞态。重读当前行并如实报告——daemon 唯一的正确性
		// 保证是「成功返回后 expires_at 新鲜到能撑到下一次轮询」，而并行写入者已经满足
		// 了它，所以在调用方看来这就是成功。
		current, getErr := h.Queries.GetPersonalAccessTokenByHash(r.Context(), hash)
		if getErr != nil {
			writeError(w, http.StatusUnauthorized, "token is no longer valid")
			return
		}
		writeJSON(w, http.StatusOK, RenewPATResponse{
			ExpiresAt: timestampToString(current.ExpiresAt),
			Renewed:   false,
		})
	default:
		writeError(w, http.StatusInternalServerError, "failed to renew token")
	}
}

// RevokePersonalAccessToken 撤销一条 PAT（仅本人）。成功时立即清缓存条目，让撤销在
// TTL 自然过期前就生效。
func (h *Handler) RevokePersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	id := chi.URLParam(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "token id")
	if !ok {
		return
	}
	hash, err := h.Queries.RevokePersonalAccessToken(r.Context(), db.RevokePersonalAccessTokenParams{
		ID:     idUUID,
		UserID: parseUUID(userID),
	})
	switch {
	case err == nil:
		// 立即清缓存条目，让撤销在 TTL 把缓存查询弄过期之前就生效。
		h.PATCache.Invalidate(r.Context(), hash)
	case errors.Is(err, pgx.ErrNoRows):
		// token 不存在或不属于该用户。保持既有的幂等 204 行为——没有缓存条目可清。
	default:
		writeError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
