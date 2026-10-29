package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeArchive(t *testing.T) {
	base := os.Getenv("MULTICA_OPENVIKING_TEST_URL")
	if base == "" {
		t.Skip("requires isolated native OpenViking")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:19446") {
		t.Fatal("test is restricted to the isolated int dependency")
	}
	c := FromEnv()
	c.URL = base
	c.Key = "isolated-test-key"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	j, err := Begin(root, Metadata{TaskID: "test-" + suffix, AgentID: "ida-test", User: "int-ida-" + suffix, Session: "int-archive-" + suffix, Provider: "codex", Input: "This is an integration test. For the demo project, the user prefers Mandarin and uses the project codename SilverPine."})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(v any) {
		t.Helper()
		data, _ := json.Marshal(v)
		if err := j.Append("codex", data); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "root", "turn": map[string]any{"id": "turn-1"}}})
	output := "api_key=archive-test-not-a-secret\n" + strings.Repeat("original tool output\n", 16000)
	appendEvent(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "child", "item": map[string]any{"type": "commandExecution", "id": "tool-1", "command": "cat large.txt", "aggregatedOutput": output}}})
	appendEvent(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "root", "item": map[string]any{"type": "agentMessage", "id": "message-1", "text": "The user named the demo project SilverPine and prefers Mandarin. The test tool output has been recorded."}}})
	var imageBuffer bytes.Buffer
	if err := png.Encode(&imageBuffer, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	imageData := imageBuffer.Bytes()
	if err = j.WantAttachment("image-1", "图像.png", "image/png"); err != nil {
		t.Fatal(err)
	}
	if err = j.SaveAttachment("image-1", "图像.png", "image/png", bytes.NewReader(imageData)); err != nil {
		t.Fatal(err)
	}
	if err = j.Finish(map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	for {
		if err = c.Replay(ctx, root); err != nil {
			t.Fatal(err)
		}
		stateRaw, err := os.ReadFile(filepath.Join(j.Dir, "sync.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state SyncState
		if err = json.Unmarshal(stateRaw, &state); err != nil {
			t.Fatal(err)
		}
		if state.Done {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native extraction did not complete", state)
		case <-time.After(time.Second):
		}
	}
	uri := "viking://user/" + j.Meta.User + "/sessions/" + j.Meta.Session
	rawURI := uri + "/multica/" + j.Meta.Attempt
	stored, err := c.Download(ctx, j.Meta.User, rawURI+"/"+j.Meta.Attachments[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, imageData) {
		t.Fatal("binary attachment changed")
	}
	raw, err := c.Download(ctx, j.Meta.User, rawURI+"/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var actual []byte
	actual, err = os.ReadFile(filepath.Join(j.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, actual) {
		t.Fatal("raw event journal changed")
	}
	seen, _, err := c.sessionSources(ctx, j.Meta.User, uri)
	if err != nil {
		t.Fatal(err)
	}
	before := len(seen)
	if before < 3 {
		t.Fatalf("expected user, tool and assistant messages, got %d", before)
	}
	// 模拟提交成功但本地回执未落盘，必须从原生归档恢复而不是提交空会话。
	if err = writeJSONFile(j.Dir, "sync.json", SyncState{Uploaded: true}); err != nil {
		t.Fatal(err)
	}
	if err = c.Replay(ctx, root); err != nil {
		t.Fatal(err)
	}
	var recovered SyncState
	saved, err := os.ReadFile(filepath.Join(j.Dir, "sync.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(saved, &recovered); err != nil || !recovered.Done || len(recovered.MemoryTasks) == 0 {
		t.Fatalf("lost receipt not recovered: %+v %v", recovered, err)
	}
	seen, _, err = c.sessionSources(ctx, j.Meta.User, uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != before {
		t.Fatal("replay duplicated native messages")
	}
	memory, err := c.Recall(ctx, j.Meta.User, "SilverPine Mandarin preference")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(memory), "silverpine") {
		t.Fatalf("real extraction/recall did not return the source fact: %s", memory)
	}
	t.Logf("native archive, binary hash, %d source messages, extraction and recall verified; user=%s session=%s", before, j.Meta.User, j.Meta.Session)
}

func TestNativeFailedExtractionRecovery(t *testing.T) {
	if os.Getenv("MULTICA_OPENVIKING_RECOVERY_FIXTURE") != "true" {
		t.Skip("requires the recorded isolated failed extraction")
	}
	c := FromEnv()
	c.URL = "http://127.0.0.1:19446"
	c.Key = "isolated-test-key"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	user := "int-ida-221436999070815"
	uri := "viking://user/" + user + "/sessions/int-archive-221436999070815"
	seen, _, err := c.sessionSources(ctx, user, uri)
	if err != nil {
		t.Fatal(err)
	}
	before := len(seen)
	for {
		done, err := c.recoverExtraction(ctx, user, uri, "b950015a-8f5b-46ad-90fe-3b68f7e16b33")
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	seen, _, err = c.sessionSources(ctx, user, uri)
	if err != nil || len(seen) != before {
		t.Fatalf("recovery modified original sources: %d %d %v", before, len(seen), err)
	}
	memory, err := c.Recall(ctx, user, "SilverPine Mandarin")
	if err != nil || !strings.Contains(strings.ToLower(memory), "silverpine") {
		t.Fatalf("recovered memory missing: %s %v", memory, err)
	}
	t.Log("terminal failed extraction recovered with real model; original conversation unchanged")
}

func TestNativeOriginalsAndSkill(t *testing.T) {
	if os.Getenv("MULTICA_OPENVIKING_TEST_URL") != "http://127.0.0.1:19446" {
		t.Skip("requires isolated native fixture")
	}
	c := FromEnv()
	c.URL = "http://127.0.0.1:19446"
	c.Key = "isolated-test-key"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	binary := []byte{0, 255, 1, 128, 0, 42}
	file := filepath.Join(root, "original.bin")
	if err := os.WriteFile(file, binary, 0600); err != nil {
		t.Fatal(err)
	}
	uri := "viking://resources/migration/raw-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	files := map[string]string{"nested/original.bin": file}
	if err := c.StoreOriginals(ctx, "int-migration", uri, files); err != nil {
		t.Fatal(err)
	}
	if err := c.StoreOriginals(ctx, "int-migration", uri, files); err != nil {
		t.Fatal(err)
	}
	stored, err := c.Download(ctx, "int-migration", uri+"/nested/original.bin")
	if err != nil || !bytes.Equal(stored, binary) {
		t.Fatalf("original bytes changed: %v", err)
	}
	skills, err := c.Skills(ctx, "int-migration", nil)
	if err != nil {
		t.Fatal(err)
	}
	var fixture *Skill
	for i := range skills {
		if skills[i].Name == "native-full-skill" {
			fixture = &skills[i]
			break
		}
	}
	if fixture == nil {
		t.Fatal("migrated native skill missing")
	}
	executable := ""
	for _, f := range fixture.Files {
		dest := filepath.Join(root, f.Path)
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, f.Data, os.FileMode(f.Mode)); err != nil {
			t.Fatal(err)
		}
		if f.Path == "scripts/check.sh" {
			executable = dest
			if f.Mode != 0755 {
				t.Fatal("executable mode lost")
			}
		}
		if f.Path == "assets/sample.bin" {
			want := make([]byte, 2048)
			for i := range want {
				want[i] = byte(i % 256)
			}
			if !bytes.Equal(want, f.Data) {
				t.Fatal("skill binary attachment changed")
			}
		}
	}
	if executable == "" {
		t.Fatal("skill script missing")
	}
	output, err := exec.CommandContext(ctx, executable).Output()
	if err != nil || string(output) != "native-skill-executed" {
		t.Fatalf("actual native skill script failed: %q %v", output, err)
	}
	t.Log("native deferred raw storage, idempotent original binary copy, native skill directory and executable script verified")
}
