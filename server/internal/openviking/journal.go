package openviking

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Metadata struct {
	Version                  int             `json:"version"`
	Attempt                  string          `json:"attempt"`
	TaskID                   string          `json:"task_id"`
	AgentID                  string          `json:"agent_id"`
	User                     string          `json:"user"`
	Session                  string          `json:"session"`
	WorkspaceID              string          `json:"workspace_id"`
	ChatID                   string          `json:"chat_id,omitempty"`
	IssueID                  string          `json:"issue_id,omitempty"`
	Provider                 string          `json:"provider"`
	NativeTempRoot           string          `json:"native_temp_root,omitempty"`
	NativeAttachmentRoot     string          `json:"native_attachment_root,omitempty"`
	PlatformEventID          string          `json:"platform_event_id,omitempty"`
	SourcePayload            json.RawMessage `json:"source_payload,omitempty"`
	InputSources             []string        `json:"input_sources,omitempty"`
	InputAttachmentIDs       []string        `json:"input_attachment_ids"`
	Input                    string          `json:"input"`
	CreatedAt                time.Time       `json:"created_at"`
	Attachments              []Attachment    `json:"attachments,omitempty"`
	NeedsAttachmentDiscovery bool            `json:"needs_attachment_discovery,omitempty"`
	Incomplete               bool            `json:"incomplete,omitempty"`
}

type Attachment struct {
	ID          string `json:"id"`
	SourcePath  string `json:"source_path,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
}

type Event struct {
	Seq    int64           `json:"seq"`
	At     time.Time       `json:"at"`
	Source string          `json:"source"`
	Data   json.RawMessage `json:"data"`
}

type Journal struct {
	Dir  string
	Meta Metadata
	mu   sync.Mutex
	file *os.File
	seq  int64
	err  error
}

func writeJSONFile(dir, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func Begin(root string, meta Metadata) (*Journal, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	meta.Version = 1
	meta.Attempt = hex.EncodeToString(id[:])
	meta.CreatedAt = time.Now().UTC()
	dir, err := os.MkdirTemp(root, meta.CreatedAt.Format("20060102T150405.000000000")+"-")
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	if err = writeJSONFile(dir, "metadata.json", meta); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Sync(); err == nil {
		err = syncDir(dir)
	}
	if err == nil {
		err = syncDir(root)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Journal{Dir: dir, Meta: meta, file: f}, nil
}

// Append 在展示、脱敏、通道投递之前落盘；失败由调用方终止执行，不能声称完整归档。
func (j *Journal) Append(source string, raw json.RawMessage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return j.err
	}
	if j.file == nil {
		return fmt.Errorf("archive journal is closed")
	}
	if !json.Valid(raw) {
		return fmt.Errorf("invalid archive event JSON")
	}
	j.seq++
	data, err := json.Marshal(Event{j.seq, time.Now().UTC(), source, raw})
	if err == nil {
		_, err = j.file.Write(append(data, '\n'))
	}
	if err == nil {
		err = j.file.Sync()
	}
	j.err = err
	return err
}

func (j *Journal) Finish(result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	appendErr := j.Append("result", raw)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file != nil {
		if err = j.file.Close(); err != nil && appendErr == nil {
			appendErr = err
		}
		j.file = nil
	}
	if appendErr != nil {
		return appendErr
	}
	return writeJSONFile(j.Dir, "ready.json", map[string]any{"at": time.Now().UTC()})
}

func (j *Journal) SaveAttachment(id, filename, contentType string, reader io.Reader, expectedSHA ...string) error {
	name := "attachment-" + SHA([]byte(id))
	f, err := os.CreateTemp(j.Dir, ".attachment-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	// 文件原件直接写入 outbox，避免任务目录清理或下载地址过期影响重放。
	h := newHashWriter(f)
	n, err := io.Copy(h, reader)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if len(expectedSHA) > 0 && h.sum() != expectedSHA[0] {
		return fmt.Errorf("attachment %s checksum mismatch", id)
	}
	if err = os.Rename(tmp, filepath.Join(j.Dir, name)); err != nil {
		return err
	}
	attachment := Attachment{ID: id, Filename: filename, ContentType: contentType, Path: name, SHA256: h.sum(), Bytes: n}
	found := false
	for i, a := range j.Meta.Attachments {
		if a.ID == id {
			attachment.SourcePath = a.SourcePath
			j.Meta.Attachments[i] = attachment
			found = true
			break
		}
	}
	if !found {
		j.Meta.Attachments = append(j.Meta.Attachments, attachment)
	}
	return writeJSONFile(j.Dir, "metadata.json", j.Meta)
}

func (j *Journal) WantAttachment(id, filename, contentType string) error {
	for _, a := range j.Meta.Attachments {
		if a.ID == id {
			return nil
		}
	}
	j.Meta.Attachments = append(j.Meta.Attachments, Attachment{ID: id, Filename: filename, ContentType: contentType})
	return writeJSONFile(j.Dir, "metadata.json", j.Meta)
}

func PendingJournals(root string) ([]*Journal, error) {
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journals []*Journal
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		var state SyncState
		stateRaw, stateErr := os.ReadFile(filepath.Join(dir, "sync.json"))
		if stateErr == nil {
			if err = json.Unmarshal(stateRaw, &state); err != nil {
				return nil, err
			}
			if state.Done {
				continue
			}
		} else if !os.IsNotExist(stateErr) {
			return nil, stateErr
		}
		if _, err = os.Stat(filepath.Join(dir, "ready.json")); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
		if err != nil {
			return nil, err
		}
		var meta Metadata
		if err = json.Unmarshal(raw, &meta); err != nil {
			return nil, err
		}
		journals = append(journals, &Journal{Dir: dir, Meta: meta})
	}
	return journals, nil
}

func readEvents(dir string) ([]Event, error) {
	var meta Metadata
	metaRaw, metaErr := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if metaErr != nil {
		return nil, metaErr
	}
	if metaErr = json.Unmarshal(metaRaw, &meta); metaErr != nil {
		return nil, metaErr
	}
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	events := []Event{}
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				if readErr == io.EOF && meta.Incomplete {
					return events, nil
				}
				return nil, fmt.Errorf("invalid journal: %w", err)
			}
			events = append(events, event)
		}
		if readErr == io.EOF {
			return events, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

// Recover 只在取得 daemon 单实例健康端口后调用；故障遗留的记录原样保留并标明中断。
func Recover(root string) error {
	dirs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		p := filepath.Join(root, dir.Name())
		if _, err = os.Stat(filepath.Join(p, "ready.json")); err == nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p, "metadata.json"))
		if err != nil {
			return err
		}
		var meta Metadata
		if err = json.Unmarshal(raw, &meta); err != nil {
			return err
		}
		meta.Incomplete = true
		if meta.PlatformEventID == "" {
			meta.NeedsAttachmentDiscovery = true
		}
		if _, statErr := os.Stat(filepath.Join(p, "events.jsonl")); os.IsNotExist(statErr) {
			// Begin 尚未完成时进程退出，没有运行时事件可恢复；输入原文已在 metadata 中。
			f, createErr := os.OpenFile(filepath.Join(p, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if createErr != nil {
				return createErr
			}
			createErr = f.Sync()
			f.Close()
			if createErr != nil {
				return createErr
			}
		}
		if err = writeJSONFile(p, "metadata.json", meta); err != nil {
			return err
		}
		if err = writeJSONFile(p, "ready.json", map[string]bool{"interrupted": true}); err != nil {
			return err
		}
	}
	return nil
}

func (j *Journal) SetAttachmentDiscovery(pending bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Meta.NeedsAttachmentDiscovery = pending
	return writeJSONFile(j.Dir, "metadata.json", j.Meta)
}
