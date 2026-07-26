package handler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/util/prompteval"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func buildPromptEvaluationToolCallChains(messages []protocol.TaskMessagePayload) []PromptEvaluationToolCallChainResponse {
	chains := []PromptEvaluationToolCallChainResponse{}
	pendingByTool := map[string][]int{}
	for _, message := range messages {
		switch message.Type {
		case "tool_use":
			tool := strings.TrimSpace(message.Tool)
			if tool == "" {
				tool = "未记录工具"
			}
			callID := promptEvaluationToolCallID(message)
			if callID == "" {
				callID = fmt.Sprintf("%s:%d", tool, message.Seq)
			}
			chain := PromptEvaluationToolCallChainResponse{
				ID:             "tool:" + callID,
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "缺少结果",
				UseSeq:         message.Seq,
				UseSpanID:      fmt.Sprintf("message:%d", message.Seq),
				Input:          message.Input,
				ResultCategory: "未返回",
				Summary:        prompteval.TruncatePromptEvaluationEvidence("工具调用："+firstNonEmptyPromptEvaluationString(tool, promptEvaluationEvidenceSummaryString(message.Input)), 240),
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
				chains[index].DurationMs = promptEvaluationDurationBetween(chains[index].CreatedAt, message.CreatedAt)
				chains[index].ResultCategory = "已返回"
				if failureSignal, failureReason := promptEvaluationToolFailureSignal(tool, message.Output); failureSignal {
					chains[index].FailureSignal = true
					chains[index].FailureReason = failureReason
					chains[index].ResultCategory = "异常线索"
				}
				chains[index].CompletedAt = message.CreatedAt
				chains[index].Summary = prompteval.TruncatePromptEvaluationEvidence(
					fmt.Sprintf("工具 %s 已配对：调用 #%d，结果 #%d", tool, chains[index].UseSeq, message.Seq),
					240,
				)
				continue
			}
			callID := promptEvaluationToolCallID(message)
			if callID == "" {
				callID = fmt.Sprintf("%s:result:%d", tool, message.Seq)
			}
			chains = append(chains, PromptEvaluationToolCallChainResponse{
				ID:             "tool:" + callID,
				TaskID:         message.TaskID,
				Tool:           tool,
				Status:         "孤立结果",
				ResultSeq:      message.Seq,
				ResultSpanID:   fmt.Sprintf("message:%d", message.Seq),
				Output:         message.Output,
				ResultCategory: "孤立返回",
				Summary:        prompteval.TruncatePromptEvaluationEvidence("工具结果没有找到对应调用："+firstNonEmptyPromptEvaluationString(message.Output, tool), 240),
				CompletedAt:    message.CreatedAt,
			})
		}
	}
	return chains
}

func buildPromptEvaluationToolCallSummary(chains []PromptEvaluationToolCallChainResponse) []PromptEvaluationToolCallSummaryResponse {
	if len(chains) == 0 {
		return []PromptEvaluationToolCallSummaryResponse{}
	}
	byTool := map[string]*PromptEvaluationToolCallSummaryResponse{}
	durationSums := map[string]int64{}
	durationCounts := map[string]int64{}
	for _, chain := range chains {
		tool := strings.TrimSpace(chain.Tool)
		if tool == "" {
			tool = "未记录工具"
		}
		item := byTool[tool]
		if item == nil {
			item = &PromptEvaluationToolCallSummaryResponse{
				Tool:             tool,
				ResultCategories: map[string]int{},
			}
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
	result := make([]PromptEvaluationToolCallSummaryResponse, 0, len(byTool))
	for tool, item := range byTool {
		if count := durationCounts[tool]; count > 0 {
			item.AverageDurationMs = durationSums[tool] / count
		}
		item.NeedsAttention = item.MissingResultCalls > 0 || item.OrphanResultCalls > 0 || item.FailureSignalCalls > 0
		item.Summary = fmt.Sprintf(
			"%s：调用 %d 次，已配对 %d 次，缺少结果 %d 次，孤立结果 %d 次，异常线索 %d 次，平均耗时 %dms，最慢 %dms",
			tool,
			item.TotalCalls,
			item.PairedCalls,
			item.MissingResultCalls,
			item.OrphanResultCalls,
			item.FailureSignalCalls,
			item.AverageDurationMs,
			item.MaxDurationMs,
		)
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

func buildPromptEvaluationToolCallChainByMessageSeq(chains []PromptEvaluationToolCallChainResponse) map[int]PromptEvaluationToolCallChainResponse {
	result := map[int]PromptEvaluationToolCallChainResponse{}
	for _, chain := range chains {
		if chain.UseSeq > 0 {
			result[chain.UseSeq] = chain
		}
		if chain.ResultSeq > 0 {
			result[chain.ResultSeq] = chain
		}
	}
	return result
}

func promptEvaluationDurationBetween(start string, end string) int64 {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" || end == "" {
		return 0
	}
	startAt, startErr := time.Parse(time.RFC3339Nano, start)
	endAt, endErr := time.Parse(time.RFC3339Nano, end)
	if startErr != nil || endErr != nil || endAt.Before(startAt) {
		return 0
	}
	return endAt.Sub(startAt).Milliseconds()
}

func promptEvaluationToolFailureSignal(tool string, output string) (bool, string) {
	displayOutput := promptEvaluationToolOutputText(output)
	normalized := strings.ToLower(strings.TrimSpace(displayOutput))
	if normalized == "" {
		return false, ""
	}
	if promptEvaluationToolOutputHasToolUseError(normalized) {
		return true, "工具调用返回错误"
	}
	if exitCode, ok := promptEvaluationToolExitCode(normalized); ok {
		if exitCode == 0 {
			return false, ""
		}
		return true, fmt.Sprintf("工具结果包含非零退出码 %d", exitCode)
	}
	if statusCode := promptEvaluationToolHTTPStatusCode(normalized); statusCode >= 400 {
		return true, fmt.Sprintf("工具结果包含 HTTP 状态码 %d", statusCode)
	}
	if promptEvaluationToolResultIsContentOnly(tool) || promptEvaluationToolOutputIsReadOnlyCommand(normalized) {
		return false, ""
	}
	if promptEvaluationToolOutputHasOnlySuccessFailureCounters(normalized) {
		return false, ""
	}
	if reason := promptEvaluationToolStructuredFailureReason(displayOutput); reason != "" {
		return true, reason
	}
	return false, ""
}

func promptEvaluationToolOutputText(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || !strings.HasPrefix(trimmed, "[") {
		return output
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parts); err != nil || len(parts) == 0 {
		return output
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return output
	}
	return strings.Join(texts, "\n")
}

func promptEvaluationToolOutputIsReadOnlyCommand(output string) bool {
	command := promptEvaluationToolMeaningfulCommand(promptEvaluationToolOutputCommand(output))
	if command == "" {
		return false
	}
	if promptEvaluationToolOutputHasNonEmptyStderr(output) {
		return false
	}
	return strings.HasPrefix(command, "git diff") ||
		strings.HasPrefix(command, "git branch") ||
		strings.HasPrefix(command, "git show") ||
		strings.HasPrefix(command, "git status") ||
		strings.HasPrefix(command, "git log") ||
		strings.HasPrefix(command, "multica issue comment list") ||
		promptEvaluationToolCommandIsReadOnlyShell(command) ||
		promptEvaluationToolCommandReadsLocalArtifact(command)
}

func promptEvaluationToolOutputCommand(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "command:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "command:"))
	}
	return ""
}

func promptEvaluationToolMeaningfulCommand(command string) string {
	segments := regexp.MustCompile(`\s+(?:&&|\|\|)\s+|;\s*`).Split(command, -1)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" || strings.HasPrefix(segment, "cd ") || strings.HasPrefix(segment, "export ") {
			continue
		}
		return segment
	}
	return strings.TrimSpace(command)
}

func promptEvaluationToolOutputHasNonEmptyStderr(output string) bool {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "stderr:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "stderr:"))
		if value != "" && value != "(empty)" {
			return true
		}
	}
	return false
}

func promptEvaluationToolOutputHasToolUseError(output string) bool {
	return strings.Contains(output, "<tool_use_error>")
}

func promptEvaluationToolStructuredFailureReason(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "error:"):
			return "工具结果包含错误信息"
		case strings.HasPrefix(lower, "traceback"):
			return "工具结果包含异常信息"
		case strings.HasPrefix(lower, "runtimeerror:") || strings.HasPrefix(lower, "exception"):
			return "工具结果包含异常信息"
		case strings.HasPrefix(lower, "--- fail:") || strings.HasPrefix(lower, "fail\t") || strings.HasPrefix(lower, "fail "):
			return "工具结果包含失败信息"
		case strings.HasPrefix(lower, "panic:"):
			return "工具结果包含崩溃信息"
		case strings.HasPrefix(lower, "fatal"):
			return "工具结果包含崩溃信息"
		case strings.HasPrefix(lower, "command failed"):
			return "工具结果包含失败信息"
		case regexp.MustCompile(`^make(?:\[\d+\])?: \*\*\* .*\berror\s+\d+\b`).MatchString(lower):
			return "工具结果包含错误信息"
		}
	}
	return ""
}

func promptEvaluationToolCommandIsReadOnlyShell(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	executable := fields[0]
	if slash := strings.LastIndex(executable, "/"); slash >= 0 {
		executable = executable[slash+1:]
	}
	switch executable {
	case "cat", "sed", "nl", "ls", "head", "tail", "rg", "grep", "find":
		return true
	default:
		return false
	}
}

func promptEvaluationToolCommandReadsLocalArtifact(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	executable := fields[0]
	if slash := strings.LastIndex(executable, "/"); slash >= 0 {
		executable = executable[slash+1:]
	}
	if executable != "curl" && executable != "wget" {
		return false
	}
	return strings.Contains(command, "/uploads/") || strings.Contains(command, "/api/attachments/")
}

func promptEvaluationToolOutputHasOnlySuccessFailureCounters(output string) bool {
	for _, fatalNeedle := range []string{"error:", "exception", "panic", "timeout", "timed out", "permission denied", "http 500", "status 500", "错误", "异常", "超时", "无权限"} {
		if strings.Contains(output, fatalNeedle) {
			return false
		}
	}
	successCounters := []*regexp.Regexp{
		regexp.MustCompile(`\b0\s+(?:failed|failure|failures)\b`),
		regexp.MustCompile(`\b0\s+chart\(s\)\s+failed\b`),
		regexp.MustCompile(`\b0\s+test(?:s)?\s+failed\b`),
	}
	for _, pattern := range successCounters {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

func promptEvaluationToolResultIsContentOnly(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "read", "grep", "glob":
		return true
	default:
		return false
	}
}

func promptEvaluationToolHTTPStatusCode(output string) int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bhttp(?:/[\d.]+)?\s*(?:status\s*)?([45]\d{2})\b`),
		regexp.MustCompile(`\bstatus(?:\s*code)?\s*[:=]?\s*([45]\d{2})\b`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}
		statusCode, err := strconv.Atoi(matches[1])
		if err == nil {
			return statusCode
		}
	}
	return 0
}

func promptEvaluationToolExitCode(output string) (int, bool) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bexit\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
		regexp.MustCompile(`\bexited\s+with\s+(?:status|code)\s*[:=]?\s*(\d+)\b`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}
		exitCode, err := strconv.Atoi(matches[1])
		if err == nil {
			return exitCode, true
		}
	}
	return 0, false
}

func promptEvaluationToolCallID(message protocol.TaskMessagePayload) string {
	for _, key := range []string{"tool_call_id", "call_id", "id", "工具调用ID", "调用ID"} {
		if value := strings.TrimSpace(prompteval.StringFromAny(message.Input[key])); value != "" {
			return value
		}
	}
	return ""
}

func incrementPromptEvaluationSummary(summary map[string]any, key string) {
	current, _ := summary[key].(int)
	summary[key] = current + 1
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func promptEvaluationTraceSpanKind(eventType string) string {
	switch {
	case strings.HasPrefix(eventType, "task."):
		return "生命周期"
	case eventType == "llm.usage_unavailable":
		return "模型用量缺失"
	case strings.HasPrefix(eventType, "llm."):
		return "模型用量"
	default:
		return "trace事件"
	}
}

func promptEvaluationTraceSpanSummary(event TaskTraceEventResponse) string {
	parts := []string{event.EventName}
	if event.FailureReason != "" {
		parts = append(parts, "失败原因："+event.FailureReason)
	}
	if event.Provider != "" || event.Model != "" {
		parts = append(parts, "模型："+strings.Trim(strings.Join([]string{event.Provider, event.Model}, "/"), "/"))
	}
	if tokenTotal := event.InputTokens + event.OutputTokens + event.CacheReadTokens + event.CacheWriteTokens; tokenTotal > 0 {
		parts = append(parts, fmt.Sprintf("token：%d", tokenTotal))
	}
	return prompteval.TruncatePromptEvaluationEvidence(strings.Join(nonEmptyStrings(parts...), "；"), 240)
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func promptEvaluationTraceDurationMs(event TaskTraceEventResponse) int64 {
	for _, value := range []*int64{event.TotalMs, event.RunMs, event.DurationMs, event.QueueWaitMs} {
		if value != nil {
			return *value
		}
	}
	return 0
}

func promptEvaluationMessageSpanKind(messageType string) string {
	switch messageType {
	case "tool_use":
		return "工具调用"
	case "tool_result":
		return "工具结果"
	case "thinking":
		return "思考消息"
	case "error":
		return "错误消息"
	default:
		return "Agent消息"
	}
}

func promptEvaluationMessageSpanName(messageType string) string {
	switch messageType {
	case "tool_use":
		return "工具调用"
	case "tool_result":
		return "工具结果"
	case "thinking":
		return "思考过程"
	case "error":
		return "错误输出"
	default:
		return "文本输出"
	}
}
