package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

// SignupError represents signup restriction errors
type SignupError struct {
	Message string
}

func (e SignupError) Error() string {
	return e.Message
}

var ErrSignupProhibited = SignupError{Message: "user registration is disabled on this self-hosted instance"}
var ErrAccountNotAllowed = SignupError{Message: "account not allowed on this instance"}

type UserResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Account   string  `json:"account"`
	AvatarURL *string `json:"avatar_url"`
	// Pinned IANA tz; nil = no preference (use browser-detected tz).
	Timezone                *string         `json:"timezone"`
	OnboardedAt             *string         `json:"onboarded_at"`
	OnboardingQuestionnaire json.RawMessage `json:"onboarding_questionnaire"`
	StarterContentState     *string         `json:"starter_content_state"`
	ProfileDescription      string          `json:"profile_description"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
}

// MaxProfileDescriptionLen caps the user-supplied profile_description body.
// Picked at 2000 chars per MUL-2406: enough room for role / stack / a few
// preferences, short enough that injecting it into every agent brief
// doesn't move the needle on prompt cost.
const MaxProfileDescriptionLen = 2000

func userToResponse(u db.User) UserResponse {
	// JSONB column is []byte with DEFAULT '{}', so it's never nil at the DB
	// level. Defensive coalesce just in case a future ALTER makes the column
	// nullable and some row comes back with no default applied.
	q := u.OnboardingQuestionnaire
	if len(q) == 0 {
		q = []byte("{}")
	}
	return UserResponse{
		ID:                      uuidToString(u.ID),
		Name:                    u.Name,
		Account:                 u.Account,
		AvatarURL:               textToPtr(u.AvatarUrl),
		Timezone:                textToPtr(u.Timezone),
		OnboardedAt:             timestampToPtr(u.OnboardedAt),
		OnboardingQuestionnaire: json.RawMessage(q),
		StarterContentState:     textToPtr(u.StarterContentState),
		ProfileDescription:      u.ProfileDescription,
		CreatedAt:               timestampToString(u.CreatedAt),
		UpdatedAt:               timestampToString(u.UpdatedAt),
	}
}

type LoginResponse struct {
	Token string       `json:"token"`
	User  UserResponse `json:"user"`
}

type PasswordLoginRequest struct {
	Account  string `json:"account"`
	Password string `json:"password"`
}

const (
	passwordHashAlgorithm  = "pbkdf2_sha256"
	passwordHashIterations = 210000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32
	minPasswordLen         = 8
)

func normalizeAccount(account string) (string, bool) {
	account = strings.ToLower(strings.TrimSpace(account))
	if len(account) < 3 || len(account) > 64 {
		return "", false
	}
	for _, ch := range account {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.' {
			continue
		}
		return "", false
	}
	return account, true
}

func derivePasswordKey(password string, salt []byte, iterations int) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	hashLen := mac.Size()
	blocks := (passwordKeyBytes + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	var blockBuf [4]byte
	for block := 1; block <= blocks; block++ {
		mac.Reset()
		mac.Write(salt)
		binary.BigEndian.PutUint32(blockBuf[:], uint32(block))
		mac.Write(blockBuf[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:passwordKeyBytes]
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := derivePasswordKey(password, salt, passwordHashIterations)
	return strings.Join([]string{
		passwordHashAlgorithm,
		strconv.Itoa(passwordHashIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashAlgorithm {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got := derivePasswordKey(password, salt, iterations)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (h *Handler) issueJWT(user db.User) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":     uuidToString(user.ID),
		"account": user.Account,
		"name":    user.Name,
		"exp":     time.Now().Add(auth.AuthTokenTTL()).Unix(),
		"iat":     time.Now().Unix(),
	})
	return token.SignedString(auth.JWTSecret())
}

func (h *Handler) checkSignupAllowed(account string, isNewUser bool) error {
	if !isNewUser {
		return nil // existing users always allowed to log in
	}

	account = strings.ToLower(strings.TrimSpace(account))
	if len(h.cfg.AllowedAccounts) > 0 && contains(h.cfg.AllowedAccounts, account) {
		return nil
	}

	if !h.cfg.AllowSignup {
		return ErrSignupProhibited
	}

	if len(h.cfg.AllowedAccounts) > 0 {
		return ErrAccountNotAllowed
	}

	return nil
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

func (h *Handler) AccountPasswordLogin(w http.ResponseWriter, r *http.Request) {
	var req PasswordLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	account, ok := normalizeAccount(req.Account)
	if !ok {
		writeError(w, http.StatusBadRequest, "account must be 3-64 characters using letters, numbers, dot, dash, or underscore")
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	user, err := h.Queries.GetUserByAccount(r.Context(), account)
	isNew := isNotFound(err)
	if err != nil && !isNew {
		writeError(w, http.StatusInternalServerError, "failed to lookup user")
		return
	}

	if isNew {
		if err := h.checkSignupAllowed(account, true); err != nil {
			var signupErr SignupError
			if errors.As(err, &signupErr) {
				writeError(w, http.StatusForbidden, signupErr.Error())
			} else {
				writeError(w, http.StatusForbidden, "user registration is disabled")
			}
			return
		}
		passwordHash, err := hashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		user, err = h.Queries.CreateUser(r.Context(), db.CreateUserParams{
			Name:    account,
			Account: account,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create user")
			return
		}
		if _, err := h.DB.Exec(r.Context(), `UPDATE "user" SET password_hash = $2, updated_at = now() WHERE id = $1`, user.ID, passwordHash); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save password")
			return
		}
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.Signup(uuidToString(user.ID), user.Account))
	} else {
		var passwordHash string
		if err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(password_hash, '') FROM "user" WHERE id = $1`, user.ID).Scan(&passwordHash); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load password")
			return
		}
		if passwordHash == "" || !verifyPassword(req.Password, passwordHash) {
			writeError(w, http.StatusUnauthorized, "invalid account or password")
			return
		}
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		slog.Warn("login failed", append(logger.RequestAttrs(r), "error", err, "account", account)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "account", account)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  userToResponse(user),
	})
}

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

	writeJSON(w, http.StatusOK, userToResponse(user))
}

type UpdateMeRequest struct {
	Name               *string `json:"name"`
	AvatarURL          *string `json:"avatar_url"`
	ProfileDescription *string `json:"profile_description"`
	// IANA tz to pin; "" clears back to NULL; nil leaves untouched.
	Timezone *string `json:"timezone"`
}

// IssueCliToken returns a fresh JWT for the authenticated user.
// This allows cookie-authenticated browser sessions to obtain a bearer token
// that can be handed off to the CLI via the cli_callback redirect.
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
		slog.Warn("cli-token: failed to issue JWT", append(logger.RequestAttrs(r), "error", err, "user_id", userID)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": tokenString})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

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
		params.AvatarUrl = pgtype.Text{String: strings.TrimSpace(*req.AvatarURL), Valid: true}
	}
	if req.ProfileDescription != nil {
		// Count runes, not bytes: 2000 chars of Chinese must not be rejected
		// as ~6000 bytes. utf8.RuneCountInString handles invalid UTF-8 by
		// counting each bad byte as one rune, which still bounds the column.
		desc := strings.TrimSpace(*req.ProfileDescription)
		if utf8.RuneCountInString(desc) > MaxProfileDescriptionLen {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("profile_description exceeds %d characters", MaxProfileDescriptionLen))
			return
		}
		params.ProfileDescription = pgtype.Text{String: desc, Valid: true}
	}

	if req.Timezone != nil {
		// Valid=false → column untouched; Valid=true + "" → clear to
		// NULL; Valid=true + IANA → set. Three-way semantics enforced
		// in the UpdateUser SQL CASE.
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

	writeJSON(w, http.StatusOK, userToResponse(updatedUser))
}
