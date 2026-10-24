package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// SubagentTrace 是一次子 Agent 调用自己的链路标识：它与父链路同属本次执行，
// 但有自己的 CID / session / SDK trace，所以走 0:N 的 traces 上报，不并进父链路。
type SubagentTrace struct {
	TraceID      string // 子调用的根请求 id（CID），与外部 trace 对齐
	SessionID    string
	SDKTraceID   string
	SDKSourceSHA string
}

type subagentRecord struct {
	SessionID string `json:"sessionId"`
	Provider  struct {
		ConversationRequestID string          `json:"conversationRequestId"`
		MessageID             string          `json:"messageId"`
		TraceID               string          `json:"traceId"`
		Model                 string          `json:"model"`
		RawUsage              json.RawMessage `json:"rawUsage"`
	} `json:"providerData"`
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

// codebuddyCollectSubagentUsage 只读本轮父 session 目录下的 subagents/*.jsonl：
// <配置目录>/projects/<工作目录>/<父 session>/subagents/*.jsonl。别的轮次、别的任务
// 一律不看。同一调用按 (CID, messageId) 去重；同一 key 内容不一致就整份放弃。
func codebuddyCollectSubagentUsage(env map[string]string, parentSessionID string) (map[string]TokenUsage, []SubagentTrace) {
	if parentSessionID == "" {
		return nil, nil
	}
	dir := resolveSDKConfigDir(env)
	if dir == "" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "projects", "*", parentSessionID, "subagents", "*.jsonl"))
	if err != nil || len(files) == 0 {
		return nil, nil
	}
	sort.Strings(files)

	usage := map[string]TokenUsage{}
	type callKey struct{ cid, message string }
	seen := map[callKey]subagentRawUsage{}
	var traces []SubagentTrace

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		traceIDs := map[string]struct{}{}
		cids := map[string]struct{}{}
		sessionID := ""
		conflict := false
		partial := false
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var record subagentRecord
			if json.Unmarshal(line, &record) != nil {
				continue
			}
			pd := record.Provider
			if len(pd.RawUsage) == 0 || pd.TraceID == "" || pd.ConversationRequestID == "" || pd.Model == "" {
				continue
			}
			var u subagentRawUsage
			if json.Unmarshal(pd.RawUsage, &u) != nil {
				continue
			}
			key := callKey{pd.ConversationRequestID, pd.MessageID}
			if old, ok := seen[key]; ok {
				if old != u {
					conflict = true
				}
				continue
			}
			seen[key] = u
			if record.SessionID != "" {
				sessionID = record.SessionID
			}
			traceIDs[pd.TraceID] = struct{}{}
			cids[pd.ConversationRequestID] = struct{}{}
			total := usage[pd.Model]
			total.OutputTokens += int64(u.CompletionTokens)
			total.CacheReadTokens += int64(subagentCacheRead(u))
			total.CacheWriteTokens += int64(subagentCacheWrite(u))
			total.InputTokens += int64(subagentInput(u))
			usage[pd.Model] = total
		}
		if scanner.Err() != nil || conflict || len(traceIDs) != 1 || len(cids) != 1 {
			partial = true
		}
		if partial {
			continue
		}
		var sdkTrace, cid string
		for id := range traceIDs {
			sdkTrace = id
		}
		for id := range cids {
			cid = id
		}
		digest := sha256.Sum256(raw)
		traces = append(traces, SubagentTrace{
			TraceID:      cid,
			SessionID:    sessionID,
			SDKTraceID:   sdkTrace,
			SDKSourceSHA: hex.EncodeToString(digest[:]),
		})
	}
	if len(usage) == 0 {
		return nil, nil
	}
	return usage, traces
}

// 缓存类别以 CLI 明确给出的字段为准；两个字段互相矛盾时按"写入"字段优先，
// 但这是数据来源的选择，不是靠总数凑出来的。
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
