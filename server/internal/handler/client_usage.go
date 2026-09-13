// 本文件实现客户端用量上报端点（POST /api/client-usage）：web 客户端每天心跳上报一次
// 「今日活跃」，按 (user, 客户端安装, UTC 日) 去重落一行；带 workspace 上下文时在显式
// 删除/创建锁协议内写入，避免与「删除 workspace」并发互相踩踏。客户端类型/版本/OS 经过
// 白名单与格式校验后才入库。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// clientUsageBodyLimit 限制上报请求体大小（16 KiB）——真实 payload 只有 install_id，上限
// 纯粹是防滥用。
const clientUsageBodyLimit = 16 * 1024

// clientVersionPattern 约束客户端版本号格式：1~64 个可打印 ASCII 字符。
var (
	clientVersionPattern = regexp.MustCompile(`^[\x20-\x7e]{1,64}$`)
)

// clientUsageRequest 是上报请求体：客户端安装 id（install_id）。
type clientUsageRequest struct {
	InstallID string `json:"install_id"`
}

// UpsertClientUsage 为每个 (用户, 客户端安装, UTC 日) 至多保留一行。重复上报刷新同一行；
// 仅活跃上报不会清掉当天早些时候已收集的运行时快照。
func (h *Handler) UpsertClientUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, clientUsageBodyLimit))
	decoder.DisallowUnknownFields()
	var req clientUsageRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	installID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.InstallID), "install_id")
	if !ok {
		return
	}

	clientType, clientVersion, clientOS := middleware.ClientMetadataFromContext(r.Context())
	clientType = strings.ToLower(strings.TrimSpace(clientType))
	if clientType != "web" {
		writeError(w, http.StatusBadRequest, "client platform must be web")
		return
	}
	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = "unknown"
	}
	if !clientVersionPattern.MatchString(clientVersion) {
		writeError(w, http.StatusBadRequest, "invalid client version")
		return
	}
	clientOS = normalizeClientUsageOS(clientOS)

	workspaceUUID := pgtype.UUID{}
	queries := h.Queries
	var tx pgx.Tx
	if workspaceID := h.resolveWorkspaceID(r); workspaceID != "" {
		workspaceUUID, ok = parseUUIDOrBadRequest(w, workspaceID, "workspace id")
		if !ok {
			return
		}
		var err error
		tx, err = h.TxStarter.Begin(r.Context())
		if err != nil {
			slog.Error("failed to begin client usage transaction", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
		defer tx.Rollback(r.Context())
		queries = h.Queries.WithTx(tx)
		// 与「删除 workspace」共用同一把显式锁协议：若删除赢，行已没了，本次上报失败；
		// 若上报赢，删除会等待并在 upsert 提交后清理上下文。
		if _, err := queries.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusForbidden, "workspace not found")
				return
			}
			slog.Error("failed to lock client usage workspace", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
		if _, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: userUUID, WorkspaceID: workspaceUUID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusForbidden, "workspace not found")
				return
			}
			slog.Error("failed to validate client usage workspace", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
	}

	if _, err := queries.UpsertClientUsageDaily(r.Context(), db.UpsertClientUsageDailyParams{
		UserID:        userUUID,
		ClientType:    clientType,
		InstallID:     installID,
		WorkspaceID:   workspaceUUID,
		ClientVersion: clientVersion,
		Os:            clientOS,
	}); err != nil {
		slog.Error("failed to upsert client usage", "error", err, "client_type", clientType)
		writeError(w, http.StatusInternalServerError, "failed to record client usage")
		return
	}
	if tx != nil {
		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("failed to commit client usage", "error", err, "client_type", clientType)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// normalizeClientUsageOS 把客户端上报的 OS 归一化到白名单集合；名单外的（含空值）一律
// 归 "unknown"，保证统计维度有限且干净。
func normalizeClientUsageOS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "macos", "windows", "linux", "ios", "android", "chromeos":
		return value
	default:
		return "unknown"
	}
}
