package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const codebuddyGoalControlMethod = "_codebuddy.ai/goalControl"

type codebuddyGoalSnapshot struct {
	OK          bool   `json:"ok"`
	Applied     string `json:"applied"`
	HasGoal     bool   `json:"hasGoal"`
	RunInFlight bool   `json:"runInFlight"`
	ActiveGoal  *struct {
		Condition string `json:"condition"`
		CreatedAt int64  `json:"createdAt"`
		Paused    bool   `json:"paused"`
	} `json:"activeGoal"`
	Recap *struct {
		Latest *struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"latest"`
	} `json:"recap"`
}

func codebuddyGoalAction(command string) (action, condition string, err error) {
	command = strings.TrimSpace(command)
	if command != "/goal" && !strings.HasPrefix(command, "/goal ") {
		return "", "", errors.New("unsupported CodeBuddy goal command")
	}
	value := strings.TrimSpace(strings.TrimPrefix(command, "/goal"))
	switch value {
	case "", "show":
		return "snapshot", "", nil
	case "pause", "resume", "clear":
		return value, "", nil
	case "complete":
		return "clear", "", nil
	default:
		if len([]rune(value)) > 4000 {
			return "", "", errors.New("goal objective must be at most 4000 characters")
		}
		return "set", value, nil
	}
}

func requestCodebuddyGoal(ctx context.Context, c *hermesClient, sessionID, action, condition string) (codebuddyGoalSnapshot, error) {
	params := map[string]any{"sessionId": sessionID, "action": action}
	if condition != "" {
		params["condition"] = condition
	}
	raw, err := c.request(ctx, codebuddyGoalControlMethod, params)
	if err != nil {
		return codebuddyGoalSnapshot{}, err
	}
	var result codebuddyGoalSnapshot
	if json.Unmarshal(raw, &result) != nil || !result.OK || result.Applied == "not-ready" || (result.HasGoal && result.ActiveGoal == nil) {
		return result, errors.New("CodeBuddy did not apply native goal control")
	}

	return result, nil
}

func codebuddyGoalSummary(state codebuddyGoalSnapshot) string {
	if state.ActiveGoal == nil {
		return "当前没有目标。"
	}
	status := "进行中"
	if state.ActiveGoal.Paused {
		status = "已暂停"
	}
	return fmt.Sprintf("目标：%s\n状态：%s", state.ActiveGoal.Condition, status)
}

// Goal 由 CLI 原生循环驱动；控制端只等状态，不再发送平台自动续跑提示词。
func waitCodebuddyGoal(ctx context.Context, c *hermesClient, sessionID string) (codebuddyGoalSnapshot, error) {
	previous := ""
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		poll, cancel := context.WithTimeout(ctx, 5*time.Second)
		state, err := requestCodebuddyGoal(poll, c, sessionID, "snapshot", "")
		cancel()
		if err != nil {
			return state, err
		}
		encoded, _ := json.Marshal(state)
		if string(encoded) != previous {
			emitCodebuddyGoal(c, sessionID, "snapshot", state)
			previous = string(encoded)
		}
		if !state.RunInFlight && (!state.HasGoal || (state.ActiveGoal != nil && state.ActiveGoal.Paused)) {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-ticker.C:
		}
	}
}

func emitCodebuddyGoal(c *hermesClient, sessionID, action string, state codebuddyGoalSnapshot) {
	if c.onMessage != nil {
		c.onMessage(runtimeTelemetry("codebuddy", "goal", map[string]any{"session_id": sessionID, "action": action, "state": state}))
	}
}
