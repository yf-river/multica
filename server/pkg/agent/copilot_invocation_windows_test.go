//go:build windows

package agent

import "testing"

func TestPlatformCopilotInvocation(t *testing.T) {
	assertWindowsLauncherContract(t, "copilot", []string{
		"-p", "You are running as a local coding agent.\n\n# Context\nDo the task.\n",
		"--output-format", "json", "--allow-all", "--no-ask-user",
	}, platformCopilotInvocation)
}
