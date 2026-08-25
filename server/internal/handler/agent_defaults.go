package handler

import "strings"

const (
	defaultAgentMaxConcurrentTasks int32 = 20
	defaultCodebuddyAgentModel           = "deepseek-v4-pro-ioa"
)

func agentModelForRuntime(provider, requested string) string {
	model := strings.TrimSpace(requested)
	if model != "" {
		return model
	}
	if strings.EqualFold(strings.TrimSpace(provider), "codebuddy") {
		return defaultCodebuddyAgentModel
	}
	return ""
}
