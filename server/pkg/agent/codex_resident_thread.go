package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (c *codexClient) currentThreadConfig() map[string]any {
	if c.threadConfig == nil {
		return codexSubagentConfig(c.cfg.CodexVersion)
	}
	// 整表和点号键不能同时覆盖同一个字段，否则原生 HashMap 迭代顺序会改变优先级。
	for key, value := range codexSubagentConfig(c.cfg.CodexVersion) {
		delete(c.threadConfig, key)
		parts := strings.Split(key, ".")
		parent := c.threadConfig
		for _, part := range parts[:len(parts)-1] {
			next, ok := parent[part].(map[string]any)
			if !ok {
				next = make(map[string]any)
				parent[part] = next
			}
			parent = next
		}
		parent[parts[len(parts)-1]] = value
	}
	return c.threadConfig
}

// 调用者已关闭控制入口并持有 commandMu；只有原生卸载和空 loaded 集合可归还租约。
func (c *codexClient) unloadResidentThread() error {
	threadID := c.getThreadID()
	if threadID == "" {
		return errors.New("Codex resident thread identity is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if c.goalActive.Load() || c.goalControlUncertain.Load() {
		c.pauseGoalBeforeStop(threadID, c.cfg.Logger)
		if c.goalActive.Load() || c.goalControlUncertain.Load() {
			return errors.New("Codex resident goal did not pause before detach")
		}
	}
	if c.activeTurnID() != "" {
		return errors.New("Codex resident turn has not ended")
	}
	if _, err := c.request(ctx, "thread/backgroundTerminals/clean", map[string]any{"threadId": threadID}); err != nil {
		return errors.New("Codex resident background terminals did not close")
	}
	raw, err := c.request(ctx, "thread/unsubscribe", map[string]any{"threadId": threadID})
	if err != nil {
		return errors.New("Codex resident thread detach was not acknowledged")
	}
	var detached struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &detached) != nil {
		return errors.New("Codex resident thread detach response is invalid")
	}
	switch detached.Status {
	case "notLoaded":
	case "unsubscribed", "notSubscribed":
		select {
		case <-c.threadClosed:
		case <-ctx.Done():
			return errors.New("Codex resident thread unload timed out")
		}
	default:
		return errors.New("Codex resident thread detach state is unknown")
	}
	raw, err = c.request(ctx, "thread/loaded/list", map[string]any{})
	if err != nil {
		return errors.New("Codex resident loaded threads could not be verified")
	}
	var loaded struct {
		Data       *[]string `json:"data"`
		NextCursor *string   `json:"nextCursor"`
	}
	if json.Unmarshal(raw, &loaded) != nil || loaded.Data == nil || len(*loaded.Data) != 0 || loaded.NextCursor != nil {
		return errors.New("Codex resident process still has loaded threads")
	}
	return nil
}
