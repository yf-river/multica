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

func hashRequestFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
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
func writeIdempotencyConflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, message)
}
func writeIdempotencyReplayJSON(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, value)
}
func gongfengAPIBase() string {
	if value := strings.TrimSpace(os.Getenv("GONGFENG_API_BASE")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://git.code.tencent.com/api/v3"
}
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
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
