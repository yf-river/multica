package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// claudeProtocolProcess owns the subprocess resources shared by Claude Code
// and compatible forks. Message decoding remains backend-specific.
type claudeProtocolProcess struct {
	cmd              *exec.Cmd
	stdout           io.ReadCloser
	stdin            io.WriteCloser
	closeStdin       func()
	stderr           *stderrTail
	mcpConfigCleanup func()
}

func startClaudeProtocolProcess(
	runCtx context.Context,
	cancel context.CancelFunc,
	cfg Config,
	opts ExecOptions,
	execPath string,
	args []string,
	backendName string,
) (*claudeProtocolProcess, error) {
	var mcpConfigPath string
	if len(opts.McpConfig) > 0 {
		path, err := writeMcpConfigToTemp(opts.McpConfig)
		if err != nil {
			cancel()
			return nil, err
		}
		mcpConfigPath = path
		args = append(args, "--mcp-config", path)
	}
	cleanupMCP := func() {
		if mcpConfigPath != "" {
			_ = os.Remove(mcpConfigPath)
		}
	}
	started := false
	defer func() {
		if !started {
			cleanupMCP()
		}
	}()

	cmd := exec.CommandContext(runCtx, execPath, args...)
	hideAgentWindow(cmd)
	cfg.Logger.Info("agent command", "exec", execPath, "args", args)
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s stdout pipe: %w", backendName, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s stdin pipe: %w", backendName, err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	stderr := newStderrTail(newLogWriter(cfg.Logger, "["+backendName+":stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start %s: %w", backendName, err)
	}
	started = true
	cfg.Logger.Info(backendName+" started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)
	return &claudeProtocolProcess{
		cmd: cmd, stdout: stdout, stdin: stdin, closeStdin: closeStdin, stderr: stderr,
		mcpConfigCleanup: cleanupMCP,
	}, nil
}
