package agent

import (
	"testing"
)

func TestChoosePiInvocation_PassthroughForNonLauncher(t *testing.T) {
	assertInvocationPassthrough(t, "pi", []string{
		"-p",
		"--mode", "json",
		"--session", "/tmp/pi-session.jsonl",
		"You are running as a chat assistant for a Multica workspace.\n\nUser message:\n我需要创建一个issue\n",
	}, choosePiInvocation)
}
