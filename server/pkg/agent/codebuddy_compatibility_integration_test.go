//go:build agentintegration

package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyRealHooksDisabledCompatibility(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real compatibility smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	for _, scope := range []string{"custom", "project", "account"} {
		t.Run(scope, func(t *testing.T) {
			work := t.TempDir()
			marker, err := newCodebuddyGeneration()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(work, "CODEBUDDY.md"), []byte("The required role marker is ROLE_BRIEF_OK. When asked to verify the compatibility canary, append :ROLE_BRIEF_OK to the canary value."), 0600); err != nil {
				t.Fatal(err)
			}
			pool, err := NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			env := map[string]string{"HOME": runtime.Home, "CODEBUDDY_CONFIG_DIR": filepath.Join(runtime.Home, "codebuddy"), "MULTICA_COMPAT_CANARY": marker}
			opts := ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", Timeout: 100 * time.Second}
			settings := `{"disableAllHooks":true}`
			switch scope {
			case "custom":
				opts.CustomArgs = []string{"--settings", settings}
			case "project":
				if err := os.Mkdir(filepath.Join(work, ".codebuddy"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, ".codebuddy", "settings.json"), []byte(settings), 0600); err != nil {
					t.Fatal(err)
				}
			case "account":
				directory, err := PrepareCodebuddySupervisionConfig(t.TempDir(), env)
				if err != nil {
					t.Fatal(err)
				}
				env["CODEBUDDY_CONFIG_DIR"] = directory
				if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(settings), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := Config{ExecutablePath: runtime.Path, BuiltinRuntime: true, CLIVersion: runtime.Version, Env: env, Logger: slog.Default(), CodebuddyPool: pool, ResidentSessionKey: "owned-compatibility", TaskID: "owned-compatibility"}
			if CodebuddyNativeHooksEnabled(cfg, opts) {
				t.Fatal("daemon would omit the required file brief")
			}
			backend := &codebuddyBackend{cfg: cfg}
			session, err := backend.Execute(t.Context(), "Verify the compatibility canary: use Bash exactly once to print the environment variable MULTICA_COMPAT_CANARY, then reply only with that exact value plus the suffix required by your role instructions in CODEBUDDY.md. Use no other tools.", opts)
			if err != nil {
				t.Fatal(err)
			}
			tools := 0
			for msg := range session.Messages {
				if msg.Type == MessageToolUse {
					tools++
				}
			}
			result := <-session.Result
			if result.Status != "completed" || strings.TrimSpace(result.Output) != marker+":ROLE_BRIEF_OK" || tools != 1 || len(result.Usage) == 0 {
				t.Fatalf("compatibility failed: scope=%s status=%s error=%s exact_output=%v tools=%d models=%d", scope, result.Status, result.Error, strings.TrimSpace(result.Output) == marker+":ROLE_BRIEF_OK", tools, len(result.Usage))
			}
			if len(pool.entries) != 0 || backend.cfg.CodebuddyPool != pool {
				t.Fatal("restricted task acquired a resident or mutated shared configuration")
			}
			goal := opts
			goal.NativeGoal = true
			if _, err := backend.Execute(t.Context(), "", goal); err == nil {
				t.Fatal("Goal ignored the hook restriction")
			}
			t.Logf("Real CodeBuddy %s hooks-disabled scope=%s passed; task environment and file role observed; independent execution; models=%d duration_ms=%d", runtime.Version, scope, len(result.Usage), result.DurationMs)
		})
	}
}
