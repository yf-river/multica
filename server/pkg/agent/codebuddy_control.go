package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// 同一条输入流上的提示词、权限应答和控制请求必须按完整 JSON 行串行写入。
type codebuddyControlClient struct {
	writer    io.Writer
	writeMu   sync.Mutex
	mu        sync.Mutex
	sequence  uint64
	sessionID string
	requestID string
	pending   map[string]chan codebuddyControlResponse
	done      chan struct{}
	closed    bool
	active    bool
}

type codebuddyControlResponse struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

func newCodebuddyControlClient(writer io.Writer) *codebuddyControlClient {
	return &codebuddyControlClient{writer: writer, pending: make(map[string]chan codebuddyControlResponse), done: make(chan struct{})}
}

func (c *codebuddyControlClient) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writer.Write(p)
}

func (c *codebuddyControlClient) observe(msg codebuddySDKMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if msg.ParentToolUseID != "" || (c.sessionID != "" && msg.SessionID != "" && msg.SessionID != c.sessionID) {
		return
	}
	if msg.Type == "result" {
		c.active = false
		return
	}
	// 只有当前主会话的 assistant 事件证明这一轮已经开始执行。
	if msg.Type != "assistant" || c.closed {
		return
	}
	if c.sessionID != "" && msg.SessionID != "" && msg.SessionID != c.sessionID {
		return
	}
	if msg.SessionID != "" {
		c.sessionID = msg.SessionID
	}
	if msg.ProviderRequestID != "" {
		c.requestID = msg.ProviderRequestID
	}
	c.active = c.sessionID != "" && c.requestID != ""
}

func (c *codebuddyControlClient) ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active && !c.closed
}

func (c *codebuddyControlClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.active = false
		close(c.done)
	}
}

func (c *codebuddyControlClient) respond(raw json.RawMessage) {
	var response codebuddyControlResponse
	if json.Unmarshal(raw, &response) != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[response.RequestID]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
}

func (c *codebuddyControlClient) supplement(ctx context.Context, instruction string) error {
	if strings.TrimSpace(instruction) == "" {
		return errors.New("codebuddy additional instruction is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.active || c.closed {
		c.mu.Unlock()
		return errors.New("codebuddy has no active provider-confirmed turn")
	}
	c.sequence++
	id := fmt.Sprintf("multica-steer-%d", c.sequence)
	responseCh := make(chan codebuddyControlResponse, 1)
	c.pending[id] = responseCh
	sessionID, requestID := c.sessionID, c.requestID
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	data, err := json.Marshal(map[string]any{
		"type": "control_request", "request_id": id,
		"request": map[string]any{
			"subtype": "steer", "session_id": sessionID, "expected_request_id": requestID,
			"content_blocks": []map[string]string{{"type": "text", "text": instruction}},
		},
	})
	if err != nil {
		return err
	}
	if _, err := c.Write(append(data, '\n')); err != nil {
		return err
	}
	select {
	case response := <-responseCh:
		var result struct {
			SessionID string `json:"session_id"`
			Steered   bool   `json:"steered"`
			Reason    string `json:"reason"`
		}
		if response.Subtype != "success" {
			return errors.New("codebuddy steer request failed")
		}
		if json.Unmarshal(response.Response, &result) != nil || result.SessionID != sessionID || !result.Steered {
			return errors.New("codebuddy did not accept the additional instruction for the active turn")
		}
		return nil
	case <-c.done:
		return errors.New("codebuddy execution ended before steer acknowledgement")
	case <-ctx.Done():
		return ctx.Err()
	}
}
