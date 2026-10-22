package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/verification"
)

// ConfigureVerificationSlot 允许人工管理员启停调度；停用不打断已有验收。
func (h *Handler) ConfigureVerificationSlot(w http.ResponseWriter, r *http.Request) {
	user, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
		Action  string `json:"action"`
		Reason  string `json:"reason"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil || (body.Enabled == nil && body.Action != "reset") {
		writeError(w, 400, "缺少槽位名称或启用状态")
		return
	}
	if body.Name != "verify-1" && body.Name != "verify-2" && body.Name != "verify-3" {
		writeError(w, 400, "未知验收槽位")
		return
	}
	// 1. 收回动作单独走 Reset：隔离态可立即收回，其余状态必须等租约过期；
	// 没有这个入口时，一次验收异常就会永久烧掉一个槽位，只能改库。
	if body.Action == "reset" {
		store := verification.Store{DB: h.TxStarter}
		lease, err := store.Reset(r.Context(), body.Name, user, strings.TrimSpace(body.Reason))
		if errors.Is(err, verification.ErrBusy) {
			writeError(w, 409, "槽位仍在使用中，等租约过期后再收回")
			return
		}
		if errors.Is(err, verification.ErrStale) {
			writeError(w, 404, "槽位不存在")
			return
		}
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": lease.Name, "state": lease.State, "generation": lease.Generation})
		return
	}
	var state string
	err := h.DB.QueryRow(r.Context(), `INSERT INTO verification_slot(name,enabled) VALUES($1,$2) ON CONFLICT(name) DO UPDATE SET enabled=EXCLUDED.enabled,updated_at=now() RETURNING state`, body.Name, *body.Enabled).Scan(&state)
	if err != nil {
		writeError(w, 500, "设置验收槽位失败")
		return
	}
	writeJSON(w, 200, map[string]any{"name": body.Name, "enabled": *body.Enabled, "state": state})
}

// AdvanceVerificationSlot 供外部交付器管理占用；工作区身份来自认证上下文。
func (h *Handler) AdvanceVerificationSlot(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	var body struct {
		verification.Lease
		Action string `json:"action"`
		Detail string `json:"detail"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
		writeError(w, 400, "槽位请求格式错误")
		return
	}
	for key, value := range map[string]string{"delivery_id": body.Delivery, "worker_token": body.Token} {
		if _, ok := parseUUIDOrBadRequest(w, value, key); !ok {
			return
		}
	}
	body.Workspace = ws
	store := verification.Store{DB: h.TxStarter}
	var err error
	if body.Action == "claim" {
		body.Lease, err = store.Claim(r.Context(), ws, body.Delivery, body.Token, body.Source)
	} else if body.Action == "progress" {
		err = store.Progress(r.Context(), body.Lease, body.Detail)
	} else if body.Action == "resume" {
		body.Lease, err = store.Resume(r.Context(), body.Lease)
	} else {
		err = store.Advance(r.Context(), body.Lease, body.Action, body.Detail)
	}
	if errors.Is(err, verification.ErrBusy) || errors.Is(err, verification.ErrStale) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "更新验收槽位失败")
		return
	}
	writeJSON(w, 200, body.Lease)
}

func (h *Handler) GetVerificationLease(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("delivery_id")
	if _, ok := parseUUIDOrBadRequest(w, id, "delivery_id"); !ok {
		return
	}
	var data json.RawMessage
	err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('name',name,'generation',generation,'workspace_id',workspace_id,'delivery_id',delivery_id,'worker_token',worker_token,'source_commit',source_commit,'candidate_id',candidate_id,'state',state) FROM verification_slot WHERE workspace_id=$1 AND delivery_id=$2`, ws, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "交付没有活动槽位")
		return
	}
	if err != nil {
		writeError(w, 500, "读取验收槽位失败")
		return
	}
	writeJSON(w, 200, data)
}
