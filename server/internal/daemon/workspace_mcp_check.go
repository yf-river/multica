package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type mcpCheckConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}
type mcpCheckReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (d *Daemon) handleWorkspaceMcpCheck(ctx context.Context, runtimeID string, check protocol.DaemonHeartbeatPendingMcpCheck) {
	count, code := checkWorkspaceMcp(ctx, check.Config)
	d.reportRuntimeResultWithRetry(ctx, "mcp_check", runtimeID, check.ID, func(ctx context.Context) error {
		return d.client.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/mcp-checks/%s/result", runtimeID, check.ID), map[string]any{"tool_count": count, "error_code": code}, nil)
	})
}

// 检测只返回状态码和数量，远端错误、输出及凭据不会进入报告。
func checkWorkspaceMcp(parent context.Context, raw json.RawMessage) (count int, code string) {
	ctx, cancel := context.WithTimeout(parent, 55*time.Second)
	defer cancel()
	var entry map[string]any
	if json.Unmarshal(raw, &entry) != nil {
		return 0, "protocol_error"
	}
	if tokens, ok := entry["command"].([]any); ok && len(tokens) > 0 {
		entry["command"] = tokens[0]
		if _, exists := entry["args"]; !exists {
			entry["args"] = tokens[1:]
		}
	}
	if _, exists := entry["env"]; !exists {
		if env, ok := entry["environment"]; ok {
			entry["env"] = env
		}
	}
	raw, _ = json.Marshal(entry)
	var cfg mcpCheckConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return 0, "protocol_error"
	}
	var call func(map[string]any) (mcpCheckReply, error)
	var cleanup func()
	negotiatedVersion := ""
	switch cfg.Type {
	case "", "stdio", "local", "http", "remote", "streamable-http":
	default:
		return 0, "unsupported_transport"
	}
	if cfg.Command != "" {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		// 使用双向管道并由已有进程树控制器负责退出后的完整清理。
		inputR, inputW := io.Pipe()
		outputR, outputW := io.Pipe()
		stopPipes := context.AfterFunc(ctx, func() { _ = inputR.CloseWithError(ctx.Err()); _ = outputW.CloseWithError(ctx.Err()) })
		defer stopPipes()
		cmd.Stdin = inputR
		cmd.Stdout = outputW
		cmd.Stderr = io.Discard
		done := make(chan error, 1)
		go func() {
			err := processtree.Run(ctx, cmd, time.Second)
			_ = outputW.CloseWithError(err)
			_ = inputR.CloseWithError(err)
			done <- err
		}()
		cleanup = func() { cancel(); _ = inputW.Close(); _ = outputR.Close(); <-done }
		reader := bufio.NewScanner(outputR)
		reader.Buffer(make([]byte, 4096), 4<<20)
		call = func(payload map[string]any) (mcpCheckReply, error) {
			var reply mcpCheckReply
			b, _ := json.Marshal(payload)
			if _, err := inputW.Write(append(b, '\n')); err != nil {
				return reply, err
			}
			if payload["id"] == nil {
				return reply, nil
			}
			wanted, _ := json.Marshal(payload["id"])
			for reader.Scan() {
				if json.Unmarshal(reader.Bytes(), &reply) != nil {
					return reply, errors.New("protocol_error")
				}
				if bytes.Equal(reply.ID, wanted) {
					return reply, nil
				}
			}
			return reply, errors.New("connection_failed")
		}
	} else if cfg.URL != "" {
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		session := ""
		call = func(payload map[string]any) (mcpCheckReply, error) {
			var reply mcpCheckReply
			b, _ := json.Marshal(payload)
			req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL, bytes.NewReader(b))
			if err != nil {
				return reply, err
			}
			for k, v := range cfg.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if session != "" {
				req.Header.Set("Mcp-Session-Id", session)
			}
			if negotiatedVersion != "" {
				req.Header.Set("MCP-Protocol-Version", negotiatedVersion)
			}
			resp, err := client.Do(req)
			if err != nil {
				return reply, err
			}
			defer resp.Body.Close()
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return reply, errors.New("authentication_failed")
			}
			if resp.StatusCode >= 300 {
				return reply, errors.New("connection_failed")
			}
			if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
				session = s
			}
			if payload["id"] == nil {
				return reply, nil
			}
			if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
				scan := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
				scan.Buffer(make([]byte, 4096), 4<<20)
				for scan.Scan() {
					line := scan.Text()
					if strings.HasPrefix(line, "data:") {
						if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &reply) == nil && len(reply.ID) > 0 {
							return reply, nil
						}
					}
				}
				return reply, errors.New("protocol_error")
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&reply)
			if payload["method"] == "initialize" {
				var init struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				_ = json.Unmarshal(reply.Result, &init)
				negotiatedVersion = init.ProtocolVersion
			}
			return reply, err
		}
		cleanup = func() {
			if session != "" {
				endCtx, endCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer endCancel()
				req, err := http.NewRequestWithContext(endCtx, "DELETE", cfg.URL, nil)
				if err == nil {
					for k, v := range cfg.Headers {
						req.Header.Set(k, v)
					}
					req.Header.Set("Mcp-Session-Id", session)
					if negotiatedVersion != "" {
						req.Header.Set("MCP-Protocol-Version", negotiatedVersion)
					}
					if resp, err := client.Do(req); err == nil {
						_ = resp.Body.Close()
					}
				}
			}
			client.CloseIdleConnections()
		}
	} else {
		return 0, "unsupported_transport"
	}
	defer cleanup()
	rpc := func(id int, method string, params any) (json.RawMessage, error) {
		reply, err := call(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return nil, errors.New("protocol_error")
		}
		return reply.Result, nil
	}
	initRaw, err := rpc(1, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "multica-connection-check", "version": "1"}})
	if err == nil {
		var init struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(initRaw, &init) != nil {
			err = errors.New("protocol_error")
		} else {
			negotiatedVersion = init.ProtocolVersion
			switch negotiatedVersion {
			case "2024-11-05", "2025-03-26", "2025-06-18":
			default:
				err = errors.New("protocol_error")
			}
		}
	}
	if err == nil {
		_, err = call(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; err == nil; page++ {
		if page >= 100 {
			err = errors.New("protocol_error")
			break
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		raw, err = rpc(page+2, "tools/list", params)
		if err != nil {
			break
		}
		var result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Tools == nil {
			err = errors.New("protocol_error")
			break
		}
		for _, tool := range result.Tools {
			seen[tool.Name] = true
		}
		if result.NextCursor == "" {
			break
		}
		if result.NextCursor == cursor {
			err = errors.New("protocol_error")
			break
		}
		cursor = result.NextCursor
	}
	if err != nil {
		if ctx.Err() != nil {
			return 0, "timeout"
		}
		if err.Error() == "authentication_failed" {
			return 0, "authentication_failed"
		}
		if err.Error() == "protocol_error" {
			return 0, "protocol_error"
		}
		return 0, "connection_failed"
	}
	return len(seen), ""
}
