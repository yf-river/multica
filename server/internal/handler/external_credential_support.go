// 职责定位：外部凭据（external credential）与工蜂（Gongfeng）集成的公共支撑函数，
// 被多个 handler 复用。它自己不含任何 HTTP 路由，只提供五类「积木」：
//  1. 取「当前用户可用的默认工蜂凭据档案」（loadUsableGongfengCredentialProfile）；
//  2. 把档案里的秘密还原成明文 token，供打外部 API 时放进请求头
//     （resolveExternalCredentialToken）；
//  3. 幂等请求的支撑：校验 Idempotency-Key、对请求内容取指纹
//     （requireIdempotencyKey / hashRequestFingerprint），以及两个极薄的错误/回放写回壳；
//  4. 工蜂 API 的出站 base 地址（gongfengAPIBase，可被环境变量覆盖）；
//  5. firstNonEmpty 这类与业务无关的通用小工具。
//
// 调用方：external_credential_profile.go（档案 CRUD + 试连接）、gongfeng_merge_request.go、
// workspace_gongfeng_repo.go、issue_source_fetch.go 等。这些函数的行为约定见各自注释。
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"net/http"
	"os"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadUsableGongfengCredentialProfile 取该用户默认的、当前可用的工蜂凭据档案。
//
// 返回语义：找不到档案、或档案已禁用/校验失败、或档案里没有任何秘密（secret_ref 与
// 加密秘密都为空）时，一律返回 (零值, false, nil)——即「拿不到可用凭据」不算系统错误，
// 调用方据此走「该用户未配置凭据」的提示分支；只有查询真正失败才返回非 nil 的 error。
func (h *Handler) loadUsableGongfengCredentialProfile(ctx context.Context, userID string) (db.ExternalCredentialProfile, bool, error) {
	userUUID, ok := h.parseUserUUIDOrZero(userID)
	if !ok {
		return db.ExternalCredentialProfile{}, false, nil
	}
	profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
		UserID:   userUUID,
		Provider: externalCredentialProviderGongfeng,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ExternalCredentialProfile{}, false, nil
	}
	if err != nil {
		return db.ExternalCredentialProfile{}, false, err
	}
	if profile.Status == "disabled" || profile.Status == "failed" || (profile.SecretRef == "" && len(profile.EncryptedSecret) == 0) {
		return db.ExternalCredentialProfile{}, false, nil
	}
	return profile, true, nil
}

// hashRequestFingerprint 把请求的「业务内容」序列化后取 SHA-256，得到幂等回放的指纹。
// 幂等键（Idempotency-Key）保证「同一份内容只处理一次」，而指纹用于区分「键相同、
// 内容其实不同」的冲突请求（见 external_credential_profile.go 的用法）。
func hashRequestFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// requireIdempotencyKey 从请求头读 Idempotency-Key 并校验它是合法 UUID。
// 缺头或格式非法时直接写 400 并返回 false，调用方应立即 return。
func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required")
		return pgtype.UUID{}, false
	}
	parsed, err := util.ParseUUID(key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be a UUID")
		return pgtype.UUID{}, false
	}
	return parsed, true
}

// writeIdempotencyConflict 是「幂等键被别的内容占用」时的标准 409 写回。
func writeIdempotencyConflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, message)
}

// writeIdempotencyReplayJSON 是「命中幂等回放」时的写回：把第一次请求的响应原样
// 重放给这次重复请求。封装它只是为了在调用点把「回放」意图写清楚。
func writeIdempotencyReplayJSON(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, value)
}

// gongfengAPIBase 返回工蜂开放 API 的 base 地址。默认指向腾讯工蜂的公开
// API v3 网关；部署方可用环境变量 GONGFENG_API_BASE 覆盖（去掉末尾斜杠）。
func gongfengAPIBase() string {
	if value := strings.TrimSpace(os.Getenv("GONGFENG_API_BASE")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://git.code.tencent.com/api/v3"
}

// resolveExternalCredentialToken 把一条凭据档案还原成可用于请求头（Authorization /
// PRIVATE-TOKEN）的明文访问令牌。秘密有三种存放方式，按优先级取：
//  1. 加密入库的 EncryptedSecret：用服务端持有的 ExternalCredentialBox 解密；解密器
//     未配置时视为配置错误直接报错；
//  2. SecretRef 以 "env:" 开头：指向环境变量名，直接读该变量的值；
//  3. SecretRef 以 "server-managed:" 开头：按 provider 映射到服务端统一维护的环境变量
//     （tapd → TAPD_ACCESS_TOKEN，gongfeng → GONGFENG_PRIVATE_TOKEN）。
//
// 三种都匹配不上说明这条档案从未配置过秘密，返回明确的配置错误而不是空串。
func (h *Handler) resolveExternalCredentialToken(profile db.ExternalCredentialProfile) (string, error) {
	if len(profile.EncryptedSecret) > 0 {
		if h.ExternalCredentialBox == nil {
			return "", errors.New("external credential decryptor is not configured")
		}
		plain, err := h.ExternalCredentialBox.Open(profile.EncryptedSecret)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(plain)), nil
	}
	if strings.HasPrefix(profile.SecretRef, "env:") {
		return strings.TrimSpace(os.Getenv(strings.TrimPrefix(profile.SecretRef, "env:"))), nil
	}
	if strings.HasPrefix(profile.SecretRef, "server-managed:") {
		key := map[string]string{"tapd": "TAPD_ACCESS_TOKEN", "gongfeng": "GONGFENG_PRIVATE_TOKEN"}[profile.Provider]
		return strings.TrimSpace(os.Getenv(key)), nil
	}
	return "", errors.New("external credential is not configured")
}

// firstNonEmpty 返回参数里第一个非空白（trim 后非空）的字符串；全部为空则返回 ""。
// 常见用法是「新值优先，旧值/缺省兜底」。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
