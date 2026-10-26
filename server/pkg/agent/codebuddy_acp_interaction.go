package agent

import (
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func handleCodebuddyACPInteraction(c *hermesClient, broker *interactionBroker, sessionID, method string, id, raw json.RawMessage) bool {
	if method != "_codebuddy.ai/question" && method != "session/request_permission" {
		return false
	}
	var request struct {
		SessionID string `json:"sessionId"`
		InputType string `json:"inputType"`
		Schema    struct {
			Questions []struct {
				ID          string                       `json:"id"`
				Header      string                       `json:"header"`
				Question    string                       `json:"question"`
				Options     []protocol.InteractionOption `json:"options"`
				MultiSelect bool                         `json:"multiSelect"`
			} `json:"questions"`
		} `json:"schema"`
		ToolCall struct {
			Title    string          `json:"title"`
			RawInput json.RawMessage `json:"rawInput"`
		} `json:"toolCall"`
		Options []acpPermissionOption `json:"options"`
	}
	respond := func(result any) {
		c.writeAgentRequestResponse(method, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	if json.Unmarshal(raw, &request) != nil || request.SessionID != sessionID {
		c.writeAgentRequestResponse(method, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "interaction session mismatch"}})
		return true
	}
	prompt := protocol.InteractionPrompt{Kind: "approval", Title: request.ToolCall.Title}
	if method == "_codebuddy.ai/question" {
		prompt.Kind, prompt.Title = "question", "需要补充信息"
		if request.InputType != "question" {
			respond(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
			return true
		}
		for _, q := range request.Schema.Questions {
			prompt.Questions = append(prompt.Questions, protocol.InteractionQuestion{ID: q.ID, Header: q.Header, Question: q.Question, Options: q.Options, MultiSelect: q.MultiSelect})
		}
	} else {
		if prompt.Title == "" {
			prompt.Title = "请求执行需要确认的操作"
		}
		if len(request.ToolCall.RawInput) > 0 {
			prompt.Title += "\n" + string(request.ToolCall.RawInput)
		}
	}
	broker.start(string(id), prompt, func(answer protocol.InteractionAnswer, err error) {
		allowed := err == nil && answer.Decision == "allow"
		if method == "_codebuddy.ai/question" {
			outcome := map[string]any{"outcome": "cancelled"}
			if allowed {
				outcome = map[string]any{"outcome": "submitted", "data": map[string]any{"answers": answer.Answers}}
			}
			respond(map[string]any{"outcome": outcome})
			return
		}
		kind := "reject_once"
		if allowed {
			kind = "allow_once"
		}
		for _, option := range request.Options {
			if option.Kind == kind {
				respond(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option.OptionID}})
				return
			}
		}
		// 不把一次授权扩大成供应商的永久授权选项。
		c.writeAgentRequestResponse(method, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": fmt.Sprintf("no %s permission option offered", kind)}})
	})
	return true
}
