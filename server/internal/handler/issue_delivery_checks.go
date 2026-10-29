package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 业务清单随已确认方案保存在交付快照中，协调检查不替代功能验收。
type requiredDeliveryCheck struct {
	Name  string `json:"name"`
	Stage string `json:"stage"`
}

type deliveryChecksGap struct{ message string }

func (e *deliveryChecksGap) Error() string { return e.message }

func normalizeRequiredDeliveryChecks(checks []requiredDeliveryCheck) ([]requiredDeliveryCheck, error) {
	return normalizeRequiredDeliveryChecksWithSkip(checks, false)
}

func normalizeRequiredDeliveryChecksWithSkip(checks []requiredDeliveryCheck, skipIntegration bool) ([]requiredDeliveryCheck, error) {
	if len(checks) == 0 || len(checks) > 100 {
		return nil, &deliveryChecksGap{"缺少方案业务验收清单，请由负责人使用 bind-checks 绑定 required_checks"}
	}
	reserved := map[string]bool{"delivery-api": true, "delivery-runtime": true, "delivery-worker": true,
		"delivery-publisher": true, "delivery-coordination": true, "batch-closeout-api": true}
	result := append([]requiredDeliveryCheck(nil), checks...)
	seen := map[string]bool{}
	stages := map[string]bool{}
	for i := range result {
		c := &result[i]
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" || reserved[c.Name] || (c.Stage != "verification" && c.Stage != "integration") {
			return nil, &deliveryChecksGap{"业务检查须有独立名称和 verification/integration 阶段，六项协调检查不能充当业务清单"}
		}
		key := c.Stage + ":" + c.Name
		if seen[key] {
			return nil, &deliveryChecksGap{"业务验收项重复：" + key}
		}
		seen[key], stages[c.Stage] = true, true
	}
	if !stages["verification"] || (!stages["integration"] && !skipIntegration) {
		return nil, &deliveryChecksGap{"业务清单须分别声明独立验收 verification 和最终集成 integration 的必要检查"}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Stage+":"+result[i].Name < result[j].Stage+":"+result[j].Name
	})
	return result, nil
}

func deliveryRequiredChecks(snapshot json.RawMessage) ([]requiredDeliveryCheck, error) {
	var value struct {
		Checks                []requiredDeliveryCheck `json:"required_checks"`
		IntegrationSkipReason string                  `json:"integration_skip_reason"`
	}
	if err := json.Unmarshal(snapshot, &value); err != nil {
		return nil, err
	}
	return normalizeRequiredDeliveryChecksWithSkip(value.Checks, strings.TrimSpace(value.IntegrationSkipReason) != "")
}

func missingDeliveryChecks(required []requiredDeliveryCheck, evidence []deliveryCheck, stage string) error {
	passed := map[string]bool{}
	for _, c := range evidence {
		if c.Status == "passed" && strings.TrimSpace(c.Evidence) != "" {
			passed[c.Name] = true
		}
	}
	missing := []string{}
	for _, c := range required {
		if c.Stage == stage && !passed[c.Name] {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) > 0 {
		return &deliveryChecksGap{"缺少 " + stage + " 业务验收证据：" + strings.Join(missing, "、")}
	}
	return nil
}

func bindDeliveryRequiredChecks(ctx context.Context, tx pgx.Tx, id, plan string, checks []requiredDeliveryCheck) error {
	checks, err := normalizeRequiredDeliveryChecks(checks)
	if err != nil {
		return err
	}
	var actualPlan, phase string
	var snapshot json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT plan_id::text,phase,plan_snapshot FROM issue_delivery WHERE id=$1 FOR UPDATE`, id).Scan(&actualPlan, &phase, &snapshot); err != nil {
		return err
	}
	if plan != actualPlan || phase == "published" || phase == "publishing" || phase == "cancelled" {
		return &deliveryChecksGap{"需要当前已确认方案及尚未发布的交付才能绑定业务清单"}
	}
	raw, _ := json.Marshal(checks)
	old, oldErr := deliveryRequiredChecks(snapshot)
	if oldErr == nil {
		previous, _ := json.Marshal(old)
		if string(previous) != string(raw) {
			return &deliveryChecksGap{"已绑定的业务清单不能删减或替换；范围变化请登记新方案和交付"}
		}
		return nil
	}
	var existing map[string]json.RawMessage
	if json.Unmarshal(snapshot, &existing) == nil && len(existing["required_checks"]) > 0 {
		return &deliveryChecksGap{"现有业务清单无效，不能覆盖；请修正方案并登记新交付"}
	}
	var gap *deliveryChecksGap
	if !errors.As(oldErr, &gap) {
		return oldErr
	}
	_, err = tx.Exec(ctx, `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{required_checks}',$2::jsonb) WHERE id=$1`, id, raw)
	return err
}

func deliveryChecksAlreadyBound(ctx context.Context, tx pgx.Tx, id, plan string, checks []requiredDeliveryCheck) bool {
	provided, err := normalizeRequiredDeliveryChecks(checks)
	if err != nil {
		return false
	}
	var actual, phase string
	var snapshot json.RawMessage
	if tx.QueryRow(ctx, `SELECT plan_id::text,phase,plan_snapshot FROM issue_delivery WHERE id=$1`, id).Scan(&actual, &phase, &snapshot) != nil || plan != actual || phase == "published" || phase == "publishing" || phase == "cancelled" {
		return false
	}
	stored, err := deliveryRequiredChecks(snapshot)
	if err != nil {
		return false
	}
	a, _ := json.Marshal(provided)
	b, _ := json.Marshal(stored)
	return string(a) == string(b)
}

// 组合验收由真实代表提交，覆盖每个成员在同一候选上的集成清单。
func requireDeliveryChecks(ctx context.Context, tx pgx.Tx, id, candidate, stage string, evidence []deliveryCheck) error {
	rows, err := tx.Query(ctx, `SELECT d.id::text,d.candidate_id,d.plan_snapshot FROM issue_delivery d
 WHERE d.id=$1 OR ($3='integration' AND EXISTS(
 SELECT 1 FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id
 WHERE m.delivery_id=d.id AND m.result='pending' AND b.project_id IS NULL AND b.status IN ('validating','publishing')
 AND b.candidate_id=$2 AND b.workspace_id=d.workspace_id
 AND $1::uuid=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=b.id AND result='pending' ORDER BY created_at,delivery_id LIMIT 1)))
 ORDER BY d.id`, id, candidate, stage)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var delivery, actual string
		var snapshot json.RawMessage
		if err = rows.Scan(&delivery, &actual, &snapshot); err != nil {
			return err
		}
		if candidate == "" || actual != candidate {
			return &deliveryChecksGap{"业务验收证据需要准确的当前候选编号"}
		}
		required, readErr := deliveryRequiredChecks(snapshot)
		if readErr == nil {
			readErr = missingDeliveryChecks(required, evidence, stage)
		}
		if readErr != nil {
			return fmt.Errorf("交付 %s：%w", delivery, readErr)
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return &deliveryChecksGap{"交付不存在"}
	}
	return nil
}

func requireStoredDeliveryChecks(ctx context.Context, tx pgx.Tx, id, stage string) error {
	var candidate string
	var evidence []deliveryCheck
	var snapshot json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT candidate_id,evidence,plan_snapshot FROM issue_delivery WHERE id=$1`, id).Scan(&candidate, &evidence, &snapshot); err != nil {
		return err
	}
	err := requireDeliveryChecks(ctx, tx, id, candidate, stage, evidence)
	if err == nil || stage != "verification" {
		return err
	}
	var gap *deliveryChecksGap
	if !errors.As(err, &gap) {
		return err
	}
	var state struct {
		Integrated bool `json:"integration_complete"`
	}
	if json.Unmarshal(snapshot, &state) != nil || !state.Integrated {
		return err
	}
	// int 保存自己的证据；生产批准复用同一交付、同一源码的完整独立验收记录。
	if requireDeliveryChecks(ctx, tx, id, candidate, "integration", evidence) != nil {
		return err
	}
	required, readErr := deliveryRequiredChecks(snapshot)
	if readErr != nil {
		return readErr
	}
	rows, readErr := tx.Query(ctx, `SELECT e.evidence FROM delivery_evidence e JOIN issue_delivery d ON d.id=e.delivery_id
 WHERE d.id=$1 AND e.workspace_id=d.workspace_id AND e.issue_id=d.issue_id
 AND e.source_commit=d.source_commit AND e.candidate_id<>'' ORDER BY e.created_at DESC,e.id DESC`, id)
	if readErr != nil {
		return readErr
	}
	defer rows.Close()
	for rows.Next() {
		var previous []deliveryCheck
		if readErr = rows.Scan(&previous); readErr != nil {
			return readErr
		}
		if missingDeliveryChecks(required, previous, "verification") == nil {
			return nil
		}
	}
	if readErr = rows.Err(); readErr != nil {
		return readErr
	}
	return err
}
