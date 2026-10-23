package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
}

type deliveryBatchMember struct {
	DeliveryID   string `json:"delivery_id"`
	IssueID      string `json:"issue_id"`
	Number       int64  `json:"number"`
	SourceCommit string `json:"source_commit"`
	Result       string `json:"result"`
	Error        string `json:"error"`
}

const deliveryBatchSelect = `SELECT id::text,status,window_seconds,collect_until,base_commit,candidate_id,candidate_commit,COALESCE(worker_token::text,''),error FROM delivery_batch`

func scanDeliveryBatch(row pgx.Row) (deliveryBatch, error) {
	var b deliveryBatch
	err := row.Scan(&b.ID, &b.Status, &b.WindowSeconds, &b.CollectUntil, &b.BaseCommit, &b.CandidateID, &b.CandidateCommit, &b.WorkerToken, &b.Error)
	return b, err
}

func (h *Handler) deliveryBatchMembers(r *http.Request, batch string) ([]deliveryBatchMember, error) {
	members := []deliveryBatchMember{}
	err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('delivery_id',m.delivery_id,'issue_id',m.issue_id,'number',i.number,'source_commit',m.source_commit,'result',m.result,'error',m.error) ORDER BY m.created_at),'[]') FROM delivery_batch_member m JOIN issue i ON i.id=m.issue_id WHERE m.batch_id=$1`, batch).Scan(&members)
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
		BaseCommit       string `json:"base_commit"`
		CandidateID      string `json:"candidate_id"`
		CandidateCommit  string `json:"candidate_commit"`
		WorkerToken      string `json:"worker_token"`
		Error            string `json:"error"`
		ConflictDelivery string `json:"conflict_delivery_id"`
		Delivery         string `json:"delivery_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil {
		writeError(w, 400, "批次请求格式错误")
		return
	}
	if body.Action == "open" {
		h.openDeliveryBatch(w, r, ws, body.WindowSeconds)
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
	var demoted []string
	switch body.Action {
	case "freeze":
		current, demoted, err = h.freezeDeliveryBatch(r, tx, ws, current)
	case "preparing":
		err = h.prepareDeliveryBatch(r, tx, ws, &current, body.BaseCommit)
	case "validating":
		err = h.validateDeliveryBatch(r, tx, ws, &current, body.CandidateID, body.CandidateCommit)
	case "publishing":
		if current.Status != batchValidating {
			err = fmt.Errorf("批次不在待发布阶段")
			break
		}
		// 修改 5. 候选固定后成员撤权，已构建的组合仍包含该成员代码；整批停下按剩余成员重建，不做局部替换。
		var withdrawn []string
		if _, withdrawn, err = h.withdrawInvalidMembers(r, tx, ws, current.ID); err == nil {
			if len(withdrawn) > 0 {
				err = h.stopStaleBatch(r, tx, current.ID, staleBatchReason)
			}
		}
		if err == nil && len(withdrawn) == 0 {
			var pending int
			if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM delivery_batch_member WHERE batch_id=$1 AND result='pending'`, current.ID).Scan(&pending); err == nil {
				if pending == 0 {
					_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='cancelled',error='没有有效授权的成员',updated_at=now() WHERE id=$1`, current.ID)
				} else {
					// 组合候选已在 int 通过验收：成员进入发布阶段，同一份产物随后切换到生产。
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery d SET phase='publishing',candidate_id=$2::text,release_validated=true,error='',revision=revision+1,updated_at=now() FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.delivery_id=d.id AND m.result='pending'`, current.ID, current.CandidateID)
					if err == nil {
						_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='publishing',updated_at=now() WHERE id=$1`, current.ID)
					}
				}
			}
		}
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
	for _, item := range demoted {
		_, _ = h.DB.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_pending}','true'::jsonb),approved_by=NULL,approved_at=NULL,release_validated=false,revision=revision+1 WHERE id=$1`, item)
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

func ternaryStage(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func (h *Handler) openDeliveryBatch(w http.ResponseWriter, r *http.Request, ws string, window int) {
	if window <= 0 {
		window = 60
	}
	if window > 300 {
		window = 300
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
	batch := deliveryBatch{ID: dbid.NewV7().String(), Status: batchCollecting, WindowSeconds: window, CollectUntil: time.Now().Add(time.Duration(window) * time.Second)}
	_, err = tx.Exec(r.Context(), `INSERT INTO delivery_batch(id,workspace_id,status,window_seconds,collect_until) VALUES($1,$2,'collecting',$3,$4)`, batch.ID, ws, window, batch.CollectUntil)
	if err != nil {
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
	rows, err := tx.Query(r.Context(), `SELECT d.id::text,d.issue_id::text,d.source_commit FROM issue_delivery d WHERE d.workspace_id=$1 AND d.phase='approved' AND d.approved_by IS NOT NULL AND NOT EXISTS(SELECT 1 FROM delivery_batch_member m JOIN delivery_batch b ON b.id=m.batch_id WHERE m.delivery_id=d.id AND b.status IN `+activeBatchStatuses+`) ORDER BY d.created_at LIMIT 8`, ws)
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
		if !ready || !planValid {
			// 修改 2. 授权或方案失效的交付不进入本批，退回待核对，不带着旧授权发布。
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='awaiting_review',approved_by=NULL,approved_at=NULL,release_validated=false,error='发布授权或方案已变化，等待负责人核对',revision=revision+1,updated_at=now() WHERE id=$1`, item.delivery)
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
	return current, demoted, nil
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
	remaining, _, err := h.withdrawInvalidMembers(r, tx, ws, current.ID)
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

// 修改 4. 候选已经按冻结成员构建完成；此后成员授权失效时旧组合不再代表已授权范围，
// 整批停止并由下一批按剩余成员重新构建和验收，禁止剩余成员继续发布旧组合。
const staleBatchReason = "成员发布授权已失效，已固定候选包含撤权成员代码，整批停止并按剩余成员重新组批"

func (h *Handler) validateDeliveryBatch(r *http.Request, tx pgx.Tx, ws string, current *deliveryBatch, candidate, commit string) error {
	if current.Status == batchValidating {
		return nil
	}
	if current.Status != batchPreparing {
		return fmt.Errorf("批次不在验收阶段")
	}
	_, withdrawn, err := h.withdrawInvalidMembers(r, tx, ws, current.ID)
	if err != nil {
		return err
	}
	if len(withdrawn) > 0 {
		return h.stopStaleBatch(r, tx, current.ID, staleBatchReason)
	}
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET candidate_id=$2::text,candidate_commit=$3::text,status='validating',updated_at=now() WHERE id=$1 AND status='preparing'`, current.ID, candidate, commit)
	return err
}

func (h *Handler) stopStaleBatch(r *http.Request, tx pgx.Tx, batch, reason string) error {
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch SET status='cancelled',error=$2::text,updated_at=now() WHERE id=$1 AND status IN ('preparing','validating','publishing')`, batch, reason)
	return err
}

// withdrawInvalidMembers 在候选固定后、发布前逐一复核：撤回授权的成员退出本批，返回剩余可发布成员数。
func (h *Handler) withdrawInvalidMembers(r *http.Request, tx pgx.Tx, ws, batch string) (int, []string, error) {
	rows, err := tx.Query(r.Context(), `SELECT m.delivery_id::text,m.issue_id::text FROM delivery_batch_member m WHERE m.batch_id=$1 AND m.result='pending'`, batch)
	if err != nil {
		return 0, nil, err
	}
	type member struct{ delivery, issue string }
	var members []member
	for rows.Next() {
		var item member
		if err = rows.Scan(&item.delivery, &item.issue); err != nil {
			rows.Close()
			return 0, nil, err
		}
		members = append(members, item)
	}
	rows.Close()
	withdrawn := []string{}
	for _, item := range members {
		ready, checkErr := deliveryAuthorizationReady(r.Context(), tx, ws, item.issue, item.delivery)
		var planValid bool
		if checkErr == nil {
			checkErr = tx.QueryRow(r.Context(), deliveryPlanValidSQL, item.delivery).Scan(&planValid)
		}
		if checkErr != nil {
			return 0, nil, checkErr
		}
		if ready && planValid {
			continue
		}
		if err = h.withdrawMember(r, tx, ws, batch, item.delivery, "发布授权已失效，退出本批"); err != nil {
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
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='awaiting_review',approved_by=NULL,approved_at=NULL,release_validated=false,error=$3::text,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, delivery, reason)
	return err
}

// publishDeliveryBatch 逐成员回填同一候选、同一产物、同一时间，未纳入的交付保持原状。
func (h *Handler) publishDeliveryBatch(r *http.Request, tx pgx.Tx, current deliveryBatch, candidateCommit string, demoted *[]string) error {
	if current.Status != batchPublishing {
		return fmt.Errorf("批次不在发布阶段")
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
	for _, item := range members {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='published',candidate_id=$2::text,plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}',to_jsonb($3::text)),error='',revision=revision+1,updated_at=now() WHERE id=$1 AND phase='publishing'`, item.delivery, current.CandidateID, candidateCommit)
		if err != nil {
			return err
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
	_, err = tx.Exec(r.Context(), `UPDATE delivery_batch SET status='published',candidate_commit=$2::text,updated_at=now() WHERE id=$1`, current.ID, candidateCommit)
	return err
}

// failDeliveryBatch 保留有效证据：只有冲突成员回到待修改，其余成员保留授权等待下一批。
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
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error=$3::text,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, conflict, message)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,'needs_changes',$2) ON CONFLICT(delivery_id,stage) DO NOTHING`, conflict, dbid.NewV7())
		if err != nil {
			return err
		}
	} else {
		// 组合验收失败时整批退出，但成员仍然拥有有效授权，下一批可以重新组批。
		if _, err := tx.Exec(r.Context(), `UPDATE delivery_batch_member SET result='pending',error=$2::text WHERE batch_id=$1 AND result='pending'`, current.ID, message); err != nil {
			return err
		}
	}
	_, err := tx.Exec(r.Context(), `UPDATE delivery_batch SET status='needs_changes',error=$2::text,updated_at=now() WHERE id=$1`, current.ID, message)
	return err
}
