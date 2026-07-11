package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	chatCreateSessionOperation = "create_session"
	chatSendMessageOperation   = "send_message"
)

var errChatIdempotencyConflict = errors.New("idempotency key was already used with a different request")

type chatIdempotencyScope struct {
	workspaceID    pgtype.UUID
	actorType      string
	actorID        pgtype.UUID
	operation      string
	idempotencyKey pgtype.UUID
	requestHash    string
}

type createChatSessionRequestFingerprint struct {
	Version int    `json:"version"`
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

type sendChatMessageRequestFingerprint struct {
	Version       int      `json:"version"`
	SessionID     string   `json:"session_id"`
	Content       string   `json:"content"`
	AttachmentIDs []string `json:"attachment_ids"`
}

func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Idempotency-Key header is required",
			"code":  "idempotency_key_required",
		})
		return pgtype.UUID{}, false
	}
	if len(values) != 1 || strings.Contains(values[0], ",") {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Idempotency-Key must contain exactly one UUIDv4",
			"code":  "idempotency_key_invalid",
		})
		return pgtype.UUID{}, false
	}
	key, err := util.ParseUUID(strings.TrimSpace(values[0]))
	if err != nil || key.Bytes[6]>>4 != 4 || key.Bytes[8]&0xc0 != 0x80 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Idempotency-Key must be a canonical UUIDv4",
			"code":  "idempotency_key_invalid",
		})
		return pgtype.UUID{}, false
	}
	if util.UUIDToString(key) != strings.ToLower(strings.TrimSpace(values[0])) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Idempotency-Key must be a canonical UUIDv4",
			"code":  "idempotency_key_invalid",
		})
		return pgtype.UUID{}, false
	}
	return key, true
}

func newChatIdempotencyScope(
	workspaceID pgtype.UUID,
	actorType string,
	actorID pgtype.UUID,
	operation string,
	idempotencyKey pgtype.UUID,
	fingerprint any,
) (chatIdempotencyScope, error) {
	raw, err := json.Marshal(fingerprint)
	if err != nil {
		return chatIdempotencyScope{}, fmt.Errorf("encode chat request fingerprint: %w", err)
	}
	digest := sha256.Sum256(raw)
	return chatIdempotencyScope{
		workspaceID:    workspaceID,
		actorType:      actorType,
		actorID:        actorID,
		operation:      operation,
		idempotencyKey: idempotencyKey,
		requestHash:    hex.EncodeToString(digest[:]),
	}, nil
}

func canonicalAttachmentIDs(ids []pgtype.UUID) []string {
	unique := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		unique[util.UUIDToString(id)] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// reserveChatIdempotencyRecord returns created=false for a committed replay.
// The initial INSERT and every business write share the caller's transaction,
// so an incomplete reservation is never visible after a crash or rollback.
func reserveChatIdempotencyRecord(
	ctx context.Context,
	queries *db.Queries,
	scope chatIdempotencyScope,
) (record db.ChatIdempotencyRecord, created bool, err error) {
	record, err = queries.ReserveChatIdempotencyRecord(ctx, db.ReserveChatIdempotencyRecordParams{
		WorkspaceID:    scope.workspaceID,
		ActorType:      scope.actorType,
		ActorID:        scope.actorID,
		Operation:      scope.operation,
		IdempotencyKey: scope.idempotencyKey,
		RequestHash:    scope.requestHash,
	})
	if err == nil {
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.ChatIdempotencyRecord{}, false, fmt.Errorf("reserve chat idempotency record: %w", err)
	}

	record, err = queries.LockChatIdempotencyRecord(ctx, db.LockChatIdempotencyRecordParams{
		WorkspaceID:    scope.workspaceID,
		ActorType:      scope.actorType,
		ActorID:        scope.actorID,
		Operation:      scope.operation,
		IdempotencyKey: scope.idempotencyKey,
	})
	if err != nil {
		return db.ChatIdempotencyRecord{}, false, fmt.Errorf("load chat idempotency replay: %w", err)
	}
	if record.RequestHash != scope.requestHash {
		return db.ChatIdempotencyRecord{}, false, errChatIdempotencyConflict
	}
	if !record.ResponseStatus.Valid || len(record.ResponseBody) == 0 {
		return db.ChatIdempotencyRecord{}, false, errors.New("chat idempotency record is incomplete")
	}
	return record, false, nil
}

func completeChatIdempotencyRecord(
	ctx context.Context,
	queries *db.Queries,
	scope chatIdempotencyScope,
	status int,
	response any,
) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode chat idempotency response: %w", err)
	}
	if _, err := queries.CompleteChatIdempotencyRecord(ctx, db.CompleteChatIdempotencyRecordParams{
		WorkspaceID:    scope.workspaceID,
		ActorType:      scope.actorType,
		ActorID:        scope.actorID,
		Operation:      scope.operation,
		IdempotencyKey: scope.idempotencyKey,
		RequestHash:    scope.requestHash,
		ResponseStatus: pgtype.Int4{Int32: int32(status), Valid: true},
		ResponseBody:   body,
	}); err != nil {
		return fmt.Errorf("complete chat idempotency record: %w", err)
	}
	return nil
}

func decodeChatIdempotencyResponse[T any](record db.ChatIdempotencyRecord) (T, int, error) {
	var response T
	if !record.ResponseStatus.Valid || len(record.ResponseBody) == 0 {
		return response, 0, errors.New("chat idempotency response is incomplete")
	}
	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return response, 0, fmt.Errorf("decode chat idempotency response: %w", err)
	}
	return response, int(record.ResponseStatus.Int32), nil
}

func writeChatIdempotencyFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errChatIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to persist chat request")
}
