package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// deliveryRepairRequest fixes a verification source while retaining its original slot and failure history.
// 1. Repair evidence is distinct from business acceptance and never grants release approval.
type deliveryRepairRequest struct {
	OperationID   string `json:"operation_id"`
	Source        string `json:"source_commit"`
	Branch        string `json:"source_branch"`
	Base          string `json:"base_commit"`
	Environment   string `json:"environment"`
	Generation    int64  `json:"generation"`
	SeedID        string `json:"seed_id"`
	SeedSHA       string `json:"seed_sha256"`
	Compatibility string `json:"compatibility_evidence"`
}

// deliveryRepairReplay rejects changed payloads even when an operation ID is reused.
// 2. The caller holds the owner and delivery locks before checking idempotency.
func deliveryRepairReplay(r *http.Request, tx pgx.Tx, id string, request deliveryRepairRequest, checks []deliveryCheck) (bool, error) {
	if _, err := util.ParseUUID(request.OperationID); err != nil {
		return false, fmt.Errorf("修复需要有效 operation_id")
	}
	raw, _ := json.Marshal(request)
	evidence, _ := json.Marshal(checks)
	var exists, same bool
	err := tx.QueryRow(r.Context(), `SELECT COALESCE(plan_snapshot->'verification_repair'->'request'->>'operation_id'=$2,false) OR COALESCE(plan_snapshot->'batch_repair'->'request'->>'operation_id'=$2,false) OR COALESCE(plan_snapshot->'verification_update'->'request'->>'operation_id'=$2,false),COALESCE(plan_snapshot->'verification_repair'->'request'=$3::jsonb AND plan_snapshot->'verification_repair'->'checks'=$4::jsonb,false) OR COALESCE(plan_snapshot->'batch_repair'->'request'=$3::jsonb AND plan_snapshot->'batch_repair'->'checks'=$4::jsonb,false) OR COALESCE(plan_snapshot->'verification_update'->'request'=$3::jsonb AND plan_snapshot->'verification_update'->'checks'=$4::jsonb,false) FROM issue_delivery WHERE id=$1`, id, request.OperationID, raw, evidence).Scan(&exists, &same)
	if err != nil {
		return false, err
	}
	if exists && !same {
		return false, fmt.Errorf("相同修复操作号不能更改请求")
	}
	return exists, nil
}

// requestVerificationUpdate keeps the current lease until the new commit is deployed.
func requestVerificationUpdate(r *http.Request, tx pgx.Tx, ws, issue, id string, b deliveryRepairRequest, checks []deliveryCheck) error {
	if _, err := util.ParseUUID(b.OperationID); err != nil || !deliveryCommit.MatchString(b.Source) || !deliveryCommit.MatchString(b.Base) || !deliveryBranch.MatchString(b.Branch) || strings.Contains(b.Branch, "..") || !validDeliveryChecks(checks) {
		return fmt.Errorf("更新需要操作号、完整提交、任务分支、基线和检查证据")
	}
	var allowed bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_delivery d JOIN issue i ON i.id=d.issue_id AND i.workspace_id=d.workspace_id JOIN release_agent_config c ON c.workspace_id=i.workspace_id AND c.project_id=i.project_id AND c.enabled JOIN verification_slot s ON s.delivery_id=d.id AND s.workspace_id=d.workspace_id JOIN verification_slot_seed seed ON seed.name=s.name WHERE d.id=$1 AND d.workspace_id=$2 AND d.issue_id=$3 AND d.phase='testing' AND d.source_branch=$4 AND d.source_commit<>$5 AND s.name=$6 AND s.generation=$7 AND s.state='verifying' AND s.lease_expires_at>now() AND seed.verified AND seed.seed_id=$8 AND seed.seed_sha256=$9 AND COALESCE(d.plan_snapshot->'verification_update'->>'state','') NOT IN ('pending','applying'))`, id, ws, issue, b.Branch, b.Source, b.Environment, b.Generation, b.SeedID, b.SeedSHA).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("当前测试槽位、种子或提交不匹配")
	}
	raw, _ := json.Marshal(b)
	evidence, _ := json.Marshal(checks)
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot||jsonb_build_object('verification_update',jsonb_build_object('request',$2::jsonb,'checks',$3::jsonb,'state','pending','previous_commit',source_commit,'requested_at',now())),acceptance_submitted=false,evidence='[]'::jsonb,revision=revision+1,updated_at=now() WHERE id=$1`, id, raw, evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE issue SET metadata=metadata||'{"review_passed":false,"verification_passed":false,"verification_commit":"","integration_passed":false,"integration_commit":""}'::jsonb,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, issue)
	return err
}

// repairFailedDelivery records a new immutable attempt without releasing or resetting the quarantined slot.
// 3. Only pre-deployment failures can resume automatically; deployed or live candidates require separate recovery evidence.
func repairFailedDelivery(r *http.Request, tx pgx.Tx, ws, issue, id string, b deliveryRepairRequest, checks []deliveryCheck) error {
	if !deliveryCommit.MatchString(b.Source) || !deliveryCommit.MatchString(b.Base) || !deliveryBranch.MatchString(b.Branch) || strings.Contains(b.Branch, "..") || !validDeliveryChecks(checks) || strings.TrimSpace(b.Compatibility) == "" {
		return fmt.Errorf("修复需要完整提交、分支、基线、检查与兼容证据")
	}
	var snapshot json.RawMessage
	var valid bool
	err := tx.QueryRow(r.Context(), `SELECT plan_snapshot,approved_by IS NULL AND approved_at IS NULL AND candidate_id='' AND NOT EXISTS(SELECT 1 FROM delivery_publication WHERE delivery_id=$1 AND state<>'idle') AND NOT EXISTS(SELECT 1 FROM delivery_batch_member m JOIN delivery_batch b ON b.id=m.batch_id WHERE m.delivery_id=$1 AND b.status IN ('collecting','frozen','preparing','validating','publishing')) FROM issue_delivery WHERE id=$1`, id).Scan(&snapshot, &valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("候选已部署、已批准或正在运行，不能覆盖")
	}
	if _, err = deliveryRequiredChecks(snapshot); err != nil {
		return err
	}
	var generation int64
	err = tx.QueryRow(r.Context(), `SELECT s.generation FROM verification_slot s JOIN verification_slot_seed seed ON seed.name=s.name WHERE s.workspace_id=$1 AND s.delivery_id=$2 AND s.name=$3 AND s.generation=$4 AND s.state='quarantined' AND s.candidate_id='' AND s.lease_expires_at<=now() AND seed.verified AND seed.seed_id=$5 AND seed.seed_sha256=$6 FOR UPDATE OF s,seed`, ws, id, b.Environment, b.Generation, b.SeedID, b.SeedSHA).Scan(&generation)
	if err != nil {
		return fmt.Errorf("原槽位、种子或隔离代次不符，或旧租约仍有效")
	}
	raw, _ := json.Marshal(b)
	evidence, _ := json.Marshal(checks)
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=(plan_snapshot-'authorization_pending'-'authorization_notice'-'authorization_review') || jsonb_build_object('verification_repair',jsonb_build_object('request',$2::jsonb,'checks',$3::jsonb,'previous',jsonb_build_object('source_commit',source_commit,'revision',revision,'error',error,'evidence',evidence,'execution',plan_snapshot->'execution','verification_repair',plan_snapshot->'verification_repair'),'requested_at',now())),source_commit=$4,source_branch=$5,base_commit=$6,phase='waiting_int',acceptance_submitted=false,verification_comment_id=$7,worker_token=NULL,error='',revision=revision+1,updated_at=now() WHERE id=$1`, id, raw, evidence, b.Source, b.Branch, b.Base, dbid.NewV7())
	return err
}

// repairBatchConflict keeps the frozen batch member while revalidating its new commit.
func repairBatchConflict(r *http.Request, tx pgx.Tx, ws, issue, id string, b deliveryRepairRequest, checks []deliveryCheck) error {
	if _, err := util.ParseUUID(b.OperationID); err != nil || !deliveryCommit.MatchString(b.Source) || !deliveryCommit.MatchString(b.Base) || !deliveryBranch.MatchString(b.Branch) || strings.Contains(b.Branch, "..") || !validDeliveryChecks(checks) {
		return fmt.Errorf("修复需要操作号、完整提交、任务分支、基线和检查证据")
	}
	var eligible bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_delivery d JOIN delivery_batch_member m ON m.delivery_id=d.id AND m.result='pending' AND m.error<>'' JOIN delivery_batch batch ON batch.id=m.batch_id AND batch.workspace_id=d.workspace_id AND batch.project_id IS NOT NULL AND batch.status='frozen' AND batch.hold_kind='merge_conflict' WHERE d.id=$1 AND d.workspace_id=$2 AND d.issue_id=$3 AND d.phase='approved' AND d.source_branch=$4 AND d.source_commit<>$5 AND NOT EXISTS(SELECT 1 FROM verification_slot s WHERE s.delivery_id=d.id AND s.state<>'idle') AND NOT EXISTS(SELECT 1 FROM delivery_publication p WHERE p.delivery_id=d.id AND p.state<>'idle'))`, id, ws, issue, b.Branch, b.Source).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return fmt.Errorf("交付不属于等待修复的冲突成员，或仍占用环境")
	}
	raw, _ := json.Marshal(b)
	evidence, _ := json.Marshal(checks)
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=(plan_snapshot-'authorization_pending'-'authorization_notice'-'authorization_review')||jsonb_build_object('source_execution_id',COALESCE((SELECT execution_id FROM delivery_owner WHERE workspace_id=issue_delivery.workspace_id AND issue_id=issue_delivery.issue_id),''),'batch_repair',jsonb_build_object('request',$2::jsonb,'checks',$6::jsonb,'previous_source_commit',source_commit,'requested_at',now())),source_commit=$3,source_branch=$4,base_commit=$5,evidence=$6,phase='waiting_int',approved_by=NULL,approved_at=NULL,auto_release=false,acceptance_submitted=false,release_validated=false,candidate_id='',candidate_url='',verification_comment_id=$7,worker_token=NULL,error='',revision=revision+1,updated_at=now() WHERE id=$1`, id, raw, b.Source, b.Branch, b.Base, evidence, dbid.NewV7())
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE issue SET status='in_progress',metadata=metadata||'{"review_passed":false,"verification_passed":false,"verification_commit":"","integration_passed":false,"integration_commit":""}'::jsonb,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, issue)
	return err
}
