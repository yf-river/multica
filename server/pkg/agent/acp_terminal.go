package agent

// 文件职责：ACP terminal/* 方法在 daemon 侧的落地实现——当客户端宣告了 terminal
// 能力时，ACP agent 的 shell 工具就通过这些调用来执行命令。
//
// 每次 terminal/create 派生一个真正的本地子进程（有 args 就直连命令，否则按平台
// 套 shell：Windows cmd /c、其它 /bin/sh -c），把 stdout/stderr 一并收进一个有界
// 输出尾部（默认 50KB）。ACP 输出是字符串，所以截断时必须保证不把多字节 rune 的
// 尾字节留在开头、也不把读了一半的不完整 rune 暴露出去（见 Write/snapshot）。
// 子进程以进程组形式拥有，kill 时整组 SIGKILL，并在报成功前确认整组已消失。
// 生命周期：create →（output/wait_for_exit/kill 任意次数）→ release；release 在
// 清理确认后才删记录，保证失败可重试。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	// defaultACPOutputByteLimit 是 terminal 输出上限的缺省值（50KB），对应 ACP
	// 的截断契约。
	defaultACPOutputByteLimit = 50_000
	// acpTerminalProcessGroupCleanupTimeout 是 SIGKILL 后确认整个进程组消失的
	// 宽限窗口（kill 里用）。
	acpTerminalProcessGroupCleanupTimeout = 2 * time.Second
)

// acpTerminal 是 ACP terminal/* 方法在 daemon 侧的实现对象。ACP agent 在客户端
// 宣告 terminal 能力后，用这些调用驱动 shell 工具。输出以有界尾部形式保留，
// 既满足 ACP 的截断契约，又不会让一条命令把内存无限撑大。
type acpTerminal struct {
	cmd        *exec.Cmd     // 被管理的子进程
	mu         sync.Mutex    // 保护 output/exitStatus/truncated
	output     []byte        // 收集到的输出（有界尾部）
	limit      int           // 字节上限；<=0 表示不限
	truncated  bool          // 是否发生过截断（要如实回给 ACP）
	done       chan struct{} // Wait 协程结束即关闭
	exitStatus *acpTerminalExitStatus
}

// acpTerminalExitStatus 描述终端进程的退出情况：要么有退出码，要么（Unix）被某个
// 信号杀死，二者互斥。
type acpTerminalExitStatus struct {
	exitCode *uint32
	signal   *string
}

// acpTerminalOutputWriter 把 stdout/stderr 写进所属 terminal 的有界输出缓冲。
type acpTerminalOutputWriter struct {
	terminal *acpTerminal
}

// Write 实现 io.Writer：追加到输出并做有界截断。
func (w acpTerminalOutputWriter) Write(p []byte) (int, error) {
	w.terminal.mu.Lock()
	defer w.terminal.mu.Unlock()

	w.terminal.output = append(w.terminal.output, p...)
	if w.terminal.limit > 0 && len(w.terminal.output) > w.terminal.limit {
		start := len(w.terminal.output) - w.terminal.limit
		// ACP 的输出是字符串，截断时绝不能留下"开头字节被扔掉的多字节 rune 尾部"。
		// 因此要一路往后挪到下一个 rune 起点，宁可从那里多留一点。
		for start < len(w.terminal.output) && !utf8.RuneStart(w.terminal.output[start]) {
			start++
		}
		w.terminal.output = append([]byte(nil), w.terminal.output[start:]...)
		w.terminal.truncated = true
	}
	return len(p), nil
}

// snapshot 返回当前输出快照、是否截断与退出状态。快照会做两件事：把管道读可能
// 切开的"多字节 rune 残尾"藏起来（等下次快照补全），并把非法 UTF-8 替换成 U+FFFD。
func (t *acpTerminal) snapshot() (output string, truncated bool, exitStatus *acpTerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()

	raw := append([]byte(nil), t.output...)
	// 管道读可能把一个多字节 rune 拆到两次 Write 里。别把这个不完整的后缀暴露
	// 出去；等它补全后，下一次快照自然会出现。
	if len(raw) > 0 {
		lastRune := len(raw) - 1
		for lastRune > 0 && !utf8.RuneStart(raw[lastRune]) {
			lastRune--
		}
		if !utf8.FullRune(raw[lastRune:]) {
			raw = raw[:lastRune]
		}
	}
	valid := bytes.ToValidUTF8(raw, []byte("\uFFFD"))
	if t.limit > 0 && len(valid) > t.limit {
		start := len(valid) - t.limit
		for start < len(valid) && !utf8.RuneStart(valid[start]) {
			start++
		}
		valid = valid[start:]
	}
	if t.exitStatus != nil {
		status := *t.exitStatus
		exitStatus = &status
	}
	return string(valid), t.truncated, exitStatus
}

// wait 阻塞直到进程结束（done 关闭）。
func (t *acpTerminal) wait() {
	<-t.done
}

// kill 对整组进程 SIGKILL，并在 Unix 上等进程组真正消失后才返回成功——命令的
// 后代进程可能在该进程退出后仍留在组里（尤其后台子进程重定向了继承的输出时），
// 不确认就报成功会让调用方误以为资源已释放。
func (t *acpTerminal) kill() error {
	t.mu.Lock()
	cmd := t.cmd
	t.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	signalProcessGroup(cmd, syscall.SIGKILL)
	// Cmd.Wait 只描述直接进程。在 Unix 上，后代进程可能在它退出后仍留在其进程
	// 组里——尤其是后台子进程重定向了继承的输出时。必须确认整组都消失后，
	// terminal/kill 或 terminal/release 才算成功。
	if runtime.GOOS != "windows" && !waitProcessGroupGone(cmd, acpTerminalProcessGroupCleanupTimeout) {
		return fmt.Errorf("process group %d still active after %s", cmd.Process.Pid, acpTerminalProcessGroupCleanupTimeout)
	}
	return nil
}

// acpTerminalCreate 实现 ACP terminal/create：解出命令/参数/环境/工作目录，按
// 平台派生 shell 子进程，登记进终端表，并起一个 Wait 协程记录退出状态。
func (c *hermesClient) acpTerminalCreate(params json.RawMessage) (map[string]any, error) {
	var p struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Env     []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"env"`
		Cwd             string `json:"cwd"`
		OutputByteLimit int    `json:"outputByteLimit"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("terminal/create params: %w", err)
	}
	// 空命令无从执行，直接拒。
	if strings.TrimSpace(p.Command) == "" {
		return nil, fmt.Errorf("terminal/create requires command")
	}

	// 工作目录逐级回退：请求自带 → 客户端侧会话 cwd → "."。
	cwd := p.Cwd
	if cwd == "" {
		cwd = c.terminalCwd
	}
	if cwd == "" {
		cwd = "."
	}

	// 环境 = 客户端侧基础环境 + 请求追加的键值对（请求里的空键名忽略）。
	env := append([]string(nil), c.terminalEnv...)
	for _, item := range p.Env {
		if item.Name == "" {
			continue
		}
		env = append(env, item.Name+"="+item.Value)
	}

	// 有 args 就直连可执行文件；否则按平台套一层 shell 解释整条命令。
	var cmd *exec.Cmd
	if len(p.Args) > 0 {
		cmd = NewCommand(p.Command, nil).exec(c.terminalContext(), p.Args...)
	} else if runtime.GOOS == "windows" {
		cmd = NewCommand("cmd.exe", nil).exec(c.terminalContext(), "/d", "/s", "/c", p.Command)
	} else {
		cmd = NewCommand("/bin/sh", nil).exec(c.terminalContext(), "-c", p.Command)
	}
	hideAgentWindow(cmd)
	cmd.Dir = cwd
	cmd.Env = env

	// 输出上限没给或给得非法时落到默认值。
	limit := p.OutputByteLimit
	if limit <= 0 {
		limit = defaultACPOutputByteLimit
	}
	t := &acpTerminal{cmd: cmd, limit: limit, done: make(chan struct{})}
	writer := acpTerminalOutputWriter{terminal: t}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := startOwnedProcessTree(cmd, c.cfg.Logger); err != nil {
		return nil, fmt.Errorf("terminal/create start: %w", err)
	}

	// 分配 id 并登记；并发 create 都能拿到自己的槽位。
	id := c.nextACPTerminalID()
	c.terminalMu.Lock()
	if c.terminals == nil {
		c.terminals = make(map[string]*acpTerminal)
	}
	c.terminals[id] = t
	c.terminalMu.Unlock()

	// Wait 协程：记录退出状态（正常退出码 / 信号），关 done 通知 waiters。
	go func() {
		defer releaseProcessGroup(cmd)
		err := cmd.Wait()
		status := &acpTerminalExitStatus{}
		if err == nil {
			code := uint32(0)
			status.exitCode = &code
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			if code := exitErr.ExitCode(); code >= 0 {
				exitCode := uint32(code)
				status.exitCode = &exitCode
			} else {
				// 退出码为负（被信号杀）时记信号名。
				status.signal = acpProcessExitSignal(exitErr.ProcessState)
			}
		}
		t.mu.Lock()
		t.exitStatus = status
		t.mu.Unlock()
		close(t.done)
	}()

	return map[string]any{"terminalId": id}, nil
}

// terminalContext 返回执行终端进程用的 context。ACP 传输通常挂在一个进行中的
// 任务 context 上；但 CommandContext 不允许 nil context，所以给直接构造
// hermesClient 的单元测试留一个防御性回退。
func (c *hermesClient) terminalContext() context.Context {
	if c.terminalCtx != nil {
		return c.terminalCtx
	}
	return context.Background()
}

// nextACPTerminalID 分配下一个自增终端 id。
func (c *hermesClient) nextACPTerminalID() string {
	c.terminalMu.Lock()
	defer c.terminalMu.Unlock()
	c.nextTerminalID++
	return fmt.Sprintf("multica-terminal-%d", c.nextTerminalID)
}

// acpTerminalFor 按 id 查终端表。
func (c *hermesClient) acpTerminalFor(id string) (*acpTerminal, bool) {
	c.terminalMu.Lock()
	defer c.terminalMu.Unlock()
	t, ok := c.terminals[id]
	return t, ok
}

// acpTerminalRelease 杀掉并移除一个终端。先确认清理完成才从表里删除，这样一次
// 失败的 release 还能重试。
func (c *hermesClient) acpTerminalRelease(id string) error {
	c.terminalMu.Lock()
	t, ok := c.terminals[id]
	c.terminalMu.Unlock()
	if !ok {
		return nil
	}
	if err := t.kill(); err != nil {
		return fmt.Errorf("release terminal %q: %w", id, err)
	}

	// 只在清理被确认后删除，失败的 release 才能重试。
	c.terminalMu.Lock()
	if c.terminals[id] == t {
		delete(c.terminals, id)
	}
	c.terminalMu.Unlock()
	return nil
}

// acpTerminalResponse 分发 terminal/output、wait_for_exit、kill、release 四个
// 方法的请求。
func (c *hermesClient) acpTerminalResponse(method string, params json.RawMessage) (map[string]any, error) {
	var p struct {
		TerminalID string `json:"terminalId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("%s params: %w", method, err)
	}
	t, ok := c.acpTerminalFor(p.TerminalID)
	if !ok {
		return nil, fmt.Errorf("unknown terminal %q", p.TerminalID)
	}

	switch method {
	case "terminal/output":
		output, truncated, exitStatus := t.snapshot()
		result := map[string]any{"output": output, "truncated": truncated}
		if exitStatus != nil {
			result["exitStatus"] = acpTerminalExitStatusResult(exitStatus)
		}
		return result, nil
	case "terminal/wait_for_exit":
		t.wait()
		_, _, exitStatus := t.snapshot()
		if exitStatus == nil {
			return nil, fmt.Errorf("terminal %q exited without status", p.TerminalID)
		}
		return acpTerminalExitStatusResult(exitStatus), nil
	case "terminal/kill":
		if err := t.kill(); err != nil {
			return nil, fmt.Errorf("kill terminal %q: %w", p.TerminalID, err)
		}
		return map[string]any{}, nil
	case "terminal/release":
		return map[string]any{}, c.acpTerminalRelease(p.TerminalID)
	default:
		return nil, fmt.Errorf("unsupported terminal method %q", method)
	}
}

// acpTerminalExitStatusResult 把内部退出状态表示翻译成 ACP 协议结构
// {exitCode, signal}。
func acpTerminalExitStatusResult(status *acpTerminalExitStatus) map[string]any {
	result := map[string]any{"exitCode": nil, "signal": nil}
	if status.exitCode != nil {
		result["exitCode"] = int(*status.exitCode)
	}
	if status.signal != nil {
		result["signal"] = *status.signal
	}
	return result
}
