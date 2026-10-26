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
		_, trace, _, perCall, valid := parseSubagentFile(buckets[cid].Bytes(), sessionID)
		if !valid {
			continue
		}
		trace.SDKSourceSHA = sourceSHA
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
		_, childTraces, childCalls := codebuddyCollectSubagentUsage(env, sessionID, cid)
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

func mergeCodebuddyCallUsage(usage map[string]TokenUsage, call SubagentCall) {
	total := usage[call.Model]
	total.InputTokens += call.Usage.InputTokens
	total.OutputTokens += call.Usage.OutputTokens
	total.CacheReadTokens += call.Usage.CacheReadTokens
	total.CacheWriteTokens += call.Usage.CacheWriteTokens
	usage[call.Model] = total
}
