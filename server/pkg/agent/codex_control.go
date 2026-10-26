package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func codexGoalActivatingCommand(command string) bool {
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

func (c *codexClient) observeGoalState(method string, params map[string]any) {
	switch method {
	case "thread/goal/cleared":
		c.goalActive.Store(false)
		c.goalStatus.Store("")
	case "thread/goal/updated":
		goal, _ := params["goal"].(map[string]any)
		status, _ := goal["status"].(string)
		c.goalStatus.Store(status)
		c.goalActive.Store(status == "active")
		if status == "active" {
			c.goalEngaged.Store(true)
		}
	}
}

func (c *codexClient) goalStopError() string {
	if !c.goalEngaged.Load() {
		return ""
	}
	status, _ := c.goalStatus.Load().(string)
	switch status {
	case "blocked", "budgetLimited", "usageLimited":
		return "codex native goal stopped: " + status
	default:
		return ""
	}
}

func (c *codexClient) applyRuntimeCommand(ctx context.Context, opts ExecOptions) (string, error) {
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		return "", nil
	}
	if command == "/plan" || command == "/plan off" || strings.HasPrefix(command, "/plan ") {
		if command == "/plan off" {
			return "已退出计划模式。", nil
		}
		return "已进入计划模式。", nil
	}
	if command != "/goal" && !strings.HasPrefix(command, "/goal ") {
		return "", fmt.Errorf("unsupported codex runtime command")
	}
	action := strings.TrimSpace(strings.TrimPrefix(command, "/goal"))
	params := map[string]any{"threadId": c.getThreadID()}
	method := "thread/goal/set"
	switch action {
	case "", "show":
		method = "thread/goal/get"
	case "clear":
		method = "thread/goal/clear"
	case "pause":
		params["status"] = "paused"
	case "resume":
		params["status"] = "active"
	case "complete":
		params["status"] = "complete"
	default:
		if len([]rune(action)) > 4000 {
			return "", fmt.Errorf("goal objective must be at most 4000 characters")
		}
		// 相同目标恢复时不替换，保留原生用量与计时起点。
		current, err := c.request(ctx, "thread/goal/get", params)
		if err != nil {
			return "", fmt.Errorf("codex goal query failed: %w", err)
		}
		var state struct {
			Goal *struct {
				Objective string `json:"objective"`
				Status    string `json:"status"`
			} `json:"goal"`
		}
		if json.Unmarshal(current, &state) == nil && state.Goal != nil && state.Goal.Objective == action && state.Goal.Status == "active" {
			c.emitRuntime("goal", map[string]any{"state": json.RawMessage(current)})
			return codexGoalSummary(current), nil
		}
		params["objective"] = action
		params["status"] = "active"
	}
	if method == "thread/goal/set" {
		params["origin"] = "user"
	}
	result, err := c.request(ctx, method, params)
	if err != nil {
		return "", fmt.Errorf("codex goal control failed: %w", err)
	}
	c.emitRuntime("goal", map[string]any{"event": method, "state": json.RawMessage(result)})
	if action == "clear" {
		return "已清除目标。", nil
	}
	return codexGoalSummary(result), nil
}

func codexGoalSummary(raw json.RawMessage) string {
	var result struct {
		Goal *struct {
			Objective string `json:"objective"`
			Status    string `json:"status"`
		} `json:"goal"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Goal == nil {
		return "当前没有目标。"
	}
	status := map[string]string{"active": "进行中", "paused": "已暂停", "blocked": "受阻", "complete": "已完成", "budgetLimited": "达到预算上限", "usageLimited": "达到用量上限"}[result.Goal.Status]
	if status == "" {
		status = result.Goal.Status
	}
	return fmt.Sprintf("目标：%s\n状态：%s", result.Goal.Objective, status)
}

func applyCodexPlanMode(params map[string]any, opts ExecOptions) error {
	mode := opts.PlanMode
	if mode == nil && (opts.Command == "/plan" || strings.HasPrefix(opts.Command, "/plan ")) {
		active := opts.Command != "/plan off"
		mode = &active
	}
	if mode == nil {
		return nil
	}
	// 协作模式必须有准确模型，不能悄悄切到另一默认模型。
	if opts.Model == "" {
		return fmt.Errorf("codex plan mode requires the resolved model")
	}
	name := "default"
	if *mode {
		name = "plan"
	}
	params["collaborationMode"] = map[string]any{"mode": name, "settings": map[string]any{
		"model": opts.Model, "reasoning_effort": nilIfEmpty(opts.ThinkingLevel), "developer_instructions": nil,
	}}
	return nil
}
