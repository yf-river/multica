package openviking

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SyncState struct {
	Uploaded      bool      `json:"uploaded"`
	CommitStarted time.Time `json:"commit_started,omitempty"`
	Sources       []string  `json:"sources,omitempty"`
	MemoryTasks   []string  `json:"memory_tasks,omitempty"`
	MemoryTask    string    `json:"memory_task,omitempty"`
	Done          bool      `json:"done"`
	Error         string    `json:"error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Message struct {
	Role      string           `json:"role"`
	Parts     []map[string]any `json:"parts"`
	TurnID    string           `json:"turn_id"`
	Sources   []string         `json:"source_message_ids"`
	CreatedAt string           `json:"created_at,omitempty"`
}

type Stats struct {
	Pending         int `json:"pending"`
	Failed          int `json:"failed"`
	Complete        int `json:"complete"`
	PlatformPending int `json:"platform_pending"`
}

func Status(root string) Stats {
	var stats Stats
	dirs, _ := os.ReadDir(root)
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		var state SyncState
		raw, _ := os.ReadFile(filepath.Join(root, dir.Name(), "sync.json"))
		_ = json.Unmarshal(raw, &state)
		if state.Done {
			stats.Complete++
		} else {
			stats.Pending++
			var meta Metadata
			metaRaw, _ := os.ReadFile(filepath.Join(root, dir.Name(), "metadata.json"))
			if json.Unmarshal(metaRaw, &meta) == nil && meta.PlatformEventID != "" {
				stats.PlatformPending++
			}
			if state.Error != "" {
				stats.Failed++
			}
		}
	}
	return stats
}

func (c *Client) Replay(ctx context.Context, root string) error {
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var first error
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !dir.IsDir() {
			continue
		}
		p := filepath.Join(root, dir.Name())
		if _, err = os.Stat(filepath.Join(p, "ready.json")); os.IsNotExist(err) {
			continue
		}
		var state SyncState
		raw, readErr := os.ReadFile(filepath.Join(p, "sync.json"))
		if readErr == nil {
			if err = json.Unmarshal(raw, &state); err != nil {
				return err
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		if state.Done {
			continue
		}
		// 同步中的会话串行处理；单条失败不会阻塞其它 Agent 的归档。
		err = c.replayOne(ctx, p, &state)
		state.UpdatedAt = time.Now().UTC()
		state.Error = ""
		if err != nil {
			state.Error = err.Error()
			if first == nil {
				first = err
			}
		}
		if writeErr := writeJSONFile(p, "sync.json", state); writeErr != nil {
			return writeErr
		}
	}
	return first
}

func (c *Client) replayOne(ctx context.Context, dir string, state *SyncState) error {
	raw, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return err
	}
	var meta Metadata
	if err = json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if meta.NeedsAttachmentDiscovery {
		return fmt.Errorf("attachment discovery awaits recovery")
	}
	if meta.Version != 1 {
		return fmt.Errorf("unsupported archive version %d", meta.Version)
	}
	if _, err = User(meta.AgentID, meta.User); err != nil {
		return err
	}
	sessionURI := "viking://user/" + meta.User + "/sessions/" + meta.Session
	resource := meta.Provider == "multica-resource"
	parentURI := sessionURI + "/multica"
	if resource {
		parentURI = "viking://resources/multica"
	}
	endpoint := "/api/v1/sessions/" + url.PathEscape(meta.Session)
	// 禁用自动提交，提交与重放由同一条平台归档链路负责。
	if !resource {
		err = c.JSON(ctx, meta.User, "POST", "/api/v1/sessions", map[string]any{"session_id": meta.Session, "auto_commit_policy": nil}, nil)
	}
	if e, ok := err.(*HTTPError); ok && e.Code == 409 {
		err = nil
	}
	if err != nil {
		return err
	}
	if !state.Uploaded {
		files := map[string]string{"metadata.json": filepath.Join(dir, "metadata.json"), "events.jsonl": filepath.Join(dir, "events.jsonl")}
		for _, a := range meta.Attachments {
			if a.Path == "" {
				return fmt.Errorf("attachment %s awaits download", a.ID)
			}
			files[a.Path] = filepath.Join(dir, a.Path)
		}
		rootURI := parentURI + "/" + meta.Attempt
		if err = c.StoreOriginals(ctx, meta.User, rootURI, files); err != nil {
			return err
		}

		state.Uploaded = true
		if err = writeJSONFile(dir, "sync.json", state); err != nil {
			return err
		}
	}
	if resource {
		state.Done = true
		return nil
	}
	if len(state.MemoryTasks) > 0 {
		return c.finishExtraction(ctx, meta, sessionURI, state)
	}
	events, err := readEvents(dir)
	if err != nil {
		return err
	}
	messages := normalize(meta, events, sessionURI+"/multica/"+meta.Attempt)
	if err = inlineMessageImages(messages, meta, dir, sessionURI+"/multica/"+meta.Attempt); err != nil {
		return err
	}
	if len(messages) == 0 {
		state.Done = true
		return nil
	}
	seen, receipts, err := c.sourceReceipts(ctx, meta.User, sessionURI)
	if err != nil {
		return err
	}
	// 先持久化本次归档负责的来源，再发网络请求；响应丢失后仍可定位原生回执。
	if state.Sources == nil {
		state.Sources = []string{}
		for _, m := range messages {
			for _, id := range m.Sources {
				state.Sources = append(state.Sources, id)
			}
		}
		if err = writeJSONFile(dir, "sync.json", state); err != nil {
			return err
		}
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
		if err = c.JSON(ctx, meta.User, "POST", endpoint+"/messages/batch", map[string]any{"messages": pending[:n]}, nil); err != nil {
			return err
		}
		pending = pending[n:]
	}
	seen, receipts, err = c.sourceReceipts(ctx, meta.User, sessionURI)
	if err != nil {
		return err
	}
	// 同一消息可同时有平台 ID 和内容修订 ID；原生服务按任一来源去重。
	// 运行时先写入平台 ID 时，不能要求它同时补写后到的别名。
	needsCommit := false
	tasks := map[string]bool{}
	for _, message := range messages {
		matched := false
		for _, id := range message.Sources {
			if !seen[id] {
				continue
			}
			matched = true
			if task, archived := receipts[id]; archived {
				if task == "" {
					return fmt.Errorf("native archive extraction receipt missing for %s", id)
				}
				tasks[task] = true
			} else {
				needsCommit = true
			}
		}
		if !matched {
			return fmt.Errorf("native message source missing: %v", message.Sources)
		}
	}
	if needsCommit {
		if c.CanCommit != nil {
			allowed, err := c.CanCommit(ctx, meta)
			if err != nil {
				return err
			}
			if !allowed {
				return nil
			}
		}
		state.CommitStarted = time.Now().UTC()
		if err = writeJSONFile(dir, "sync.json", state); err != nil {
			return err
		}
		var commit struct {
			TaskID string `json:"task_id"`
		}
		if err = c.JSON(ctx, meta.User, "POST", endpoint+"/commit", map[string]any{}, &commit); err != nil {
			return err
		}
		if commit.TaskID == "" {
			return fmt.Errorf("commit did not return an extraction task")
		}
		tasks[commit.TaskID] = true
		state.MemoryTask = commit.TaskID
	}
	state.MemoryTasks = nil
	for taskID := range tasks {
		state.MemoryTasks = append(state.MemoryTasks, taskID)
	}
	if err = writeJSONFile(dir, "sync.json", state); err != nil {
		return err
	}
	return c.finishExtraction(ctx, meta, sessionURI, state)
}

func (c *Client) finishExtraction(ctx context.Context, meta Metadata, sessionURI string, state *SyncState) error {
	for _, taskID := range state.MemoryTasks {
		var task struct {
			Status string `json:"status"`
			Error  any    `json:"error"`
		}
		if err := c.JSON(ctx, meta.User, "GET", "/api/v1/tasks/"+url.PathEscape(taskID), nil, &task); err != nil {
			return err
		}
		if task.Status == "failed" || task.Status == "cancelled" {
			recovered, err := c.recoverExtraction(ctx, meta.User, sessionURI, taskID)
			if err != nil {
				return err
			}
			if !recovered {
				return nil
			}
			continue
		}
		if task.Status != "completed" {
			return nil
		}
	}
	state.Done = true
	return nil
}

func (c *Client) sessionSources(ctx context.Context, user, uri string) (map[string]bool, bool, error) {
	seen, receipts, err := c.sourceReceipts(ctx, user, uri)
	live := false
	for id := range seen {
		if _, ok := receipts[id]; !ok {
			live = true
		}
	}
	return seen, live, err
}

func (c *Client) sourceReceipts(ctx context.Context, user, sessionURI string) (map[string]bool, map[string]string, error) {
	seen := map[string]bool{}
	receipts := map[string]string{}
	paths := []string{sessionURI + "/messages.jsonl"}
	archives, err := c.List(ctx, user, sessionURI+"/history")
	if err != nil {
		return nil, nil, err
	}
	for _, a := range archives {
		if a.IsDir {
			paths = append(paths, strings.TrimRight(a.URI, "/")+"/messages.jsonl")
		}
	}
	for i, p := range paths {
		data, err := c.Download(ctx, user, p)
		if e, ok := err.(*HTTPError); ok && e.Code == 404 {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		taskID := ""
		if i > 0 {
			metaRaw, err := c.Download(ctx, user, strings.TrimSuffix(p, "/messages.jsonl")+"/.meta.json")
			if err != nil {
				return nil, nil, err
			}
			var meta struct {
				Phase1 struct {
					Queue struct {
						TaskID string `json:"task_id"`
					} `json:"queue_message"`
				} `json:"phase1"`
			}
			if err = json.Unmarshal(metaRaw, &meta); err != nil {
				return nil, nil, err
			}
			taskID = meta.Phase1.Queue.TaskID
		}
		reader := bufio.NewReader(bytes.NewReader(data))
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var message struct {
					Sources []string `json:"source_message_ids"`
				}
				if err = json.Unmarshal(line, &message); err != nil {
					return nil, nil, err
				}
				for _, id := range message.Sources {
					seen[id] = true
					if i > 0 {
						receipts[id] = taskID
					}
				}
			}
			if readErr != nil {
				break
			}
		}
	}
	return seen, receipts, nil
}
