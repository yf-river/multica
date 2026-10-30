package handler

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// StartTestAgentControl 让模型目录与已保存策略跟随控制端存活，复用环境维护脚本的锁、身份核对和空闲检查。
func (h *Handler) StartTestAgentControl(ctx context.Context) {
	root := os.Getenv("MULTICA_RELEASE_ROOT")
	if auth.EnvironmentID() != "prod" || !filepath.IsAbs(root) || os.Getenv("MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID") == "" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		return
	}
	script := filepath.Join(filepath.Dir(executable), "../../operations/test-agent-control.py")
	if _, err := os.Stat(script); err != nil {
		slog.Error("test Agent control script unavailable", "error", err)
		return
	}
	wake := make(chan struct{}, 1)
	h.RefreshTestAgents = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-timer.C:
			}
			runCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
			cmd := exec.CommandContext(runCtx, "python3", script)
			cmd.Dir = root
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
			cmd.WaitDelay = 5 * time.Second
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil && ctx.Err() == nil {
				slog.Warn("test Agent control refresh failed", "error", err)
			}
			cancel()
			timer.Reset(30 * time.Second)
		}
	}()
}
