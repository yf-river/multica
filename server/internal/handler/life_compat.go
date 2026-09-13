// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_compat.go）实为 handler 包内被 life/plugin 等场景共用的几个
// 小型工具：识别客户端已断开的错误、通用 JSON 解码、实体装载失败的错误
// 映射，以及"私有 agent 访问权"的请求级校验。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// writeClientClosedIfCanceled 当错误是 context.Canceled（客户端已断开/取消）
// 时写 499 并返回 true；否则返回 false，让调用方按真实错误处理。
func writeClientClosedIfCanceled(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	writeError(w, 499, "request cancelled")
	return true
}

// decodeRequiredJSON 解码请求体到 dst；body 非法时写 400 并返回 false。
func decodeRequiredJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// writeEntityLoadError 把"装载某实体"的错误翻译成响应：查无此行→404，
// 客户端取消→499，其余记日志并回 500。attrs 用于补充日志上下文。
func writeEntityLoadError(w http.ResponseWriter, err error, entity string, attrs ...any) {
	if writeClientClosedIfCanceled(w, err) {
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, entity+" not found")
		return
	}
	slog.Error("load "+entity+" failed", append(attrs, "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to load "+entity)
}

// requirePersonalAgentAccess 校验调用者对某 agent（可能是私有 agent）有访问权；
// 无权限时写 403（deniedMessage）并返回 false。
func (h *Handler) requirePersonalAgentAccess(w http.ResponseWriter, r *http.Request, agent db.Agent, actorType, actorID, workspaceID, deniedMessage string) bool {
	if h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		return true
	}
	writeError(w, http.StatusForbidden, deniedMessage)
	return false
}
