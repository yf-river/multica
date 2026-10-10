package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/health"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func healthInstructions(reportID string, attempt int, epoch int64, analysisOnly ...bool) string {
	if len(analysisOnly) > 0 && analysisOnly[0] {
		return fmt.Sprintf("只补完成体检报告分析，不重新提交实测指标。报告 %s，attempt=%d，epoch=%d。执行 multica health report get %s 和 multica health overview，读取已有提取结果及当前身体数据版本。将中文分析与 context_revision 保存为 ./analysis.json，运行 multica health report submit %s --phase analysis --attempt %d --epoch %d --file ./analysis.json。分析须引用 PDF 页码；不得把医学内容粘贴到任务、评论或聊天。提交成功后仅反馈数量、报告链接和任务状态。", reportID, attempt, epoch, reportID, reportID, attempt, epoch)
	}
	return fmt.Sprintf(`处理本人授权的体检报告。原件和医学数据只保存在身体数据接口中，不得上传为普通附件，不得把数值、姓名、OCR 或完整分析粘贴到任务描述、评论、聊天或日志。
报告 ID: %s；处理轮次: %d；数据代次: %d。
1. 执行 multica health report prepare %s，得到逐页目录；通过 multica health report pages %s --from 1 --to 4 分批读取全部页面，每次最多四页。需要时用 multica health report image %s --page N 下载高清页图；处理扫描表、横向心电图和描述中的明确测量值。
2. 阅读 multica health overview 获得本人历史及基础资料。保存真实测量日期，不使用上传日期。不猜测单位、生日或缺失指标，不把模型推断写成检查结果。重复结果合并，不同方法、标本、部位分开保存。最后导入的结果直接覆盖涉及的当前指标，旧值与原件保留；识别疑点写入 uncertainty，不设置确认门槛。
3. 将结构化提取结果保存为当前工作目录的 result.json，先使用 multica health report submit %s --phase extraction --attempt %d --epoch %d --file ./result.json 入库。结构须包含 exam_date、institution、patient_name、observations、pages、conclusions、uncertainty。每个 observation 包含 name、original_name、code、category、value、original_value、unit、original_unit、reference_range、abnormal、observed_at(YYYY-MM-DD，缺失留空)、method、specimen、site、source(text/ocr)、page、evidence(同页原文连续摘录，含原始名称与原始结果)、status(accepted)、uncertainty。pages 覆盖全部页码，status 为 extracted/no_data/unreadable，note 说明处理结果。不要输出全量结果到 stdout，使用私有文件提交。
4. 再次运行 multica health overview，记录 revision。将 analysis 与 context_revision(刚读取的 revision) 保存到 ./analysis.json。analysis 用中文 Markdown 解释报告摘要、异常项目、原报告结论、可比较的历史变化和识别说明，逐项引用原 PDF 页码。不自动修改饮食或目标。运行同一 report submit 命令，改用 --phase analysis 和 --file ./analysis.json 提交分析。分析失败不能撤销已入库指标。
JSON 类型契约：observations 与 pages 是数组；page 和 context_revision 是整数；其他字段全部使用字符串。value、original_value、unit、original_unit、reference_range、abnormal 不能用数字、布尔、对象或 null；缺失字符串用空字符串。conclusions、uncertainty 为文本字符串，不是数组。保留原始结果和异常标记的语义，不为修正类型重新猜测数值。
5. 提交成功后，只反馈报告链接、入库数量与任务链接。完成两阶段提交后将任务设为 done，识别说明不阻塞完成。读取失败或分析失败应明确指出步骤和可重试原因，不能声称完成。`, reportID, attempt, epoch, reportID, reportID, reportID, reportID, attempt, epoch)
}

func (h *Handler) ProcessHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	if s.task != nil && !s.task.ChatSessionID.Valid {
		writeError(w, 403, "执行报告的任务不能再次下发自己")
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	var input struct {
		AgentID   string `json:"agent_id"`
		Reprocess bool   `json:"reprocess"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	if input.AgentID == "" && s.task != nil {
		input.AgentID = uuidToString(s.task.AgentID)
	}
	agentID, ok := parseUUIDOrBadRequest(w, input.AgentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: parseUUID(s.workspace)})
	if err != nil || agent.ArchivedAt.Valid || !h.canInvokeAgent(r.Context(), agent, "member", s.user, s.user, s.workspace) {
		writeError(w, 403, "请选择有调用权限的助手")
		return
	}
	if !agent.RuntimeID.Valid {
		writeError(w, 409, "助手尚未绑定运行时")
		return
	}
	_, _, epoch, err := h.healthState(r.Context(), h.DB, s, false)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if p.IssueID != nil {
		if err := h.enrichHealthReport(r.Context(), s, &p); err != nil {
			healthFailure(w, err)
			return
		}
		if p.TaskStatus == "running" || p.TaskStatus == "queued" || p.TaskStatus == "deferred" || p.TaskStatus == "claimed" {
			writeJSON(w, 200, p)
			return
		}
		if p.Result != nil && p.Result.Analysis != "" && !input.Reprocess && p.Epoch == epoch {
			writeJSON(w, 200, p)
			return
		}
		// 手动重试仍使用原任务面板条目，只登记新的报告处理轮次。
		attempt := p.Attempt + 1
		analysisOnly := p.Result != nil && p.Result.Analysis == "" && p.Epoch == epoch
		if analysisOnly {
			attempt = p.Attempt
		}
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			healthFailure(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		tag, err := tx.Exec(r.Context(), `UPDATE health_report SET attempt=$4,epoch=$5,agent_id=$6,attempt_started_at=now() WHERE id=$1 AND workspace_id=$2 AND user_id=$3 AND attempt=$7`, p.ID, s.workspace, s.user, attempt, epoch, input.AgentID, p.Attempt)
		if err != nil || tag.RowsAffected() != 1 {
			healthFailure(w, errHealthConflict)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue SET description=$2,status='todo',assignee_type='agent',assignee_id=$3,revision=revision+1,updated_at=now() WHERE id=$1 AND workspace_id=$4`, *p.IssueID, healthInstructions(p.ID, attempt, epoch, analysisOnly), input.AgentID, s.workspace)
		if err != nil {
			healthFailure(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			healthFailure(w, err)
			return
		}
		issue, err := h.Queries.GetIssue(r.Context(), parseUUID(*p.IssueID))
		if err == nil {
			_, err = h.TaskService.EnqueueTaskForIssueWithHandoff(r.Context(), issue, healthInstructions(p.ID, attempt, epoch, analysisOnly), parseUUID(s.user))
		}
		if err != nil {
			writeError(w, 503, "报告已归档，任务入队失败，请重试")
			return
		}
	} else {
		attempt := 1
		result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{WorkspaceID: parseUUID(s.workspace), Title: "录入并分析体检报告", Description: pgtype.Text{String: healthInstructions(p.ID, attempt, epoch), Valid: true}, Status: "todo", Priority: "important_not_urgent", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: agentID, CreatorType: "member", CreatorID: parseUUID(s.user), AllowDuplicate: true, OriginType: pgtype.Text{String: "health_report", Valid: true}, OriginID: parseUUID(p.ID)}, service.IssueCreateOpts{RelatedRecord: func(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
			tag, err := tx.Exec(ctx, `UPDATE health_report SET issue_id=$4,agent_id=$5,attempt=1,epoch=$6,attempt_started_at=now() WHERE id=$1 AND workspace_id=$2 AND user_id=$3 AND issue_id IS NULL`, p.ID, s.workspace, s.user, uuidToString(issue.ID), input.AgentID, epoch)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errHealthConflict
			}
			return nil
		}})
		if err != nil {
			healthFailure(w, err)
			return
		}
		if !result.AssignedTaskID.Valid {
			writeError(w, 503, "报告任务已创建，执行尚未入队，请重试")
			return
		}
	}
	p, ok = h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	if err := h.enrichHealthReport(r.Context(), s, &p); err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (h *Handler) SubmitHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	if s.task == nil || !s.task.IssueID.Valid {
		writeError(w, 403, "需要对应报告的运行中任务")
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	var input struct {
		Attempt int           `json:"attempt"`
		Epoch   int64         `json:"epoch"`
		Result  health.Result `json:"result"`
		Phase   string        `json:"phase"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	if input.Attempt != p.Attempt || input.Epoch != p.Epoch || p.StartedAt == nil || s.task.CreatedAt.Time.Before(*p.StartedAt) {
		writeError(w, 409, "报告处理轮次已过期")
		return
	}
	if input.Phase == "analysis" {
		h.submitHealthAnalysis(w, r, s, p, input.Result, input.Epoch)
		return
	}
	if input.Phase != "" && input.Phase != "complete" && input.Phase != "extraction" {
		writeError(w, 400, "未知提交阶段")
		return
	}
	data, err := os.ReadFile(filepath.Join(healthCachePath(s, p.ID), "index.json"))
	if err != nil {
		writeError(w, 409, "尚未完成报告页面读取")
		return
	}
	var prep health.Preparation
	if err = json.Unmarshal(data, &prep); err != nil {
		healthFailure(w, err)
		return
	}
	result := input.Result
	if input.Phase == "extraction" {
		result.Analysis = ""
	}
	if len(result.Pages) != p.PageCount || (input.Phase != "extraction" && strings.TrimSpace(result.Analysis) == "") {
		writeError(w, 422, "结果必须覆盖全部页码并包含分析")
		return
	}
	coverage := map[int]string{}
	for _, page := range result.Pages {
		if page.Page < 1 || page.Page > p.PageCount || coverage[page.Page] != "" || (page.Status != "extracted" && page.Status != "no_data" && page.Status != "unreadable") {
			writeError(w, 422, "页面处理记录无效或重复")
			return
		}
		coverage[page.Page] = page.Status
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	old, rev, epoch, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if epoch != input.Epoch {
		writeError(w, 409, "身体数据已回退，旧任务不能提交，请重新处理报告")
		return
	}
	var existingRev int64
	err = tx.QueryRow(r.Context(), `SELECT revision FROM health_report_result WHERE report_id=$1 AND attempt=$2`, p.ID, input.Attempt).Scan(&existingRev)
	if err == nil {
		writeJSON(w, 200, map[string]any{"revision": existingRev, "already_applied": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		healthFailure(w, err)
		return
	}
	identityConflict := old.Profile.Name != "" && result.PatientName != "" && old.Profile.Name != result.PatientName
	existing, err := h.healthObservations(r.Context(), tx, s, old.ObservationIDs)
	if err != nil {
		healthFailure(w, err)
		return
	}
	next := old
	// 重新提取替换该报告当前采用的记录，旧结果仍在报告和版本快照中。
	oldRows, err := tx.Query(r.Context(), `SELECT id::text FROM health_observation WHERE workspace_id=$1 AND user_id=$2 AND id=ANY($3::uuid[]) AND data->>'report_id'=$4`, s.workspace, s.user, next.ObservationIDs, p.ID)
	if err != nil {
		healthFailure(w, err)
		return
	}
	replace := map[string]bool{}
	for oldRows.Next() {
		var id string
		if err := oldRows.Scan(&id); err != nil {
			oldRows.Close()
			healthFailure(w, err)
			return
		}
		replace[id] = true
	}
	err = oldRows.Err()
	oldRows.Close()
	if err != nil {
		healthFailure(w, err)
		return
	}
	ids := []string{}
	for _, id := range next.ObservationIDs {
		if !replace[id] {
			ids = append(ids, id)
		}
	}
	next.ObservationIDs = ids
	observations := []health.Observation{}
	seen := map[string]bool{}
	accepted := 0
	for _, o := range result.Observations {
		if o.Page < 1 || o.Page > p.PageCount || coverage[o.Page] == "no_data" {
			writeError(w, 422, "指标页码与页面处理记录不一致")
			return
		}
		o.ID = uuid.NewString()
		o.ReportID = p.ID
		o.Supersedes = ""
		if o.Source != "text" && o.Source != "ocr" {
			writeError(w, 422, "报告指标来源只能是 text 或 ocr")
			return
		}
		if o.Status == "rejected" {
			writeError(w, 422, "Agent 不能舍弃报告指标，请保留原始结果与识别说明")
			return
		}
		o.Status = "accepted"
		if o.ObservedAt == "" {
			o.ObservedAt = result.ExamDate
		}
		if o.OriginalValue != "" {
			o.Value = o.OriginalValue
		}
		if o.OriginalUnit != "" {
			o.Unit = o.OriginalUnit
		}
		if err := health.Normalize(&o); err != nil {
			writeError(w, 422, err.Error())
			return
		}
		page := prep.Pages[o.Page-1]
		cleanEvidence := strings.Join(strings.Fields(o.Evidence), "")
		if cleanEvidence != "" && !strings.Contains(strings.Join(strings.Fields(page.Text), ""), cleanEvidence) && strings.Contains(strings.Join(strings.Fields(page.OCR), ""), cleanEvidence) {
			o.Source = "ocr"
		}
		if !health.EvidenceMatches(page, o) {
			o.Uncertainty = strings.TrimSpace(o.Uncertainty + " 原始证据无法自动定位")
		}
		if o.Source == "ocr" && !health.OCRValueReliable(page, o.OriginalValue) {
			o.Uncertainty = strings.TrimSpace(o.Uncertainty + " 扫描结果识别置信度较低")
		}
		if identityConflict {
			o.Uncertainty = strings.TrimSpace(o.Uncertainty + " 报告姓名与基础资料不同")
		}
		key := health.MetricKey(o) + "\x1f" + o.ObservedAt + "\x1f" + o.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		accepted++
		serialized, _ := json.Marshal(o)
		if _, err = tx.Exec(r.Context(), `INSERT INTO health_observation(id,workspace_id,user_id,data) VALUES($1,$2,$3,$4)`, o.ID, s.workspace, s.user, serialized); err != nil {
			healthFailure(w, err)
			return
		}
		next.ObservationIDs = append(next.ObservationIDs, o.ID)
		observations = append(observations, o)
	}
	if len(observations) == 0 {
		writeError(w, 422, "报告没有提取出任何指标，请明确失败原因并核对页面")
		return
	}
	next = health.Apply(old, existing, observations)
	ids = []string{}
	for _, id := range next.ObservationIDs {
		if !replace[id] {
			ids = append(ids, id)
		}
	}
	next.ObservationIDs = ids
	ids = []string{}
	for _, id := range next.CurrentObservationIDs {
		if !replace[id] {
			ids = append(ids, id)
		}
	}
	next.CurrentObservationIDs = ids
	result.Observations = observations
	raw, err := json.Marshal(result)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO health_report_result(id,report_id,workspace_id,user_id,attempt,revision,data) VALUES($1,$2,$3,$4,$5,$6,$7)`, uuid.NewString(), p.ID, s.workspace, s.user, p.Attempt, rev+1, raw); err != nil {
		healthFailure(w, err)
		return
	}
	if err = h.saveHealthState(r.Context(), tx, s, old, next, rev, "导入体检报告", false); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	status := "done"
	if input.Phase == "extraction" {
		status = "in_progress"
	}
	writeJSON(w, 200, map[string]any{"revision": rev + 1, "accepted": accepted, "issue_status": status, "report_id": p.ID})
}
