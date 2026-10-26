package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexNativeResumeRequiresOriginalIdentityAndPersistentRollout(t *testing.T) {
	root := t.TempDir()
	expected, other := filepath.Join(root, "persistent.jsonl"), filepath.Join(root, "other.jsonl")
	for _, path := range []string{expected, other} {
		if err := os.WriteFile(path, []byte("owned-rollout"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, id, path string
		rejected, ok   bool
	}{
		{name: "expected", id: "original", path: expected, ok: true},
		{name: "wrong identity", id: "replacement", path: expected},
		{name: "different file", id: "original", path: other},
		{name: "missing path", id: "original"},
		{name: "missing identity", path: expected},
		{name: "native refusal", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, _ := newTestCodexClient(t)
			client.cfg.CodexResumePath = expected
			stdin := &fakeStdinWithHook{}
			stdin.afterWrite = func() {
				lines := stdin.Lines()
				var request struct {
					ID     int            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
					t.Fatal(err)
				}
				if request.Method != "thread/resume" || request.Params["path"] != nil {
					t.Fatal("native indexed resume used a path override or created a replacement")
				}
				response := map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": tc.id, "path": tc.path}}}
				if tc.rejected {
					delete(response, "result")
					response["error"] = map[string]any{"code": -32600, "message": "native history unavailable"}
				}
				data, _ := json.Marshal(response)
				client.handleLine(string(data))
			}
			client.stdin = stdin
			id, resumed, err := client.startOrResumeThread(context.Background(), ExecOptions{ResumeSessionID: "original"}, slog.Default())
			if tc.ok && (err != nil || !resumed || id != "original") {
				t.Fatalf("valid native restore rejected: %q %t %v", id, resumed, err)
			}
			if !tc.ok && err == nil {
				t.Fatal("unverified native restore silently replaced the session")
			}
			if len(stdin.Lines()) != 1 {
				t.Fatal("failed restore sent additional native requests")
			}
		})
	}
}

func TestCodexNativeResumeAcceptsOnlySameDirectoryCompressionTransition(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	compressed := filepath.Join(root, "rollout-owned.jsonl.zst")
	plain := filepath.Join(root, "rollout-owned.jsonl")
	for _, path := range []string{compressed, plain, filepath.Join(other, filepath.Base(plain))} {
		if err := os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !sameCodexResidentRollout(plain, compressed) {
		t.Fatal("native decompression changed the logical rollout identity")
	}
	if sameCodexResidentRollout(filepath.Join(other, filepath.Base(plain)), compressed) {
		t.Fatal("compression transition accepted another directory")
	}
	if sameCodexResidentRollout(filepath.Join(root, "another.jsonl"), compressed) {
		t.Fatal("compression transition accepted another thread filename")
	}
	if err := os.Remove(compressed); err != nil {
		t.Fatal(err)
	}
	if !sameCodexResidentRollout(plain, compressed) {
		t.Fatal("native removal of the old encoding broke resumed identity")
	}
}
