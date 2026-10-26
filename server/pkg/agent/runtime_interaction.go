package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

type interactionBroker struct {
	provider string
	ctx      context.Context
	cancel   context.CancelFunc
	request  func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error)
	status   func(Message)
	mu       sync.Mutex
	closed   bool
	pending  map[string]context.CancelFunc
	count    atomic.Int32
	wg       sync.WaitGroup
}

func newInteractionBroker(provider string, ctx context.Context, request func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error), status func(Message)) *interactionBroker {
	ctx, cancel := context.WithCancel(ctx)
	return &interactionBroker{provider: provider, ctx: ctx, cancel: cancel, request: request, status: status, pending: make(map[string]context.CancelFunc)}
}

func (b *interactionBroker) waiting() bool { return b.count.Load() > 0 }

// 协议读协程只登记请求，不阻塞后续流、取消和服务端的请求撤销通知。
func (b *interactionBroker) start(id string, prompt protocol.InteractionPrompt, respond func(protocol.InteractionAnswer, error)) {
	b.mu.Lock()
	if b.closed || b.pending[id] != nil {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	b.pending[id] = cancel
	b.wg.Add(1)
	b.count.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		started := time.Now()
		outcome := "failed"
		if b.provider != "" && b.status != nil {
			b.status(runtimeTelemetry(b.provider, "interaction", map[string]any{"phase": "start", "request_id": id, "interaction_kind": prompt.Kind}))
			defer func() {
				b.status(runtimeTelemetry(b.provider, "interaction", map[string]any{"phase": "end", "request_id": id, "interaction_kind": prompt.Kind, "waiting_ms": time.Since(started).Milliseconds(), "outcome": outcome}))
			}()
		}
		defer cancel()
		defer func() {
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			if b.count.Add(-1) == 0 && b.ctx.Err() == nil && b.status != nil {
				b.status(Message{Type: MessageStatus, Status: "running"})
			}
		}()
		if b.status != nil {
			b.status(Message{Type: MessageStatus, Status: "waiting_input"})
		}
		if err := prompt.Validate(); err != nil {
			respond(protocol.InteractionAnswer{}, err)
			return
		}
		if b.request == nil {
			respond(protocol.InteractionAnswer{}, errors.New("runtime interaction is unavailable"))
			return
		}
		answer, err := b.request(ctx, prompt)
		if err == nil {
			err = prompt.ValidateAnswer(answer)
		}
		if ctx.Err() != nil {
			outcome = "cancelled"
		} else if err == nil {
			outcome = answer.Decision
		}
		respond(answer, err)
	}()
}

func (b *interactionBroker) resolve(id string) {
	b.mu.Lock()
	cancel := b.pending[id]
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (b *interactionBroker) stop() {
	b.mu.Lock()
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.wg.Wait()
}

func codexQuestionPrompt(params json.RawMessage) (protocol.InteractionPrompt, error) {
	var request struct {
		Questions []struct {
			protocol.InteractionQuestion
			IsSecret bool `json:"isSecret"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return protocol.InteractionPrompt{}, err
	}
	prompt := protocol.InteractionPrompt{Kind: "question", Title: "需要补充信息"}
	for _, question := range request.Questions {
		// 普通问答记录会持久化，凭据必须走独立的凭据入口。
		if question.IsSecret {
			return protocol.InteractionPrompt{}, errors.New("secret input requires the credential workflow")
		}
		prompt.Questions = append(prompt.Questions, question.InteractionQuestion)
	}
	return prompt, nil
}

func codexQuestionAnswer(answer protocol.InteractionAnswer) map[string]any {
	answers := make(map[string]any, len(answer.Answers))
	for id, values := range answer.Answers {
		answers[id] = map[string]any{"answers": values}
	}
	return map[string]any{"answers": answers}
}

func (c *codexClient) handleInteractionRequest(id json.RawMessage, method string, raw json.RawMessage) {
	var params map[string]any
	if json.Unmarshal(raw, &params) != nil {
		c.respondError(id, -32602, "invalid interaction request")
		return
	}
	if c.isNotificationFromOtherThread(params) {
		c.respondError(id, -32602, "interaction belongs to another thread")
		return
	}
	if turn, ok := params["turnId"].(string); ok && turn != "" && turn != c.activeTurnID() {
		c.respondError(id, -32602, "interaction belongs to another turn")
		return
	}
	prompt := protocol.InteractionPrompt{Kind: "approval", Title: "请求执行需要确认的操作"}
	isQuestion := method == "item/tool/requestUserInput" || method == "tool/requestUserInput"
	if isQuestion {
		var err error
		prompt, err = codexQuestionPrompt(raw)
		if err != nil {
			c.respondError(id, -32602, "invalid question request")
			return
		}
	} else {
		switch method {
		case "item/commandExecution/requestApproval", "execCommandApproval":
			prompt.Title = fmt.Sprintf("允许执行命令：%v", params["command"])
		case "item/fileChange/requestApproval", "applyPatchApproval":
			prompt.Title = "允许应用本次文件修改？"
		case "item/permissions/requestApproval":
			prompt.Title = fmt.Sprintf("允许本轮使用以下目录或网络权限：%v", params["permissions"])
		}
		if reason, ok := params["reason"].(string); ok && reason != "" {
			prompt.Title += "\n" + reason
		}
	}
	c.interactions.start(string(id), prompt, func(answer protocol.InteractionAnswer, err error) {
		if isQuestion {
			if err != nil || answer.Decision != "allow" {
				c.respondError(id, -32000, "user input cancelled or declined")
				return
			}
			c.respond(id, codexQuestionAnswer(answer))
			return
		}
		allowed := err == nil && answer.Decision == "allow"
		if method == "item/permissions/requestApproval" {
			if allowed {
				c.respond(id, codexPermissionsApprovalResponse(raw, c.cfg.Logger))
			} else {
				c.respond(id, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
			}
			return
		}
		decision := "decline"
		if allowed {
			decision = "accept"
		}
		c.respond(id, map[string]any{"decision": decision})
	})
}

func (c *codexClient) write(data []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.stdin.Write(data)
}

func handleCodebuddyInteraction(msg codebuddySDKMessage, writer io.Writer, broker *interactionBroker) bool {
	var request codebuddyControlRequestPayload
	if json.Unmarshal(msg.Request, &request) != nil {
		return false
	}
	if request.Subtype != "can_use_tool" {
		return false
	}
	var input map[string]any
	if json.Unmarshal(request.Input, &input) != nil {
		input = map[string]any{}
	}
	prompt := protocol.InteractionPrompt{Kind: "approval", Title: "允许调用工具：" + request.ToolName}
	sourceQuestions := []struct {
		Header      string                       `json:"header"`
		Question    string                       `json:"question"`
		Options     []protocol.InteractionOption `json:"options"`
		MultiSelect bool                         `json:"multiSelect"`
	}{}
	if request.ToolName == "AskUserQuestion" {
		var payload struct {
			Questions json.RawMessage `json:"questions"`
		}
		if json.Unmarshal(request.Input, &payload) == nil {
			_ = json.Unmarshal(payload.Questions, &sourceQuestions)
		}
		prompt.Kind, prompt.Title = "question", "需要补充信息"
		for i, q := range sourceQuestions {
			prompt.Questions = append(prompt.Questions, protocol.InteractionQuestion{ID: strconv.Itoa(i), Header: q.Header, Question: q.Question, Options: q.Options, MultiSelect: q.MultiSelect})
		}
	} else if request.ToolName == "ExitPlanMode" {
		prompt.Kind, prompt.Title = "plan", "计划已准备好，是否开始执行？"
	} else {
		if command, ok := input["command"].(string); ok {
			prompt.Title += "\n" + command
		}
		if path, ok := input["file_path"].(string); ok {
			prompt.Title += "\n" + path
		}
	}
	broker.start(msg.RequestID, prompt, func(answer protocol.InteractionAnswer, err error) {
		allowed := err == nil && answer.Decision == "allow"
		response := map[string]any{"allowed": allowed, "behavior": "deny"}
		if allowed {
			response["behavior"] = "allow"
			if request.ToolName == "AskUserQuestion" {
				answers := map[string]string{}
				for i, q := range sourceQuestions {
					answers[q.Question] = strings.Join(answer.Answers[strconv.Itoa(i)], ", ")
				}
				input["answers"] = answers
			}
			response["updatedInput"] = input
		} else {
			response["message"] = "用户拒绝或交互已取消。"
		}
		data, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg.RequestID, "response": response}})
		_, _ = writer.Write(append(data, '\n'))
	})
	return true
}
