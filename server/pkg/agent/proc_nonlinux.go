//go:build !linux

package agent

import "os/exec"

func bindAgentProcessToParent(_ *exec.Cmd) {}
