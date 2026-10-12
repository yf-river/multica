// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（account_login.go）实现"账号密码登录/自动注册"：登录失败且该账号
// 不存在时，若注册开放则自动创建账号（first-run 自助引导）。账号规则、
// 密码强度校验与 PBKDF2 口令哈希都集中在这里。
package handler

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
)

// accountPasswordIterations 是 PBKDF2 口令哈希的迭代次数（210k）。
const accountPasswordIterations = 210000

// accountName 规范化账号名（小写去空格），并校验格式：3~64 位，仅允许
// 小写字母/数字/点/横线/下划线。合法返回规范化后的名字。
func accountName(value string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if len(v) < 3 || len(v) > 64 {
		return "", false
	}
	for _, r := range v {
		if !(r == '.' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return "", false
		}
	}
	return v, true
}

// accountPasswordValid 校验注册时的密码强度：8~32 位，字符属于允许集合，
// 且大小写字母/数字/符号至少占三类（即至少含三种字符族）。
func accountPasswordValid(v string) bool {
	if len(v) < 8 || len(v) > 32 {
		return false
	}
	var upper, lower, digit, special bool
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		case strings.ContainsRune(`!"#$%&'()*+,-./:;<=>?@[\]^_`+"`{|}~", r):
			special = true
		default:
			return false
		}
	}
	n := 0
	for _, ok := range []bool{upper, lower, digit, special} {
		if ok {
			n++
		}
	}
	return n >= 3
}

// accountPasswordLoginValid 接受已有账号的非空密码，由存储的哈希验证身份。
// 新账号的密码强度由 accountPasswordValid 单独校验。
func accountPasswordLoginValid(v string) bool {
	return len(v) > 0 && len(v) <= 32
}

// accountPasswordHash 用随机盐做 PBKDF2-SHA256，返回
// "pbkdf2_sha256$<迭代次数>$<盐>$<哈希>"四段式编码，可自描述地存库。
func accountPasswordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, accountPasswordIterations, 32)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{"pbkdf2_sha256", strconv.Itoa(accountPasswordIterations), base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)}, "$"), nil
}

// accountPasswordMatches 校验明文密码与存储编码是否匹配；用常量时间比较
// 防时序侧信道。迭代次数从编码里读取，支持将来升级。
func accountPasswordMatches(password, encoded string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 4 || p[0] != "pbkdf2_sha256" {
		return false
	}
	it, err := strconv.Atoi(p[1])
	if err != nil || it <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(p[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(p[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, it, 32)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// AccountPasswordLogin 账号密码登录：账号不存在时若允许注册则当场创建
// （首次登录即自助开通）；成功后签发 JWT 并设置认证 cookie。
func (h *Handler) AccountPasswordLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	account, ok := accountName(req.Account)
	if !ok || !accountPasswordLoginValid(req.Password) {
		writeError(w, http.StatusBadRequest, "invalid account or password")
		return
	}
	user, err := h.Queries.GetUserByAccount(r.Context(), account)
	if err != nil {
		if signupErr := h.checkSignupAllowed(account, true); signupErr != nil {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		if !accountPasswordValid(req.Password) {
			writeError(w, http.StatusBadRequest, "invalid account or password")
			return
		}
		hash, hashErr := accountPasswordHash(req.Password)
		if hashErr != nil {
			writeError(w, 500, "failed to hash password")
			return
		}
		var id pgtype.UUID
		if err = h.DB.QueryRow(r.Context(), `INSERT INTO "user" (name, account, email, password_hash) VALUES ($1,$2,$2,$3) RETURNING id`, account, account, hash).Scan(&id); err != nil {
			writeError(w, http.StatusConflict, "account already exists")
			return
		}
		user, err = h.Queries.GetUser(r.Context(), id)
		if err != nil {
			writeError(w, 500, "failed to load user")
			return
		}
		user.Account = account
	} else {
		var hash string
		if err = h.DB.QueryRow(r.Context(), `SELECT COALESCE(password_hash,'') FROM "user" WHERE id=$1`, user.ID).Scan(&hash); err != nil || !accountPasswordMatches(req.Password, hash) {
			writeError(w, http.StatusUnauthorized, "invalid account or password")
			return
		}
	}
	token, err := h.issueJWT(user)
	if err != nil {
		writeError(w, 500, "failed to generate token")
		return
	}
	_ = auth.SetAuthCookies(w, token)
	writeJSON(w, http.StatusOK, LoginResponse{Token: token, User: h.userToResponse(user)})
}
