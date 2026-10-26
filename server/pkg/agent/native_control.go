package agent

import (
	"errors"
	"strings"
)

func nativeGoalActivatingCommand(command string) bool {
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "/goal ") {
		return false
	}
	switch strings.TrimSpace(strings.TrimPrefix(command, "/goal")) {
	case "", "show", "pause", "clear", "complete":
		return false
	default:
		return true
	}
}

// 原生目标激活后可以自行调用模型，必须纳入完整执行与计量生命周期。
func validateNativeControlOnly(opts ExecOptions) error {
	if opts.CommandOnly && nativeGoalActivatingCommand(opts.Command) {
		return errors.New("activating a native goal requires an execution turn")
	}
	return nil
}
