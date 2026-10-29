package openviking

import (
	"encoding/json"
	"fmt"
	"strings"
)

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}
func blocks(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	rows, _ := v.([]any)
	var b strings.Builder
	for _, row := range rows {
		m := object(row)
		switch m["type"] {
		case "text", "reasoning", "output_text", "input_text":
			b.WriteString(text(m["text"]))
		}
	}
	return b.String()
}

func normalize(meta Metadata, events []Event, rawURI string) []Message {
	messages := []Message{}
	add := func(role, id string, parts []map[string]any) {
		if len(parts) == 0 {
			return
		}
		messages = append(messages, Message{Role: role, Parts: parts, TurnID: meta.Attempt, Sources: []string{"multica:" + meta.Attempt + ":" + id}, CreatedAt: meta.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
	}
	parts := []map[string]any{{"type": "text", "text": meta.Input}}
	for _, a := range meta.Attachments {
		if meta.InputAttachmentIDs != nil {
			selected := false
			for _, id := range meta.InputAttachmentIDs {
				if id == a.ID {
					selected = true
					break
				}
			}
			if !selected {
				continue
			}
		}
		uri := rawURI + "/" + a.Path
		if strings.HasPrefix(a.ContentType, "image/") {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}})
		} else {
			parts = append(parts, map[string]any{"type": "context", "context_type": "resource", "uri": uri, "abstract": a.Filename})
		}
	}
	if !strings.HasPrefix(meta.Provider, "multica-") {
		add("user", "input", parts)
		if len(meta.InputSources) > 0 {
			messages[len(messages)-1].Sources = meta.InputSources
		}
	} else {
		var source struct {
			Operation string         `json:"operation"`
			Changed   bool           `json:"content_changed"`
			Row       map[string]any `json:"row"`
		}
		if json.Unmarshal(meta.SourcePayload, &source) == nil {
			row := source.Row
			role := "user"
			kind := "message"
			content := text(row["content"])
			sourceID := text(row["id"])
			if meta.Provider == "multica-comment" {
				kind = "comment"
				if row["author_type"] == "agent" {
					role = "assistant"
				}
			}
			if meta.Provider == "multica-chat_message" {
				role = text(row["role"])
			}
			if meta.Provider == "multica-attachment" {
				kind = "attachment"
				content = "附件：" + text(row["filename"])
				if row["uploader_type"] == "agent" {
					role = "assistant"
				}
			}
			project := content != "" && !(source.Operation == "UPDATE" && !source.Changed)
			if kind == "attachment" && meta.ChatID != "" && row["chat_message_id"] == nil && row["task_id"] == nil {
				project = false
			}
			if role == "assistant" && (row["task_id"] != nil || row["source_task_id"] != nil) && kind != "attachment" {
				project = false
			}
			if project {
				projected := []map[string]any{{"type": "text", "text": content}}
				if kind == "attachment" {
					for _, a := range meta.Attachments {
						uri := rawURI + "/" + a.Path
						if strings.HasPrefix(a.ContentType, "image/") {
							projected = append(projected, map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}})
						} else {
							projected = append(projected, map[string]any{"type": "context", "context_type": "resource", "uri": uri, "abstract": a.Filename})
						}
					}
				}
				add(role, "platform", projected)
				base := "multica-" + kind + ":" + sourceID
				revision := base + ":" + SHA([]byte(content))
				messages[len(messages)-1].Sources = []string{base, revision}
				if source.Operation == "UPDATE" && source.Changed {
					messages[len(messages)-1].Sources = []string{revision}
				}
				if at := text(row["created_at"]); at != "" {
					messages[len(messages)-1].CreatedAt = at
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, event := range events {
		var frame map[string]any
		if json.Unmarshal(event.Data, &frame) != nil {
			continue
		}
		var body string
		var tool map[string]any
		id := fmt.Sprint(event.Seq)
		if method := text(frame["method"]); method == "multica/supplement" || method == "multica/command" || method == "multica/interaction-answer" {
			body := text(object(frame["params"])["content"])
			if body == "" {
				body = text(frame["params"])
			}
			add("user", id, []map[string]any{{"type": "text", "text": body}})
			continue
		}
		if event.Source == "dsh" && frame["type"] == "archive" {
			e := object(frame["event"])
			data := object(e["data"])
			id = "dsh:" + text(frame["session_id"]) + ":" + text(e["seq"])
			switch e["type"] {
			case "assistant/message":
				body = blocks(object(data["message"])["content"])
			case "tool/call":
				input := object(data["arguments"])
				if input == nil {
					_ = json.Unmarshal([]byte(text(data["arguments"])), &input)
				}
				tool = map[string]any{"type": "tool", "tool_id": text(data["callId"]), "tool_name": text(data["name"]), "tool_input": input, "tool_status": "pending"}
			case "tool/result":
				m := object(data["message"])
				status := "completed"
				if m["isError"] == true {
					status = "error"
				}
				output := blocks(m["content"])
				if output == "" {
					output = text(m["content"])
				}
				tool = map[string]any{"type": "tool", "tool_id": text(m["toolCallId"]), "tool_name": "tool", "tool_output": output, "tool_status": status}
			}
		} else if event.Source == "codex" && frame["method"] == "item/completed" {
			p := object(frame["params"])
			item := object(p["item"])
			id = "codex:" + text(p["threadId"]) + ":" + text(item["id"])
			switch item["type"] {
			case "agentMessage":
				body = text(item["text"])
				if body == "" {
					body = blocks(item["content"])
				}
			case "reasoning":
				body = blocks(item["summary"])
				if body == "" {
					body = blocks(item["content"])
				}
			case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "collabAgentToolCall":
				output := text(item["aggregatedOutput"])
				if output == "" {
					output = text(item["result"])
				}
				if output == "" {
					output = text(item["changes"])
				}
				tool = map[string]any{"type": "tool", "tool_id": text(item["id"]), "tool_name": text(item["type"]), "tool_input": item, "tool_output": output, "tool_status": "completed"}
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if body != "" {
			add("assistant", id, []map[string]any{{"type": "text", "text": body}})
		} else if tool != nil {
			add("assistant", id, []map[string]any{tool})
		}
		if (body != "" || tool != nil) && !event.At.IsZero() {
			messages[len(messages)-1].CreatedAt = event.At.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
	}
	inputHashes := map[string]bool{}
	for _, id := range meta.InputAttachmentIDs {
		for _, a := range meta.Attachments {
			if a.ID == id {
				inputHashes[a.SHA256] = true
			}
		}
	}
	for _, a := range meta.Attachments {
		if (strings.HasPrefix(a.ID, "dsh-sha256-") || strings.HasPrefix(a.ID, "inline-sha256-")) && !inputHashes[a.SHA256] && strings.HasPrefix(a.ContentType, "image/") {
			add("assistant", "native-image-"+a.SHA256, []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": rawURI + "/" + a.Path}}})
		}
	}
	if meta.Incomplete {
		add("assistant", "interrupted", []map[string]any{{"type": "text", "text": "本次 Multica 执行在 daemon 中断时尚未结束；原始事件与附件已保留，不能将此记录视为任务完成。"}})
	}
	return messages
}
