package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// deliveryOwner 在业务事务内锁定归属，防止旧会话在交接后继续写入。
func deliveryOwner(r *http.Request, tx pgx.Tx, ws, issue string) error {
	token := r.Header.Get("X-Delivery-Token")
	generation, _ := strconv.ParseInt(r.Header.Get("X-Delivery-Generation"), 10, 64)
	if token == "" || generation < 1 {
		return fmt.Errorf("需要先领取交付归属；其他会话可以查询同一交付")
	}
	var valid bool
	err := tx.QueryRow(r.Context(), `SELECT token::text=$3 AND generation=$4 AND expires_at>now() FROM delivery_owner WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, issue, token, generation).Scan(&valid)
	if err != nil || !valid {
		return fmt.Errorf("交付归属已失效，请读取状态并显式交接；禁止重新提交覆盖")
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_owner SET expires_at=now()+interval '60 seconds',updated_at=now() WHERE workspace_id=$1 AND issue_id=$2`, ws, issue)
	return err
}

// ManageDeliveryOwner 只在领取时返回凭据，查询不会暴露写入令牌。
func (h *Handler) ManageDeliveryOwner(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		var raw json.RawMessage
		err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('execution_id',execution_id,'generation',generation,'expires_at',expires_at,'expired',expires_at<=now()) FROM delivery_owner WHERE workspace_id=$1 AND issue_id=$2`, ws, issue).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 200, map[string]any{"state": "unclaimed"})
			return
		}
		if err != nil {
			writeError(w, 500, "读取交付归属失败")
			return
		}
		writeJSON(w, 200, raw)
		return
	}
	var b struct {
		Action     string `json:"action"`
		Execution  string `json:"execution_id"`
		Token      string `json:"token"`
		Generation int64  `json:"generation"`
		Evidence   string `json:"recovery_evidence"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&b) != nil || strings.TrimSpace(b.Execution) == "" || len(b.Execution) > 200 {
		writeError(w, 400, "缺少执行会话身份")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "领取交付归属失败")
		return
	}
	defer tx.Rollback(r.Context())
	// 任务行锁覆盖首次创建，两个客户端不能同时成为负责人。
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "delivery-owner:"+ws+":"+issue); err != nil {
		// 2. 这里是锁失败而不是任务缺失，报 404 会把排障引到错误方向。
		writeError(w, 500, "锁定交付归属失败")
		return
	}
	var old, token string
	var generation int64
	var live bool
	err = tx.QueryRow(r.Context(), `SELECT execution_id,token::text,generation,expires_at>now() FROM delivery_owner WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, issue).Scan(&old, &token, &generation, &live)
	fresh := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !fresh {
		writeError(w, 500, "读取交付归属失败")
		return
	}
	own := !fresh && old == b.Execution && token == b.Token && generation == b.Generation
	if b.Action == "release" {
		if !own {
			writeError(w, 409, "交付归属不匹配")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_owner SET expires_at=now(),generation=generation+1,updated_at=now() WHERE workspace_id=$1 AND issue_id=$2`, ws, issue)
	} else if b.Action == "claim" || b.Action == "heartbeat" || b.Action == "recover" {
		if !fresh && !own {
			if (live && !own) || b.Action != "recover" || strings.TrimSpace(b.Evidence) == "" {
				writeError(w, 409, "已有交付负责人；过期后仍需显式恢复核对，不能自动抢占")
				return
			}
			var running bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_task_queue WHERE id::text=$1 AND status IN ('queued','dispatched','running','waiting_local_directory'))`, old).Scan(&running); err != nil || running {
				writeError(w, 409, "原执行仍在运行，不能交接")
				return
			}
			// 环境变更由外部发布器独立归属，交接 Agent 不会转移或释放发布占用。
			generation++
			token = dbid.NewV7().String()
		}
		if fresh {
			if b.Action != "claim" {
				writeError(w, 409, "交付尚未领取")
				return
			}
			generation = 1
			token = dbid.NewV7().String()
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_owner(workspace_id,issue_id,execution_id,token,generation,expires_at,recovery_evidence) VALUES($1,$2,$3,$4,$5,now()+interval '60 seconds',$6) ON CONFLICT(workspace_id,issue_id) DO UPDATE SET execution_id=EXCLUDED.execution_id,token=EXCLUDED.token,generation=EXCLUDED.generation,expires_at=EXCLUDED.expires_at,recovery_evidence=EXCLUDED.recovery_evidence,updated_at=now()`, ws, issue, b.Execution, token, generation, b.Evidence)
	} else {
		writeError(w, 400, "未知归属操作")
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "保存交付归属失败")
		return
	}
	writeJSON(w, 200, map[string]any{"execution_id": b.Execution, "token": token, "generation": generation, "expires_in": 60})
}

// PublicationLease 在整个环境组内串行最终集成，进程锁之外仍保留可恢复的代次。
func (h *Handler) PublicationLease(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		var raw json.RawMessage
		err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('delivery_id',delivery_id,'generation',generation,'state',state,'expired',COALESCE(expires_at<=now(),false),'snapshot',snapshot) FROM delivery_publication WHERE resource='int-prod'`).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 200, map[string]any{"state": "idle"})
			return
		}
		if err != nil {
			writeError(w, 500, "读取发布占用失败")
			return
		}
		writeJSON(w, 200, raw)
		return
	}
	var b struct {
		Action     string          `json:"action"`
		Delivery   string          `json:"delivery_id"`
		Token      string          `json:"worker_token"`
		Generation int64           `json:"generation"`
		Snapshot   json.RawMessage `json:"snapshot"`
		Evidence   string          `json:"recovery_evidence"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&b) != nil {
		writeError(w, 400, "发布占用格式错误")
		return
	}
	for k, v := range map[string]string{"delivery_id": b.Delivery, "worker_token": b.Token} {
		if _, ok = parseUUIDOrBadRequest(w, v, k); !ok {
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "领取发布占用失败")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO delivery_publication(resource) VALUES('int-prod') ON CONFLICT(resource) DO NOTHING`)
	if err != nil {
		writeError(w, 500, "初始化发布占用失败")
		return
	}
	var owner, token, state string
	var generation int64
	var live bool
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(delivery_id::text,''),COALESCE(worker_token::text,''),generation,state,COALESCE(expires_at>now(),false) FROM delivery_publication WHERE resource='int-prod' FOR UPDATE`).Scan(&owner, &token, &generation, &state, &live)
	if err != nil {
		writeError(w, 500, "读取发布占用失败")
		return
	}
	var valid bool
	err = tx.QueryRow(r.Context(), `SELECT worker_token::text=$3 AND ($4='release' OR ($4 IN('check','heartbeat','resume') AND phase IN('awaiting_review','approved','published','needs_changes','cancelled')) OR phase IN ('testing','preparing_release','publishing')) FROM issue_delivery WHERE workspace_id=$1 AND id=$2`, ws, b.Delivery, b.Token, b.Action).Scan(&valid)
	if err == nil && !valid && b.Action == "claim" {
		// 冻结组合由首个有效成员领取最终占用，仍使用该交付的原凭据和逐任务授权。
		var issue string
		err = tx.QueryRow(r.Context(), `SELECT d.issue_id::text FROM issue_delivery d JOIN delivery_batch_member m ON m.delivery_id=d.id JOIN delivery_batch batch ON batch.id=m.batch_id WHERE d.workspace_id=$1 AND d.id=$2 AND d.worker_token::text=$3 AND d.phase='approved' AND d.approved_by IS NOT NULL AND batch.workspace_id=d.workspace_id AND batch.status IN ('frozen','preparing') AND m.result='pending' AND m.source_commit=d.source_commit AND m.delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=batch.id AND result='pending' ORDER BY created_at,delivery_id LIMIT 1) FOR UPDATE OF d,m,batch`, ws, b.Delivery, b.Token).Scan(&issue)
		if err == nil {
			var planValid bool
			valid, err = deliveryAuthorizationReady(r.Context(), tx, ws, issue, b.Delivery)
			if err == nil && valid {
				err = tx.QueryRow(r.Context(), deliveryPlanValidSQL, b.Delivery).Scan(&planValid)
				valid = planValid
			}
		}
	}
	if err != nil || !valid {
		writeError(w, 409, "交付领取凭据已失效")
		return
	}
	own := owner == b.Delivery && token == b.Token && generation == b.Generation
	switch b.Action {
	case "claim":
		if state != "idle" {
			// 1. 过期占用不再永久挡路：只有原持有交付已不在活动阶段、且调用方给出恢复依据时
			// 才允许跨交付接管，代次继续递增，旧进程持有的凭据随即失效。
			if live || owner == b.Delivery || strings.TrimSpace(b.Evidence) == "" || state != "active" {
				writeError(w, 409, "最终集成正由另一交付使用，查询现有占用而非重复登记")
				return
			}
			var holderActive bool
			if err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT phase IN ('waiting_int','approved','testing','preparing_release','publishing') FROM issue_delivery WHERE workspace_id=$1 AND id=$2),false)`, ws, owner).Scan(&holderActive); err != nil {
				writeError(w, 500, "核对原发布占用失败")
				return
			}
			if holderActive {
				writeError(w, 409, "原发布交付仍在活动中，不能接管")
				return
			}
		}
		generation++
		if len(b.Snapshot) == 0 {
			b.Snapshot = json.RawMessage(`{}`)
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_publication SET workspace_id=$1,delivery_id=$2,worker_token=$3,generation=$4,state='active',expires_at=now()+interval '60 seconds',snapshot=$5,updated_at=now() WHERE resource='int-prod'`, ws, b.Delivery, b.Token, generation, b.Snapshot)
	case "freeze":
		if !own || !live || len(b.Snapshot) == 0 {
			writeError(w, 409, "发布占用不允许固定候选")
			return
		}
		var same bool
		if err = tx.QueryRow(r.Context(), `SELECT NOT(snapshot ? 'base_commit') OR snapshot=$1::jsonb FROM delivery_publication WHERE resource='int-prod'`, b.Snapshot).Scan(&same); err != nil || !same {
			writeError(w, 409, "本次最终集成基线已固定，不能自动换版")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_publication SET snapshot=$1,updated_at=now() WHERE resource='int-prod'`, b.Snapshot)
	case "heartbeat", "check":
		if !own || !live || state != "active" {
			writeError(w, 409, "发布占用已失效，需要恢复核对")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_publication SET expires_at=now()+interval '60 seconds',updated_at=now() WHERE resource='int-prod'`)
	case "resume":
		if owner != b.Delivery || token != b.Token || live || strings.TrimSpace(b.Evidence) == "" {
			writeError(w, 409, "必须核对已退出执行者及环境后才能恢复")
			return
		}
		generation++
		_, err = tx.Exec(r.Context(), `UPDATE delivery_publication SET generation=$1,state='active',expires_at=now()+interval '60 seconds',updated_at=now() WHERE resource='int-prod'`, generation)
	case "release":
		if !own || !live {
			writeError(w, 409, "旧发布器不能释放当前占用")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_publication SET state='idle',delivery_id=NULL,workspace_id=NULL,worker_token=NULL,expires_at=NULL,snapshot='{}',updated_at=now() WHERE resource='int-prod'`)
	default:
		writeError(w, 400, "未知发布占用操作")
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "更新发布占用失败")
		return
	}
	writeJSON(w, 200, map[string]any{"delivery_id": b.Delivery, "worker_token": b.Token, "generation": generation, "expires_in": 60})
}
