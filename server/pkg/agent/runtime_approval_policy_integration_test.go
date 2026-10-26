//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// 原生纯控制验证角色策略可由已固定的 CLI 接受，不生成模型回合。
func TestRealNativeNeverApprovalPolicy(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("real runtime control smoke is opt-in")
	}
	for _, provider := range []string{"codex", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			prefix := "MULTICA_CODEX_CONTROL_SMOKE_"
			if provider == "codebuddy" {
				prefix = "MULTICA_CODEBUDDY_CONTROL_SMOKE_"
			}
			path, home := os.Getenv(prefix+"PATH"), os.Getenv(prefix+"HOME")
			if path == "" || home == "" {
				t.Fatal("explicit executable and isolated home required")
			}
			cfg := Config{ExecutablePath: path, Logger: slog.Default(), Env: map[string]string{"HOME": home}}
			var backend Backend
			if provider == "codex" {
				cfg.Env["CODEX_HOME"] = home
				backend = &codexBackend{cfg: cfg}
			} else {
				cfg.Env["CODEBUDDY_CONFIG_DIR"] = home + "/codebuddy"
				backend = &codebuddyBackend{cfg: cfg}
			}
			ctx, stop := context.WithTimeout(context.Background(), 35*time.Second)
			defer stop()
			session, err := backend.Execute(ctx, "", ExecOptions{Cwd: home, Command: "/goal show", CommandOnly: true, ApprovalPolicy: "never", Timeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.Status != "completed" || len(result.Usage) != 0 {
				t.Fatalf("native control rejected role policy: status=%s error=%s reported_models=%d", result.Status, result.Error, len(result.Usage))
			}
		})
	}
}
