// Package processtree 用来运行"受边界约束"的辅助命令：这些命令的后裔进程在
// 取消时不得存活。实现上，Unix 用进程组、Windows 用 Job Object 来圈住整棵树。
package processtree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const gracefulStopTimeout = time.Second

// CombinedOutput 运行一个尚未启动的命令并返回其合并后的输出。取消会终止整棵
// 进程树、等待其彻底消失，并返回 context 的取消原因，而不是平台相关的退出码。
func CombinedOutput(ctx context.Context, cmd *exec.Cmd, waitDelay time.Duration) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := run(ctx, cmd, waitDelay)
	return output.Bytes(), err
}

// Output 是 exec.Cmd.Output 的"进程树安全"等价物。
func Output(ctx context.Context, cmd *exec.Cmd, waitDelay time.Duration) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := run(ctx, cmd, waitDelay)
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		exitErr.Stderr = append([]byte(nil), stderr.Bytes()...)
	}
	return stdout.Bytes(), err
}

// Run 执行一个尚未启动的命令，并全权负责其整棵进程树的清理。
func Run(ctx context.Context, cmd *exec.Cmd, waitDelay time.Duration) error {
	return run(ctx, cmd, waitDelay)
}

// run 是三个公开入口（CombinedOutput/Output/Run）共享的实现：先把命令放进一个
// 进程组/Job 里启动，再在 ctx 取消时先 SIGTERM（优雅）后 SIGKILL（兜底）地杀掉
// 整棵树，最后确认进程组已消失才返回，避免后裔进程泄漏。
func run(ctx context.Context, cmd *exec.Cmd, waitDelay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	controller, err := newController(cmd)
	if err != nil {
		return fmt.Errorf("create process-tree controller: %w", err)
	}
	defer controller.close()

	cmd.WaitDelay = waitDelay
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := controller.attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = controller.finish(cmd)
		return fmt.Errorf("attach process tree: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var stopErr error
	cancelled := false
	select {
	case err = <-waitDone:
	case <-ctx.Done():
		cancelled = true
		stopErr = controller.interrupt(cmd)
		timer := time.NewTimer(gracefulStopTimeout)
		select {
		case err = <-waitDone:
			timer.Stop()
		case <-timer.C:
			stopErr = errors.Join(stopErr, controller.stop(cmd))
			err = <-waitDone
		}
	}
	finishErr := controller.finish(cmd)
	if lifecycleErr := errors.Join(stopErr, finishErr); lifecycleErr != nil {
		return fmt.Errorf("stop process tree: %w", lifecycleErr)
	}
	if cancelled {
		return context.Cause(ctx)
	}
	return err
}
