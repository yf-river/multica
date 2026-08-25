package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// cursorBackend implements Backend by spawning the Cursor Agent CLI
// (cursor-agent) with --output-format stream-json and parsing the JSONL
// event stream.
type cursorBackend struct {
	cfg Config
}

func (b *cursorBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execName := b.cfg.ExecutablePath
	if execName == "" {
		execName = "cursor-agent"
	}
	lookedUp, err := exec.LookPath(execName)
	if err != nil {
		return nil, fmt.Errorf("cursor-agent executable not found at %q: %w", execName, err)
	}

	args := buildCursorArgs(prompt, opts, b.cfg.Logger)
	argv0, cmdArgs := chooseCursorInvocation(execName, lookedUp, args, b.cfg.Logger)
	return executeStreamCommand(ctx, opts.Timeout, streamCommandSpec{
		name:       "cursor-agent",
		pipeName:   "cursor",
		stderrName: "cursor",
		executable: argv0,
		args:       cmdArgs,
		env:        buildEnv(b.cfg.Env),
		cwd:        opts.Cwd,
		waitDelay:  500 * time.Millisecond,
		logger:     b.cfg.Logger,
		model:      opts.Model,
		parse: func(stdout io.Reader, msgCh chan<- Message, cancel context.CancelFunc) streamCommandResult {
			configuredModel := strings.TrimSpace(opts.Model)
			var output strings.Builder
			var sessionID string
			finalStatus := "completed"
			var finalError string
			resultSeen := false
			usage := make(map[string]TokenUsage)

			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}

				var evt cursorStreamEvent
				if err := json.Unmarshal([]byte(line), &evt); err != nil {
					continue
				}

				if sid := strings.TrimSpace(evt.SessionID); sid != "" {
					sessionID = sid
				}

				switch evt.Type {
				case "system":
					if evt.Subtype == "init" {
						trySend(msgCh, Message{Type: MessageStatus, Status: "running"})
					}
					if evt.Subtype == "error" {
						errMsg := cursorErrorText(&evt)
						if errMsg != "" {
							trySend(msgCh, Message{Type: MessageError, Content: errMsg})
						}
					}

				case "assistant":
					b.handleCursorAssistant(&evt, msgCh, &output)

				case "tool_call":
					if msg, ok := cursorToolCallMessage(&evt); ok {
						trySend(msgCh, msg)
					}

				case "result":
					resultSeen = true
					if evt.IsError || evt.Subtype == "error" {
						finalStatus = "failed"
						finalError = cursorErrorText(&evt)
					}
					if evt.ResultText != "" && output.Len() == 0 {
						output.WriteString(evt.ResultText)
					}
					b.accumulateResultUsage(usage, &evt, configuredModel)
					// Current Cursor Agent versions can emit the terminal result
					// event but keep a worker process alive. Treat result as the
					// protocol boundary so the daemon can report completion.
					cancel()

				case "error":
					errMsg := cursorErrorText(&evt)
					if errMsg != "" {
						finalError = errMsg
					}
					trySend(msgCh, Message{Type: MessageError, Content: errMsg})
				}
			}

			return streamCommandResult{
				status:             finalStatus,
				output:             output.String(),
				errMsg:             finalError,
				sessionID:          sessionID,
				usage:              usage,
				terminalResultSeen: resultSeen,
			}
		},
	})
}

func (b *cursorBackend) handleCursorAssistant(evt *cursorStreamEvent, ch chan<- Message, output *strings.Builder) {
	if evt.Message == nil {
		return
	}

	var content cursorAssistantMessage
	if err := json.Unmarshal(evt.Message, &content); err != nil {
		return
	}

	// Note: per-message usage in assistant events is intentionally ignored.
	// Token usage is taken exclusively from "result" events (session totals)
	// to avoid double-counting.

	for _, block := range content.Content {
		if block.Type == "text" && block.Text != "" {
			output.WriteString(block.Text)
			trySend(ch, Message{Type: MessageText, Content: block.Text})
		}
	}
}

func cursorUsageModel(evtModel, configuredModel string) string {
	if model := strings.TrimSpace(evtModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(configuredModel); model != "" {
		return model
	}
	return "cursor"
}

func (b *cursorBackend) accumulateResultUsage(usage map[string]TokenUsage, evt *cursorStreamEvent, configuredModel string) {
	model := cursorUsageModel(evt.Model, configuredModel)
	u := usage[model]

	if evt.Usage == nil {
		return
	}
	u.InputTokens += evt.Usage.InputTokens
	u.OutputTokens += evt.Usage.OutputTokens
	u.CacheReadTokens += evt.Usage.CacheReadTokens
	u.CacheWriteTokens += evt.Usage.CacheWriteTokens

	usage[model] = u
}

// ── Cursor stream-json types ──

type cursorStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`

	// assistant fields
	Message json.RawMessage `json:"message,omitempty"`

	// tool_call fields
	CallID   string          `json:"call_id,omitempty"`
	ToolCall json.RawMessage `json:"tool_call,omitempty"`

	// result fields
	ResultText string       `json:"result,omitempty"`
	IsError    bool         `json:"is_error,omitempty"`
	Usage      *cursorUsage `json:"usage,omitempty"`

	// error fields
	ErrorMsg string `json:"error,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type cursorUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

type cursorAssistantMessage struct {
	Content []cursorContentBlock `json:"content"`
}

type cursorContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ── Helpers ──

type cursorToolCall struct {
	Args   map[string]any  `json:"args"`
	Result json.RawMessage `json:"result"`
}

func cursorToolCallMessage(evt *cursorStreamEvent) (Message, bool) {
	var calls map[string]cursorToolCall
	if len(evt.ToolCall) == 0 || json.Unmarshal(evt.ToolCall, &calls) != nil || len(calls) != 1 {
		return Message{}, false
	}
	for kind, call := range calls {
		tool := strings.TrimSuffix(kind, "ToolCall")
		if evt.Subtype == "started" {
			return Message{Type: MessageToolUse, Tool: tool, CallID: evt.CallID, Input: call.Args}, true
		}
		if evt.Subtype == "completed" {
			return Message{Type: MessageToolResult, Tool: tool, CallID: evt.CallID, Output: string(call.Result)}, true
		}
	}
	return Message{}, false
}

func cursorErrorText(evt *cursorStreamEvent) string {
	if evt.ErrorMsg != "" {
		return evt.ErrorMsg
	}
	if evt.Detail != "" {
		return evt.Detail
	}
	if evt.ResultText != "" {
		return evt.ResultText
	}
	return ""
}

// cursorBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. Overriding these would break
// the daemon↔cursor-agent communication protocol.
var cursorBlockedArgs = map[string]blockedArgMode{
	"-p":              blockedStandalone, // non-interactive print mode
	"--output-format": blockedWithValue,  // stream-json protocol
	"-f":              blockedStandalone, // auto-approval for autonomous operation
	"--force":         blockedStandalone, // auto-approval for autonomous operation
}

// buildCursorArgs assembles the argv for a one-shot cursor-agent invocation.
//
// Usage: cursor-agent -p <prompt> --output-format stream-json --force
//
//	[--model <m>] [--resume <id>]
func buildCursorArgs(prompt string, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--force",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// NOTE: cursor-agent CLI does not support --system-prompt or --max-turns.
	// Instructions are injected via AGENTS.md and .cursor/skills/ files instead.
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, cursorBlockedArgs, logger)...)
	return args
}
