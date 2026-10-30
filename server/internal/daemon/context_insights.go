package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type insightsSpool struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func (d *Daemon) insightsRoot() string {
	sum := sha256.Sum256([]byte(d.cfg.ServerBaseURL + "\x00" + d.cfg.DaemonID))
	return filepath.Join(d.cfg.WorkspacesRoot, ".insights", hex.EncodeToString(sum[:12]))
}
func (d *Daemon) newInsightsSpool(taskID string) (*insightsSpool, error) {
	root := d.insightsRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, taskID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	directory, dirErr := os.Open(root)
	if dirErr != nil {
		file.Close()
		return nil, dirErr
	}
	dirErr = directory.Sync()
	directory.Close()
	if dirErr != nil {
		file.Close()
		return nil, dirErr
	}
	return &insightsSpool{path: path, file: file}, nil
}
func (s *insightsSpool) append(raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.file.Write(append(append([]byte{}, raw...), '\n')); err != nil {
		return err
	}
	return s.file.Sync()
}
func (d *Daemon) contextInsightsLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deleted := time.NewTicker(time.Minute)
	defer deleted.Stop()
	for {
		d.syncInsights(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-deleted.C:
			d.pruneDeletedInsights(ctx)
		}
	}
}
func (d *Daemon) syncInsights(ctx context.Context) {
	paths, _ := filepath.Glob(filepath.Join(d.insightsRoot(), "*.jsonl"))
	for _, path := range paths {
		if err := d.syncInsightsFile(ctx, path); err != nil && ctx.Err() == nil {
			d.logger.Warn("Context Insights pending", "error", err)
		}
	}
}
func (d *Daemon) syncInsightsFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	ackPath := path + ".ack"
	offset := int64(0)
	if raw, e := os.ReadFile(ackPath); e == nil {
		offset, _ = strconv.ParseInt(string(raw), 10, 64)
	}
	if _, err = f.Seek(offset, 0); err != nil {
		return err
	}
	reader := bufio.NewReader(f)
	batch := []json.RawMessage{}
	next := offset
	size := 0
	for len(batch) < 32 && size < 1024*1024 {
		line, e := reader.ReadBytes('\n')
		if e != nil {
			if e != io.EOF {
				return e
			}
			break
		}
		if !json.Valid(line) {
			return fmt.Errorf("invalid persisted insight frame at %d", next)
		}
		batch = append(batch, json.RawMessage(line))
		next += int64(len(line))
		size += len(line)
	}
	if len(batch) == 0 {
		return nil
	}
	taskID := filepath.Base(path)
	taskID = taskID[:len(taskID)-len(".jsonl")]
	var bodies []any
	var small []json.RawMessage
	flush := func() {
		if len(small) > 0 {
			bodies = append(bodies, map[string]any{"frames": small})
			small = nil
		}
	}
	for _, frame := range batch {
		const chunkSize = 256 * 1024
		if len(frame) <= chunkSize {
			small = append(small, frame)
			continue
		}
		flush()
		sum := sha256.Sum256(frame)
		id := hex.EncodeToString(sum[:])
		total := (len(frame) + chunkSize - 1) / chunkSize
		for part := 0; part < total; part++ {
			end := min(len(frame), (part+1)*chunkSize)
			bodies = append(bodies, map[string]any{"chunk": map[string]any{"id": id, "part": part, "total": total, "data": base64.StdEncoding.EncodeToString(frame[part*chunkSize : end])}})
		}
	}
	flush()
	for _, body := range bodies {
		var ack struct {
			OK bool `json:"ok"`
		}
		if err = d.client.postJSON(ctx, "/api/daemon/tasks/"+taskID+"/insights", body, &ack); err != nil {
			return err
		}
		if !ack.OK {
			return fmt.Errorf("Context Insights upload not acknowledged")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ack-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(strconv.FormatInt(next, 10)); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, ackPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// 附件原件在运行时文件仍可读时进入同一可靠队列，独立于 OpenViking。
func (s *insightsSpool) capture(raw json.RawMessage, root string, seen map[string]bool) error {
	if err := s.append(raw); err != nil {
		return err
	}
	var frame map[string]any
	if json.Unmarshal(raw, &frame) != nil || (frame["type"] != "archive" && frame["type"] != "insight_context") {
		return nil
	}
	session, _ := frame["session_id"].(string)
	var visit func(any) error
	visit = func(value any) error {
		switch node := value.(type) {
		case map[string]any:
			if id, ok := node["attachmentId"].(string); ok && strings.HasPrefix(id, "sha256:") && !seen[session+id] {
				digest := strings.TrimPrefix(id, "sha256:")
				if !nativeAttachmentDigest.MatchString(digest) {
					return fmt.Errorf("invalid insight attachment identity")
				}
				content, err := os.ReadFile(filepath.Join(root, digest[:2], digest))
				if err != nil {
					return err
				}
				sum := sha256.Sum256(content)
				if hex.EncodeToString(sum[:]) != digest {
					return fmt.Errorf("insight attachment checksum mismatch")
				}
				media, _ := node["mediaType"].(string)
				name, _ := node["name"].(string)
				attachment, _ := json.Marshal(map[string]any{"type": "insight_attachment", "session_id": session, "attachment_id": id, "media_type": media, "filename": name, "content": content})
				if err = s.append(attachment); err != nil {
					return err
				}
				seen[session+id] = true
			}
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(frame["event"])
}

func (d *Daemon) insightsActivation(ctx context.Context) (int64, error) {
	var configuration struct {
		EnabledSince *int64 `json:"enabled_since"`
	}
	path := filepath.Join(d.insightsRoot(), "capture-config.json")
	err := d.client.getJSON(ctx, "/api/daemon/insights/config", &configuration)
	if err == nil && configuration.EnabledSince != nil && *configuration.EnabledSince >= 0 {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return 0, err
		}
		file, e := os.CreateTemp(filepath.Dir(path), ".capture-")
		if e != nil {
			return 0, e
		}
		defer os.Remove(file.Name())
		raw, _ := json.Marshal(configuration)
		if _, e = file.Write(raw); e == nil {
			e = file.Sync()
		}
		closeErr := file.Close()
		if e != nil {
			return 0, e
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if e = os.Rename(file.Name(), path); e != nil {
			return 0, e
		}
		directory, e := os.Open(filepath.Dir(path))
		if e != nil {
			return 0, e
		}
		e = directory.Sync()
		directory.Close()
		if e != nil {
			return 0, e
		}
		return *configuration.EnabledSince, nil
	}
	if func() bool {
		var request *requestError
		return errors.As(err, &request) && request.StatusCode == http.StatusNotFound
	}() {
		return 0, nil
	}
	if raw, e := os.ReadFile(path); e == nil && json.Unmarshal(raw, &configuration) == nil && configuration.EnabledSince != nil && *configuration.EnabledSince >= 0 {
		return *configuration.EnabledSince, nil
	}
	if err == nil {
		err = fmt.Errorf("invalid Context Insights activation")
	}
	return 0, err
}

// 上传与删除核对在同一循环串行执行；已正式删除的任务不保留本地待上报原件。
func (d *Daemon) pruneDeletedInsights(ctx context.Context) {
	if d.activeTasks.Load() != 0 {
		return
	}
	paths, _ := filepath.Glob(filepath.Join(d.insightsRoot(), "*.jsonl"))
	for _, path := range paths {
		taskID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		_, err := d.client.GetTaskGCCheck(ctx, taskID)
		if !isAccessNotFound(err) {
			continue
		}
		d.claimMu.Lock()
		if d.activeTasks.Load() == 0 && d.claimsInFlight == 0 {
			err = os.Remove(path)
			if err == nil || os.IsNotExist(err) {
				err = os.Remove(path + ".ack")
			}
			if err != nil && !os.IsNotExist(err) {
				d.logger.Warn("洞察已删除任务队列清理失败", "task", taskID, "error", err)
			}
		}
		d.claimMu.Unlock()
	}
	if dir, err := os.Open(d.insightsRoot()); err == nil {
		defer dir.Close()
		if err = dir.Sync(); err != nil {
			d.logger.Warn("洞察队列删除确认失败", "error", err)
		}
	}
}
