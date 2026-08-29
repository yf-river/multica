package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"
)

type streamCommandSpec struct {
	name          string
	pipeName      string
	stderrName    string
	executable    string
	args          []string
	env           []string
	cwd           string
	waitDelay     time.Duration
	logger        *slog.Logger
	model         string
	closeStdin    bool
	captureStderr bool
	parse         func(io.Reader, chan<- Message, context.CancelFunc) streamCommandResult
	afterWait     func(*streamCommandResult)
	cleanup       func()
}

type streamCommandResult struct {
	status             string
	output             string
	errMsg             string
	sessionID          string
	usage              map[string]TokenUsage
	terminalResultSeen bool
}

func executeStreamCommand(ctx context.Context, timeout time.Duration, spec streamCommandSpec) (*Session, error) {
	runCtx, cancel := runContext(ctx, timeout)
	cleanup := func() {
		cancel()
		if spec.cleanup != nil {
			spec.cleanup()
		}
	}

	cmd := exec.CommandContext(runCtx, spec.executable, spec.args...)
	hideAgentWindow(cmd)
	bindAgentProcessToParent(cmd)
	spec.logger.Info("agent command", "exec", spec.executable, "args", spec.args)
	cmd.WaitDelay = spec.waitDelay
	cmd.Dir = spec.cwd
	cmd.Env = spec.env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		name := spec.pipeName
		if name == "" {
			name = spec.name
		}
		return nil, fmt.Errorf("%s stdout pipe: %w", name, err)
	}

	var stdin io.WriteCloser
	if spec.closeStdin {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("%s stdin pipe: %w", spec.name, err)
		}
	}

	var stderrBuf *stderrTail
	stderrName := spec.stderrName
	if stderrName == "" {
		stderrName = spec.name
	}
	stderrWriter := newLogWriter(spec.logger, "["+stderrName+":stderr] ")
	if spec.captureStderr {
		stderrBuf = newStderrTail(stderrWriter, agentStderrTailBytes)
		cmd.Stderr = stderrBuf
	} else {
		cmd.Stderr = stderrWriter
	}

	if err := cmd.Start(); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		cleanup()
		return nil, fmt.Errorf("start %s: %w", spec.name, err)
	}
	if stdin != nil {
		_ = stdin.Close()
	}

	spec.logger.Info(spec.name+" started", "pid", cmd.Process.Pid, "cwd", spec.cwd, "model", spec.model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	go func() {
		<-runCtx.Done()
		_ = stdout.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		if spec.cleanup != nil {
			defer spec.cleanup()
		}

		startTime := time.Now()
		result := spec.parse(stdout, msgCh, cancel)
		if result.status == "" {
			result.status = "completed"
		}

		exitErr := cmd.Wait()
		duration := time.Since(startTime)
		if spec.afterWait != nil {
			spec.afterWait(&result)
		}

		if runCtx.Err() == context.DeadlineExceeded {
			result.status = "timeout"
			result.errMsg = fmt.Sprintf("%s timed out after %s", spec.name, timeout)
		} else if runCtx.Err() == context.Canceled && !result.terminalResultSeen {
			result.status = "aborted"
			result.errMsg = "execution cancelled"
		} else if exitErr != nil && result.status == "completed" && !result.terminalResultSeen {
			result.status = "failed"
			result.errMsg = fmt.Sprintf("%s exited with error: %v", spec.name, exitErr)
		}
		if result.errMsg != "" && stderrBuf != nil {
			result.errMsg = withAgentStderr(result.errMsg, spec.name, stderrBuf.Tail())
		}

		spec.logger.Info(spec.name+" finished", "pid", cmd.Process.Pid, "status", result.status, "duration", duration.Round(time.Millisecond).String())

		resCh <- Result{
			Status:     result.status,
			Output:     result.output,
			Error:      result.errMsg,
			DurationMs: duration.Milliseconds(),
			SessionID:  result.sessionID,
			Usage:      result.usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}
