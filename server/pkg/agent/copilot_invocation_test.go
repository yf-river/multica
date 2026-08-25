package agent

import (
	"testing"
)

func TestChooseCopilotInvocation_PassthroughForNonLauncher(t *testing.T) {
	assertInvocationPassthrough(t, "copilot", []string{
		"-p", "You are running as a local coding agent.\n\nDo something.",
		"--output-format", "json",
		"--allow-all",
		"--no-ask-user",
	}, chooseCopilotInvocation)
}
