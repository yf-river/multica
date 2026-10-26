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
