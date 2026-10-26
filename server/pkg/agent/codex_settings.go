package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type codexPlanSelection struct {
	mode   string
	model  string
	effort string
}

func (c *codexClient) rememberNativeThreadModel(raw json.RawMessage) {
	var result struct {
		Model  string `json:"model"`
		Thread struct {
			Model string `json:"model"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return
	}
	if result.Model == "" {
		result.Model = result.Thread.Model
	}
	c.threadIDMu.Lock()
	c.threadModel = result.Model
	c.threadIDMu.Unlock()
}

func (c *codexClient) nativeThreadModel() string {
	c.threadIDMu.RLock()
	defer c.threadIDMu.RUnlock()
	return c.threadModel
}

// 原生 Goal 恢复可能在回合门开启前启动。turn/start 对活跃回合执行 steer，
// 其回执是当前回合身份的权威来源；在读协程内补齐身份，不接收历史内容。
func (c *codexClient) adoptTurnStartResponse(raw json.RawMessage) {
	if c.notificationProtocol == "legacy" {
		return
	}
	var result struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Turn.ID == "" || result.Turn.ID == c.usageTurnID() {
		return
	}
	if result.Turn.Status != "" && result.Turn.Status != "inProgress" {
		return
	}
	params := map[string]any{"threadId": c.getThreadID(), "turn": map[string]any{"id": result.Turn.ID}}
	if c.acceptNotification != nil && !c.acceptNotification("turn/started", params) {
		return
	}
	c.notificationProtocol = "raw"
	c.handleRawNotification("turn/started", params)
}

// settings/update 只确认排队；模式回执来自同线程 settings/updated 的实际状态。
func (c *codexClient) setPlanMode(ctx context.Context, opts ExecOptions) error {
	params := map[string]any{"threadId": c.getThreadID()}
	if err := applyCodexPlanMode(params, opts); err != nil {
		return err
	}
	mode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		return fmt.Errorf("codex plan command has no selected mode")
	}
	want := codexPlanSelection{mode: mode["mode"].(string), model: opts.Model, effort: opts.ThinkingLevel}
	changed := make(chan struct{}, 1)
	c.settingsMu.Lock()
	if c.settingsWaiters == nil {
		c.settingsWaiters = make(map[chan struct{}]codexPlanSelection)
	}
	c.settingsWaiters[changed] = want
	c.settingsMu.Unlock()
	defer func() { c.settingsMu.Lock(); delete(c.settingsWaiters, changed); c.settingsMu.Unlock() }()
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.request(call, "thread/settings/update", params); err != nil {
		return fmt.Errorf("codex plan mode update failed: %w", err)
	}
	select {
	case <-changed:
		return nil
	case <-c.processDone:
		return fmt.Errorf("codex exited before confirming plan mode")
	case <-call.Done():
		return fmt.Errorf("codex plan mode confirmation failed: %w", call.Err())
	}
}

func (c *codexClient) observeThreadSettings(method string, params map[string]any) {
	if method != "thread/settings/updated" {
		return
	}
	settings, _ := params["threadSettings"].(map[string]any)
	mode, _ := settings["collaborationMode"].(map[string]any)
	selection, _ := mode["settings"].(map[string]any)
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	for changed, want := range c.settingsWaiters {
		if mode["mode"] == want.mode && selection["model"] == want.model && (want.effort == "" || selection["reasoning_effort"] == want.effort) {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	}
}
