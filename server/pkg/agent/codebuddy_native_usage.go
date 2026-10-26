package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
)

// 只读取流中确认属于本次执行的 CID；会话累计用量和旧轮次不进入账单。
func codebuddyNativeAccounting(env map[string]string, sessionID string, cids map[string]bool) (map[string]TokenUsage, []SubagentTrace, []SubagentCall) {
	if sessionID == "" || len(cids) == 0 {
		return nil, nil, nil
	}
	directory := resolveSDKConfigDir(env)
	if directory == "" {
		return nil, nil, nil
	}
	files, err := filepath.Glob(filepath.Join(directory, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(files) != 1 {
		return nil, nil, nil
	}
	raw, ok := readRegularFileBounded(files[0])
	if !ok {
		return nil, nil, nil
	}
	buckets := map[string]*bytes.Buffer{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record subagentRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			return nil, nil, nil
		}
		cid := record.Provider.ConversationRequestID
		if !cids[cid] || len(record.Provider.RawUsage) == 0 {
			continue
		}
		if record.SessionID != sessionID {
			return nil, nil, nil
		}
		var usage map[string]json.RawMessage
		if json.Unmarshal(record.Provider.RawUsage, &usage) != nil || usage["prompt_tokens"] == nil || usage["completion_tokens"] == nil {
			return nil, nil, nil
		}
		if buckets[cid] == nil {
			buckets[cid] = &bytes.Buffer{}
		}
		buckets[cid].Write(scanner.Bytes())
		buckets[cid].WriteByte('\n')
	}
	if scanner.Err() != nil {
		return nil, nil, nil
	}
	sum := sha256.Sum256(raw)
	sourceSHA := hex.EncodeToString(sum[:])
	usage := map[string]TokenUsage{}
	var traces []SubagentTrace
	var calls []SubagentCall
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, cid := range ids {
		trace, perCall, valid := parseCodebuddyNativeCalls(buckets[cid].Bytes(), sessionID, sourceSHA)
		if !valid {
			continue
		}
		traces = append(traces, trace)
		calls = append(calls, perCall...)
	}
	seen := map[codebuddyUsageCall]bool{}
	for _, call := range calls {
		key := codebuddyUsageCall{request: call.CID, message: call.MessageID, model: call.Model}
		seen[key] = true
		mergeCodebuddyCallUsage(usage, call)
	}
	for _, cid := range ids {
		_, childTraces, childCalls := codebuddyCollectScopedSubagentUsage(env, sessionID, cid, cids)
		traces = append(traces, childTraces...)
		for _, call := range childCalls {
			key := codebuddyUsageCall{request: call.CID, message: call.MessageID, model: call.Model}
			if seen[key] {
				continue
			}
			seen[key] = true
			mergeCodebuddyCallUsage(usage, call)
			calls = append(calls, call)
		}
	}
	if len(usage) == 0 {
		return nil, traces, calls
	}
	return usage, traces, calls
}

// Goal 的同一 CID 可以跨越多个 SDK trace。逐 trace 校验、跨 trace 按模型响应去重，
// 多链路的 CID 不填写单一 SDK 溯源；每次调用的实际 trace 和文件指纹单独保留。
func parseCodebuddyNativeCalls(raw []byte, sessionID, sourceSHA string) (SubagentTrace, []SubagentCall, bool) {
	groups := map[string]*bytes.Buffer{}
	var order []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record subagentRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Provider.TraceID == "" {
			return SubagentTrace{}, nil, false
		}
		id := record.Provider.TraceID
		if groups[id] == nil {
			groups[id] = &bytes.Buffer{}
			order = append(order, id)
		}
		groups[id].Write(scanner.Bytes())
		groups[id].WriteByte('\n')
	}
	if scanner.Err() != nil || len(order) == 0 {
		return SubagentTrace{}, nil, false
	}
	seen := map[string]SubagentCall{}
	var calls []SubagentCall
	var trace SubagentTrace
	for _, id := range order {
		_, parsed, _, perCall, valid := parseSubagentFile(groups[id].Bytes(), sessionID)
		if !valid || trace.TraceID != "" && parsed.TraceID != trace.TraceID {
			return SubagentTrace{}, nil, false
		}
		trace = parsed
		for _, call := range perCall {
			if old, exists := seen[call.MessageID]; exists {
				if old.Model != call.Model || old.Usage != call.Usage {
					return SubagentTrace{}, nil, false
				}
				continue
			}
			call.SDKSourceSHA = sourceSHA
			seen[call.MessageID] = call
			calls = append(calls, call)
		}
	}
	trace.SDKSourceSHA = sourceSHA
	if len(order) > 1 {
		trace.SDKTraceID = ""
		trace.SDKSourceSHA = ""
	}
	return trace, calls, true
}

func mergeCodebuddyCallUsage(usage map[string]TokenUsage, call SubagentCall) {
	total := usage[call.Model]
	total.InputTokens += call.Usage.InputTokens
	total.OutputTokens += call.Usage.OutputTokens
	total.CacheReadTokens += call.Usage.CacheReadTokens
	total.CacheWriteTokens += call.Usage.CacheWriteTokens
	usage[call.Model] = total
}
