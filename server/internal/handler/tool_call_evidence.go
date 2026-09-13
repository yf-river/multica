// 本文件把任务消息流（protocol.TaskMessagePayload）里的工具调用整理成「证据」，
// 供前端展示 agent 干活的过程：
//
//   - buildToolCallChains：把 tool_use / tool_result 两种消息配对成一条条「调用链」
//     （调用 → 结果），配对不上的各自标记为「缺少结果」/「孤立结果」；
//   - buildToolCallSummary：按工具聚合出统计（总次数、配对/缺失/孤立数、耗时、异常
//     线索等），并给出排序与「需要关注」标记；
//   - toolCallFailureSignal 等启发式：从工具输出文本里嗅探失败信号（<tool_use_error>、
//     非零退出码、≥400 的 HTTP 状态、error:/panic: 等结构化行），供 UI 高亮问题调用。
//
// 注意：这里的「工具调用—结果」配对是尽力而为的展示逻辑，按同一工具名先进先出配对；
// 不等于 agent 执行轨迹的真实因果。
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

// toolCallChainResponse 是一条「工具调用链」的对外结构：从一次 tool_use 出发，
// 配到其 tool_result（成功配对），或保持「缺少结果 / 孤立结果」状态。
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

// toolCallSummaryResponse 是按工具聚合后的统计结构。
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

// buildToolCallChains 把消息流转成调用链列表。tool_use 创建一条「等待结果」的链；
// 随后的 tool_result 按「同工具名先进先出」配对（同一工具可多次调用）。没有待配对
// 调用的结果单独记为「孤立结果」。
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

// buildToolCallSummary 按工具聚合调用链的统计，并按「需要关注 → 最慢 → 次数多 →
// 名称」排序，方便 UI 把问题工具顶到前面。
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

// toolCallInputSummary 把调用入参序列化成短字符串，用于摘要文案。
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

// toolCallDurationBetween 用两个 RFC3339 时间戳算出调用耗时（毫秒）；解析失败或结束
// 早于开始都返回 0。
func toolCallDurationBetween(start, end string) int64 {
	startAt, startErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(start))
	endAt, endErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(end))
	if startErr != nil || endErr != nil || endAt.Before(startAt) {
		return 0
	}
	return endAt.Sub(startAt).Milliseconds()
}

// toolCallFailureSignal 从工具输出里嗅探失败信号，返回是否失败及一句人话原因。
// 判定顺序：工具错误标记 → 非零退出码 → ≥400 的 HTTP 状态 → 结构化错误行
// （error:/panic: 等）；read/grep/glob 与只读 shell 命令、纯成功/失败计数器的输出
// 不算失败。
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

// toolCallOutputText 若输出是 JSON 数组（常见的 text 分段格式），抽出所有 text 段
// 拼成纯文本返回；否则原样返回。让后续匹配不用关心是哪种形态。
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

// toolCallOutputIsReadOnlyCommand 判断输出里的 command: 行是否属于只读/本地制品
// 命令（cat、git diff、rg、访问本地上传文件的 curl 等）。这类命令的非空输出通常是
// 正常内容而不是失败证据。
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

// toolCallStructuredFailureReason 从输出行里识别 error: / traceback / panic: /
// make *** error 等结构化失败行，命中则返回对应的中文原因。
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

// toolCallOutputHasOnlySuccessFailureCounters 判断输出是否只是「成功/失败计数器」
// 之类的统计文本（如「0 tests failed」）。这类输出不含失败关键词且失败计数为 0，
// 视为正常，不当作失败信号。
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

// toolCallHTTPStatusCode 从输出文本里提取 4xx/5xx HTTP 状态码；没找到返回 0。
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

// toolCallExitCode 从输出文本里提取进程退出码；找不到返回 ok=false。
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
