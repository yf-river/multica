// 职责定位：外部凭据档案（external credential profile）的 HTTP 处理器。一个「凭据档案」是
// 某个站内用户给某个外部系统（目前支持 tapd / gongfeng）保存的一份访问凭证，供后续集成调用。
// 提供增删改查 + 「试连接」校验；凭证本体绝不落回响应——对外只暴露脱敏后的绑定描述
// （secret_binding），真正的秘密要么引用服务端环境变量（secret_ref），要么用 ExternalCredentialBox
// 加密后入库。
//
// 幂等约定：创建走 Idempotency-Key（同时充当档案主键）——同样的键 + 同样的请求指纹会原样
// 回放第一次的创建结果；键相同但请求内容不同则返回冲突。档案作用域固定在「账号」级
// （Scope: account），只归属创建它的用户。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 目前支持的外部凭据提供方。
const (
	externalCredentialProviderTAPD     = "tapd"
	externalCredentialProviderGongfeng = "gongfeng"
)

// externalCredentialProfileResponse 是凭据档案的接口响应。SecretBinding 只描述「秘密以哪种
// 方式存放」（secret_ref / encrypted_secret / missing）和一段 hint，绝不携带秘密本身。
type externalCredentialProfileResponse struct {
	ID             string         `json:"id"`
	UserID         string         `json:"user_id"`
	Scope          string         `json:"scope"`
	Provider       string         `json:"provider"`
	Name           string         `json:"name"`
	SecretBinding  map[string]any `json:"secret_binding"`
	Capabilities   map[string]any `json:"capabilities"`
	Status         string         `json:"status"`
	LastVerifiedAt *string        `json:"last_verified_at"`
	LastError      string         `json:"last_error,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// CreateExternalCredentialProfileRequest 是创建档案的请求体。SecretRef 与 Token 二选一：
// SecretRef 引用服务端环境变量或 server-managed 凭据，Token 是用户直接粘贴的访问令牌
// （由服务端加密保存）。Capabilities 是需要该凭据支撑的能力集合。
type CreateExternalCredentialProfileRequest struct {
	Provider     string          `json:"provider"`
	Name         string          `json:"name"`
	SecretRef    string          `json:"secret_ref"`
	Token        string          `json:"token"`
	Capabilities json.RawMessage `json:"capabilities"`
	VerifyNow    bool            `json:"verify_now"`
}

// UpdateExternalCredentialProfileRequest 是更新档案的请求体。指针字段为 nil 表示不改；
// Capabilities 用整段替换语义（传了才替换）。
type UpdateExternalCredentialProfileRequest struct {
	Name         *string         `json:"name"`
	SecretRef    *string         `json:"secret_ref"`
	Token        *string         `json:"token"`
	Capabilities json.RawMessage `json:"capabilities"`
	Status       *string         `json:"status"`
	LastError    *string         `json:"last_error"`
	VerifyNow    bool            `json:"verify_now"`
}

// TestExternalCredentialProfileRequest 是「试连接」请求体：不落库，临时用传入的凭据打一次
// 外部 API。
type TestExternalCredentialProfileRequest struct {
	Provider  string `json:"provider"`
	SecretRef string `json:"secret_ref"`
	Token     string `json:"token"`
}

// TestExternalCredentialProfileResponse 返回试连接的结果。
type TestExternalCredentialProfileResponse struct {
	Provider       string         `json:"provider"`
	SecretBinding  map[string]any `json:"secret_binding"`
	Status         string         `json:"status"`
	LastVerifiedAt *string        `json:"last_verified_at"`
	LastError      string         `json:"last_error,omitempty"`
}

// externalCredentialProfileToResponse 把数据库行转成接口响应：解析 Capabilities、拼出脱敏的
// SecretBinding。Capabilities 坏了只能当 500 处理——说明之前入库时写坏了数据。
func externalCredentialProfileToResponse(profile db.ExternalCredentialProfile) (externalCredentialProfileResponse, error) {
	var capabilities map[string]any
	if err := json.Unmarshal(profile.Capabilities, &capabilities); err != nil {
		return externalCredentialProfileResponse{}, fmt.Errorf("decode credential capabilities: %w", err)
	}
	if capabilities == nil {
		return externalCredentialProfileResponse{}, fmt.Errorf("decode credential capabilities: expected JSON object")
	}
	// 对外只描述「秘密在不在、以什么模式存放」，并给出便于用户辨认的 hint（脱敏）。
	binding := map[string]any{
		"configured": profile.SecretRef != "" || len(profile.EncryptedSecret) > 0,
		"redacted":   true,
	}
	switch {
	case profile.SecretRef != "":
		binding["mode"] = "secret_ref"
		binding["hint"] = secretRefHint(profile.SecretRef)
	case len(profile.EncryptedSecret) > 0:
		binding["mode"] = "encrypted_secret"
		binding["hint"] = profile.SecretHint
	default:
		binding["mode"] = "missing"
	}
	return externalCredentialProfileResponse{
		ID:             uuidToString(profile.ID),
		UserID:         uuidToString(profile.UserID),
		Scope:          "account",
		Provider:       profile.Provider,
		Name:           profile.Name,
		SecretBinding:  binding,
		Capabilities:   capabilities,
		Status:         profile.Status,
		LastVerifiedAt: timestampToPtr(profile.LastVerifiedAt),
		LastError:      profile.LastError,
		CreatedAt:      timestampToString(profile.CreatedAt),
		UpdatedAt:      timestampToString(profile.UpdatedAt),
	}, nil
}

// writeCredentialCapabilitiesDecodeError 统一处理「库里的 Capabilities 解析不出来」这个
// 数据完整性错误。
func writeCredentialCapabilitiesDecodeError(w http.ResponseWriter, r *http.Request, profileID string, err error) {
	slog.Error("decode credential capabilities failed", append(logger.RequestAttrs(r), "profile_id", profileID, "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to decode credential capabilities")
}

// ListExternalCredentialProfiles 列出当前用户的凭据档案，可按 provider 过滤
// （tapd / gongfeng）。
func (h *Handler) ListExternalCredentialProfiles(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	provider := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("provider")))
	var providerFilter pgtype.Text
	if provider != "" {
		if !isSupportedExternalCredentialProvider(provider) {
			writeError(w, http.StatusBadRequest, "provider must be tapd or gongfeng")
			return
		}
		providerFilter = pgtype.Text{String: provider, Valid: true}
	}
	rows, err := h.Queries.ListExternalCredentialProfilesByUser(r.Context(), db.ListExternalCredentialProfilesByUserParams{
		UserID:   parseUUID(userID),
		Provider: providerFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list credential profiles")
		return
	}
	resp := make([]externalCredentialProfileResponse, len(rows))
	for i, row := range rows {
		resp[i], err = externalCredentialProfileToResponse(row)
		if err != nil {
			writeCredentialCapabilitiesDecodeError(w, r, uuidToString(row.ID), err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": resp})
}

// CreateExternalCredentialProfile 创建一条凭据档案。整体是「幂等创建」：先把规范化后的
// 入参算成请求指纹（requestHash），再查 Idempotency-Key 是否用过——用过且内容一致就回放
// 第一次的结果，内容不一致返回冲突；没用过则以该键为主键插入，撞唯一约束时同样按回放/冲突
// 处理。VerifyNow 为真时创建前先做一次真实校验。
func (h *Handler) CreateExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateExternalCredentialProfileRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	// 名称/能力/提供方先做规范化（缺省名、大小写、JSON 结构校验）。
	provider, name, capabilities, statusCode, msg := h.normalizeExternalCredentialInput(req.Provider, req.Name, req.Capabilities)
	if statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}
	// 凭据二选一：secret_ref 或 token，不能都填也不能都不填。
	secretRef, token, statusCode, msg := normalizeExternalCredentialSecretInput(req.SecretRef, req.Token)
	if statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}
	// 把「规范化后的完整请求」哈希成指纹，用于幂等重放时区分「同键同请求」与「同键不同请求」。
	requestHash, err := hashRequestFingerprint(struct {
		Provider     string          `json:"provider"`
		Name         string          `json:"name"`
		SecretRef    string          `json:"secret_ref"`
		Token        string          `json:"token"`
		Capabilities json.RawMessage `json:"capabilities"`
		VerifyNow    bool            `json:"verify_now"`
	}{
		Provider: provider, Name: name, SecretRef: secretRef, Token: token,
		Capabilities: json.RawMessage(capabilities), VerifyNow: req.VerifyNow,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint credential profile request")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID := parseUUID(userID)
	// 先查重放：这个 Idempotency-Key 已创建过档案 → 校验指纹后把当时的结果原样返回。
	if existing, err := h.Queries.GetExternalCredentialProfileByCreateRequest(r.Context(), db.GetExternalCredentialProfileByCreateRequestParams{
		UserID: actorID, IdempotencyKey: idempotencyKey,
	}); err == nil {
		h.writeExternalCredentialProfileReplay(w, r, existing, requestHash)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to recover credential profile request")
		return
	}
	// 把凭据落成可存形态：secret_ref 原样保留，token 则加密并生成 hint。
	secretRef, encrypted, hint, statusCode, msg := h.prepareNormalizedExternalCredentialSecret(secretRef, token)
	if statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}
	status := "unverified"
	var lastVerified pgtype.Timestamptz
	var lastError pgtype.Text
	if req.VerifyNow {
		status, lastVerified, lastError = verifyExternalCredentialProfile(provider, secretRef, encrypted)
	}
	// 用 Idempotency-Key 直接当主键：天然挡住并发下同一键的重复创建。
	profile, err := h.Queries.CreateExternalCredentialProfile(r.Context(), db.CreateExternalCredentialProfileParams{
		ID:              idempotencyKey,
		UserID:          actorID,
		Provider:        provider,
		Name:            name,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
		SecretHint:      hint,
		Capabilities:    capabilities,
		Status:          pgtype.Text{String: status, Valid: true},
		LastVerifiedAt:  lastVerified,
		LastError:       lastError,
		IdempotencyKey:  idempotencyKey,
		RequestHash:     pgtype.Text{String: requestHash, Valid: true},
	})
	// 撞主键冲突：多半是「同键请求并发」中另一请求已插入成功，查出来按回放处理；真查不到
	// 才是别的键冲突，返回 409。
	if isUniqueViolation(err) {
		if existing, replayErr := h.Queries.GetExternalCredentialProfileByCreateRequest(r.Context(), db.GetExternalCredentialProfileByCreateRequestParams{
			UserID: actorID, IdempotencyKey: idempotencyKey,
		}); replayErr == nil {
			h.writeExternalCredentialProfileReplay(w, r, existing, requestHash)
			return
		}
		writeError(w, http.StatusConflict, "credential profile already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create credential profile")
		return
	}
	resp, err := externalCredentialProfileToResponse(profile)
	if err != nil {
		writeCredentialCapabilitiesDecodeError(w, r, uuidToString(profile.ID), err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// writeExternalCredentialProfileReplay 处理幂等重放：先核对请求指纹与当初创建时一致
// （不一致说明同一个 Idempotency-Key 被拿来发了不同的请求，属于滥用，回冲突），一致则把
// 当初创建的档案按 201 原样返回。
func (h *Handler) writeExternalCredentialProfileReplay(
	w http.ResponseWriter,
	r *http.Request,
	profile db.ExternalCredentialProfile,
	requestHash string,
) {
	if !profile.RequestHash.Valid || profile.RequestHash.String != requestHash {
		writeIdempotencyConflict(w, "Idempotency-Key was already used with a different credential profile request")
		return
	}
	resp, err := externalCredentialProfileToResponse(profile)
	if err != nil {
		writeCredentialCapabilitiesDecodeError(w, r, uuidToString(profile.ID), err)
		return
	}
	writeIdempotencyReplayJSON(w, http.StatusCreated, resp)
}

// GetExternalCredentialProfile 按 id 取当前用户的某条档案（查不到 404）。
func (h *Handler) GetExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadExternalCredentialProfile(w, r)
	if !ok {
		return
	}
	resp, err := externalCredentialProfileToResponse(profile)
	if err != nil {
		writeCredentialCapabilitiesDecodeError(w, r, uuidToString(profile.ID), err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateExternalCredentialProfile 部分更新档案：只改请求体里非 nil 的字段。换凭据时状态
// 强制回到 unverified（旧凭据的校验结果对新凭据没意义）；VerifyNow 为真则更新后用「当前
// 生效的凭据」做一次真实校验并回写状态。
func (h *Handler) UpdateExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "credential profile id")
	if !ok {
		return
	}
	current, err := h.Queries.GetExternalCredentialProfileForUser(r.Context(), db.GetExternalCredentialProfileForUserParams{
		ID:     id,
		UserID: parseUUID(userID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "credential profile not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load credential profile")
		return
	}
	var req UpdateExternalCredentialProfileRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	params := db.UpdateExternalCredentialProfileParams{ID: id, UserID: parseUUID(userID)}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if len(req.Capabilities) > 0 {
		capabilities, ok := normalizeCredentialCapabilities(w, req.Capabilities)
		if !ok {
			return
		}
		params.Capabilities = capabilities
	}
	if req.Status != nil {
		status := strings.TrimSpace(strings.ToLower(*req.Status))
		if !isValidExternalCredentialStatus(status) {
			writeError(w, http.StatusBadRequest, "status must be unverified, verified, failed, or disabled")
			return
		}
		params.Status = pgtype.Text{String: status, Valid: true}
	}
	if req.LastError != nil {
		params.LastError = pgtype.Text{String: strings.TrimSpace(*req.LastError), Valid: true}
	}
	if req.SecretRef != nil || req.Token != nil {
		secretRef, encrypted, hint, statusCode, msg := h.prepareExternalCredentialSecret(util.ValueOrEmpty(req.SecretRef), util.ValueOrEmpty(req.Token))
		if statusCode != 0 {
			writeError(w, statusCode, msg)
			return
		}
		params.SecretRef = pgtype.Text{String: secretRef, Valid: true}
		params.EncryptedSecret = encrypted
		params.SecretHint = pgtype.Text{String: hint, Valid: true}
		params.Status = pgtype.Text{String: "unverified", Valid: true}
	}
	if req.VerifyNow {
		// 用「更新后生效的凭据」来验证：本请求换了新凭据就用新的，否则用库里现存那份。
		secretRef := current.SecretRef
		encrypted := current.EncryptedSecret
		if params.SecretRef.Valid {
			secretRef = params.SecretRef.String
			encrypted = params.EncryptedSecret
		}
		status, lastVerified, lastError := verifyExternalCredentialProfile(current.Provider, secretRef, encrypted)
		params.Status = pgtype.Text{String: status, Valid: true}
		params.LastVerifiedAt = lastVerified
		params.LastError = lastError
	}
	profile, err := h.Queries.UpdateExternalCredentialProfile(r.Context(), params)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "credential profile already exists")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "credential profile not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update credential profile")
		return
	}
	resp, err := externalCredentialProfileToResponse(profile)
	if err != nil {
		writeCredentialCapabilitiesDecodeError(w, r, uuidToString(profile.ID), err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// TestExternalCredentialProfile 用请求里临时给的凭据试连一次外部系统（工蜂/TAPD），不落库。
// 校验分两层：先做本地格式/可解密的「基础校验」，再真正打外部 API（只要本地校验没失败就
// 试）；两层都通过才标记 verified，否则 status=failed 并带可读原因。
func (h *Handler) TestExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	var req TestExternalCredentialProfileRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	provider := strings.TrimSpace(strings.ToLower(req.Provider))
	if !isSupportedExternalCredentialProvider(provider) {
		writeError(w, http.StatusBadRequest, "provider must be tapd or gongfeng")
		return
	}
	secretRef, encrypted, hint, statusCode, msg := h.prepareExternalCredentialSecret(req.SecretRef, req.Token)
	if statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}
	// 第一层：本地校验（格式、环境变量是否已配、能否解密）。
	status, lastVerified, lastError := verifyExternalCredentialProfile(provider, secretRef, encrypted)
	lastErrorString := lastError.String
	// 目前「仅本地结构有效」时给一个更友好的文案，提示实时 API 校验尚未接入。
	if status == "unverified" && strings.Contains(lastErrorString, "凭据绑定已保存") {
		lastErrorString = "凭据绑定格式有效；实时工蜂/TAPD API 校验尚未接入。"
	}
	// 第二层：本地校验没判失败，就真正打一次外部 API。
	if provider == externalCredentialProviderGongfeng && status != "failed" {
		status, lastVerified, lastError = h.verifyGongfengCredentialConnection(r.Context(), secretRef, encrypted)
		lastErrorString = lastError.String
	}
	if provider == externalCredentialProviderTAPD && status != "failed" {
		status, lastVerified, lastError = h.verifyTAPDCredentialConnection(r.Context(), secretRef, encrypted)
		lastErrorString = lastError.String
	}
	binding := map[string]any{
		"configured": true,
		"redacted":   true,
	}
	if secretRef != "" {
		binding["mode"] = "secret_ref"
		binding["hint"] = secretRefHint(secretRef)
	} else {
		binding["mode"] = "encrypted_secret"
		binding["hint"] = hint
	}
	writeJSON(w, http.StatusOK, TestExternalCredentialProfileResponse{
		Provider:       provider,
		SecretBinding:  binding,
		Status:         status,
		LastVerifiedAt: timestampToPtr(lastVerified),
		LastError:      lastErrorString,
	})
}

// newNoRedirectHTTPClient 构造一个禁跟随重定向、带超时的 HTTP 客户端。禁用重定向是为了让
// 校验逻辑能看清外部系统的真实响应（有些服务会拿 3xx 把不合法 token 引开）。
func newNoRedirectHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// setGongfengCredentialHeaders 同时设置三种工蜂/自建 Git 服务的 token 头写法——不同版本的
// 服务认不同的头，全带上以提高兼容性。
func setGongfengCredentialHeaders(req *http.Request, token string) {
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Private-Token", token)
	req.Header.Set("Authorization", "Bearer "+token)
}

// verifyGongfengCredentialConnection 用真实 HTTP 请求工蜂 GET /user 验证凭据可用性：
// 打不通网络、token 无效/权限不足/过期都会落到 status=failed 并附上中文原因。
func (h *Handler) verifyGongfengCredentialConnection(ctx context.Context, secretRef string, encrypted []byte) (string, pgtype.Timestamptz, pgtype.Text) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	token, err := h.resolveExternalCredentialToken(db.ExternalCredentialProfile{
		Provider:        externalCredentialProviderGongfeng,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
	})
	if err != nil {
		return "failed", now, pgtype.Text{String: "工蜂 token 无法解密；请重新保存凭据。", Valid: true}
	}
	if strings.TrimSpace(token) == "" {
		return "failed", now, pgtype.Text{String: "工蜂 token 不可用；请检查输入或服务端环境变量。", Valid: true}
	}
	target := gongfengAPIBase() + "/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "failed", now, pgtype.Text{String: "工蜂连接测试地址无效。", Valid: true}
	}
	setGongfengCredentialHeaders(req, token)
	client := newNoRedirectHTTPClient(8 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "failed", now, pgtype.Text{String: "无法连接工蜂 API；请检查网络或 GONGFENG_API_BASE。", Valid: true}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "verified", now, pgtype.Text{}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "failed", now, pgtype.Text{String: "工蜂 token 无效或已过期。", Valid: true}
	}
	if resp.StatusCode == http.StatusForbidden {
		return "failed", now, pgtype.Text{String: "工蜂 token 权限不足。", Valid: true}
	}
	return "failed", now, pgtype.Text{String: "工蜂连接测试失败，HTTP " + resp.Status, Valid: true}
}

// verifyTAPDCredentialConnection 用真实 HTTP 请求 TAPD /workspaces 验证凭据可用性，逻辑与
// 工蜂版镜像（base 地址取 TAPD_API_BASE_URL，缺省 api.tapd.cn）。
func (h *Handler) verifyTAPDCredentialConnection(ctx context.Context, secretRef string, encrypted []byte) (string, pgtype.Timestamptz, pgtype.Text) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	token, err := h.resolveExternalCredentialToken(db.ExternalCredentialProfile{
		Provider:        externalCredentialProviderTAPD,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
	})
	if err != nil {
		return "failed", now, pgtype.Text{String: "TAPD token 无法解密；请重新保存凭据。", Valid: true}
	}
	if strings.TrimSpace(token) == "" {
		return "failed", now, pgtype.Text{String: "TAPD token 不可用；请检查输入或服务端环境变量。", Valid: true}
	}
	base := strings.TrimRight(firstNonEmpty(os.Getenv("TAPD_API_BASE_URL"), "https://api.tapd.cn"), "/")
	target := base + "/workspaces?page=1&limit=1&s=mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "failed", now, pgtype.Text{String: "TAPD 连接测试地址无效。", Valid: true}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Via", "mcp")
	client := newNoRedirectHTTPClient(8 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "failed", now, pgtype.Text{String: "无法连接 TAPD API；请检查网络或 TAPD_API_BASE_URL。", Valid: true}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "verified", now, pgtype.Text{}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "failed", now, pgtype.Text{String: "TAPD token 无效或已过期。", Valid: true}
	}
	if resp.StatusCode == http.StatusForbidden {
		return "failed", now, pgtype.Text{String: "TAPD token 权限不足。", Valid: true}
	}
	return "failed", now, pgtype.Text{String: "TAPD 连接测试失败，HTTP " + resp.Status, Valid: true}
}

// DeleteExternalCredentialProfile 删除当前用户的某条档案。历史集成记录不受影响，
// 只是从此没有可用凭据。
func (h *Handler) DeleteExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "credential profile id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteExternalCredentialProfile(r.Context(), db.DeleteExternalCredentialProfileParams{ID: id, UserID: parseUUID(userID)}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete credential profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loadExternalCredentialProfile 按 URL 里的 id 加载「当前登录用户自己的」一条档案；解析
// 失败/查不到（含查到别人的）时写好错误响应并返回 false。
func (h *Handler) loadExternalCredentialProfile(w http.ResponseWriter, r *http.Request) (db.ExternalCredentialProfile, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.ExternalCredentialProfile{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "credential profile id")
	if !ok {
		return db.ExternalCredentialProfile{}, false
	}
	profile, err := h.Queries.GetExternalCredentialProfileForUser(r.Context(), db.GetExternalCredentialProfileForUserParams{
		ID:     id,
		UserID: parseUUID(userID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "credential profile not found")
		return db.ExternalCredentialProfile{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load credential profile")
		return db.ExternalCredentialProfile{}, false
	}
	return profile, true
}

// normalizeExternalCredentialInput 对创建/更新共用的三个字段做规范化：provider 小写化并校验、
// name 裁剪（空则给默认名 "tapd-default"/"gongfeng-default"，超过 80 字符拒绝）、capabilities
// 必须是合法 JSON 对象。返回 statusCode!=0 表示校验失败（msg 可直接当 400 文案）。
func (h *Handler) normalizeExternalCredentialInput(provider, name string, rawCapabilities json.RawMessage) (string, string, []byte, int, string) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !isSupportedExternalCredentialProvider(provider) {
		return "", "", nil, http.StatusBadRequest, "provider must be tapd or gongfeng"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = provider + "-default"
	}
	if utf8.RuneCountInString(name) > 80 {
		return "", "", nil, http.StatusBadRequest, "name is too long"
	}
	capabilities, ok := normalizeCredentialCapabilities(nil, rawCapabilities)
	if !ok {
		return "", "", nil, http.StatusBadRequest, "capabilities must be a JSON object"
	}
	return provider, name, capabilities, 0, ""
}

// normalizeCredentialCapabilities 把 capabilities 规范成「重排过的 JSON 对象字节」：空输入当
// 空对象 {}，非对象（数组/裸值/坏 JSON）拒绝。w 非 nil 时顺手把错误写进响应（供 handler
// 直接 return），为 nil 时由调用方自行处理失败。
func normalizeCredentialCapabilities(w http.ResponseWriter, raw json.RawMessage) ([]byte, bool) {
	if len(raw) == 0 {
		return []byte(`{}`), true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		if w != nil {
			writeError(w, http.StatusBadRequest, "capabilities must be a JSON object")
		}
		return nil, false
	}
	if obj == nil {
		if w != nil {
			writeError(w, http.StatusBadRequest, "capabilities must be a JSON object")
		}
		return nil, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		if w != nil {
			writeError(w, http.StatusBadRequest, "capabilities must be a JSON object")
		}
		return nil, false
	}
	return out, true
}

// prepareExternalCredentialSecret 把「用户原始输入」的凭据做成可存形态的完整流程：
// 先做二选一/长度校验，再转存（secret_ref 原样，token 加密）。测试与更新共用。
func (h *Handler) prepareExternalCredentialSecret(secretRef, token string) (string, []byte, string, int, string) {
	secretRef, token, statusCode, msg := normalizeExternalCredentialSecretInput(secretRef, token)
	if statusCode != 0 {
		return "", nil, "", statusCode, msg
	}
	return h.prepareNormalizedExternalCredentialSecret(secretRef, token)
}

// normalizeExternalCredentialSecretInput 校验 secret_ref / token 二选一：都填或都不填都拒绝，
// secret_ref 超过 240 字符拒绝。返回清理后的值。
func normalizeExternalCredentialSecretInput(secretRef, token string) (string, string, int, string) {
	secretRef = strings.TrimSpace(secretRef)
	token = strings.TrimSpace(token)
	if secretRef != "" && token != "" {
		return "", "", http.StatusBadRequest, "provide either secret_ref or token, not both"
	}
	if secretRef == "" && token == "" {
		return "", "", http.StatusBadRequest, "secret_ref or token is required"
	}
	if secretRef != "" {
		if utf8.RuneCountInString(secretRef) > 240 {
			return "", "", http.StatusBadRequest, "secret_ref is too long"
		}
	}
	return secretRef, token, 0, ""
}

// prepareNormalizedExternalCredentialSecret 把已通过校验的凭据转成落库形态：
//   - secret_ref 模式：原样保留，不加密（秘密在服务端环境变量里）；
//   - token 模式：用 ExternalCredentialBox 加密成 EncryptedSecret，并生成一个可回显的 hint。
func (h *Handler) prepareNormalizedExternalCredentialSecret(secretRef, token string) (string, []byte, string, int, string) {
	if secretRef != "" {
		return secretRef, nil, "", 0, ""
	}
	if h.ExternalCredentialBox == nil {
		return "", nil, "", http.StatusServiceUnavailable, "external credential encryption is not configured"
	}
	encrypted, err := h.ExternalCredentialBox.Seal([]byte(token))
	if err != nil {
		return "", nil, "", http.StatusInternalServerError, "failed to encrypt token"
	}
	return "", encrypted, secretHint(token), 0, ""
}

// verifyExternalCredentialProfile 是「第一层」本地校验：只检查凭据形态是否站得住
// （provider 非空、secret_ref / token 至少一个、引用的环境变量确实已配），不做网络请求。
// 一切就绪时返回 unverified（意味着「可存，但还没真连过」）；格式有问题返回 failed + 原因。
func verifyExternalCredentialProfile(provider, secretRef string, encrypted []byte) (string, pgtype.Timestamptz, pgtype.Text) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	if provider == "" || (secretRef == "" && len(encrypted) == 0) {
		return "failed", now, pgtype.Text{String: "credential binding is missing", Valid: true}
	}
	if msg := externalCredentialSecretRefError(secretRef); msg != "" {
		return "failed", now, pgtype.Text{String: msg, Valid: true}
	}
	return "unverified", now, pgtype.Text{String: "凭据绑定已保存；实时工蜂/TAPD API 校验尚未接入。", Valid: true}
}

// externalCredentialSecretRefError 检查 secret_ref 指向的「服务端侧秘密」现在是否可用：
//   - env:KEY —— 对应环境变量必须已设置；
//   - server-managed:provider —— 该 provider 的服务端环境变量必须已设置。
//
// 不可用时返回中文原因，可用/非这两种前缀返回空串。
func externalCredentialSecretRefError(secretRef string) string {
	secretRef = strings.TrimSpace(secretRef)
	if secretRef == "" {
		return ""
	}
	if strings.HasPrefix(secretRef, "env:") {
		key := strings.TrimSpace(strings.TrimPrefix(secretRef, "env:"))
		if key == "" {
			return "服务端环境变量名称为空；请填写 env:GONGFENG_PRIVATE_TOKEN 这类引用。"
		}
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return "服务器环境变量 " + key + " 未设置；请改用访问令牌，或让管理员配置该变量并重启服务。"
		}
		return ""
	}
	if strings.HasPrefix(secretRef, "server-managed:") {
		parts := strings.Split(secretRef, ":")
		if len(parts) < 2 {
			return "server-managed 凭据引用格式无效。"
		}
		key := externalCredentialProviderEnvKey(parts[1])
		if key == "" {
			return "server-managed 凭据 provider 不支持：" + parts[1]
		}
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return ""
		}
		return "服务器环境变量 " + key + " 未设置；请改用访问令牌，或让管理员配置变量并重启服务。"
	}
	return ""
}

// externalCredentialProviderEnvKey 返回 provider 对应「服务端托管凭据」的环境变量名；
// 不支持的 provider 返回空串。
func externalCredentialProviderEnvKey(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case externalCredentialProviderTAPD:
		return "TAPD_ACCESS_TOKEN"
	case externalCredentialProviderGongfeng:
		return "GONGFENG_PRIVATE_TOKEN"
	default:
		return ""
	}
}

// isSupportedExternalCredentialProvider 判断 provider 是否当前支持的两种之一。
func isSupportedExternalCredentialProvider(provider string) bool {
	return provider == externalCredentialProviderTAPD || provider == externalCredentialProviderGongfeng
}

// isValidExternalCredentialStatus 判断状态字段是否合法（档案状态的完整取值集合）。
func isValidExternalCredentialStatus(status string) bool {
	switch status {
	case "unverified", "verified", "failed", "disabled":
		return true
	default:
		return false
	}
}

// secretHint 把秘密脱敏成可回显的提示：只留末尾 4 个字符、前面用 **** 盖住；太短则整段盖掉。
func secretHint(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

// secretRefHint 从 secret_ref 里抽一段便于用户辨认的尾部做提示：按 / : . 切分取最后一段，
// 超过 32 字符截断。
func secretRefHint(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	parts := strings.FieldsFunc(ref, func(r rune) bool { return r == '/' || r == ':' || r == '.' })
	last := ref
	if len(parts) > 0 {
		last = parts[len(parts)-1]
	}
	if utf8.RuneCountInString(last) > 32 {
		runes := []rune(last)
		last = string(runes[:32])
	}
	return last
}
