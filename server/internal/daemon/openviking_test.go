package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/openviking"
)

func TestOpenVikingOutagePausesClaimsAndRecoveryClaimsOnce(t *testing.T) {
	var healthy atomic.Bool
	var claims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/sessions" {
			_, _ = w.Write([]byte(`{"status":"ok","result":[]}`))
			return
		}
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]bool{"healthy": healthy.Load()})
			return
		}
		claims.Add(1)
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_OPENVIKING_ENABLED", "true")
	t.Setenv("MULTICA_OPENVIKING_URL", srv.URL)
	d := New(Config{ServerBaseURL: srv.URL, WorkspacesRoot: t.TempDir(), MaxConcurrentTasks: 1}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	for i := 0; i < 2; i++ {
		if _, err := d.ClaimTasksWSFirst(context.Background(), "daemon", []string{"codex", "dsh", "review", "quality", "upstream"}, 1); err == nil {
			t.Fatal("unhealthy OpenViking did not pause new claims")
		}
	}
	if claims.Load() != 0 {
		t.Fatal("claimed tasks during outage")
	}
	healthy.Store(true)
	if _, err := d.ClaimTasksWSFirst(context.Background(), "daemon", []string{"codex", "dsh", "review", "quality", "upstream"}, 1); err != nil {
		t.Fatal(err)
	}
	if claims.Load() != 1 {
		t.Fatalf("recovery issued %d claims", claims.Load())
	}
}

func TestDSHImageReferencesCopyOriginalBinary(t *testing.T) {
	data := []byte{0, 1, 255, 0, 128}
	digest := openviking.SHA(data)
	native := t.TempDir()
	if err := os.Mkdir(filepath.Join(native, digest[:2]), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, digest[:2], digest), data, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := openviking.Begin(t.TempDir(), openviking.Metadata{User: "ida", AgentID: "ida", Session: "test", NativeAttachmentRoot: native})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Finish(map[string]string{"status": "test"})
	raw, _ := json.Marshal(map[string]any{"attachment": map[string]any{"attachmentId": "sha256:" + digest, "mediaType": "image/png"}})
	if err = collectRuntimeAttachmentRefs(j, append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{}
	if err = d.downloadOpenVikingAttachments(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if len(j.Meta.Attachments) != 1 || j.Meta.Attachments[0].SHA256 != digest {
		t.Fatal("native image original was not saved")
	}
}

func TestOpenVikingCapturesNativeSpillBeforeTaskCleanup(t *testing.T) {
	root := t.TempDir()
	j, err := openviking.Begin(t.TempDir(), openviking.Metadata{AgentID: "ida", User: "ida", Session: "spill"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Finish(nil)
	if err = j.ProtectTempRoot(root); err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(root, "dsh-subprocess-test", "stdout.log")
	spill := filepath.Join(root, "dsh-spill-test", "result.txt")
	for _, f := range []string{stdout, spill} {
		if err = os.MkdirAll(filepath.Dir(f), 0700); err != nil {
			t.Fatal(err)
		}
	}
	full := []byte("OV-LONG-HEAD\napi_key=not-redacted\n" + strings.Repeat("X", 300000) + "\nOV-LONG-TAIL")
	if err = os.WriteFile(stdout, full, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(spill, []byte("display slice\n[output truncated; full output: "+stdout+"]"), 0600); err != nil {
		t.Fatal(err)
	}
	frame := map[string]any{"type": "archive", "event": map[string]any{"type": "tool/result", "data": map[string]any{"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "(Omitted bytes. Full formatted result stored at: " + spill + ". Use read with offset/limit.)"}}}}}}
	raw, _ := json.Marshal(frame)
	if err = j.Append("dsh", raw); err != nil {
		t.Fatal(err)
	}
	if err = collectRuntimeAttachmentRefs(j, raw); err != nil {
		t.Fatal(err)
	}
	if len(j.Meta.Attachments) != 2 || j.PendingNativeOutputs() {
		t.Fatalf("nested original outputs missing: %+v", j.Meta.Attachments)
	}
	if err = j.ClearTempProtection(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join(j.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = collectRuntimeAttachmentRefs(j, journal); err != nil {
		t.Fatalf("retry depended on deleted runtime temp files: %v", err)
	}
	found := false
	for _, a := range j.Meta.Attachments {
		if a.SourcePath == stdout {
			data, err := os.ReadFile(filepath.Join(j.Dir, a.Path))
			if err != nil || !bytes.Equal(data, full) {
				t.Fatal("full native stdout changed")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("full stdout not captured")
	}
}

func TestDisabledOpenVikingIgnoresUnreachableNativeAndUsesPlatformSkills(t *testing.T) {
	t.Setenv("MULTICA_OPENVIKING_ENABLED", "false")
	t.Setenv("MULTICA_OPENVIKING_URL", "http://127.0.0.1:1")
	d := New(Config{WorkspacesRoot: t.TempDir()}, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	if err := d.openVikingReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if journal, err := d.beginOpenViking(context.Background(), nil, "any-system-runtime"); err != nil || journal != nil {
		t.Fatalf("disabled archive created: %v %v", journal, err)
	}
	if err := d.ensureTaskSkillBundles(context.Background(), &Task{Agent: &AgentData{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.openVikingRoot()); !os.IsNotExist(err) {
		t.Fatalf("disabled task touched archive directory: %v", err)
	}
}
