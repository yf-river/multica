package agent

import (
	"strings"
	"testing"
)

func TestNativeGoalActivationCannotBypassExecutionLifecycle(t *testing.T) {
	for _, command := range []string{"/goal new objective", "/goal resume"} {
		for _, provider := range []string{"codex", "codebuddy"} {
			t.Run(provider+command, func(t *testing.T) {
				cfg := Config{ExecutablePath: "/nonexistent-native-runtime"}
				var backend Backend = &codexBackend{cfg: cfg}
				if provider == "codebuddy" {
					backend = &codebuddyBackend{cfg: cfg}
				}
				_, err := backend.Execute(t.Context(), "", ExecOptions{Command: command, CommandOnly: true})
				if err == nil || !strings.Contains(err.Error(), "requires an execution turn") {
					t.Fatalf("unaccounted native generation reached process launch: %v", err)
				}
			})
		}
	}
}
