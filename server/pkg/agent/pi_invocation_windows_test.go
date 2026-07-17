//go:build windows

package agent

import "testing"

func TestPlatformPiInvocation(t *testing.T) {
	assertWindowsLauncherContract(t, "pi", []string{
		"-p", "--mode", "json",
		"--session", `C:\Users\X\.multica\pi-sessions\20260528T040000.jsonl`,
		"You are running as a chat assistant for a Multica workspace.\n\nUser message:\n我需要创建一个issue\n",
	}, platformPiInvocation)
}
