package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/verification"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

var slotSeedSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errSlotBusy = errors.New("槽位正在验收")

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
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil || (body.Enabled == nil && body.Action != "reset" && body.Action != "maintain" && body.Action != "resume-maintenance") {
		writeError(w, 400, "缺少槽位名称或启用状态")
		return
	}
	if body.Name != "verify-1" && body.Name != "verify-2" && body.Name != "verify-3" {
		writeError(w, 400, "未知验收槽位")
		return
	}
	// 修改 1. 单槽位维护：维护只关本槽位接单，正在验收的任务和另外两个槽位都不受影响；
	// 恢复要求槽位已经空闲，避免把一个刚被领取的槽位重新放回调度。
	if body.Action == "reset-quarantine" {
		if strings.TrimSpace(body.Reason) == "" {
			writeError(w, 400, "缺少维护原因")
			return
		}
		if err := h.resetVerificationQuarantine(r, body.Name, user, strings.TrimSpace(body.Reason)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, 404, "槽位尚未登记")
				return
			}
			if err == errSlotBusy {
				writeError(w, 409, "只有被隔离的槽位可以重置")
				return
			}
			writeError(w, 500, "重置验收槽位失败")
			return
		}
		writeJSON(w, 200, map[string]any{"name": body.Name, "state": "idle"})
		return
	}
	if body.Action == "maintain" || body.Action == "resume-maintenance" {
		if strings.TrimSpace(body.Reason) == "" {
			writeError(w, 400, "缺少维护原因")
			return
		}
		action := "maintain"
		if body.Action == "resume-maintenance" {
			action = "resume"
		}
		state, enabled, err := h.setVerificationMaintenance(r, body.Name, action, user, strings.TrimSpace(body.Reason))
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "槽位尚未登记")
			return
		}
		if err == errSlotBusy {
			writeError(w, 409, "槽位正在验收，不能进入或退出维护")
			return
		}
		if err != nil {
			writeError(w, 500, "更新槽位维护状态失败")
			return
		}
		writeJSON(w, 200, map[string]any{"name": body.Name, "enabled": enabled, "state": state, "action": body.Action})
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
		Action            string `json:"action"`
		Detail            string `json:"detail"`
		ReplacementToken  string `json:"replacement_worker_token"`
		ReplacementSource string `json:"replacement_source_commit"`
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
	} else if body.Action == "repair" {
		// 1. Only the external publisher can attest the exact artifact and recover the fenced original lease.
		if _, ok := parseUUIDOrBadRequest(w, body.ReplacementToken, "replacement_worker_token"); !ok {
			return
		}
		if !deliveryCommit.MatchString(body.ReplacementSource) {
			writeError(w, 400, "需要完整修复提交")
			return
		}
		body.Lease, err = store.Repair(r.Context(), body.Lease, body.ReplacementToken, body.ReplacementSource, body.Detail)
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
	// 修改 2. 不带交付编号时返回槽位清单：发布器据此挑选干净槽位，并判断是否需要先恢复种子。
	if id == "" {
		var data json.RawMessage
		err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('name',s.name,'enabled',s.enabled,'state',s.state,'delivery_id',s.delivery_id,'seed_id',COALESCE(x.seed_id,''),'seed_sha256',COALESCE(x.seed_sha256,''),'seed_verified',COALESCE(x.verified,false)) ORDER BY s.name),'[]') FROM verification_slot s LEFT JOIN verification_slot_seed x ON x.name=s.name`).Scan(&data)
		if err != nil {
			writeError(w, 500, "读取验收槽位失败")
			return
		}
		writeJSON(w, 200, data)
		return
	}
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

// setVerificationMaintenance 只调整本槽位的接单开关并留下审计；槽位被占用时拒绝，不打断验收。
func (h *Handler) setVerificationMaintenance(r *http.Request, name, action, actor, reason string) (string, bool, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(r.Context())
	var state string
	var delivery string
	err = tx.QueryRow(r.Context(), `SELECT state,COALESCE(delivery_id::text,'') FROM verification_slot WHERE name=$1 FOR UPDATE`, name).Scan(&state, &delivery)
	if err != nil {
		return "", false, err
	}
	// 修改 3. 维护与恢复都要求槽位空闲：正在验收的槽位不能被重新放回调度，也不能被当成干净数据重置。
	if state != "idle" || delivery != "" {
		return "", false, errSlotBusy
	}
	enabled := action == "resume"
	_, err = tx.Exec(r.Context(), `UPDATE verification_slot SET enabled=$2,updated_at=now() WHERE name=$1`, name, enabled)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO verification_slot_maintenance(id,name,action,actor,reason) VALUES($1,$2,$3,$4,$5)`, dbid.NewV7(), name, action, actor, reason)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	return state, enabled, err
}

// resetVerificationQuarantine 把被隔离的槽位放回空闲调度；只允许 operator 在写明原因后执行，
// 旧的故障信息写入维护审计，隔离记录本身不静默丢弃。
func (h *Handler) resetVerificationQuarantine(r *http.Request, name, actor, reason string) error {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var state, previous string
	if err = tx.QueryRow(r.Context(), `SELECT state,COALESCE(error,'') FROM verification_slot WHERE name=$1 FOR UPDATE`, name).Scan(&state, &previous); err != nil {
		return err
	}
	if state != "quarantined" {
		return errSlotBusy
	}
	if _, err = tx.Exec(r.Context(), `UPDATE verification_slot SET state='idle',enabled=true,delivery_id=NULL,worker_token=NULL,candidate_id='',source_commit='',lease_expires_at=NULL,error='',generation=generation+1,updated_at=now() WHERE name=$1`, name); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO verification_slot_maintenance(id,name,action,actor,reason) VALUES($1,$2,$3,$4,$5)`, dbid.NewV7(), name, "resume", actor, "reset-quarantine："+reason+"；原隔离原因："+previous); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}

// GetVerificationSlotSeed 返回槽位当前种子；没有登记时 404，调用方据此选择完整恢复。
func (h *Handler) GetVerificationSlotSeed(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.deliveryOperator(w, r); !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name != "verify-1" && name != "verify-2" && name != "verify-3" {
		writeError(w, 400, "未知验收槽位")
		return
	}
	var data json.RawMessage
	err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('name',name,'seed_id',seed_id,'seed_sha256',seed_sha256,'verified',verified,'restored_at',restored_at,'updated_at',updated_at) FROM verification_slot_seed WHERE name=$1`, name).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "槽位尚未登记种子")
		return
	}
	if err != nil {
		writeError(w, 500, "读取槽位种子失败")
		return
	}
	writeJSON(w, 200, data)
}

// RecordVerificationSlotSeed 只允许在维护态登记：此时槽位空闲，登记的种子就是恢复后的实际数据版本。
func (h *Handler) RecordVerificationSlotSeed(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     string `json:"name"`
		SeedID   string `json:"seed_id"`
		SHA      string `json:"seed_sha256"`
		Verified bool   `json:"verified"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil || (body.Name != "verify-1" && body.Name != "verify-2" && body.Name != "verify-3") ||
		strings.TrimSpace(body.SeedID) == "" || len(body.SeedID) > 200 || !slotSeedSHA.MatchString(body.SHA) {
		writeError(w, 400, "需要槽位名称、种子编号和校验和")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "登记槽位种子失败")
		return
	}
	defer tx.Rollback(r.Context())
	var state string
	var enabled bool
	err = tx.QueryRow(r.Context(), `SELECT state,enabled FROM verification_slot WHERE name=$1 FOR UPDATE`, body.Name).Scan(&state, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "槽位尚未登记")
		return
	}
	if err != nil {
		writeError(w, 500, "登记槽位种子失败")
		return
	}
	// 修改 4. 只有在维护态、槽位空闲时登记的种子才代表当前数据版本，避免把正在验收的槽位误标成已恢复。
	if state != "idle" || enabled {
		writeError(w, 409, "槽位不在维护态，不能登记种子")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO verification_slot_seed(name,seed_id,seed_sha256,verified) VALUES($1,$2,$3,$4) ON CONFLICT(name) DO UPDATE SET seed_id=EXCLUDED.seed_id,seed_sha256=EXCLUDED.seed_sha256,verified=EXCLUDED.verified,restored_at=now(),updated_at=now()`, body.Name, strings.TrimSpace(body.SeedID), body.SHA, body.Verified)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "登记槽位种子失败")
		return
	}
	writeJSON(w, 200, map[string]any{"name": body.Name, "seed_id": body.SeedID, "verified": body.Verified, "workspace_id": ws})
}
