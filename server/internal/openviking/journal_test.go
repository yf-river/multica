package openviking

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalCrashPreservesRawAndCompletePrefix(t *testing.T) {
	root := t.TempDir()
	j, err := Begin(root, Metadata{AgentID: "ida", User: "ida", Session: "test", Input: "original input"})
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("original-api_key=not-a-secret\n", 14000)
	raw, _ := json.Marshal(map[string]string{"text": payload})
	if err = j.Append("codex", raw); err != nil {
		t.Fatal(err)
	}
	if err = j.file.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(j.Dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"seq":2,"data":`)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(j.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = Recover(root); err != nil {
		t.Fatal(err)
	}
	events, err := readEvents(j.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("complete event prefix was lost")
	}
	var decoded map[string]string
	if err = json.Unmarshal(events[0].Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["text"] != payload {
		t.Fatal("original content was clipped or redacted")
	}
	after, _ := os.ReadFile(filepath.Join(j.Dir, "events.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("recovery rewrote the original source")
	}
	journals, err := PendingJournals(root)
	if err != nil || len(journals) != 1 || !journals[0].Meta.Incomplete {
		t.Fatal("interrupted archive not exposed", err)
	}
}

type brokenAttachment struct{}

func (brokenAttachment) Read(p []byte) (int, error) {
	copy(p, "partial")
	return len("partial"), errors.New("connection lost")
}

func TestAttachmentDownloadRetriesWithoutCommittingPartialBytes(t *testing.T) {
	j, err := Begin(t.TempDir(), Metadata{User: "ida", AgentID: "ida", Session: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.file.Close()
	if err = j.WantAttachment("file", "file.bin", "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if err = j.SaveAttachment("file", "file.bin", "application/octet-stream", brokenAttachment{}); err == nil {
		t.Fatal("partial download accepted")
	}
	if j.Meta.Attachments[0].Path != "" {
		t.Fatal("partial attachment marked ready")
	}
	if err = j.SaveAttachment("file", "file.bin", "application/octet-stream", strings.NewReader("complete"), SHA([]byte("other"))); err == nil {
		t.Fatal("bad source digest accepted")
	}
	if err = j.SaveAttachment("file", "file.bin", "application/octet-stream", strings.NewReader("complete"), SHA([]byte("complete"))); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(j.Dir, j.Meta.Attachments[0].Path))
	if err != nil || string(data) != "complete" {
		t.Fatal("retry did not save original bytes", err)
	}
}

func TestJournalWriteFailureCannotBecomeReady(t *testing.T) {
	j, err := Begin(t.TempDir(), Metadata{User: "ida", AgentID: "ida", Session: "test"})
	if err != nil {
		t.Fatal(err)
	}
	j.file.Close()
	if err = j.Append("codex", json.RawMessage(`{"text":"original"}`)); err == nil {
		t.Fatal("closed journal accepted a record")
	}
	if err = j.Finish(map[string]string{"status": "completed"}); err == nil {
		t.Fatal("failed original recording accepted as complete")
	}
	if _, err = os.Stat(filepath.Join(j.Dir, "ready.json")); !os.IsNotExist(err) {
		t.Fatal("failed recording was published to replay")
	}
}

func TestDSHChildrenAndFullToolsNormalize(t *testing.T) {
	output := strings.Repeat("tool bytes\n", 40000)
	frames := []map[string]any{
		{"type": "archive", "session_id": "child", "event": map[string]any{"type": "assistant/message", "seq": 1, "data": map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "child reply"}}}}}},
		{"type": "archive", "session_id": "child", "event": map[string]any{"type": "tool/call", "seq": 2, "data": map[string]any{"callId": "call-1", "name": "bash", "arguments": `{"command":"cat file"}`}}},
		{"type": "archive", "session_id": "child", "event": map[string]any{"type": "tool/result", "seq": 3, "data": map[string]any{"message": map[string]any{"toolCallId": "call-1", "content": []any{map[string]any{"type": "text", "text": output}}}}}},
	}
	var events []Event
	for i, frame := range frames {
		raw, _ := json.Marshal(frame)
		events = append(events, Event{Seq: int64(i + 1), Source: "dsh", Data: raw})
	}
	messages := normalize(Metadata{Attempt: "attempt", Input: "input"}, events, "viking://user/ida/sessions/session/raw")
	if len(messages) != 4 {
		t.Fatalf("child events omitted: %d", len(messages))
	}
	if messages[1].Parts[0]["text"] != "child reply" {
		t.Fatal("child reply lost")
	}
	if messages[3].Parts[0]["tool_output"] != output {
		t.Fatal("large tool output clipped")
	}
}
