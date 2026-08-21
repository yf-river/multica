package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const taskContextEnvRelPath = ".agent_context/task_env.json"

type taskContextEnvFile struct {
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

func writeTaskContextEnv(workDir string, env map[string]string) (func(), error) {
	path := filepath.Join(workDir, taskContextEnvRelPath)
	data, err := json.MarshalIndent(taskContextEnvFile{
		Token:        env["MULTICA_TOKEN"],
		ServerURL:    env["MULTICA_SERVER_URL"],
		DaemonPort:   env["MULTICA_DAEMON_PORT"],
		WorkspaceID:  env["MULTICA_WORKSPACE_ID"],
		AgentName:    env["MULTICA_AGENT_NAME"],
		AgentID:      env["MULTICA_AGENT_ID"],
		TaskID:       env["MULTICA_TASK_ID"],
		TaskSlot:     env["MULTICA_TASK_SLOT"],
		AutopilotRun: env["MULTICA_AUTOPILOT_RUN_ID"],
		AutopilotID:  env["MULTICA_AUTOPILOT_ID"],
		QuickCreate:  env["MULTICA_QUICK_CREATE_TASK_ID"],
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal task context env: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create task context env dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write task context env: %w", err)
	}
	return func() {
		_ = os.Remove(path)
	}, nil
}

func runtimeCodexHomeForBroker() string {
	if v := strings.TrimSpace(os.Getenv("MULTICA_CODEX_HOME")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("CODEX_HOME"))
}

func prepareCodexBrokerProcessEnv(agentEnv map[string]string, taskCodexHome string, logger *slog.Logger) (map[string]string, error) {
	runtimeHome := strings.TrimSpace(taskCodexHome)
	if runtimeHome == "" {
		runtimeHome = runtimeCodexHomeForBroker()
	}
	if runtimeHome == "" {
		return nil, fmt.Errorf("codex broker requires MULTICA_CODEX_HOME or CODEX_HOME")
	}
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		return nil, fmt.Errorf("create codex broker home: %w", err)
	}
	skillsHash, err := hashDir(filepath.Join(runtimeHome, "skills"))
	if err != nil {
		logger.Warn("codex broker: skill hash failed; continuing with empty hash", "error", err)
	}

	out := make(map[string]string, len(agentEnv)+4)
	for k, v := range agentEnv {
		out[k] = v
	}
	for _, key := range codexBrokerTaskScopedEnvKeys {
		delete(out, key)
	}
	out["CODEX_HOME"] = runtimeHome
	out["MULTICA_CODEX_HOME"] = runtimeHome
	out["MULTICA_CODEX_BROKER"] = "1"
	out["MULTICA_CODEX_BROKER_SKILLS_HASH"] = skillsHash
	return out, nil
}

var codexBrokerTaskScopedEnvKeys = []string{
	"MULTICA_TOKEN",
	"MULTICA_WORKSPACE_ID",
	"MULTICA_AGENT_NAME",
	"MULTICA_AGENT_ID",
	"MULTICA_TASK_ID",
	"MULTICA_TASK_SLOT",
	"MULTICA_AUTOPILOT_RUN_ID",
	"MULTICA_AUTOPILOT_ID",
	"MULTICA_QUICK_CREATE_TASK_ID",
	"MULTICA_QUICK_CREATE_ATTACHMENT_IDS",
}

func hashDir(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "", nil
	}
	var files []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
