package handler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

type toolCallChainResponse struct {
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id,omitempty"`
	Tool           string         `json:"tool,omitempty"`
	Status         string         `json:"status"`
	UseSeq         int            `json:"use_seq,omitempty"`
	ResultSeq      int            `json:"result_seq,omitempty"`
	UseSpanID      string         `json:"use_span_id,omitempty"`
	ResultSpanID   string         `json:"result_span_id,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	Output         string         `json:"output,omitempty"`
	DurationMs     int64          `json:"duration_ms,omitempty"`
	ResultCategory string         `json:"result_category,omitempty"`
	FailureSignal  bool           `json:"failure_signal"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	Summary        string         `json:"summary"`
	CreatedAt      string         `json:"created_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
}

type toolCallSummaryResponse struct {
	Tool                   string         `json:"tool"`
	TotalCalls             int            `json:"total_calls"`
	PairedCalls            int            `json:"paired_calls"`
	MissingResultCalls     int            `json:"missing_result_calls"`
	OrphanResultCalls      int            `json:"orphan_result_calls"`
	AverageDurationMs      int64          `json:"average_duration_ms,omitempty"`
	MaxDurationMs          int64          `json:"max_duration_ms,omitempty"`
	SlowestToolCallChainID string         `json:"slowest_tool_call_chain_id,omitempty"`
	ResultCategories       map[string]int `json:"result_categories,omitempty"`
	FailureSignalCalls     int            `json:"failure_signal_calls"`
	NeedsAttention         bool           `json:"needs_attention"`
	Summary                string         `json:"summary"`
}

func buildToolCallChains(messages []protocol.TaskMessagePayload) []toolCallChainResponse {
	chains := []toolCallChainResponse{}
	pendingByTool := map[string][]int{}
	for _, message := range messages {
		switch message.Type {
		case "tool_use":
			tool := strings.TrimSpace(message.Tool)
			if tool == "" {
				tool = "未记录工具"
			}
			chain := toolCallChainResponse{
				ID:             fmt.Sprintf("tool:%s:%d", tool, message.Seq),
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "缺少结果",
				UseSeq:         message.Seq,
				UseSpanID:      fmt.Sprintf("message:%d", message.Seq),
				Input:          message.Input,
				ResultCategory: "未返回",
				Summary:        truncateRunes("工具调用："+firstNonEmpty(tool, toolCallInputSummary(message.Input)), 240),
				CreatedAt:      message.CreatedAt,
			}
			chains = append(chains, chain)
			pendingByTool[tool] = append(pendingByTool[tool], len(chains)-1)
		case "tool_result":
			tool := strings.TrimSpace(message.Tool)
			if tool == "" {
				tool = "未记录工具"
			}
			pending := pendingByTool[tool]
			if len(pending) > 0 {
				index := pending[0]
				pendingByTool[tool] = pending[1:]
				chains[index].Status = "已配对"
				chains[index].ResultSeq = message.Seq
				chains[index].ResultSpanID = fmt.Sprintf("message:%d", message.Seq)
				chains[index].Output = message.Output
				chains[index].DurationMs = toolCallDurationBetween(chains[index].CreatedAt, message.CreatedAt)
				chains[index].ResultCategory = "已返回"
				if failureSignal, failureReason := toolCallFailureSignal(tool, message.Output); failureSignal {
					chains[index].FailureSignal = true
					chains[index].FailureReason = failureReason
					chains[index].ResultCategory = "异常线索"
				}
				chains[index].CompletedAt = message.CreatedAt
				chains[index].Summary = truncateRunes(fmt.Sprintf("工具 %s 已配对：调用 #%d，结果 #%d", tool, chains[index].UseSeq, message.Seq), 240)
				continue
			}
			chains = append(chains, toolCallChainResponse{
				ID:             fmt.Sprintf("tool:%s:result:%d", tool, message.Seq),
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "孤立结果",
				ResultSeq:      message.Seq,
				ResultSpanID:   fmt.Sprintf("message:%d", message.Seq),
				Output:         message.Output,
				ResultCategory: "孤立返回",
				Summary:        truncateRunes("工具结果没有找到对应调用："+firstNonEmpty(message.Output, tool), 240),
				CompletedAt:    message.CreatedAt,
			})
		}
	}
	return chains
}

func buildToolCallSummary(chains []toolCallChainResponse) []toolCallSummaryResponse {
	if len(chains) == 0 {
		return []toolCallSummaryResponse{}
	}
	byTool := map[string]*toolCallSummaryResponse{}
	durationSums := map[string]int64{}
	durationCounts := map[string]int64{}
	for _, chain := range chains {
		tool := strings.TrimSpace(chain.Tool)
		if tool == "" {
			tool = "未记录工具"
		}
		item := byTool[tool]
		if item == nil {
			item = &toolCallSummaryResponse{Tool: tool, ResultCategories: map[string]int{}}
			byTool[tool] = item
		}
		item.TotalCalls++
		switch chain.Status {
		case "已配对":
			item.PairedCalls++
		case "缺少结果":
			item.MissingResultCalls++
		case "孤立结果":
			item.OrphanResultCalls++
		}
		category := strings.TrimSpace(chain.ResultCategory)
		if category == "" {
			category = "未归类"
		}
		item.ResultCategories[category]++
		if chain.FailureSignal {
			item.FailureSignalCalls++
		}
		if chain.DurationMs > 0 {
			durationSums[tool] += chain.DurationMs
			durationCounts[tool]++
			if chain.DurationMs > item.MaxDurationMs {
				item.MaxDurationMs = chain.DurationMs
				item.SlowestToolCallChainID = chain.ID
			}
		}
	}
	result := make([]toolCallSummaryResponse, 0, len(byTool))
	for tool, item := range byTool {
		if count := durationCounts[tool]; count > 0 {
			item.AverageDurationMs = durationSums[tool] / count
		}
		item.NeedsAttention = item.MissingResultCalls > 0 || item.OrphanResultCalls > 0 || item.FailureSignalCalls > 0
		item.Summary = fmt.Sprintf("%s：调用 %d 次，已配对 %d 次，缺少结果 %d 次，孤立结果 %d 次，异常线索 %d 次，平均耗时 %dms，最慢 %dms", tool, item.TotalCalls, item.PairedCalls, item.MissingResultCalls, item.OrphanResultCalls, item.FailureSignalCalls, item.AverageDurationMs, item.MaxDurationMs)
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].NeedsAttention != result[j].NeedsAttention {
			return result[i].NeedsAttention
		}
		if result[i].MaxDurationMs != result[j].MaxDurationMs {
			return result[i].MaxDurationMs > result[j].MaxDurationMs
		}
		if result[i].TotalCalls != result[j].TotalCalls {
			return result[i].TotalCalls > result[j].TotalCalls
		}
		return result[i].Tool < result[j].Tool
	})
	return result
}

func toolCallInputSummary(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(raw)
}

func toolCallDurationBetween(start, end string) int64 {
	startAt, startErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(start))
	endAt, endErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(end))
	if startErr != nil || endErr != nil || endAt.Before(startAt) {
		return 0
	}
	return endAt.Sub(startAt).Milliseconds()
}

func toolCallFailureSignal(tool, output string) (bool, string) {
	displayOutput := toolCallOutputText(output)
	normalized := strings.ToLower(strings.TrimSpace(displayOutput))
	if normalized == "" {
		return false, ""
	}
	if strings.Contains(normalized, "<tool_use_error>") {
		return true, "工具调用返回错误"
	}
	if exitCode, ok := toolCallExitCode(normalized); ok {
		if exitCode == 0 {
			return false, ""
		}
		return true, fmt.Sprintf("工具结果包含非零退出码 %d", exitCode)
	}
	if statusCode := toolCallHTTPStatusCode(normalized); statusCode >= 400 {
		return true, fmt.Sprintf("工具结果包含 HTTP 状态码 %d", statusCode)
	}
	contentOnly := false
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read", "grep", "glob":
		contentOnly = true
	}
	if contentOnly || toolCallOutputIsReadOnlyCommand(normalized) || toolCallOutputHasOnlySuccessFailureCounters(normalized) {
		return false, ""
	}
	if reason := toolCallStructuredFailureReason(displayOutput); reason != "" {
		return true, reason
	}
	return false, ""
}

func toolCallOutputText(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return output
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parts); err != nil || len(parts) == 0 {
		return output
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return output
	}
	return strings.Join(texts, "\n")
}

func toolCallOutputIsReadOnlyCommand(output string) bool {
	command := ""
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "command:") {
			command = strings.TrimSpace(strings.TrimPrefix(line, "command:"))
			break
		}
	}
	segments := regexp.MustCompile(`\s+(?:&&|\|\|)\s+|;\s*`).Split(command, -1)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment != "" && !strings.HasPrefix(segment, "cd ") && !strings.HasPrefix(segment, "export ") {
			command = segment
			break
		}
	}
	if command == "" {
		return false
	}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "stderr:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "stderr:"))
			if value != "" && value != "(empty)" {
				return false
			}
		}
	}
	fields := strings.Fields(command)
	executable := ""
	if len(fields) > 0 {
		executable = fields[0]
		if slash := strings.LastIndex(executable, "/"); slash >= 0 {
			executable = executable[slash+1:]
		}
	}
	readOnlyShell := false
	switch executable {
	case "cat", "sed", "nl", "ls", "head", "tail", "rg", "grep", "find":
		readOnlyShell = true
	}
	readsLocalArtifact := (executable == "curl" || executable == "wget") && (strings.Contains(command, "/uploads/") || strings.Contains(command, "/api/attachments/"))
	return strings.HasPrefix(command, "git diff") || strings.HasPrefix(command, "git branch") || strings.HasPrefix(command, "git show") || strings.HasPrefix(command, "git status") || strings.HasPrefix(command, "git log") || strings.HasPrefix(command, "multica issue comment list") || readOnlyShell || readsLocalArtifact
}

func toolCallStructuredFailureReason(output string) string {
	makeError := regexp.MustCompile(`^make(?:\[\d+\])?: \*\*\* .*\berror\s+\d+\b`)
	for _, rawLine := range strings.Split(output, "\n") {
		lower := strings.ToLower(strings.TrimSpace(rawLine))
		switch {
		case strings.HasPrefix(lower, "error:"):
			return "工具结果包含错误信息"
		case strings.HasPrefix(lower, "traceback"), strings.HasPrefix(lower, "runtimeerror:"), strings.HasPrefix(lower, "exception"):
			return "工具结果包含异常信息"
		case strings.HasPrefix(lower, "--- fail:"), strings.HasPrefix(lower, "fail\t"), strings.HasPrefix(lower, "fail "), strings.HasPrefix(lower, "command failed"):
			return "工具结果包含失败信息"
		case strings.HasPrefix(lower, "panic:"), strings.HasPrefix(lower, "fatal"):
			return "工具结果包含崩溃信息"
		case makeError.MatchString(lower):
			return "工具结果包含错误信息"
		}
	}
	return ""
}

func toolCallOutputHasOnlySuccessFailureCounters(output string) bool {
	for _, needle := range []string{"error:", "exception", "panic", "timeout", "timed out", "permission denied", "http 500", "status 500", "错误", "异常", "超时", "无权限"} {
		if strings.Contains(output, needle) {
			return false
		}
	}
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`\b0\s+(?:failed|failure|failures)\b`),
		regexp.MustCompile(`\b0\s+chart\(s\)\s+failed\b`),
		regexp.MustCompile(`\b0\s+test(?:s)?\s+failed\b`),
	} {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

func toolCallHTTPStatusCode(output string) int {
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`\bhttp(?:/[\d.]+)?\s*(?:status\s*)?([45]\d{2})\b`),
		regexp.MustCompile(`\bstatus(?:\s*code)?\s*[:=]?\s*([45]\d{2})\b`),
	} {
		if matches := pattern.FindStringSubmatch(output); len(matches) >= 2 {
			if statusCode, err := strconv.Atoi(matches[1]); err == nil {
				return statusCode
			}
		}
	}
	return 0
}

func toolCallExitCode(output string) (int, bool) {
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`\bexit\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
		regexp.MustCompile(`\bexited\s+with\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
	} {
		if matches := pattern.FindStringSubmatch(output); len(matches) >= 2 {
			if exitCode, err := strconv.Atoi(matches[1]); err == nil {
				return exitCode, true
			}
		}
	}
	return 0, false
}
