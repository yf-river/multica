package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const taskContextEnvFile = ".agent_context/task_env.json"

type taskContextEnv struct {
	Token        string `json:"token,omitempty"`
	ServerURL    string `json:"server_url,omitempty"`
	DaemonPort   string `json:"daemon_port,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	TaskSlot     string `json:"task_slot,omitempty"`
	AutopilotRun string `json:"autopilot_run_id,omitempty"`
	AutopilotID  string `json:"autopilot_id,omitempty"`
	QuickCreate  string `json:"quick_create_task_id,omitempty"`
}

func loadTaskContextEnvFromCWD() taskContextEnv {
	wd, err := os.Getwd()
	if err != nil {
		return taskContextEnv{}
	}
	for {
		path := filepath.Join(wd, taskContextEnvFile)
		data, err := os.ReadFile(path)
		if err == nil {
			var env taskContextEnv
			if json.Unmarshal(data, &env) == nil {
				return env
			}
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return taskContextEnv{}
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
