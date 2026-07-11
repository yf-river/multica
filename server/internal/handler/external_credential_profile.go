package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	externalCredentialProviderTAPD     = "tapd"
	externalCredentialProviderGongfeng = "gongfeng"
)

type ExternalCredentialProfileResponse struct {
	ID             string         `json:"id"`
	UserID         string         `json:"user_id"`
	Scope          string         `json:"scope"`
	Provider       string         `json:"provider"`
	Name           string         `json:"name"`
	SecretBinding  map[string]any `json:"secret_binding"`
	Capabilities   any            `json:"capabilities"`
	Status         string         `json:"status"`
	LastVerifiedAt *string        `json:"last_verified_at"`
	LastError      string         `json:"last_error,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

type CreateExternalCredentialProfileRequest struct {
	Provider     string          `json:"provider"`
	Name         string          `json:"name"`
	SecretRef    string          `json:"secret_ref"`
	Token        string          `json:"token"`
	Capabilities json.RawMessage `json:"capabilities"`
	VerifyNow    bool            `json:"verify_now"`
}

type UpdateExternalCredentialProfileRequest struct {
	Name         *string         `json:"name"`
	SecretRef    *string         `json:"secret_ref"`
	Token        *string         `json:"token"`
	Capabilities json.RawMessage `json:"capabilities"`
	Status       *string         `json:"status"`
	LastError    *string         `json:"last_error"`
	VerifyNow    bool            `json:"verify_now"`
}

type TestExternalCredentialProfileRequest struct {
	Provider  string `json:"provider"`
	SecretRef string `json:"secret_ref"`
	Token     string `json:"token"`
}

type TestExternalCredentialProfileResponse struct {
	Provider       string         `json:"provider"`
	SecretBinding  map[string]any `json:"secret_binding"`
	Status         string         `json:"status"`
	LastVerifiedAt *string        `json:"last_verified_at"`
	LastError      string         `json:"last_error,omitempty"`
}

func externalCredentialProfileToResponse(profile db.ExternalCredentialProfile) ExternalCredentialProfileResponse {
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
	return ExternalCredentialProfileResponse{
		ID:             uuidToString(profile.ID),
		UserID:         uuidToString(profile.UserID),
		Scope:          "account",
		Provider:       profile.Provider,
		Name:           profile.Name,
		SecretBinding:  binding,
		Capabilities:   decodeJSONDefault(profile.Capabilities, map[string]any{}),
		Status:         profile.Status,
		LastVerifiedAt: timestampToPtr(profile.LastVerifiedAt),
		LastError:      profile.LastError,
		CreatedAt:      timestampToString(profile.CreatedAt),
		UpdatedAt:      timestampToString(profile.UpdatedAt),
	}
}

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
	resp := make([]ExternalCredentialProfileResponse, len(rows))
	for i, row := range rows {
		resp[i] = externalCredentialProfileToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": resp})
}

func (h *Handler) CreateExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateExternalCredentialProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	provider, name, capabilities, statusCode, msg := h.normalizeExternalCredentialInput(req.Provider, req.Name, req.Capabilities)
	if statusCode != 0 {
		writeError(w, statusCode, msg)
		return
	}
	secretRef, encrypted, hint, statusCode, msg := h.prepareExternalCredentialSecret(req.SecretRef, req.Token)
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
	profile, err := h.Queries.CreateExternalCredentialProfile(r.Context(), db.CreateExternalCredentialProfileParams{
		UserID:          parseUUID(userID),
		Provider:        provider,
		Name:            name,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
		SecretHint:      hint,
		Capabilities:    capabilities,
		Status:          pgtype.Text{String: status, Valid: true},
		LastVerifiedAt:  lastVerified,
		LastError:       lastError,
	})
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "credential profile already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create credential profile")
		return
	}
	writeJSON(w, http.StatusCreated, externalCredentialProfileToResponse(profile))
}

func (h *Handler) GetExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.loadExternalCredentialProfile(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, externalCredentialProfileToResponse(profile))
}

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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
		secretRef, encrypted, hint, statusCode, msg := h.prepareExternalCredentialSecret(valueOrEmpty(req.SecretRef), valueOrEmpty(req.Token))
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
	writeJSON(w, http.StatusOK, externalCredentialProfileToResponse(profile))
}

func (h *Handler) TestExternalCredentialProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	var req TestExternalCredentialProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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
	status, lastVerified, lastError := verifyExternalCredentialProfile(provider, secretRef, encrypted)
	lastErrorString := lastError.String
	if status == "unverified" && strings.Contains(lastErrorString, "凭据绑定已保存") {
		lastErrorString = "凭据绑定格式有效；实时工蜂/TAPD API 校验尚未接入。"
	}
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

func (h *Handler) verifyGongfengCredentialConnection(ctx context.Context, secretRef string, encrypted []byte) (string, pgtype.Timestamptz, pgtype.Text) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	token := h.resolveExternalCredentialToken(db.ExternalCredentialProfile{
		Provider:        externalCredentialProviderGongfeng,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
	})
	if strings.TrimSpace(token) == "" {
		return "failed", now, pgtype.Text{String: "工蜂 token 不可用；请检查输入或服务端环境变量。", Valid: true}
	}
	target := gongfengAPIBase() + "/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "failed", now, pgtype.Text{String: "工蜂连接测试地址无效。", Valid: true}
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Private-Token", token)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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

func (h *Handler) verifyTAPDCredentialConnection(ctx context.Context, secretRef string, encrypted []byte) (string, pgtype.Timestamptz, pgtype.Text) {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	token := h.resolveExternalCredentialToken(db.ExternalCredentialProfile{
		Provider:        externalCredentialProviderTAPD,
		SecretRef:       secretRef,
		EncryptedSecret: encrypted,
	})
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
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
	out, err := json.Marshal(obj)
	if err != nil {
		if w != nil {
			writeError(w, http.StatusBadRequest, "capabilities must be a JSON object")
		}
		return nil, false
	}
	return out, true
}

func (h *Handler) prepareExternalCredentialSecret(secretRef, token string) (string, []byte, string, int, string) {
	secretRef = strings.TrimSpace(secretRef)
	token = strings.TrimSpace(token)
	if secretRef != "" && token != "" {
		return "", nil, "", http.StatusBadRequest, "provide either secret_ref or token, not both"
	}
	if secretRef == "" && token == "" {
		return "", nil, "", http.StatusBadRequest, "secret_ref or token is required"
	}
	if secretRef != "" {
		if utf8.RuneCountInString(secretRef) > 240 {
			return "", nil, "", http.StatusBadRequest, "secret_ref is too long"
		}
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

func externalCredentialSecretRefError(secretRef string) string {
	secretRef = strings.TrimSpace(secretRef)
	if secretRef == "" {
		return ""
	}
	if strings.HasPrefix(secretRef, "env:") {
		key := strings.TrimSpace(strings.TrimPrefix(secretRef, "env:"))
		if key == "" {
			return "服务端环境变量名称为空；请填写 env:GONGFENG_ACCESS_TOKEN 这类引用。"
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
		keys := externalCredentialProviderEnvKeys(parts[1])
		if len(keys) == 0 {
			return "server-managed 凭据 provider 不支持：" + parts[1]
		}
		for _, key := range keys {
			if strings.TrimSpace(os.Getenv(key)) != "" {
				return ""
			}
		}
		return "服务器环境变量 " + strings.Join(keys, " / ") + " 未设置；请改用访问令牌，或让管理员配置变量并重启服务。"
	}
	return ""
}

func externalCredentialProviderEnvKeys(provider string) []string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case externalCredentialProviderTAPD:
		return []string{"TAPD_ACCESS_TOKEN"}
	case externalCredentialProviderGongfeng:
		return []string{"GONGFENG_ACCESS_TOKEN", "GONGFENG_PRIVATE_TOKEN"}
	default:
		return nil
	}
}

func isSupportedExternalCredentialProvider(provider string) bool {
	return provider == externalCredentialProviderTAPD || provider == externalCredentialProviderGongfeng
}

func isValidExternalCredentialStatus(status string) bool {
	switch status {
	case "unverified", "verified", "failed", "disabled":
		return true
	default:
		return false
	}
}

func secretHint(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) <= 4 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}

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

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
