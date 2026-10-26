package agent

import (
	"encoding/json"
	"time"
)

// 遥测独立于模型消息；字段只包含已观测数据，不用累计用量估计模型请求耗时。
func runtimeTelemetry(provider, kind string, fields map[string]any) Message {
	fields["provider"] = provider
	fields["kind"] = kind
	fields["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, _ := json.Marshal(fields)
	return Message{Type: MessageRuntime, Content: string(data)}
}

func (c *codexClient) emitRuntime(kind string, fields map[string]any) {
	if c.onMessage == nil {
		return
	}
	fields["session_id"] = c.getThreadID()
	c.onMessage(runtimeTelemetry("codex", kind, fields))
}

// 供应商明确报告的工具用时；缺失字段不从模型或整轮耗时倒推。
func (c *codexClient) emitToolDuration(params, item map[string]any) {
	var duration float64
	found := false
	for _, key := range []string{"durationMs", "duration_ms"} {
		if value, ok := item[key].(float64); ok && value >= 0 {
			duration, found = value, true
			break
		}
	}
	id, _ := item["id"].(string)
	if !found || id == "" {
		return
	}
	turn, _ := params["turnId"].(string)
	key := turn + "\x00" + id
	c.mu.Lock()
	if c.runtimeToolCompletions == nil {
		c.runtimeToolCompletions = make(map[string]bool)
	}
	duplicate := c.runtimeToolCompletions[key]
	c.runtimeToolCompletions[key] = true
	c.mu.Unlock()
	if !duplicate {
		c.emitRuntime("tool", map[string]any{"turn_id": turn, "call_id": id, "duration_ms": duration, "status": item["status"]})
	}
}

func emitCodebuddyACPContext(line string, emit func(Message)) {
	var envelope struct {
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Used *int64                     `json:"used"`
				Size *int64                     `json:"size"`
				Meta map[string]json.RawMessage `json:"_meta"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(line), &envelope) != nil {
		return
	}
	update := envelope.Params.Update
	fields := map[string]any{"session_id": envelope.Params.SessionID, "scope": "snapshot", "source": "native_acp"}
	if update.Used != nil {
		fields["pressure_tokens"] = *update.Used
	}
	if update.Size != nil {
		fields["context_window"] = *update.Size
	}
	var categories map[string]int64
	if json.Unmarshal(update.Meta["codebuddy.ai/usageByCategory"], &categories) == nil {
		breakdown := map[string]int64{}
		for source, target := range map[string]string{"systemPrompt": "system_tokens", "tools": "tools_tokens", "conversation": "message_tokens", "mcp": "mcp_tokens", "skills": "skills_tokens"} {
			if value, ok := categories[source]; ok {
				breakdown[target] = value
			}
		}
		if len(breakdown) > 0 {
			fields["breakdown"] = breakdown
		}
	}
	if update.Used != nil || update.Size != nil || fields["breakdown"] != nil {
		emit(runtimeTelemetry("codebuddy", "context", fields))
	}
}
