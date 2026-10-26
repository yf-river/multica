//go:build agentintegration

package execenv

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// 测试包装器只允许 Skill 工具；真实 CLI 和模型验证可调用及禁用两种行为。
func TestCodebuddyRealSkillPolicy(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("native skill model smoke is opt-in")
	}
	if runtime.GOOS == "windows" {
		t.Skip("isolated tool wrapper uses POSIX shell")
	}
	executable, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if executable == "" || home == "" {
		t.Fatal("explicit executable and isolated account home required")
	}
	config := filepath.Join(home, "codebuddy")
	skills := filepath.Join(config, "skills")
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	skill, err := os.MkdirTemp(skills, "multica-disable-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(skill) })
	name := filepath.Base(skill)
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Native skill policy smoke.\nuser-invocable: true\n---\nReply with the exact text SKILL_POLICY_NATIVE_OK and do nothing else.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := prepareCodebuddySkillSettings(t.TempDir(), []RuntimeSkillRefForEnv{{Root: "provider", Key: name, Name: name}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "codebuddy-skill-only")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' --tools Skill --strict-mcp-config \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := agent.New("codebuddy", agent.Config{ExecutablePath: wrapper, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": config, "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			opts := agent.ExecOptions{Cwd: home, Model: "deepseek-v4.1-flash", NativeGoal: native, Timeout: 60 * time.Second, MaxTurns: 3}
			if disabled {
				opts.CodebuddySettingsPath = settings
			}
			prompt := "Invoke the Skill tool with the exact skill name " + name + ". Follow that skill and provide its result. Do not invoke any other tool."
			if disabled {
				prompt = "/" + name
			}
			session, err := backend.Execute(ctx, prompt, opts)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			var toolOutput strings.Builder
			for msg := range session.Messages {
				if msg.Type == agent.MessageToolResult {
					toolOutput.WriteString(msg.Output)
				}
			}
			result := <-session.Result
			cancel()
			if result.Status != "completed" {
				t.Fatalf("native=%v disabled=%v status=%s error=%s", native, disabled, result.Status, result.Error)
			}
			if !disabled && !strings.Contains(result.Output, "SKILL_POLICY_NATIVE_OK") {
				t.Fatalf("native=%v positive skill control failed: %s", native, result.Output)
			}
			if disabled && (strings.Contains(result.Output, "SKILL_POLICY_NATIVE_OK") || !strings.Contains(strings.ToLower(toolOutput.String()+result.Output), "disabled via skilloverrides")) {
				t.Fatalf("native=%v disabled skill was not natively rejected; output=%s tool=%s", native, result.Output, toolOutput.String())
			}
			t.Logf("Real skill execution: native=%v disabled=%v reported_models=%d", native, disabled, len(result.Usage))
		}
	}
}
