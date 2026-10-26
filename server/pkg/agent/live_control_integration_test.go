//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestRealNativeLiveGoalPause(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real native model control is opt-in")
	}
	for _, provider := range []string{"codex", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var backend Backend
			var opts ExecOptions
			objective := "Explain in 250 words how binary search works. Do not use tools, read files, run commands, or delegate."
			if provider == "codex" {
				path, home, cwd := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_CWD")
				if path == "" || home == "" || cwd == "" {
					t.Fatal("explicit executable, isolated account home and cwd required")
				}
				backend = &codexBackend{cfg: Config{ExecutablePath: path, Env: map[string]string{"CODEX_HOME": home}, Logger: slog.Default()}}
				opts = ExecOptions{Cwd: cwd, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", ExtraArgs: []string{"--disable", "multi_agent"}}
			} else {
				path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
				if path == "" || home == "" {
					t.Fatal("explicit executable and isolated account home required")
				}
				backend = &codebuddyBackend{cfg: Config{ExecutablePath: path, Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": home + "/codebuddy", "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1"}, Logger: slog.Default()}}
				opts = ExecOptions{Cwd: home, Model: "deepseek-v4.1-flash", NativeGoal: true}
			}
			opts.Command, opts.Timeout = "/goal "+objective, 75*time.Second
			session, err := backend.Execute(ctx, objective, opts)
			if err != nil {
				t.Fatal(err)
			}
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				for range session.Messages {
				}
			}()
			if session.RunCommand == nil || session.CommandReady == nil {
				t.Fatal("native live command bridge absent")
			}
			for !session.CommandReady() {
				select {
				case <-ctx.Done():
					t.Fatal("native command bridge never became ready")
				case <-drained:
					t.Fatalf("execution ended before controls: %+v", <-session.Result)
				case <-time.After(5 * time.Millisecond):
				}
			}
			for _, command := range []string{"/goal show", "/goal pause"} {
				output, err := session.RunCommand(ctx, command)
				if err != nil {
					t.Fatalf("%s: %v", command, err)
				}
				t.Logf("native command confirmed: %s: %s", command, output)
			}
			<-drained
			result := <-session.Result
			if result.Status != "completed" || session.CommandReady() {
				t.Fatalf("paused goal did not finish: status=%s error=%s", result.Status, result.Error)
			}
			t.Logf("paused native Goal finished; duration_ms=%d reported_models=%d", result.DurationMs, len(result.Usage))
		})
	}
}
