package agent

import (
	"encoding/json"
	"strings"
	"time"
)

type codebuddyLifecycle struct {
	sessionID string
	children  map[string]bool
	completed map[string]bool
	attached  map[string]bool
	tools     map[string]time.Time
	emit      func(Message)
}

func newCodebuddyLifecycle(emit func(Message)) *codebuddyLifecycle {
	return &codebuddyLifecycle{children: make(map[string]bool), completed: make(map[string]bool), attached: make(map[string]bool), tools: make(map[string]time.Time), emit: emit}
}

// 只按协议给出的父调用、会话和工具完成事件归属，不从正文猜测子调用。
func (l *codebuddyLifecycle) observe(msg codebuddySDKMessage) bool {
	if l.sessionID == "" && msg.ParentToolUseID == "" && msg.SessionID != "" {
		l.sessionID = msg.SessionID
	}
	root := msg.ParentToolUseID == "" && (msg.SessionID == "" || l.sessionID == "" || msg.SessionID == l.sessionID)
	if msg.ParentToolUseID != "" && msg.Type == "assistant" {
		if !l.children[msg.ParentToolUseID] && !l.completed[msg.ParentToolUseID] {
			l.children[msg.ParentToolUseID] = true
			l.emit(runtimeTelemetry("codebuddy", "subagent", map[string]any{"phase": "start", "parent_call_id": msg.ParentToolUseID}))
		}
		key := msg.ParentToolUseID + "\x00" + msg.SessionID + "\x00" + msg.ProviderRequestID
		if !l.attached[key] {
			l.attached[key] = true
			l.emit(runtimeTelemetry("codebuddy", "subagent", map[string]any{"phase": "attached", "parent_call_id": msg.ParentToolUseID, "session_id": msg.SessionID, "request_id": msg.ProviderRequestID}))
		}
	}
	var content codebuddyMessageContent
	if (msg.Type != "assistant" && msg.Type != "user") || json.Unmarshal(msg.Message, &content) != nil {
		return root
	}
	for _, block := range content.Content {
		switch block.Type {
		case "tool_use":
			if block.ID == "" {
				continue
			}
			if _, exists := l.tools[block.ID]; !exists {
				l.tools[block.ID] = time.Now()
			}
			if strings.EqualFold(block.Name, "Task") || strings.EqualFold(block.Name, "Agent") {
				if !l.children[block.ID] && !l.completed[block.ID] {
					l.children[block.ID] = true
					l.emit(runtimeTelemetry("codebuddy", "subagent", map[string]any{"phase": "start", "parent_call_id": block.ID, "session_id": msg.SessionID}))
				}
			}
		case "tool_result":
			if start, exists := l.tools[block.ToolUseID]; exists {
				l.emit(runtimeTelemetry("codebuddy", "tool", map[string]any{"call_id": block.ToolUseID, "parent_call_id": msg.ParentToolUseID, "duration_ms": time.Since(start).Milliseconds()}))
				delete(l.tools, block.ToolUseID)
			}
			if l.children[block.ToolUseID] {
				l.emit(runtimeTelemetry("codebuddy", "subagent", map[string]any{"phase": "end", "parent_call_id": block.ToolUseID, "session_id": msg.SessionID}))
				delete(l.children, block.ToolUseID)
				l.completed[block.ToolUseID] = true
			}
		}
	}
	return root
}
