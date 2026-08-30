package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const taskContextEnvFile = ".agent_context/task_env.json"

func loadTaskContextEnvFromCWD() protocol.TaskContextEnvironment {
	if path := strings.TrimSpace(os.Getenv("MULTICA_TASK_CONTEXT_FILE")); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var env protocol.TaskContextEnvironment
			if json.Unmarshal(data, &env) == nil {
				return env
			}
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return protocol.TaskContextEnvironment{}
	}
	for {
		path := filepath.Join(wd, taskContextEnvFile)
		data, err := os.ReadFile(path)
		if err == nil {
			var env protocol.TaskContextEnvironment
			if json.Unmarshal(data, &env) == nil {
				return env
			}
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return protocol.TaskContextEnvironment{}
		}
		wd = parent
	}
}

func taskContextValue(envKey string) string {
	ctx := loadTaskContextEnvFromCWD()
	switch envKey {
	case "MULTICA_TOKEN":
		return strings.TrimSpace(ctx.Token)
	case "MULTICA_SERVER_URL":
		return strings.TrimSpace(ctx.ServerURL)
	case "MULTICA_DAEMON_PORT":
		return strings.TrimSpace(ctx.DaemonPort)
	case "MULTICA_WORKSPACE_ID":
		return strings.TrimSpace(ctx.WorkspaceID)
	case "MULTICA_AGENT_NAME":
		return strings.TrimSpace(ctx.AgentName)
	case "MULTICA_AGENT_ID":
		return strings.TrimSpace(ctx.AgentID)
	case "MULTICA_TASK_ID":
		return strings.TrimSpace(ctx.TaskID)
	case "MULTICA_TASK_SLOT":
		return strings.TrimSpace(ctx.TaskSlot)
	case "MULTICA_AUTOPILOT_RUN_ID":
		return strings.TrimSpace(ctx.AutopilotRun)
	case "MULTICA_AUTOPILOT_ID":
		return strings.TrimSpace(ctx.AutopilotID)
	case "MULTICA_QUICK_CREATE_TASK_ID":
		return strings.TrimSpace(ctx.QuickCreate)
	default:
		return ""
	}
}

func envOrTaskContext(envKey string) string {
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	return taskContextValue(envKey)
}
