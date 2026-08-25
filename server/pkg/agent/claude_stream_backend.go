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

type claudeStreamBackendSpec struct {
	provider          string
	defaultExecutable string
	blockedArgs       map[string]blockedArgMode
	rejectAsyncTools  bool
}

func executeClaudeStreamBackend(ctx context.Context, prompt string, opts ExecOptions, cfg Config, spec claudeStreamBackendSpec) (*Session, error) {
	execPath := cfg.ExecutablePath
	if execPath == "" {
		execPath = spec.defaultExecutable
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("%s executable not found at %q: %w", spec.provider, execPath, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)
	args := buildClaudeStreamArgs(opts, spec.blockedArgs, cfg.Logger)
	process, err := startClaudeProtocolProcess(runCtx, cancel, cfg, opts, execPath, args, spec.provider)
	if err != nil {
		return nil, err
	}
	cmd, stdout, stdin := process.cmd, process.stdout, process.stdin
	closeStdin, stderrBuf := process.closeStdin, process.stderr
	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	writeDone := make(chan error, 1)
	go func() {
		err := writeClaudeStreamInput(stdin, prompt, spec.provider)
		if err != nil {
			closeStdin()
		}
		writeDone <- err
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer process.mcpConfigCleanup()

		startTime := time.Now()
		var output strings.Builder
		var sessionID string
		finalStatus := "completed"
		var finalError string
		sawAsyncLaunch := false
		usage := make(map[string]TokenUsage)

		go func() {
			<-runCtx.Done()
			closeStdin()
			_ = stdout.Close()
		}()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var msg claudeStreamMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "assistant":
				handleClaudeStreamAssistant(msg, msgCh, &output, usage)
			case "user":
				if handleClaudeStreamUser(msg, msgCh, spec.rejectAsyncTools) {
					sawAsyncLaunch = true
				}
			case "system":
				if msg.SessionID != "" {
					sessionID = msg.SessionID
				}
				trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
			case "result":
				sessionID = msg.SessionID
				if msg.ResultText != "" {
					output.Reset()
					output.WriteString(msg.ResultText)
				}
				if resultUsage := claudeStreamResultUsage(msg, opts.Model); len(resultUsage) > 0 {
					usage = resultUsage
				}
				if msg.IsError {
					finalStatus = "failed"
					finalError = msg.ResultText
				}
				closeStdin()
			case "log":
				if msg.Log != nil {
					trySend(msgCh, Message{Type: MessageLog, Level: msg.Log.Level, Content: msg.Log.Message})
				}
			case "control_request":
				handleClaudeStreamControlRequest(cfg.Logger, spec.provider, spec.rejectAsyncTools, msg, stdin)
			}
		}

		closeStdin()
		exitErr := cmd.Wait()
		duration := time.Since(startTime)
		writeErr := <-writeDone
		switch {
		case runCtx.Err() == context.DeadlineExceeded:
			finalStatus = "timeout"
			finalError = fmt.Sprintf("%s timed out after %s", spec.provider, timeout)
		case runCtx.Err() == context.Canceled:
			finalStatus = "aborted"
			finalError = "execution cancelled"
		case writeErr != nil && finalStatus == "completed" && sessionID == "":
			finalStatus = "failed"
			finalError = fmt.Sprintf("write %s input: %v", spec.provider, writeErr)
		case exitErr != nil && finalStatus == "completed":
			finalStatus = "failed"
			finalError = fmt.Sprintf("%s exited with error: %v", spec.provider, exitErr)
		}
		if finalStatus == "completed" && sawAsyncLaunch {
			finalStatus = "failed"
			finalError = spec.provider + " launched an async background task; Multica-managed runs require foreground execution"
		}
		if finalError != "" {
			finalError = withAgentStderr(finalError, spec.provider, stderrBuf.Tail())
		}

		cfg.Logger.Info(spec.provider+" finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())
		reportedSessionID := resolveSessionID(opts.ResumeSessionID, sessionID, finalStatus == "failed")
		if reportedSessionID != sessionID {
			cfg.Logger.Info(spec.provider+" resume did not land; clearing fresh session id for daemon fallback",
				"requested_resume", opts.ResumeSessionID, "emitted_session", sessionID)
		}
		resCh <- Result{
			Status: finalStatus, Output: output.String(), Error: finalError,
			DurationMs: duration.Milliseconds(), SessionID: reportedSessionID, Usage: usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

func handleClaudeStreamUser(msg claudeStreamMessage, ch chan<- Message, detectAsync bool) bool {
	var content claudeStreamMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		return false
	}
	sawAsyncLaunch := false
	for _, block := range content.Content {
		if block.Type != "tool_result" {
			continue
		}
		result := ""
		if block.Content != nil {
			result = string(block.Content)
			if detectAsync && claudeToolResultHasAsyncLaunch(block.Content) {
				sawAsyncLaunch = true
			}
		}
		trySend(ch, Message{Type: MessageToolResult, CallID: block.ToolUseID, Output: result})
	}
	return sawAsyncLaunch
}

func handleClaudeStreamControlRequest(logger *slog.Logger, provider string, forceForeground bool, msg claudeStreamMessage, stdin interface{ Write([]byte) (int, error) }) {
	var req claudeStreamControlRequest
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		return
	}
	var input map[string]any
	if req.Input != nil {
		_ = json.Unmarshal(req.Input, &input)
	}
	if input == nil {
		input = map[string]any{}
	}
	if forceForeground && forceClaudeToolInputForeground(input) {
		logger.Info(provider+": forced foreground tool execution", "request_id", msg.RequestID, "tool", req.ToolName)
	}
	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": msg.RequestID,
			"response": map[string]any{"behavior": "allow", "updatedInput": input},
		},
	}
	data, err := json.Marshal(response)
	if err != nil {
		logger.Warn(provider+": failed to marshal control response", "error", err)
		return
	}
	if _, err := stdin.Write(append(data, '\n')); err != nil {
		logger.Warn(provider+": failed to write control response", "error", err)
	}
}

func writeClaudeStreamInput(w io.Writer, prompt, provider string) error {
	data, err := buildClaudeStreamInput(prompt, provider)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func buildClaudeStreamInput(prompt, provider string) ([]byte, error) {
	data, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": prompt}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s input: %w", provider, err)
	}
	return append(data, '\n'), nil
}
