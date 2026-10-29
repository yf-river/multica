package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 批次状态是发布单元的状态；成员交付只在冻结、发布和失败时改变阶段。
const (
	batchCollecting = "collecting"
	batchFrozen     = "frozen"
	batchPreparing  = "preparing"
	batchValidating = "validating"
	batchPublishing = "publishing"
	batchPublished  = "published"
	batchFailed     = "needs_changes"
	batchCancelled  = "cancelled"
)

const activeBatchStatuses = `('collecting','frozen','preparing','validating','publishing')`

type deliveryBatch struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	WindowSeconds   int       `json:"window_seconds"`
	CollectUntil    time.Time `json:"collect_until"`
	BaseCommit      string    `json:"base_commit"`
	CandidateID     string    `json:"candidate_id"`
	CandidateCommit string    `json:"candidate_commit"`
	WorkerToken     string    `json:"worker_token"`
	Error           string    `json:"error"`
	ProjectID       string    `json:"project_id,omitempty"`
	HoldKind        string    `json:"hold_kind,omitempty"`
}

type deliveryBatchMember struct {
	DeliveryID   string `json:"delivery_id"`
	IssueID      string `json:"issue_id"`
	Number       int64  `json:"number"`
	SourceCommit string `json:"source_commit"`
	Result       string `json:"result"`
	Error        string `json:"error"`
}

const deliveryBatchSelect = `SELECT id::text,status,window_seconds,collect_until,base_commit,candidate_id,candidate_commit,COALESCE(worker_token::text,''),error,COALESCE(project_id::text,''),hold_kind FROM delivery_batch`

func scanDeliveryBatch(row pgx.Row) (deliveryBatch, error) {
	var b deliveryBatch
	err := row.Scan(&b.ID, &b.Status, &b.WindowSeconds, &b.CollectUntil, &b.BaseCommit, &b.CandidateID, &b.CandidateCommit, &b.WorkerToken, &b.Error, &b.ProjectID, &b.HoldKind)
	return b, err
}

func (h *Handler) deliveryBatchMembers(r *http.Request, batch string) ([]deliveryBatchMember, error) {
	members := []deliveryBatchMember{}
	err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('delivery_id',m.delivery_id,'issue_id',m.issue_id,'number',i.number,'source_commit',m.source_commit,'result',m.result,'error',m.error) ORDER BY m.created_at,m.delivery_id),'[]') FROM delivery_batch_member m JOIN issue i ON i.id=m.issue_id WHERE m.batch_id=$1`, batch).Scan(&members)
	return members, err
}

// DeliveryBatch 是选择性组合发布的唯一入口：收集、冻结、登记候选、发布和失败回填都在这里。
func (h *Handler) DeliveryBatch(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		batch, err := scanDeliveryBatch(h.DB.QueryRow(r.Context(), deliveryBatchSelect+` WHERE workspace_id=$1 AND status IN `+activeBatchStatuses, ws))
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 200, map[string]any{"batch": nil, "members": []deliveryBatchMember{}})
			return
		}
		if err != nil {
			writeError(w, 500, "读取发布批次失败")
			return
		}
		members, err := h.deliveryBatchMembers(r, batch.ID)
		if err != nil {
			writeError(w, 500, "读取批次成员失败")
			return
		}
		writeJSON(w, 200, map[string]any{"batch": batch, "members": members})
		return
	}
	var body struct {
		Action           string `json:"action"`
		BatchID          string `json:"batch_id"`
		WindowSeconds    int    `json:"window_seconds"`
		ProjectID        string `json:"project_id"`
		BaseCommit       string `json:"base_commit"`
		CandidateID      string `json:"candidate_id"`
		CandidateCommit  string `json:"candidate_commit"`
		CandidateURL     string `json:"candidate_url"`
		WorkerToken      string `json:"worker_token"`
		Error            string `json:"error"`
		ConflictDelivery string `json:"conflict_delivery_id"`
		Delivery         string `json:"delivery_id"`
		Stage            string `json:"stage"`
		Automatic        bool   `json:"automatic"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil {
		writeError(w, 400, "批次请求格式错误")
		return
	}
	if body.Action == "open" {
		if body.ProjectID != "" {
			if _, ok = parseUUIDOrBadRequest(w, body.ProjectID, "project_id"); !ok {
				return
			}
		}
		h.openDeliveryBatch(w, r, ws, body.WindowSeconds, body.ProjectID)
		return
	}
	if _, ok = parseUUIDOrBadRequest(w, body.BatchID, "batch_id"); !ok {
		return
	}
	if body.Action != "freeze" {
		if _, ok = parseUUIDOrBadRequest(w, body.WorkerToken, "worker_token"); !ok {
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "更新发布批次失败")
		return
	}
	defer tx.Rollback(r.Context())
	var issueEvents []events.Event
	var current deliveryBatch
	current, err = scanDeliveryBatch(tx.QueryRow(r.Context(), deliveryBatchSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, body.BatchID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "发布批次不存在")
		return
	}
	if err != nil {
		writeError(w, 500, "更新发布批次失败")
		return
	}
	// 修改 2. 冻结是批次流程的起点，此时还没有领取凭据；其余操作都必须带同一凭据，
	// 重启或重复请求不会让第二个发布器推进同一批。
	if body.Action != "freeze" && (body.WorkerToken == "" || body.WorkerToken != current.WorkerToken) {
		writeError(w, 409, "发布批次领取凭据已失效")
		return
	}
	// 修改 1. 已发布批次只回执当前状态：重启和重复请求不能产生第二次发布或改回早期阶段。
	if current.Status == batchPublished {
		writeJSON(w, 200, map[string]any{"batch": current, "members": []deliveryBatchMember{}})
		return
	}
	if body.Action == "dispatch-agent" {
		if current.ProjectID == "" || (body.Stage != "batch" && body.Stage != "conflict" && body.Stage != "integration") || (body.Stage == "batch" && (current.Status != batchFrozen || current.Error != "")) || (body.Stage == "conflict" && (current.Status != batchFrozen || current.Error == "")) || (body.Stage == "integration" && current.Status != batchValidating) {
			writeError(w, 409, "批次当前阶段不能派发发布 Agent")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "读取发布批次失败")
			return
		}
		if err = h.dispatchReleaseAgent(r.Context(), ws, current, body.Stage); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"batch": current})
		return
	}
	var demoted []string
	switch body.Action {
	case "freeze":
		current, demoted, err = h.freezeDeliveryBatch(r, tx, ws, current)
	case "hold":
		if current.ProjectID == "" || current.Status != batchFrozen || strings.TrimSpace(body.Error) == "" {
			err = fmt.Errorf("需要待准备的项目批次和具体原因")
			break
		}
		if current.Error != "" {
			break
		}
		if body.ConflictDelivery != "" {
			var member bool
			if _, parseErr := util.ParseUUID(body.ConflictDelivery); parseErr != nil {
				err = fmt.Errorf("冲突交付编号无效")
				break
			}
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM delivery_batch_member WHERE batch_id=$1 AND delivery_id=$2 AND result='pending')`, current.ID, body.ConflictDelivery).Scan(&member)
			if err != nil || !member {
				err = fmt.Errorf("冲突交付不属于当前批次")
				break
			}
			if _, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET error=$3 WHERE batch_id=$1 AND delivery_id=$2`, current.ID, body.ConflictDelivery, body.Error); err != nil {
				break
			}
			if _, err = tx.Exec(r.Context(), `INSERT INTO verification_event(workspace_id,delivery_id,stage,detail) VALUES($1,$2,'delivery.batch_conflict',jsonb_build_object('batch_id',$3::text,'reason',$4::text)::text)`, ws, body.ConflictDelivery, current.ID, body.Error); err != nil {
				break
			}
		}
		if body.ConflictDelivery == "" {
			if _, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET error=$2 WHERE batch_id=$1 AND delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at,delivery_id LIMIT 1)`, current.ID, body.Error); err != nil {
				break
			}
		}
		kind := "unready"
		if body.ConflictDelivery != "" {
			kind = "merge_conflict"
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET error=$2,hold_kind=$3,updated_at=now() WHERE id=$1`, current.ID, body.Error, kind)
		if err == nil {
			err = ensureReleaseAgentNotice(r, tx, current.ID, "conflict", body.ConflictDelivery)
		}
	case "retry":
		if current.ProjectID == "" || current.Status != batchFrozen || current.Error == "" {
			err = fmt.Errorf("批次没有待解决的问题")
			break
		}
		requireCommit := body.Automatic && current.HoldKind == "merge_conflict"
		err = h.retryHeldDeliveryBatch(r, tx, ws, current, true, requireCommit)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET error='' WHERE batch_id=$1`, current.ID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET error='',hold_kind='',updated_at=now() WHERE id=$1`, current.ID)
		}
	case "preparing":
		err = h.prepareDeliveryBatch(r, tx, ws, &current, body.BaseCommit)
	case "validating":
		err = h.validateDeliveryBatch(r, tx, ws, &current, body.CandidateID, body.CandidateCommit, body.CandidateURL)
		if err == nil && current.ProjectID != "" && current.Status == batchPreparing {
			var fixed bool
			err = tx.QueryRow(r.Context(), `SELECT status='validating' AND candidate_id=$2 AND candidate_commit=$3 FROM delivery_batch WHERE id=$1`, current.ID, body.CandidateID, body.CandidateCommit).Scan(&fixed)
			if err == nil && fixed {
				issueEvents, err = h.setBatchIntegrationResult(r, tx, current, false)
			}
		}
	case "publishing":
		if current.Status != batchValidating && current.Status != batchPublishing {
			err = fmt.Errorf("批次不在待发布阶段")
			break
		}
		if current.ProjectID != "" && current.Status == batchValidating {
			var held bool
			held, err = h.holdInvalidProjectBatch(r, tx, ws, current, true)
			if err != nil || held {
				break
			}
		}
		// 修改 5. 候选固定后成员撤权，已构建的组合仍包含该成员代码；整批停下按剩余成员重建，不做局部替换。
		var withdrawn []string
		memberPhase := "approved"
		if current.Status == batchPublishing {
			memberPhase = "publishing"
		}
		if _, withdrawn, err = h.withdrawInvalidMembers(r, tx, ws, current.ID, memberPhase); err == nil {
			if len(withdrawn) > 0 {
				err = h.stopStaleBatch(r, tx, current.ID, staleBatchReason)
			}
		}
		if err == nil && len(withdrawn) == 0 {
			var pending int
			if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1 AND result='pending'`, current.ID).Scan(&pending); err == nil {
				// 修改 9. 组合候选必须有成员在固定组合上提交的实际验收证据；没有证据就停下，
				// 不能用批次状态直接宣布已验收后发布。
				var verified bool
				if pending > 0 {
					err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at,delivery_id LIMIT 1) AND e.candidate_id=$2::text AND e.source_commit=$3::text)`, current.ID, current.CandidateID, current.CandidateCommit).Scan(&verified)
				}
				if err == nil && pending > 0 && !verified {
					err = fmt.Errorf("组合候选缺少成员实际验收证据")
				}
				if err == nil && pending > 0 {
					var representative string
					err = tx.QueryRow(r.Context(), `SELECT delivery_id::text FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at,delivery_id LIMIT 1`, current.ID).Scan(&representative)
					if err == nil {
						err = requireStoredDeliveryChecks(r.Context(), tx, representative, "integration")
					}
				}
				if err == nil && pending > 0 && current.ProjectID != "" {
					err = requireProjectBatchIntegration(r, tx, current)
				}
				if err == nil && pending == 0 {
					_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='cancelled',error='没有有效授权的成员',updated_at=now() WHERE id=$1`, current.ID)
				} else if err == nil {
					// 组合候选已在 int 通过验收：成员进入发布阶段，同一份产物随后切换到生产。
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery d SET phase='publishing',candidate_id=$2::text,release_validated=true,error='',revision=revision+1,updated_at=now() FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.delivery_id=d.id AND m.result='pending' AND d.phase='approved'`, current.ID, current.CandidateID)
					if err == nil && current.ProjectID != "" && current.Status == batchValidating {
						issueEvents, err = h.setBatchIntegrationResult(r, tx, current, true)
					}
					if err == nil {
						_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='publishing',updated_at=now() WHERE id=$1`, current.ID)
					}
				}
			}
		}
	case "abort":
		if strings.TrimSpace(body.Error) == "" || (current.Status != batchPreparing && current.Status != batchValidating && current.Status != batchPublishing) {
			err = fmt.Errorf("需要活动批次及具体停止原因")
			break
		}
		err = h.stopStaleBatch(r, tx, current.ID, body.Error)
	case "published":
		err = h.publishDeliveryBatch(r, tx, current, body.CandidateCommit, &demoted)
	case "fail":
		if strings.TrimSpace(body.Error) == "" {
			err = fmt.Errorf("需要具体失败原因")
			break
		}
		err = h.failDeliveryBatch(r, tx, ws, current, body.Error, body.ConflictDelivery, &demoted)
	case "withdraw":
		if _, ok = parseUUIDOrBadRequest(w, body.Delivery, "delivery_id"); !ok {
			return
		}
		err = h.withdrawMember(r, tx, ws, current.ID, body.Delivery, "授权已失效")
		// 修改 6. 候选已固定时单独撤权同样会让旧组合失真，批次整体停下，剩余成员由下一批重建。
		if err == nil && current.Status != batchCollecting && current.Status != batchFrozen {
			err = h.stopStaleBatch(r, tx, current.ID, staleBatchReason)
		}
	default:
		writeError(w, 400, "未知批次操作")
		return
	}
	if err != nil {
		if errors.Is(err, errSlotBusy) {
			writeError(w, 409, err.Error())
			return
		}
		writeError(w, 409, err.Error())
		return
	}
	if commitErr := tx.Commit(r.Context()); commitErr != nil {
		writeError(w, 500, "更新发布批次失败")
		return
	}
	for _, event := range issueEvents {
		h.publishEvent(event)
	}
	for _, item := range demoted {
		_, _ = h.DB.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_pending}','true'::jsonb),approved_by=NULL,approved_at=NULL,auto_release=false,release_validated=false,revision=revision+1 WHERE id=$1 AND phase='awaiting_review'`, item)
	}
	batch, err := scanDeliveryBatch(h.DB.QueryRow(r.Context(), deliveryBatchSelect+` WHERE id=$1`, body.BatchID))
	if err != nil {
		writeError(w, 500, "读取发布批次失败")
		return
	}
	members, err := h.deliveryBatchMembers(r, batch.ID)
	if err != nil {
		writeError(w, 500, "读取批次成员失败")
		return
	}
	writeJSON(w, 200, map[string]any{"batch": batch, "members": members})
}

func requireProjectBatchIntegration(r *http.Request, tx pgx.Tx, batch deliveryBatch) error {
	rows, err := tx.Query(r.Context(), `SELECT d.id::text,d.plan_snapshot,EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=d.id AND e.issue_id=d.issue_id AND e.workspace_id=d.workspace_id AND e.candidate_id=$2 AND e.source_commit=$3) FROM delivery_batch_member m JOIN issue_delivery d ON d.id=m.delivery_id WHERE m.batch_id=$1 AND m.result='pending' ORDER BY m.delivery_id`, batch.ID, batch.CandidateID, batch.CandidateCommit)
	if err != nil {
		return err
	}
	type entry struct {
		id       string
		snapshot json.RawMessage
		recorded bool
	}
	var entries []entry
	for rows.Next() {
		var item entry
		if err = rows.Scan(&item.id, &item.snapshot, &item.recorded); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range entries {
		checks, err := deliveryRequiredChecks(item.snapshot)
		if err != nil {
			return err
		}
		needsIntegration := false
		for _, check := range checks {
			if check.Stage == "integration" {
				needsIntegration = true
				break
			}
		}
		if !needsIntegration {
			continue
		}
		if !item.recorded {
			return fmt.Errorf("交付 %s 缺少当前组合的联调证据", item.id)
		}
		if err = requireStoredDeliveryChecks(r.Context(), tx, item.id, "integration"); err != nil {
			return err
		}
	}
	return nil
}

// setBatchIntegrationResult binds the checkmark to the current accepted candidate.
func (h *Handler) setBatchIntegrationResult(r *http.Request, tx pgx.Tx, batch deliveryBatch, passed bool) ([]events.Event, error) {
	rows, err := tx.Query(r.Context(), `SELECT issue_id::text FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY issue_id`, batch.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	q := h.Queries.WithTx(tx)
	var result []events.Event
	for _, id := range ids {
		previous, err := q.GetIssue(r.Context(), parseUUID(id))
		if err != nil {
			return nil, err
		}
		commit := ""
		if passed {
			commit = batch.CandidateCommit
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue SET metadata=metadata||jsonb_build_object('integration_passed',$2::boolean,'integration_commit',$3::text),revision=revision+1,updated_at=now() WHERE id=$1`, id, passed, commit)
		if err != nil {
			return nil, err
		}
		updated, err := q.GetIssue(r.Context(), parseUUID(id))
		if err != nil {
			return nil, err
		}
		event, err := service.RecordDurableEventTx(r.Context(), q, buildIssueUpdatedEvent(r.Context(), h, updated, previous, UpdateIssueRequest{}, nil, "system", ""))
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, nil
}

// retryHeldDeliveryBatch 在整批保留的前提下接收已重新审查和测试的成员提交。
func (h *Handler) retryHeldDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current deliveryBatch, allowNewCommit, requireNewCommit bool) error {
	rows, err := tx.Query(r.Context(), `SELECT m.delivery_id::text,m.issue_id::text,m.source_commit,d.source_commit,m.result,d.phase FROM delivery_batch_member m JOIN issue_delivery d ON d.id=m.delivery_id AND d.workspace_id=$2 WHERE m.batch_id=$1 ORDER BY m.delivery_id FOR UPDATE OF d,m`, current.ID, ws)
	if err != nil {
		return err
	}
	type member struct{ delivery, issue, fixed, source, result, phase string }
	var members []member
	for rows.Next() {
		var item member
		if err = rows.Scan(&item.delivery, &item.issue, &item.fixed, &item.source, &item.result, &item.phase); err != nil {
			rows.Close()
			return err
		}
		members = append(members, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(members) == 0 {
		return fmt.Errorf("批次没有成员")
	}
	changed := false
	for _, item := range members {
		if item.result != "pending" || item.phase != "approved" {
			return fmt.Errorf("批次成员尚未完成修复与验收")
		}
		ready, checkErr := deliveryAuthorizationReady(r.Context(), tx, ws, item.issue, item.delivery)
		var planValid bool
		if checkErr == nil {
			checkErr = tx.QueryRow(r.Context(), deliveryPlanValidSQL, item.delivery).Scan(&planValid)
		}
		if checkErr != nil {
			return checkErr
		}
		if !ready || !planValid {
			return fmt.Errorf("批次成员的方案或授权已变化")
		}
		if item.source == item.fixed {
			continue
		}
		if !allowNewCommit {
			return fmt.Errorf("批次成员提交已变化，需要重新核对")
		}
		changed = true
		var verified bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_review r WHERE r.issue_id=$1 AND r.workspace_id=$2 AND r.state='passed' AND r.head_sha=$3) AND EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=$4 AND e.workspace_id=$2 AND e.issue_id=$1 AND e.source_commit=$3 AND e.candidate_id<>'')`, item.issue, ws, item.source, item.delivery).Scan(&verified)
		if err != nil {
			return err
		}
		if !verified {
			return fmt.Errorf("新提交缺少同版本审查或测试证据")
		}
		if err = requireStoredDeliveryChecks(r.Context(), tx, item.delivery, "verification"); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET source_commit=$3,error='' WHERE batch_id=$1 AND delivery_id=$2`, current.ID, item.delivery, item.source); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO verification_event(workspace_id,delivery_id,stage,detail) VALUES($1,$2,'delivery.batch_source_replaced',jsonb_build_object('batch_id',$3::text,'previous_commit',$4::text,'source_commit',$5::text)::text)`, ws, item.delivery, current.ID, item.fixed, item.source); err != nil {
			return err
		}
	}
	if requireNewCommit && !changed {
		return fmt.Errorf("批次仍在等待修复后的新提交")
	}
	return nil
}

func ternaryStage(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func (h *Handler) openDeliveryBatch(w http.ResponseWriter, r *http.Request, ws string, window int, projectID string) {
	if window <= 0 {
		window = 60
	}
	if window > 600 {
		window = 600
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "创建发布批次失败")
		return
	}
	defer tx.Rollback(r.Context())
	existing, err := scanDeliveryBatch(tx.QueryRow(r.Context(), deliveryBatchSelect+` WHERE workspace_id=$1 AND status IN `+activeBatchStatuses, ws))
	if err == nil {
		_ = tx.Commit(r.Context())
		writeJSON(w, 200, map[string]any{"batch": existing, "members": []deliveryBatchMember{}})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "创建发布批次失败")
		return
	}
	start := time.Now()
	if projectID != "" {
		var first *time.Time
		err = tx.QueryRow(r.Context(), `SELECT min(d.approved_at) FROM issue_delivery d JOIN issue i ON i.id=d.issue_id AND i.workspace_id=d.workspace_id WHERE d.workspace_id=$1 AND i.project_id=$2 AND d.phase='approved' AND (d.approved_by IS NOT NULL OR d.auto_release)`, ws, projectID).Scan(&first)
		if err != nil || first == nil {
			writeError(w, 409, "项目没有可组批的交付")
			return
		}
		start = *first
	}
	batch := deliveryBatch{ID: dbid.NewV7().String(), Status: batchCollecting, WindowSeconds: window, CollectUntil: start.Add(time.Duration(window) * time.Second), ProjectID: projectID}
	_, err = tx.Exec(r.Context(), `INSERT INTO delivery_batch(id,workspace_id,status,window_seconds,collect_until,project_id) VALUES($1,$2,'collecting',$3,$4,NULLIF($5,'')::uuid)`, batch.ID, ws, window, batch.CollectUntil, projectID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "delivery_batch_active" {
			_ = tx.Rollback(r.Context())
			existing, readErr := scanDeliveryBatch(h.DB.QueryRow(r.Context(), deliveryBatchSelect+` WHERE workspace_id=$1 AND status IN `+activeBatchStatuses, ws))
			if readErr == nil {
				writeJSON(w, 200, map[string]any{"batch": existing, "members": []deliveryBatchMember{}})
				return
			}
		}
		writeError(w, 500, "创建发布批次失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "创建发布批次失败")
		return
	}
	writeJSON(w, 200, map[string]any{"batch": batch, "members": []deliveryBatchMember{}})
}

// freezeDeliveryBatch 把当前已批准、授权仍有效的交付固定成这一批成员；新批准的任务进入下一批。
func (h *Handler) freezeDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current deliveryBatch) (deliveryBatch, []string, error) {
	if current.Status != batchCollecting {
		return current, nil, nil
	}
	if current.ProjectID != "" {
		var elapsed bool
		if err := tx.QueryRow(r.Context(), `SELECT now()>=$1`, current.CollectUntil).Scan(&elapsed); err != nil {
			return current, nil, err
		}
		if !elapsed {
			return current, nil, fmt.Errorf("发布窗口尚未结束")
		}
	}
	rows, err := tx.Query(r.Context(), `SELECT d.id::text,d.issue_id::text,d.source_commit FROM issue_delivery d JOIN issue i ON i.id=d.issue_id AND i.workspace_id=d.workspace_id WHERE d.workspace_id=$1 AND d.phase='approved' AND (d.approved_by IS NOT NULL OR d.auto_release) AND (NULLIF($2,'')::uuid IS NULL OR i.project_id=NULLIF($2,'')::uuid) AND NOT EXISTS(SELECT 1 FROM delivery_batch_member m JOIN delivery_batch b ON b.id=m.batch_id WHERE m.delivery_id=d.id AND b.status IN `+activeBatchStatuses+`) ORDER BY d.created_at,d.id LIMIT CASE WHEN $2='' THEN 8 ELSE NULL END FOR UPDATE OF d`, ws, current.ProjectID)
	if err != nil {
		return current, nil, err
	}
	type candidate struct{ delivery, issue, commit string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.delivery, &item.issue, &item.commit); err != nil {
			rows.Close()
			return current, nil, err
		}
		candidates = append(candidates, item)
	}
	rows.Close()
	var demoted []string
	token := dbid.NewV7().String()
	for _, item := range candidates {
		ready, checkErr := deliveryAuthorizationReady(r.Context(), tx, ws, item.issue, item.delivery)
		var planValid bool
		if checkErr == nil {
			checkErr = tx.QueryRow(r.Context(), deliveryPlanValidSQL, item.delivery).Scan(&planValid)
		}
		if checkErr != nil {
			return current, nil, checkErr
		}
		if (!ready || !planValid) && current.ProjectID == "" {
			// 修改 2. 授权或方案失效的交付不进入本批，退回待核对，不带着旧授权发布。
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='awaiting_review',approved_by=NULL,approved_at=NULL,auto_release=false,release_validated=false,error='发布授权或方案已变化，等待负责人核对',revision=revision+1,updated_at=now() WHERE id=$1 AND phase='approved'`, item.delivery)
			if err != nil {
				return current, nil, err
			}
			demoted = append(demoted, item.delivery)
			continue
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_batch_member(batch_id,delivery_id,issue_id,source_commit) VALUES($1,$2,$3,$4) ON CONFLICT(batch_id,delivery_id) DO NOTHING`, current.ID, item.delivery, item.issue, item.commit)
		if err != nil {
			return current, nil, err
		}
	}
	var members int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1`, current.ID).Scan(&members); err != nil {
		return current, nil, err
	}
	status := batchFrozen
	errorText := ""
	if members == 0 {
		status = batchCancelled
		errorText = "窗口内没有可组批的交付"
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status=$2::text,worker_token=$3,error=$4::text,updated_at=now() WHERE id=$1`, current.ID, status, token, errorText)
	if err != nil {
		return current, nil, err
	}
	current.Status = status
	current.WorkerToken = token
	current.Error = errorText
	if current.ProjectID != "" && status == batchFrozen {
		if err = ensureReleaseAgentNotice(r, tx, current.ID, "batch", ""); err != nil {
			return current, nil, err
		}
	}
	return current, demoted, nil
}

func ensureReleaseAgentNotice(r *http.Request, tx pgx.Tx, batch, stage, delivery string) error {
	_, err := tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id,delivered_at,created_at) SELECT n.delivery_id,'release_agent:history:'||n.comment_id::text,n.comment_id,n.delivered_at,n.created_at FROM delivery_notice n JOIN delivery_batch_member m ON m.delivery_id=n.delivery_id WHERE m.batch_id=$1::uuid AND n.stage=$2::text||':'||$1::uuid::text ON CONFLICT(delivery_id,stage) DO NOTHING`, batch, "release_agent:"+stage)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) SELECT m.delivery_id,$2::text||':'||$1::uuid::text,$3 FROM delivery_batch_member m WHERE m.batch_id=$1::uuid AND m.result='pending' AND (NULLIF($4,'')::uuid IS NULL OR m.delivery_id=NULLIF($4,'')::uuid) ORDER BY m.created_at,m.delivery_id LIMIT 1 ON CONFLICT(delivery_id,stage) DO UPDATE SET comment_id=EXCLUDED.comment_id,delivered_at=NULL`, batch, "release_agent:"+stage, dbid.NewV7(), delivery)
	return err
}

// 修改 3. 构建开始前再核对一次授权：构建用冻结后的成员列表，失效成员必须先退出本批，
// 否则候选会带上已撤权代码；重复请求按幂等处理，构建中断后可直接续接。
func (h *Handler) prepareDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current *deliveryBatch, baseCommit string) error {
	if current.Status == batchPreparing {
		return nil
	}
	if current.Status != batchFrozen {
		return fmt.Errorf("批次不在准备阶段")
	}
	if current.ProjectID != "" {
		held, err := h.holdInvalidProjectBatch(r, tx, ws, *current, false)
		if err != nil || held {
			return err
		}
	}
	remaining, _, err := h.withdrawInvalidMembers(r, tx, ws, current.ID, "approved")
	if err != nil {
		return err
	}
	if remaining == 0 {
		_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='cancelled',error='批次成员授权全部失效',updated_at=now() WHERE id=$1 AND status='frozen'`, current.ID)
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET base_commit=$2::text,status='preparing',updated_at=now() WHERE id=$1 AND status='frozen'`, current.ID, baseCommit)
	return err
}

// holdInvalidProjectBatch 保留固定成员，候选已绑定时清除候选并等待重新验收。
func (h *Handler) holdInvalidProjectBatch(r *http.Request, tx pgx.Tx, ws string, current deliveryBatch, resetCandidate bool) (bool, error) {
	if _, err := tx.Exec(r.Context(), `SAVEPOINT batch_members`); err != nil {
		return false, err
	}
	checkErr := h.retryHeldDeliveryBatch(r, tx, ws, current, false, false)
	if checkErr == nil {
		_, err := tx.Exec(r.Context(), `RELEASE SAVEPOINT batch_members`)
		return false, err
	}
	if _, err := tx.Exec(r.Context(), `ROLLBACK TO SAVEPOINT batch_members`); err != nil {
		return false, err
	}
	if resetCandidate {
		if err := h.resetPendingBatchMembers(r, tx, current.ID, checkErr.Error()); err != nil {
			return false, err
		}
	}
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch SET status='frozen',candidate_id='',candidate_commit='',error=$2,hold_kind='unready',updated_at=now() WHERE id=$1`, current.ID, checkErr.Error())
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET error=$2 WHERE batch_id=$1 AND delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at,delivery_id LIMIT 1)`, current.ID, checkErr.Error())
	}
	if err == nil {
		err = ensureReleaseAgentNotice(r, tx, current.ID, "conflict", "")
	}
	return err == nil, err
}

// 修改 4. 候选已经按冻结成员构建完成；此后成员授权失效时旧组合不再代表已授权范围，
// 整批停止并由下一批按剩余成员重新构建和验收，禁止剩余成员继续发布旧组合。
const staleBatchReason = "成员交付阶段或发布授权已变化，已固定候选包含撤权成员代码或其它失效成员代码，整批停止并按剩余成员重新组批"

func (h *Handler) validateDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current *deliveryBatch, candidate, commit, url string) error {
	if current.Status == batchValidating {
		if current.CandidateID != candidate || current.CandidateCommit != commit {
			return fmt.Errorf("组合候选已固定，不能替换")
		}
		return nil
	}
	if current.Status != batchPreparing {
		return fmt.Errorf("批次不在验收阶段")
	}
	if strings.TrimSpace(candidate) == "" || !deliveryCommit.MatchString(commit) {
		return fmt.Errorf("需要准确组合候选及完整提交")
	}
	if current.ProjectID != "" {
		held, err := h.holdInvalidProjectBatch(r, tx, ws, *current, false)
		if err != nil || held {
			return err
		}
	}
	_, withdrawn, err := h.withdrawInvalidMembers(r, tx, ws, current.ID, "approved")
	if err != nil {
		return err
	}
	if len(withdrawn) > 0 {
		return h.stopStaleBatch(r, tx, current.ID, staleBatchReason)
	}
	// 修改 7. 固定组合就是成员这一轮要验收的候选：把候选归属和授权复核里的候选同步到同一份组合，
	// 成员才能通过正式验收接口提交证据，后续撤权复核也不会把同一组合误判成授权失效。
	// 组合重绑保留成员已提交的验收：批候选包含该成员的提交，批验收直接复用成员证据，
	// 不再要求人工对批候选重新 accept 一次。
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery d SET candidate_id=$2::text,candidate_url=$3::text,acceptance_submitted=d.acceptance_submitted,plan_snapshot=jsonb_set(jsonb_set(jsonb_set(jsonb_set(COALESCE(d.plan_snapshot,'{}'::jsonb),'{batch_previous_candidate}',to_jsonb(d.candidate_id)),'{batch_previous_review}',COALESCE(d.plan_snapshot->'authorization_review','{}'::jsonb)),'{candidate_commit}',to_jsonb($4::text)),'{authorization_review}',CASE WHEN d.plan_snapshot ? 'authorization_review' THEN jsonb_set(d.plan_snapshot->'authorization_review','{candidate_id}',to_jsonb($2::text)) ELSE '{}'::jsonb END),revision=revision+1,updated_at=now() FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.delivery_id=d.id AND m.result='pending' AND d.phase='approved'`, current.ID, candidate, url, commit)
	if err != nil {
		return err
	}
	// 修改 8. 组合就绪通知要有持久通知号，否则成员侧的 notify-result 一定失败，批次也停在验收阶段。
	rows, err := tx.Query(r.Context(), `SELECT m.delivery_id::text FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.result='pending'`, current.ID)
	if err != nil {
		return err
	}
	var notice []string
	for rows.Next() {
		var delivery string
		if err = rows.Scan(&delivery); err != nil {
			rows.Close()
			return err
		}
		notice = append(notice, delivery)
	}
	rows.Close()
	for _, delivery := range notice {
		// 保存每轮平台通知的真实来源，后续批次替换当前通知号时历史评论仍可准确识别。
		if _, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id,delivered_at,created_at) SELECT delivery_id,'batch_ready_history:'||comment_id::text,comment_id,delivered_at,created_at FROM delivery_notice WHERE delivery_id=$1 AND stage='batch_ready' ON CONFLICT(delivery_id,stage) DO NOTHING`, delivery); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,'batch_ready',$2) ON CONFLICT(delivery_id,stage) DO UPDATE SET comment_id=EXCLUDED.comment_id,delivered_at=NULL`, delivery, dbid.NewV7()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET candidate_id=$2::text,candidate_commit=$3::text,status='validating',updated_at=now() WHERE id=$1 AND status='preparing'`, current.ID, candidate, commit)
	if err == nil && current.ProjectID != "" {
		err = ensureReleaseAgentNotice(r, tx, current.ID, "integration", "")
	}
	return err
}

func (h *Handler) stopStaleBatch(r *http.Request, tx pgx.Tx, batch, reason string) error {
	if err := h.resetPendingBatchMembers(r, tx, batch, reason); err != nil {
		return err
	}
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch SET status='cancelled',error=$2::text,updated_at=now() WHERE id=$1 AND status IN ('preparing','validating','publishing')`, batch, reason)
	return err
}

// 候选作废后恢复成员原批准候选；新批次重新领取，不复用旧组合验收。
func (h *Handler) resetPendingBatchMembers(r *http.Request, tx pgx.Tx, batch, reason string) error {
	_, err := tx.Exec(r.Context(), `UPDATE issue_delivery d SET phase='approved',release_validated=false,acceptance_submitted=false,candidate_url='',candidate_id=COALESCE(NULLIF(d.plan_snapshot->>'batch_previous_candidate',''),d.candidate_id),plan_snapshot=CASE WHEN d.plan_snapshot ? 'batch_previous_review' THEN jsonb_set(d.plan_snapshot,'{authorization_review}',d.plan_snapshot->'batch_previous_review') ELSE d.plan_snapshot END,error=$2::text,revision=revision+1,updated_at=now() FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.delivery_id=d.id AND m.result='pending' AND d.phase IN ('approved','preparing_release','publishing')`, batch, reason)
	return err
}

// withdrawInvalidMembers 在候选固定后、发布前逐一复核：撤回授权的成员退出本批，返回剩余可发布成员数。
func (h *Handler) withdrawInvalidMembers(r *http.Request, tx pgx.Tx, ws, batch, expectedPhase string) (int, []string, error) {
	rows, err := tx.Query(r.Context(), `SELECT m.delivery_id::text,m.issue_id::text,d.phase FROM delivery_batch_member m JOIN issue_delivery d ON d.id=m.delivery_id WHERE m.batch_id=$1 AND m.result='pending' AND d.workspace_id=$2 ORDER BY m.created_at,m.delivery_id FOR UPDATE OF d`, batch, ws)
	if err != nil {
		return 0, nil, err
	}
	type member struct{ delivery, issue, phase string }
	var members []member
	for rows.Next() {
		var item member
		if err = rows.Scan(&item.delivery, &item.issue, &item.phase); err != nil {
			rows.Close()
			return 0, nil, err
		}
		members = append(members, item)
	}
	rows.Close()
	withdrawn := []string{}
	for _, item := range members {
		// 真实成员行锁覆盖授权检查到晋级，取消事务先提交时必须退出本批。
		eligible := item.phase == expectedPhase
		ready, planValid := false, false
		if eligible {
			var checkErr error
			ready, checkErr = deliveryAuthorizationReady(r.Context(), tx, ws, item.issue, item.delivery)
			if checkErr == nil {
				checkErr = tx.QueryRow(r.Context(), deliveryPlanValidSQL, item.delivery).Scan(&planValid)
			}
			if checkErr != nil {
				return 0, nil, checkErr
			}
		}
		if ready && planValid {
			continue
		}
		if err = h.withdrawMember(r, tx, ws, batch, item.delivery, "交付阶段或发布授权已变化，退出本批"); err != nil {
			return 0, nil, err
		}
		withdrawn = append(withdrawn, item.delivery)
	}
	return len(members) - len(withdrawn), withdrawn, nil
}

func (h *Handler) withdrawMember(r *http.Request, tx pgx.Tx, ws, batch, delivery, reason string) error {
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='withdrawn',error=$3::text WHERE batch_id=$1 AND delivery_id=$2 AND result='pending'`, batch, delivery, reason)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='awaiting_review',approved_by=NULL,approved_at=NULL,auto_release=false,release_validated=false,error=$3::text,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2 AND phase IN ('approved','preparing_release','publishing')`, ws, delivery, reason)
	return err
}

// publishDeliveryBatch 逐成员回填同一候选、同一产物、同一时间，未纳入的交付保持原状。
func (h *Handler) publishDeliveryBatch(r *http.Request, tx pgx.Tx, current deliveryBatch, candidateCommit string, demoted *[]string) error {
	if current.Status != batchPublishing {
		return fmt.Errorf("批次不在发布阶段")
	}
	if candidateCommit != current.CandidateCommit {
		return fmt.Errorf("生产提交与固定候选不一致")
	}
	rows, err := tx.Query(r.Context(), `SELECT delivery_id::text,issue_id::text FROM delivery_batch_member WHERE batch_id=$1 AND result='pending' ORDER BY created_at`, current.ID)
	if err != nil {
		return err
	}
	type member struct{ delivery, issue string }
	var members []member
	for rows.Next() {
		var item member
		if err = rows.Scan(&item.delivery, &item.issue); err != nil {
			rows.Close()
			return err
		}
		members = append(members, item)
	}
	rows.Close()
	backfillFailed := false
	for _, item := range members {
		tag, err := tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='published',candidate_id=$2::text,plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}',to_jsonb($3::text)),error='',revision=revision+1,updated_at=now() WHERE id=$1 AND phase='publishing'`, item.delivery, current.CandidateID, candidateCommit)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// 产物已部署；回填失败不能伪造交付成功，也不代表代码已从生产摘除。
			_, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='failed',error='生产产物已部署，但成员交付阶段不允许回填，需要核对' WHERE batch_id=$1 AND delivery_id=$2`, current.ID, item.delivery)
			if err != nil {
				return err
			}
			backfillFailed = true
			continue
		}
		_, err = tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='published' WHERE batch_id=$1 AND delivery_id=$2`, current.ID, item.delivery)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,'published',$2) ON CONFLICT(delivery_id,stage) DO NOTHING`, item.delivery, dbid.NewV7())
		if err != nil {
			return err
		}
	}
	message := ""
	if backfillFailed {
		message = "生产产物已部署，部分成员交付状态回填失败，需要核对成员记录"
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='published',candidate_commit=$2::text,error=$3::text,updated_at=now() WHERE id=$1`, current.ID, candidateCommit, message)
	return err
}

// failDeliveryBatch 区分成员冲突与整批失败；真正失败保留授权，等待修复或显式重试。
func (h *Handler) failDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current deliveryBatch, message, conflict string, demoted *[]string) error {
	if conflict != "" {
		if _, parseErr := util.ParseUUID(conflict); parseErr != nil {
			return fmt.Errorf("冲突交付编号无效")
		}
		tag, err := tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='failed',error=$3::text WHERE batch_id=$1 AND delivery_id=$2 AND result='pending'`, current.ID, conflict, message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("冲突交付不属于当前批次")
		}
		tag, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error=$3::text,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2 AND phase NOT IN ('cancelled','published')`, ws, conflict, message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,'needs_changes',$2) ON CONFLICT(delivery_id,stage) DO NOTHING`, conflict, dbid.NewV7())
			if err != nil {
				return err
			}
		}
	} else {
		// 失败成员保留具体原因及有效授权，不能由下一轮调度自动重开同一组合。
		if _, err := tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='pending',error=$2::text WHERE batch_id=$1 AND result='pending'`, current.ID, message); err != nil {
			return err
		}
	}
	if err := h.resetPendingBatchMembers(r, tx, current.ID, message); err != nil {
		return err
	}
	if conflict == "" {
		if _, err := tx.Exec(r.Context(), `UPDATE issue_delivery d SET phase='needs_changes',revision=revision+1,updated_at=now() FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.delivery_id=d.id AND m.result='pending' AND d.phase NOT IN ('cancelled','published')`, current.ID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch SET status='needs_changes',error=$2::text,updated_at=now() WHERE id=$1`, current.ID, message)
	return err
}

// 组合验收由确定顺序的代表交付提交，证据绑定本批固定候选。
func batchAcceptanceCandidate(r *http.Request, tx pgx.Tx, ws, delivery string) (string, error) {
	var candidate string
	err := tx.QueryRow(r.Context(), `SELECT b.candidate_id FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id WHERE b.workspace_id=$1 AND m.delivery_id=$2 AND m.result='pending' AND b.status='validating' AND b.candidate_id<>'' AND (b.project_id IS NOT NULL OR m.delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=b.id AND result='pending' ORDER BY created_at,delivery_id LIMIT 1))`, ws, delivery).Scan(&candidate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return candidate, err
}
