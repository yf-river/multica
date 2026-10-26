package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type codexGoalConfirmation struct {
	cleared   bool
	objective string
	status    string
}

// 在读取 RPC 回执时登记，只有该回执之后的有序原生事件才能确认此变更。
func (c *codexClient) prepareGoalConfirmation(request *pendingRPC, raw json.RawMessage) error {
	var result struct {
		Cleared *bool `json:"cleared"`
		Goal    *struct {
			Objective string `json:"objective"`
			Status    string `json:"status"`
		} `json:"goal"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return errors.New("invalid codex goal control response")
	}
	want := codexGoalConfirmation{}
	if request.method == "thread/goal/clear" {
		if result.Cleared == nil {
			return errors.New("codex goal clear did not report its outcome")
		}
		if !*result.Cleared {
			request.goalConfirmation <- struct{}{}
			return nil
		}
		want.cleared = true
	} else {
		if result.Goal == nil || result.Goal.Objective == "" || result.Goal.Status == "" {
			return errors.New("codex goal set did not report its state")
		}
		want.objective, want.status = result.Goal.Objective, result.Goal.Status
	}
	c.goalStateMu.Lock()
	defer c.goalStateMu.Unlock()
	if c.goalConfirmations == nil {
		c.goalConfirmations = make(map[chan struct{}]codexGoalConfirmation)
	}
	c.goalConfirmations[request.goalConfirmation] = want
	return nil
}

func (c *codexClient) runLiveCommand(ctx, execution context.Context, command string, opts ExecOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	command = strings.TrimSpace(command)
	if command != "/plan" && command != "/plan off" && command != "/goal" && !strings.HasPrefix(command, "/goal ") {
		return "", errors.New("unsupported live Codex command")
	}
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	if !c.commandReady.Load() || execution.Err() != nil {
		return "", errors.New("codex execution no longer accepts commands")
	}
	// 领取后的控制意图必须等到原生回执；调用方断连不把已发送操作变成可重发操作。
	call, cancel := context.WithTimeout(context.WithoutCancel(execution), 5*time.Second)
	defer cancel()
	opts.Command, opts.CommandOnly, opts.PlanMode = command, true, nil
	activating := codexGoalActivatingCommand(command)
	c.goalStateMu.Lock()
	revision, wasActive, wasEngaged := c.goalRevision, c.goalActive.Load(), c.goalEngaged.Load()
	if activating {
		c.goalActive.Store(true)
		c.goalEngaged.Store(true)
	}
	c.goalStateMu.Unlock()
	output, err := c.applyRuntimeCommandUnlocked(call, opts)
	if activating {
		// 原生预算/失败终态可能先于 RPC 调用方恢复；不能用旧 active 回执覆盖它。
		c.goalStateMu.Lock()
		if c.goalRevision == revision {
			if err != nil {
				c.goalActive.Store(wasActive)
				c.goalEngaged.Store(wasEngaged)
			}
		}
		c.goalStateMu.Unlock()
	}
	if !c.goalActive.Load() && c.activeTurnID() == "" && c.onTurnDone != nil {
		c.onTurnDone(false)
	}
	return output, err
}
