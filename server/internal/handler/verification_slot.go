package handler

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"

	"github.com/multica-ai/multica/server/internal/verification"
)

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
