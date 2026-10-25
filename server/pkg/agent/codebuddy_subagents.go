package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SubagentTrace 是一次子 Agent 调用自己的链路标识：它与父链路同属本次执行，
// 但有自己的 CID / session / SDK trace，所以走 0:N 的 traces 上报，不并进父链路。
// SubagentCall 是一条可对账的子调用：它的 (CID, messageId, model) 与用量。
// 调用方拿它和本轮 stream 已观测的调用集合比对，决定是否还需要补用量。
type SubagentCall struct {
	CID       string
	MessageID string
	Model     string
	Usage     TokenUsage
}

type SubagentTrace struct {
	TraceID      string
	SessionID    string
	SDKTraceID   string
	SDKSourceSHA string
}

// agentToolResultPattern 是父会话里 Agent 工具结果的固定前缀，用来把"本轮确实调用过
// 哪个子 Agent"钉死在证据上，而不是按 session 目录猜。
const agentToolResultPattern = "[Agent ID: "

type subagentRecord struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Provider  struct {
		ConversationRequestID string          `json:"conversationRequestId"`
		MessageID             string          `json:"messageId"`
		TraceID               string          `json:"traceId"`
		Model                 string          `json:"model"`
		RawUsage              json.RawMessage `json:"rawUsage"`
	} `json:"providerData"`
	Content json.RawMessage `json:"content"`
}

type subagentRawUsage struct {
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	PromptCacheHit     int `json:"prompt_cache_hit_tokens"`
	PromptCacheMiss    int `json:"prompt_cache_miss_tokens"`
	CacheReadInput     int `json:"cache_read_input_tokens"`
	CacheCreationInput int `json:"cache_creation_input_tokens"`
	PromptCacheWrite   int `json:"prompt_cache_write_tokens"`
}

// codebuddyCollectSubagentUsage 采集本轮真正调用过的子 Agent。
//
// 归属链是硬的：父会话文件唯一 → 父文件里 conversationRequestId 等于本轮根 CID 的记录
// → 这些记录里 Agent 工具结果明确写出的 child ID → 只读该 child 自己的文件
// <父 session>/subagents/<childID>.jsonl。整条链任一环不唯一或不完整，就整份放弃，
// 不按 session 目录批量读，也不把上一轮的 child 当成这一轮。
func codebuddyCollectSubagentUsage(env map[string]string, parentSessionID, rootCID string) (map[string]TokenUsage, []SubagentTrace, []SubagentCall) {
	if parentSessionID == "" || rootCID == "" {
		return nil, nil, nil
	}
	dir := resolveSDKConfigDir(env)
	if dir == "" {
		return nil, nil, nil
	}
	parentFiles, err := filepath.Glob(filepath.Join(dir, "projects", "*", parentSessionID+".jsonl"))
	if err != nil || len(parentFiles) != 1 {
		return nil, nil, nil
	}
	parentPath := parentFiles[0]
	parentDir := filepath.Dir(parentPath)
	parentRaw, ok := readRegularFileBounded(parentPath)
	if !ok {
		return nil, nil, nil
	}
	childIDs := agentToolChildIDs(parentRaw, rootCID)
	if len(childIDs) == 0 {
		return nil, nil, nil
	}

	usage := map[string]TokenUsage{}
	var traces []SubagentTrace
	var callsOut []SubagentCall
	// 不同 child 文件之间也必须按 (CID, messageId) 唯一：同一个调用出现在两个文件里，
	// 归属就说不清了，整份放弃而不是把用量加两遍。
	globalCalls := map[[2]string]struct{}{}
	for _, childID := range childIDs {
		// 只在父文件所在的同一个项目目录下找，不跨项目。
		childPath := filepath.Join(parentDir, parentSessionID, "subagents", childID+".jsonl")
		raw, ok := readRegularFileBounded(childPath)
		if !ok {
			return nil, nil, nil
		}
		childUsage, trace, calls, perCall, ok := parseSubagentFile(raw, childID)
		if !ok {
			// 任何一个子文件不成立就不合并，避免"部分可信"的用量进账。
			return nil, nil, nil
		}
		for _, call := range calls {
			if _, dup := globalCalls[call]; dup {
				return nil, nil, nil
			}
			globalCalls[call] = struct{}{}
		}
		callsOut = append(callsOut, perCall...)
		for model, u := range childUsage {
			current := usage[model]
			current.InputTokens += u.InputTokens
			current.OutputTokens += u.OutputTokens
			current.CacheReadTokens += u.CacheReadTokens
			current.CacheWriteTokens += u.CacheWriteTokens
			usage[model] = current
		}
		traces = append(traces, trace)
	}
	if len(usage) == 0 {
		return nil, nil, nil
	}
	sort.Slice(traces, func(i, j int) bool { return traces[i].TraceID < traces[j].TraceID })
	return usage, traces, callsOut
}

// agentToolChildIDs 只认本轮根 CID 下、真正由 Agent/Task 工具返回的记录：
// type=function_call_result、name 属于 Agent/Task、callId 在同一 CID 里有对应的
// function_call，且 child id 出现在该记录的 output 文本里。assistant 正文提到旧 id、
// 或 Bash 输出里出现同样字样，都不算。
func agentToolChildIDs(parent []byte, rootCID string) []string {
	type call struct {
		name string
	}
	calls := map[string]string{} // callId → tool name
	results := []struct {
		callID string
		output string
	}{}

	scanner := bufio.NewScanner(bytes.NewReader(parent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Call struct {
				CallID string `json:"callId"`
			} `json:"call"`
			CallID    string          `json:"callId"`
			Output    json.RawMessage `json:"output"`
			SessionID string          `json:"sessionId"`
			Cwd       string          `json:"cwd"`
			Provider  struct {
				ConversationRequestID string `json:"conversationRequestId"`
			} `json:"providerData"`
		}
		if json.Unmarshal(line, &record) != nil {
			return nil
		}
		if record.Provider.ConversationRequestID != rootCID {
			continue
		}
		switch record.Type {
		case "function_call":
			if record.CallID != "" {
				calls[record.CallID] = record.Name
			}
		case "function_call_result":
			text := outputText(record.Output)
			results = append(results, struct {
				callID string
				output string
			}{record.CallID, text})
		}
	}
	if scanner.Err() != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var order []string
	for _, result := range results {
		name, ok := calls[result.callID]
		if !ok || (name != "Agent" && name != "Task") {
			continue
		}
		for _, id := range agentIDsInText(result.output) {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			order = append(order, id)
		}
	}
	return order
}

// outputText 取工具结果的文本部分；结构不对就返回空，不做字符串化兜底。
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asObject struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &asObject) == nil && asObject.Text != "" {
		return asObject.Text
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	return ""
}

// agentIDsInText 只接受 [Agent ID: agent-xxxxxxxx] 这种形态，且 id 必须是安全文件名。
func agentIDsInText(text string) []string {
	var out []string
	rest := text
	for {
		idx := strings.Index(rest, agentToolResultPattern)
		if idx < 0 {
			return out
		}
		rest = rest[idx+len(agentToolResultPattern):]
		end := strings.IndexByte(rest, ']')
		if end <= 0 {
			return out
		}
		id := strings.TrimSpace(rest[:end])
		if validAgentID(id) {
			out = append(out, id)
		}
		rest = rest[end:]
	}
}

// validAgentID 限制成"可以直接拼进路径"的形态：只允许 agent- 前缀 + [a-z0-9-]，
// 且不允许路径分隔符、点段或首尾连字符。真实 2.174.0 会给带连字符的 UUID
// （agent-3cf4114c-b604-4974-8278-8a6549f0b370），这类必须接受。
func validAgentID(id string) bool {
	if !strings.HasPrefix(id, "agent-") || len(id) > 64 {
		return false
	}
	body := id[len("agent-"):]
	if body == "" || strings.HasPrefix(body, "-") || strings.HasSuffix(body, "-") {
		return false
	}
	if strings.Contains(body, "..") || strings.ContainsAny(body, `/\`) {
		return false
	}
	for _, r := range body {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// parseSubagentFile 严格解析一个子会话文件：任何一行解析失败、字段不全、
// 同一 (CID,messageId) 用量不一致、trace 不唯一，都返回 false，不做部分采纳。
func parseSubagentFile(raw []byte, childID string) (map[string]TokenUsage, SubagentTrace, [][2]string, []SubagentCall, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	usage := map[string]TokenUsage{}
	type callKey struct{ cid, message string }
	seen := map[callKey]subagentRawUsage{}
	traceIDs := map[string]struct{}{}
	cids := map[string]struct{}{}
	sessions := map[string]struct{}{}
	cwds := map[string]struct{}{}
	sawUsage := false
	var perCall []SubagentCall

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record subagentRecord
		if json.Unmarshal(line, &record) != nil {
			return nil, SubagentTrace{}, nil, nil, false
		}
		pd := record.Provider
		if len(pd.RawUsage) == 0 {
			continue
		}
		var u subagentRawUsage
		if json.Unmarshal(pd.RawUsage, &u) != nil {
			return nil, SubagentTrace{}, nil, nil, false
		}
		if pd.TraceID == "" || pd.ConversationRequestID == "" || pd.MessageID == "" || pd.Model == "" ||
			record.SessionID == "" || len(pd.TraceID) != 32 || !isLowerHex(pd.TraceID) {
			return nil, SubagentTrace{}, nil, nil, false
		}
		sawUsage = true
		key := callKey{pd.ConversationRequestID, pd.MessageID}
		if old, ok := seen[key]; ok {
			if old != u {
				return nil, SubagentTrace{}, nil, nil, false
			}
			continue
		}
		seen[key] = u
		traceIDs[pd.TraceID] = struct{}{}
		cids[pd.ConversationRequestID] = struct{}{}
		sessions[record.SessionID] = struct{}{}
		if record.Cwd != "" {
			cwds[record.Cwd] = struct{}{}
		}
		perCall = append(perCall, SubagentCall{
			CID:       pd.ConversationRequestID,
			MessageID: pd.MessageID,
			Model:     pd.Model,
			Usage: TokenUsage{
				InputTokens:      int64(subagentInput(u)),
				OutputTokens:     int64(u.CompletionTokens),
				CacheReadTokens:  int64(subagentCacheRead(u)),
				CacheWriteTokens: int64(subagentCacheWrite(u)),
			},
		})
		total := usage[pd.Model]
		total.OutputTokens += int64(u.CompletionTokens)
		total.CacheReadTokens += int64(subagentCacheRead(u))
		total.CacheWriteTokens += int64(subagentCacheWrite(u))
		total.InputTokens += int64(subagentInput(u))
		usage[pd.Model] = total
	}
	if scanner.Err() != nil || !sawUsage || len(traceIDs) != 1 || len(cids) != 1 || len(sessions) != 1 || len(cwds) > 1 {
		return nil, SubagentTrace{}, nil, nil, false
	}
	var sdkTrace, cid, session string
	for id := range traceIDs {
		sdkTrace = id
	}
	for id := range cids {
		cid = id
	}
	for id := range sessions {
		session = id
	}
	digest := sha256.Sum256(raw)
	calls := make([][2]string, 0, len(seen))
	for key := range seen {
		calls = append(calls, [2]string{key.cid, key.message})
	}
	return usage, SubagentTrace{
		TraceID:      cid,
		SessionID:    session,
		SDKTraceID:   sdkTrace,
		SDKSourceSHA: hex.EncodeToString(digest[:]),
	}, calls, perCall, true
}

// readRegularFileBounded 复用父 SDK 读取的保护：只读普通文件、有上限、读完才算数。
func readRegularFileBounded(path string) ([]byte, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSDKLogBytes {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxSDKLogBytes+1))
	if err != nil || int64(len(raw)) > maxSDKLogBytes {
		return nil, false
	}
	return raw, true
}

func subagentCacheRead(u subagentRawUsage) int {
	if u.CacheReadInput > 0 {
		return u.CacheReadInput
	}
	return u.PromptCacheHit
}

func subagentCacheWrite(u subagentRawUsage) int {
	if u.PromptCacheWrite > 0 {
		return u.PromptCacheWrite
	}
	return u.CacheCreationInput
}

func subagentInput(u subagentRawUsage) int {
	rest := u.PromptTokens - subagentCacheRead(u) - subagentCacheWrite(u)
	if rest < 0 {
		return 0
	}
	return rest
}
