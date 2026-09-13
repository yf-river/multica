// 本文件实现 cron 表达式预览端点：调度编辑器在保存前先拿到权威的下次触发时刻。
// 纯计算、只读——工作区成员资格由路由组保证，不触碰其它资源。
package handler

import (
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// 拒绝码让编辑器说清楚是哪个输入有错：服务端 tzdata 不认识的时区，不是用户的
// cron 写错——两者在编辑器里由不同控件修复。
const (
	cronPreviewInvalidCron     = "invalid_cron"
	cronPreviewInvalidTimezone = "invalid_timezone"
)

// writeCronPreviewError 以统一的 400 + {error, code} 形状回写预览错误。
func writeCronPreviewError(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg, "code": code})
}

// CronPreview 计算候选 cron 表达式的接下来几次触发时刻，让调度编辑器在保存前
// 展示权威预览。纯计算：工作区成员资格由路由组强制，不触碰其它资源。
func (h *Handler) CronPreview(w http.ResponseWriter, r *http.Request) {
	expr := r.URL.Query().Get("expr")
	if expr == "" {
		writeCronPreviewError(w, cronPreviewInvalidCron, "expr is required")
		return
	}
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	if err := service.ValidateTimezone(tz); err != nil {
		writeCronPreviewError(w, cronPreviewInvalidTimezone, err.Error())
		return
	}

	const previewCount = 3
	// 语法合法但永不触发的表达式会返回短（或空）切片，而非错误：编辑器靠状态码
	// 区分「永远不会跑」与「cron 写错了」。
	occurrences, err := service.NextOccurrencesAfterUTC(expr, tz, time.Now().UTC(), previewCount)
	if err != nil {
		writeCronPreviewError(w, cronPreviewInvalidCron, err.Error())
		return
	}
	nextRuns := make([]string, 0, len(occurrences))
	for _, at := range occurrences {
		nextRuns = append(nextRuns, at.Format(time.RFC3339))
	}
	writeJSON(w, http.StatusOK, map[string]any{"next_runs": nextRuns})
}
