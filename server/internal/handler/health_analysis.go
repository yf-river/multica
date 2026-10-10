package handler

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/health"
	"net/http"
	"strings"
)

// 分析更新不修改已经入库的实测数据，也不消耗身体数据的历史版本。
func (h *Handler) submitHealthAnalysis(w http.ResponseWriter, r *http.Request, s healthScope, p healthReport, input health.Result, expectedEpoch int64) {
	analysis := input.Analysis
	if strings.TrimSpace(analysis) == "" {
		writeError(w, 422, "报告分析不能为空")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, currentRevision, epoch, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if epoch != expectedEpoch {
		writeError(w, 409, "身体数据已回退，不能提交旧分析")
		return
	}
	if input.ContextRevision < 0 || input.ContextRevision > currentRevision {
		writeError(w, 422, "分析引用的数据版本无效")
		return
	}
	var raw []byte
	var revision int64
	if err = tx.QueryRow(r.Context(), `SELECT data,revision FROM health_report_result WHERE report_id=$1 AND attempt=$2 FOR UPDATE`, p.ID, p.Attempt).Scan(&raw, &revision); err != nil {
		writeError(w, 409, "请先提交指标提取结果")
		return
	}
	var result health.Result
	if err = json.Unmarshal(raw, &result); err != nil {
		healthFailure(w, err)
		return
	}
	if result.Analysis != "" {
		writeJSON(w, 200, map[string]any{"revision": revision, "already_applied": true})
		return
	}
	result.Analysis = analysis
	result.ContextRevision = input.ContextRevision
	data, err := json.Marshal(result)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE health_report_result SET data=$3 WHERE report_id=$1 AND attempt=$2`, p.ID, p.Attempt, data)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	if err = h.enrichHealthReport(r.Context(), s, &p); err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": revision, "issue_status": "done", "report_id": p.ID})
}
