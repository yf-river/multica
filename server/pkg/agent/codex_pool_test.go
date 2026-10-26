//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func codexPoolFixture(t *testing.T) (Config, ExecOptions, Command, []string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`
[features]
multi_agent = true
[shell_environment_policy]
inherit = "none"
[shell_environment_policy.set]
RETAIN_USER = "custom"
MULTICA_TOKEN = "old-source-token"
MULTICA_QUICK_CREATE_TASK_ID = "previous-task-only"
[mcp_servers.owned]
command = "fixture-mcp"
env_vars = ["MULTICA_TOKEN", "MULTICA_TASK_ID"]
[mcp_servers.api]
url = "http://localhost/example"
bearer_token_env_var = "MULTICA_TOKEN"
[mcp_servers.api.env_http_headers]
X-Task-ID = "MULTICA_TASK_ID"
`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture-runtime")
	if err := os.WriteFile(path, []byte(`#!/usr/bin/env python3
import socket,sys,signal
address=next(x[len("unix://"):] for x in sys.argv if x.startswith("unix://"))
s=socket.socket(socket.AF_UNIX);s.bind(address);s.listen()
while True:
 c,_=s.accept();c.close()
`), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ExecutablePath: path, CodexVersion: "0.164.0", TaskID: "current-task", ResidentSessionKey: "workspace/runtime/agent/chat", Env: map[string]string{"CODEX_HOME": home, "MULTICA_TOKEN": "current-token", "MULTICA_TASK_ID": "current-task"}, Logger: slog.Default()}
	return cfg, ExecOptions{Cwd: t.TempDir()}, NewCommand(path, nil), []string{"app-server", "--listen", "stdio://"}
}

func TestCodexResidentConfigurationUsesCurrentCredentials(t *testing.T) {
	t.Setenv("MULTICA_AMBIENT_CREDENTIAL", "daemon-parent-only")
	cfg, opts, command, args := codexPoolFixture(t)
	config, first, err := codexResidentConfig(cfg, opts, command, args)
	if err != nil {
		t.Fatal(err)
	}
	set := config["shell_environment_policy"].(map[string]any)["set"].(map[string]any)
	if set["MULTICA_TOKEN"] != "current-token" || set["RETAIN_USER"] != "custom" || set["MULTICA_QUICK_CREATE_TASK_ID"] != nil {
		t.Fatal("current task shell environment was not isolated")
	}
	servers := config["mcp_servers"].(map[string]any)
	if servers["owned"].(map[string]any)["env"].(map[string]any)["MULTICA_TOKEN"] != "current-token" {
		t.Fatal("stdio MCP did not receive the current task credential")
	}
	api := servers["api"].(map[string]any)
	headers := api["http_headers"].(map[string]any)
	if api["bearer_token_env_var"] != nil || headers["Authorization"] != "Bearer current-token" || headers["X-Task-ID"] != "current-task" || api["env"] != nil {
		t.Fatal("HTTP MCP credential was resolved from resident process state")
	}
	cfg.Env["MULTICA_TOKEN"], cfg.Env["MULTICA_TASK_ID"], cfg.TaskID = "next-token", "next-task", "next-task"
	_, next, err := codexResidentConfig(cfg, opts, command, args)
	if err != nil || first != next {
		t.Fatal("a new task credential invalidated the static process configuration")
	}
	rootEnv := strings.Join(codexResidentEnvironment(cfg.Env), "\x00")
	if strings.Contains(rootEnv, "MULTICA_TOKEN=") || strings.Contains(rootEnv, "MULTICA_TASK_ID=") || strings.Contains(rootEnv, "MULTICA_AMBIENT_CREDENTIAL=") {
		t.Fatal("native root environment contains task credentials")
	}
	cfg.Env["CUSTOM_ACCOUNT_SETTING"] = "changed"
	_, changed, err := codexResidentConfig(cfg, opts, command, args)
	if err != nil || changed == first {
		t.Fatal("account/profile environment change did not invalidate the process")
	}
}

func TestCodexResidentHomeKeepsPrimaryStateAndRemovesTaskDefaults(t *testing.T) {
	cfg, _, _, _ := codexPoolFixture(t)
	source := cfg.Env["CODEX_HOME"]
	if err := os.Mkdir(filepath.Join(source, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "home")
	sqliteHome, err := prepareCodexResidentHome(source, cache, "")
	if err != nil || sqliteHome != source {
		t.Fatalf("primary SQLite binding: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cache, "config.toml"))
	if err != nil || bytes.Contains(data, []byte("old-source-token")) {
		t.Fatal("resident defaults retain a task credential")
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["mcp_servers"] != nil || config["shell_environment_policy"] != nil || config["sqlite_home"] != source {
		t.Fatal("resident root retained task defaults or changed durable state")
	}
	for _, name := range []string{"sessions", "skills"} {
		resolved, err := filepath.EvalSymlinks(filepath.Join(cache, name))
		if err != nil || resolved != filepath.Join(source, name) {
			t.Fatalf("primary %s binding: %v", name, err)
		}
	}
}

func TestCodexResidentLeaseCapacityCancellationAndIdleCleanup(t *testing.T) {
	cfg, opts, command, args := codexPoolFixture(t)
	var pins atomic.Int32
	cfg.PinResidentHome = func() func() { pins.Add(1); return func() { pins.Add(-1) } }
	pool, err := NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 1, 150*time.Millisecond, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lease, err := pool.acquire(t.Context(), cfg, opts, command, args)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release(false)
	for _, key := range []string{cfg.ResidentSessionKey, "another-conversation"} {
		waiting := cfg
		waiting.ResidentSessionKey = key
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		_, err := pool.acquire(ctx, waiting, opts, command, args)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("busy session/capacity admission: %v", err)
		}
	}
	pid := lease.entry.cmd.Process.Pid
	// 夹具只监听 socket，没有建立原生线程，满足空闲租约前提。
	lease.release(true)
	cfg.TaskID = "next-task"
	next, err := pool.acquire(t.Context(), cfg, opts, command, args)
	if err != nil {
		t.Fatal(err)
	}
	if next.entry.cmd.Process.Pid != pid || !next.reused || next.execution == lease.execution || pins.Load() != 1 {
		t.Fatal("resident lease did not reuse the process with a fresh execution scope")
	}
	next.release(true)
	select {
	case <-next.entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle native process was not reaped")
	}
	deadline := time.Now().Add(time.Second)
	for pins.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pins.Load() != 0 {
		t.Fatal("idle process retained its task-home GC pin")
	}
}

func TestCodexResidentRootHasExclusiveOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	first, err := NewCodexProcessPool(root, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := NewCodexProcessPool(root, 1, time.Minute, slog.Default()); err == nil {
		second.Close()
		t.Fatal("a second owner acquired the live process pool")
	}
}

func TestCodexResidentRecoveryReapsOnlyItsRecordedProcess(t *testing.T) {
	cfg, opts, command, args := codexPoolFixture(t)
	root := filepath.Join(t.TempDir(), "pool")
	first, err := NewCodexProcessPool(root, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	lease, err := first.acquire(t.Context(), cfg, opts, command, args)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release(false)
	// 模拟所有者退出而原生进程仍存活；不依赖一个旧状态文件推断进程已结束。
	if !codexPoolProcessMatches(captureCLIIdentity(lease.entry.cmd.Process.Pid), lease.entry.owner) {
		t.Fatal("fixture process is not live with its recorded owner")
	}
	if err := first.lock.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewCodexProcessPool(root, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	select {
	case <-lease.entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not reap the verified live process")
	}
	first.Close()
	next, err := recovered.acquire(t.Context(), cfg, opts, command, args)
	if err != nil {
		t.Fatal(err)
	}
	defer next.release(false)
	if next.entry.owner == lease.entry.owner {
		t.Fatal("new ownership reused the crashed process identity")
	}
}

func TestCodexResidentExecutionCleanupDoesNotKillAnotherLease(t *testing.T) {
	_, _, command, _ := codexPoolFixture(t)
	spawn := func(owner string) *codexPoolEntry {
		t.Helper()
		dir, err := os.MkdirTemp("", "multica-codex-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		cmd := command.exec(t.Context(), "app-server", "--listen", "unix://"+filepath.Join(dir, "rpc.sock"))
		cmd.Env = buildEnv(map[string]string{codexExecutionOwnerEnv: owner})
		if err := startOwnedProcessTree(cmd, slog.Default()); err != nil {
			t.Fatal(err)
		}
		entry := &codexPoolEntry{cmd: cmd, done: make(chan struct{})}
		go func() { _ = cmd.Wait(); close(entry.done) }()
		t.Cleanup(func() {
			select {
			case <-entry.done:
			default:
				signalProcessGroup(cmd, 9)
			}
			<-entry.done
			releaseProcessGroup(cmd)
		})
		deadline := time.Now().Add(time.Second)
		for !codexOwnedProcessMatches(captureCLIIdentity(cmd.Process.Pid), codexExecutionOwnerEnv, owner) {
			if time.Now().After(deadline) {
				t.Fatal("fixture process did not acquire its execution owner")
			}
			time.Sleep(time.Millisecond)
		}
		return entry
	}
	first, second := spawn("owned-first-execution"), spawn("owned-second-execution")
	if err := reapCodexResidentExecution("owned-first-execution"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("old execution process survived cleanup")
	}
	if !codexOwnedProcessMatches(captureCLIIdentity(second.cmd.Process.Pid), codexExecutionOwnerEnv, "owned-second-execution") {
		t.Fatal("cleanup affected another execution")
	}
}

func TestCodexResidentNestedConfigurationCannotEnableSubagents(t *testing.T) {
	c := &codexClient{cfg: Config{CodexVersion: "0.164.0"}, threadConfig: map[string]any{
		"features": map[string]any{"multi_agent": true, "multi_agent_v2": true, "other_user_feature": true},
		"agents":   map[string]any{"enabled": true, "other_user_setting": "preserved"},
	}}
	config := c.currentThreadConfig()
	features := config["features"].(map[string]any)
	agents := config["agents"].(map[string]any)
	if features["multi_agent"] != false || features["multi_agent_v2"] != false || agents["enabled"] != false || features["other_user_feature"] != true || agents["other_user_setting"] != "preserved" {
		t.Fatal("thread policy did not preserve user fields while disabling subagents")
	}
	if _, exists := config["features.multi_agent"]; exists {
		t.Fatal("overlapping table and dotted-key overrides remain order-dependent")
	}
}

func TestCodexResidentResourceFingerprintTracksContentPermissionsAndLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"a", ".git"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"a/note": "alpha", "b": "beta", ".git/ignored": "ignored"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"c": "a", "d": "missing", "e": "."} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func(path string) string {
		t.Helper()
		var output bytes.Buffer
		if err := hashCodexResidentSkills(&output, path, "skills", make(map[string]bool)); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	want := "skills:700\nskills/a:700\nskills/a/note:600\nalphaskills/b:600\nbetaskills/c:700\nskills/e:700\n"
	if got := fingerprint(root); got != want {
		t.Fatalf("resource content, aliases or directory cycle changed fingerprint: %q", got)
	}
	alias := filepath.Join(t.TempDir(), "account-skills")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if fingerprint(alias) != want {
		t.Fatal("linked resource root changed its fingerprint")
	}
	note := filepath.Join(root, "a", "note")
	info, err := os.Stat(note)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("omega"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(note, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed := fingerprint(root)
	if changed == want {
		t.Fatal("same-size content change with restored mtime did not invalidate fingerprint")
	}
	if err := os.Chmod(filepath.Join(root, "b"), 0700); err != nil {
		t.Fatal(err)
	}
	if fingerprint(root) == changed {
		t.Fatal("resource permission change did not invalidate fingerprint")
	}
}

func TestCodexResidentResourceFingerprintRejectsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := hashCodexResidentSkills(&bytes.Buffer{}, root, "plugins", make(map[string]bool)); err == nil {
		t.Fatal("special resource must be rejected before attempting to read it")
	}
}
