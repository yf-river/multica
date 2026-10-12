// 职责定位：账号认证相关端点：Google OAuth 登录（授权码换 token → 拉用户资料 → 按邮箱
// find-or-create 用户 → 签发站内 JWT 与 cookie）、给浏览器会话换发 CLI 用的 bearer token、
// 登出清理 cookie、以及 /me 的读与更新。
//
// 注册管控：是否允许自注册由实例级配置控制（h.cfg.AllowSignup + 邮箱/域名白名单），失败以
// SignupError 表达；「被临时禁用」的账号在发 token 与登录两处都会拦截（见 internal/auth）。
// 时区/简介等字段有长度与时区名合法性校验。真实口令登录在 account_login.go。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SignupError 表示注册被限制类错误（配置不允许自注册、邮箱不在白名单等），message 可直接
// 给用户看。
type SignupError struct {
	Message string
}

func (e SignupError) Error() string {
	return e.Message
}

var ErrSignupProhibited = SignupError{Message: "user registration is disabled on this self-hosted instance"}
var ErrEmailNotAllowed = SignupError{Message: "email address or domain not allowed on this instance"}

// UserResponse 是用户资料的接口形态。
type UserResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Account   string  `json:"account"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatar_url"`
	// 钉住的 IANA 时区；nil = 未设置（用浏览器探测的时区）。
	Timezone                *string         `json:"timezone"`
	OnboardedAt             *string         `json:"onboarded_at"`
	OnboardingQuestionnaire json.RawMessage `json:"onboarding_questionnaire"`
	StarterContentState     *string         `json:"starter_content_state"`
	ProfileDescription      string          `json:"profile_description"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
}

// MaxProfileDescriptionLen 是用户 profile_description 的上限。按 MUL-2406 定为 2000 字符：
// 足够写清角色/技术栈/少量偏好，又短到「每次注入 agent brief」都不至于让提示词成本起飞。
const MaxProfileDescriptionLen = 2000

func (h *Handler) userToResponse(u db.User) UserResponse {
	// JSONB 列是带 DEFAULT '{}' 的 []byte，库层面不会为 nil。这里做防御性兜底，以防将来
	// 有 ALTER 把列改成可空、某行回来时没套默认值。
	q := u.OnboardingQuestionnaire
	if len(q) == 0 {
		q = []byte("{}")
	}
	return UserResponse{
		ID:                      uuidToString(u.ID),
		Name:                    u.Name,
		Account:                 u.Account,
		Email:                   u.Email,
		AvatarURL:               h.resolveAvatarURLPtr(textToPtr(u.AvatarUrl)),
		Timezone:                textToPtr(u.Timezone),
		OnboardedAt:             timestampToPtr(u.OnboardedAt),
		OnboardingQuestionnaire: json.RawMessage(q),
		StarterContentState:     textToPtr(u.StarterContentState),
		ProfileDescription:      u.ProfileDescription,
		CreatedAt:               timestampToString(u.CreatedAt),
		UpdatedAt:               timestampToString(u.UpdatedAt),
	}
}

// LoginResponse 是登录成功后的响应：站内 JWT + 用户资料。
type LoginResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

// issueJWT 为已登录用户签发 HS256 JWT（sub/account/email/name，带过期时间）。被临时禁用的
// 用户在这里会被拒。account 缺省时退回用 email。
func (h *Handler) issueJWT(user db.User) (string, error) {
	if auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return "", auth.ErrTemporarilyDisabledUser
	}
	account := user.Account
	if account == "" {
		account = user.Email
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":     uuidToString(user.ID),
		"account": account,
		"email":   user.Email,
		"name":    user.Name,
		"exp":     time.Now().Add(auth.AuthTokenTTL()).Unix(),
		"iat":     time.Now().Unix(),
	})
	return token.SignedString(auth.JWTSecret())
}

// findOrCreateUser 按邮箱取已存在的用户，不存在则创建。isNew 报告本次调用是否新建了用户——
// 新用户经认证提供方首次进来时，会用它触发 signup 埋点。已禁用用户在两端都会被拒。
func (h *Handler) findOrCreateUser(ctx context.Context, email string) (user db.User, isNew bool, err error) {
	if auth.IsTemporarilyDisabledUserEmail(email) {
		return db.User{}, false, auth.ErrTemporarilyDisabledUser
	}

	user, err = h.Queries.GetUserByEmail(ctx, email)
	isNew = isNotFound(err)
	if err != nil && !isNew {
		return db.User{}, false, err
	}
	if !isNew && auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return db.User{}, false, auth.ErrTemporarilyDisabledUser
	}

	if err := h.checkSignupAllowed(email, isNew); err != nil {
		return db.User{}, false, err
	}

	if !isNew {
		return user, false, nil
	}

	name := email
	if at := strings.Index(email, "@"); at > 0 {
		name = email[:at]
	}
	created, err := h.Queries.CreateUser(ctx, db.CreateUserParams{
		Name:    name,
		Account: email,
		Email:   email,
	})
	if err != nil {
		return db.User{}, false, err
	}
	return created, true, nil
}

// signupSourceFromRequest 读 web 前端在首次访问时种下的归因 cookie（UTM + referrer 捆绑）。
// 前端把 JSON 字符串 URL 编码后放进 cookie 值——Go 不会自动解码 Cookie.Value，所以必须先
// 在这里 unescape，字符串才能干净地进 PostHog。cookie 缺失/解码失败一律归空串：效果只是
// signup_source 不出现在事件里，而不是把百分号编码的垃圾发出去。绝不要退回 r.Referer()——
// 前端已经清洗过归因，而原始 referer 可能从回调 URL 里漏出 OAuth code/state。
//
// 上限是服务端对「客户端塞了个超大 cookie」的防御；与前端 SIGNUP_SOURCE_MAX_LEN 对齐。
const signupSourceMaxLen = 512

func signupSourceFromRequest(r *http.Request) string {
	c, err := r.Cookie(auth.CookieName("multica_signup_source"))
	if err != nil || c == nil {
		return ""
	}
	decoded, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	if len(decoded) > signupSourceMaxLen {
		return ""
	}
	return decoded
}

// checkSignupAllowed 决定是否允许新注册：已存在的用户总能登录；新用户依次看「邮箱白名单 →
// 域名白名单 → 全局开关」，并遵守「设了白名单就必须命中白名单」的语义。
func (h *Handler) checkSignupAllowed(email string, isNewUser bool) error {
	if !isNewUser {
		return nil // 已存在用户始终允许登录
	}

	email = strings.ToLower(email)
	domain := ""
	if at := strings.Index(email, "@"); at > 0 {
		domain = email[at+1:]
	}

	// 1. 显式邮箱白名单永远优先
	if len(h.cfg.AllowedEmails) > 0 && contains(h.cfg.AllowedEmails, email) {
		return nil
	}

	// 2. 域名白名单永远优先
	if len(h.cfg.AllowedEmailDomains) > 0 && contains(h.cfg.AllowedEmailDomains, domain) {
		return nil
	}

	// 3. 全局注册开关
	if !h.cfg.AllowSignup {
		return ErrSignupProhibited
	}

	// 4. 配了白名单但没命中 → 拒绝（白名单模式 = 默认全拒）
	if len(h.cfg.AllowedEmailDomains) > 0 || len(h.cfg.AllowedEmails) > 0 {
		return ErrSignupProhibited
	}

	return nil
}

// contains 大小写不敏感地判断 slice 里是否含 s。
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// UpdateMeRequest 是「更新我的资料」请求体：指针为 nil 表示不改对应字段。
type UpdateMeRequest struct {
	Name               *string `json:"name"`
	AvatarURL          *string `json:"avatar_url"`
	ProfileDescription *string `json:"profile_description"`
	Timezone           *string `json:"timezone"`
}

// GoogleLoginRequest 是 Google OAuth 登录请求体：前端交过来的授权码与重定向地址。
type GoogleLoginRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// googleTokenResponse 是 Google token 交换接口的最小响应形态。
type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

// googleUserInfo 是 Google userinfo 接口的最小响应形态。
type googleUserInfo struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// GoogleLogin 走 Google OAuth 登录：授权码换 token → 拉用户资料 → find-or-create 用户 →
// 签发站内 JWT 并种 cookie。新用户会补发带 auth_method=google 的 signup 埋点，并把 Google
// 的真实姓名/头像回填进用户资料（仅当名字还是默认的邮箱前缀、或还没头像时）。完成后给
// Cloudflare（CF）签一套短期 cookie。依赖 GOOGLE_CLIENT_ID/SECRET 已配置，否则 503。
func (h *Handler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	var req GoogleLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "Google login is not configured")
		return
	}

	redirectURI := req.RedirectURI
	if redirectURI == "" {
		redirectURI = os.Getenv("GOOGLE_REDIRECT_URI")
	}

	// 用授权码向 Google 换 access/id token。
	tokenResp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {req.Code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		slog.Error("google oauth token exchange failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to exchange code with Google")
		return
	}
	defer tokenResp.Body.Close()

	tokenBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read Google token response")
		return
	}

	if tokenResp.StatusCode != http.StatusOK {
		slog.Error("google oauth token exchange returned error", "status", tokenResp.StatusCode, "body", string(tokenBody))
		writeError(w, http.StatusBadRequest, "failed to exchange code with Google")
		return
	}

	var gToken googleTokenResponse
	if err := json.Unmarshal(tokenBody, &gToken); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google token response")
		return
	}

	// 拿 access token 去 Google 拉用户基本信息。
	userInfoReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		slog.Error("failed to create userinfo request", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	userInfoReq.Header.Set("Authorization", "Bearer "+gToken.AccessToken)

	userInfoResp, err := http.DefaultClient.Do(userInfoReq)
	if err != nil {
		slog.Error("google userinfo fetch failed", "error", err)
		writeError(w, http.StatusBadGateway, "failed to fetch user info from Google")
		return
	}
	defer userInfoResp.Body.Close()

	var gUser googleUserInfo
	if err := json.NewDecoder(userInfoResp.Body).Decode(&gUser); err != nil {
		writeError(w, http.StatusBadGateway, "failed to parse Google user info")
		return
	}

	if gUser.Email == "" {
		writeError(w, http.StatusBadRequest, "Google account has no email")
		return
	}

	email := strings.ToLower(strings.TrimSpace(gUser.Email))
	if auth.IsTemporarilyDisabledUserEmail(email) {
		writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
		return
	}

	user, isNew, err := h.findOrCreateUser(r.Context(), email)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	if isNew {
		evt := analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r))
		evt.Properties["auth_method"] = "google"
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, evt)
	}

	// 把 Google 档案里的姓名/头像回填进站内资料：仅在用户「刚创建（默认名还是邮箱前缀）」
	// 或「还没有头像」时才覆盖。
	needsUpdate := false
	newName := user.Name
	newAvatar := user.AvatarUrl

	if gUser.Name != "" && user.Name == strings.Split(email, "@")[0] {
		newName = gUser.Name
		needsUpdate = true
	}
	if gUser.Picture != "" && !user.AvatarUrl.Valid {
		newAvatar = pgtype.Text{String: gUser.Picture, Valid: true}
		needsUpdate = true
	}

	if needsUpdate {
		updated, err := h.Queries.UpdateUser(r.Context(), db.UpdateUserParams{
			ID:        user.ID,
			Name:      newName,
			AvatarUrl: newAvatar,
		})
		if err == nil {
			user = updated
		}
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("google login failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}

	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(72 * time.Hour)) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via google", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  h.userToResponse(user),
	})
}

// IssueCliToken 给已认证用户发一枚全新 JWT。用途：让 cookie 认证的浏览器会话能拿到一个
// bearer token，经 cli_callback 重定向交给 CLI 使用。
func (h *Handler) IssueCliToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("cli-token: failed to issue JWT", append(logger.RequestAttrs(r), "error", err, "user_id", userID)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": tokenString})
}

// Logout 清掉认证 cookie 并返回成功。
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

// GetMe 返回当前登录用户的完整资料。
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	user, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, h.userToResponse(user))
}

// UpdateMe 更新当前用户资料。name / avatar_url / profile_description / timezone 只有传了非
// nil 才更新；头像走 acceptAvatarURL 的白名单与远程校验；简介按「字符数」（非字节）限长；
// timezone 若传了必须是合法 IANA 时区。
func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	currentUser, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	name := currentUser.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}

	params := db.UpdateUserParams{
		ID:   currentUser.ID,
		Name: name,
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, currentUser.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.ProfileDescription != nil {
		// 按「字符」而非「字节」数：2000 字的中文不该被当成约 6000 字节而拒掉。
		// utf8.RuneCountInString 对非法 UTF-8 会把每个坏字节算成一个 rune，仍然兜得住列宽。
		desc := strings.TrimSpace(*req.ProfileDescription)
		if utf8.RuneCountInString(desc) > MaxProfileDescriptionLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("profile_description exceeds %d characters", MaxProfileDescriptionLen))
			return
		}
		params.ProfileDescription = pgtype.Text{String: desc, Valid: true}
	}

	if req.Timezone != nil {
		// 三态语义由 UpdateUser 的 SQL CASE 落实：Valid=false → 不动该列；Valid=true + "" →
		// 清成 NULL；Valid=true + IANA 名 → 设置。
		tz := strings.TrimSpace(*req.Timezone)
		if tz != "" {
			if loc, err := time.LoadLocation(tz); err != nil || loc == nil {
				writeError(w, http.StatusBadRequest, "invalid timezone")
				return
			}
		}
		params.Timezone = pgtype.Text{String: tz, Valid: true}
	}

	updatedUser, err := h.Queries.UpdateUser(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}

	writeJSON(w, http.StatusOK, h.userToResponse(updatedUser))
}
