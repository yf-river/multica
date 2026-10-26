package daemon

import "strings"

func runtimeCommandOnly(command, input string) bool {
	if strings.TrimSpace(input) != command {
		return false
	}
	switch command {
	case "/plan", "/plan off", "/goal", "/goal show", "/goal pause", "/goal clear", "/goal complete":
		return true
	}
	return false
}
