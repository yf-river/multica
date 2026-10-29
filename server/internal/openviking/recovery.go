package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// recoverExtraction 使用公开会话接口重试完整失败归档，原会话和原件保持不变。
// 原生服务没有终态任务重试接口，独立恢复会话使重试来源和结果可以追溯。
func (c *Client) recoverExtraction(ctx context.Context, user, sessionURI, failedTask string) (bool, error) {
	archives, err := c.List(ctx, user, sessionURI+"/history")
	if err != nil {
		return false, err
	}
	var original []byte
	for _, archive := range archives {
		if !archive.IsDir {
			continue
		}
		root := strings.TrimRight(archive.URI, "/")
		raw, err := c.Download(ctx, user, root+"/.meta.json")
		if err != nil {
			return false, err
		}
		var meta struct {
			Phase1 struct {
				Queue struct {
					TaskID string `json:"task_id"`
				} `json:"queue_message"`
			} `json:"phase1"`
		}
		if err = json.Unmarshal(raw, &meta); err != nil {
			return false, err
		}
		if meta.Phase1.Queue.TaskID == failedTask {
			original, err = c.Download(ctx, user, root+"/messages.jsonl")
			if err != nil {
				return false, err
			}
			break
		}
	}
	if len(original) == 0 {
		return false, fmt.Errorf("failed extraction archive missing: %s", failedTask)
	}
	messages := []Message{}
	for _, line := range bytes.Split(original, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m Message
		if err = json.Unmarshal(line, &m); err != nil {
			return false, err
		}
		if len(m.Sources) == 0 {
			return false, fmt.Errorf("failed archive message has no platform source")
		}
		messages = append(messages, m)
	}
	for generation := 1; generation <= 3; generation++ {
		session := fmt.Sprintf("multica-recovery-%s-%d", SHA([]byte(sessionURI + "\x00" + failedTask))[:32], generation)
		endpoint := "/api/v1/sessions/" + url.PathEscape(session)
		err = c.JSON(ctx, user, "POST", "/api/v1/sessions", map[string]any{"session_id": session, "auto_commit_policy": nil}, nil)
		if e, ok := err.(*HTTPError); ok && e.Code == 409 {
			err = nil
		}
		if err != nil {
			return false, err
		}
		uri := "viking://user/" + user + "/sessions/" + session
		seen, receipts, err := c.sourceReceipts(ctx, user, uri)
		if err != nil {
			return false, err
		}
		pending := []Message{}
		for _, m := range messages {
			saved := false
			for _, id := range m.Sources {
				if seen[id] {
					saved = true
					break
				}
			}
			if !saved {
				pending = append(pending, m)
			}
		}
		for len(pending) > 0 {
			n := min(100, len(pending))
			if err = c.JSON(ctx, user, "POST", endpoint+"/messages/batch", map[string]any{"messages": pending[:n]}, nil); err != nil {
				return false, err
			}
			pending = pending[n:]
		}
		tasks := map[string]bool{}
		live := false
		for _, m := range messages {
			for _, id := range m.Sources {
				if task, ok := receipts[id]; ok {
					if task == "" {
						return false, fmt.Errorf("recovery archive receipt missing")
					}
					tasks[task] = true
				} else {
					live = true
				}
			}
		}
		if live {
			var commit struct {
				TaskID string `json:"task_id"`
			}
			if err = c.JSON(ctx, user, "POST", endpoint+"/commit", map[string]any{}, &commit); err != nil {
				return false, err
			}
			if commit.TaskID == "" {
				return false, fmt.Errorf("recovery commit has no extraction task")
			}
			tasks[commit.TaskID] = true
		}
		failed := false
		for taskID := range tasks {
			var task struct {
				Status string `json:"status"`
			}
			if err = c.JSON(ctx, user, "GET", "/api/v1/tasks/"+url.PathEscape(taskID), nil, &task); err != nil {
				return false, err
			}
			if task.Status == "failed" || task.Status == "cancelled" {
				failed = true
			} else if task.Status != "completed" {
				return false, nil
			}
		}
		if !failed {
			return true, nil
		}
	}
	return false, fmt.Errorf("memory extraction failed after three recovery sessions; original task %s", failedTask)
}
