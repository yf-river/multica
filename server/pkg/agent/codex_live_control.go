package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	activating := nativeGoalActivatingCommand(command)
	mutationSequence := c.goalMutationSequence.Load()
	c.goalStateMu.Lock()
	revision, wasActive, wasEngaged := c.goalRevision, c.goalActive.Load(), c.goalEngaged.Load()
	if activating {
		c.goalActive.Store(true)
		c.goalEngaged.Store(true)
	}
	c.goalStateMu.Unlock()
	output, err := c.applyRuntimeCommandUnlocked(call, opts)
	if err != nil && c.goalMutationSequence.Load() != mutationSequence {
		if verifyErr := c.reconcileGoalControl(execution, command, wasEngaged); verifyErr != nil {
			c.commandReady.Store(false)
			c.setTurnError("codex goal control outcome could not be verified")
			if c.onTurnDone != nil {
				c.onTurnDone(false)
			}
		}
	} else if activating {
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

// 变更回执缺失时只查询真实状态，不重放用户命令；查询期间的新事件优先于快照。
func (c *codexClient) reconcileGoalControl(execution context.Context, command string, wasEngaged bool) error {
	c.goalStateMu.Lock()
	revision := c.goalRevision
	c.goalStateMu.Unlock()
	verify, cancel := context.WithTimeout(context.WithoutCancel(execution), 2*time.Second)
	defer cancel()
	raw, err := c.request(verify, "thread/goal/get", map[string]any{"threadId": c.getThreadID()})
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return errors.New("invalid codex goal reconciliation")
	}
	goalRaw, present := envelope["goal"]
	var goal *struct {
		Objective string `json:"objective"`
		Status    string `json:"status"`
	}
	if !present || json.Unmarshal(goalRaw, &goal) != nil || (goal != nil && (goal.Objective == "" || goal.Status == "")) {
		return errors.New("codex goal reconciliation did not report its state")
	}
	c.goalStateMu.Lock()
	if c.goalRevision == revision {
		status, engaged := "", wasEngaged
		if goal != nil {
			status = goal.Status
			action := strings.TrimSpace(strings.TrimPrefix(command, "/goal"))
			engaged = engaged || status == "active" || (nativeGoalActivatingCommand(command) && (action == "resume" || action == goal.Objective))
		}
		c.goalStatus.Store(status)
		c.goalActive.Store(status == "active")
		c.goalEngaged.Store(engaged)
	}
	c.goalControlUncertain.Store(false)
	c.goalStateMu.Unlock()
	c.emitRuntime("control", map[string]any{"event": "thread/goal/reconciled", "state": json.RawMessage(raw)})
	return nil
}

// 调用方持有 commandMu；退出前保存暂停状态，避免未知控制在进程恢复后自行续跑。
func (c *codexClient) pauseGoalBeforeStop(threadID string, logger *slog.Logger) {
	if c.goalControlUncertain.Load() {
		if err := c.reconcileGoalControl(context.Background(), "/goal show", c.goalEngaged.Load()); err == nil && !c.goalActive.Load() {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.request(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "status": "paused", "origin": "automatic"})
	if err != nil {
		logger.Warn("codex goal pause before stop failed", "thread_id", threadID, "error", err)
	} else {
		c.goalActive.Store(false)
	}
}
