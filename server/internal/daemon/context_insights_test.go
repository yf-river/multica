package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInsightsSpoolRetriesAndKeepsPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.jsonl")
	first := `{"type":"archive","event":{"seq":1}}` + "\n"
	second := `{"type":"archive","event":{"seq":2}}` + "\n"
	if err := os.WriteFile(path, []byte(first+second[:10]), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "offline", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL)}
	if err := d.syncInsightsFile(context.Background(), path); err == nil {
		t.Fatal("failed upload acknowledged")
	}
	if _, err := os.Stat(path + ".ack"); !os.IsNotExist(err) {
		t.Fatal("ack advanced on failure")
	}
	if err := d.syncInsightsFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	ack, _ := os.ReadFile(path + ".ack")
	if string(ack) != strconv.Itoa(len(first)) {
		t.Fatal("invalid ack")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(second[10:])
	f.Close()
	restarted := &Daemon{client: NewClient(server.URL)}
	if err := restarted.syncInsightsFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("replayed acknowledged frame: calls=%d", calls)
	}
}

func TestInsightsLargeFrameSplitsWithoutTruncation(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"type": "insights", "content": strings.Repeat("原", 300000)})
	path := filepath.Join(t.TempDir(), "task.jsonl")
	os.WriteFile(path, append(raw, '\n'), 0600)
	var restored []byte
	parts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Chunk struct {
				Data []byte `json:"data"`
				Part int    `json:"part"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Chunk.Part != parts {
			t.Error("wrong chunk order")
		}
		parts++
		restored = append(restored, body.Chunk.Data...)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL)}
	if err := d.syncInsightsFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(raw)+"\n" || parts < 3 {
		t.Fatal("large original truncated")
	}
}

func TestInsightsActivationRecoversAcrossServerOutage(t *testing.T) {
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			http.Error(w, "unavailable", status)
			return
		}
		w.Write([]byte(`{"enabled_since":1793340000000}`))
	}))
	defer server.Close()
	d := &Daemon{cfg: Config{ServerBaseURL: server.URL, DaemonID: "daemon-insights", WorkspacesRoot: t.TempDir()}, client: NewClient(server.URL)}
	first, err := d.insightsActivation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status = 500
	restarted := &Daemon{cfg: d.cfg, client: NewClient(server.URL)}
	restored, err := restarted.insightsActivation(context.Background())
	if err != nil || restored != first {
		t.Fatalf("lost activation: %d %v", restored, err)
	}
	restarted.cfg.WorkspacesRoot = t.TempDir()
	if _, err = restarted.insightsActivation(context.Background()); err == nil {
		t.Fatal("missing activation silently disabled collection")
	}
	status = 404
	if since, err := restarted.insightsActivation(context.Background()); err != nil || since != 0 {
		t.Fatal("older server should leave collection inactive")
	}
}

func TestInsightsDeletionRequiresAuthoritativeAbsenceAndIdle(t *testing.T) {
	status := 503
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 200 {
			w.Write([]byte(`{"status":"completed"}`))
			return
		}
		http.Error(w, "unavailable", status)
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	d.cfg.WorkspacesRoot = t.TempDir()
	spool, err := d.newInsightsSpool("task")
	if err != nil {
		t.Fatal(err)
	}
	spool.append(json.RawMessage(`{"event":1}`))
	spool.file.Close()
	for _, code := range []int{503, 200} {
		status = code
		d.pruneDeletedInsights(context.Background())
		if _, err = os.Stat(spool.path); err != nil {
			t.Fatal("existing or unreachable task lost its original")
		}
	}
	status = 404
	d.activeTasks.Store(1)
	d.pruneDeletedInsights(context.Background())
	if _, err = os.Stat(spool.path); err != nil {
		t.Fatal("active writer lost its original")
	}
	d.activeTasks.Store(0)
	d.claimsInFlight = 1
	d.pruneDeletedInsights(context.Background())
	if _, err = os.Stat(spool.path); err != nil {
		t.Fatal("in-flight claim lost its original")
	}
	d.claimsInFlight = 0
	d.pruneDeletedInsights(context.Background())
	if _, err = os.Stat(spool.path); !os.IsNotExist(err) {
		t.Fatal("deleted task original retained")
	}
}

func TestInsightsExplicitInactiveConfigSurvivesOffline(t *testing.T) {
	status := 200
	enabled := 123
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			http.Error(w, "offline", status)
			return
		}
		json.NewEncoder(w).Encode(map[string]int{"enabled_since": enabled, "version": 1})
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL)}
	d.cfg.WorkspacesRoot = t.TempDir()
	if since, err := d.insightsActivation(context.Background()); err != nil || since != 123 {
		t.Fatalf("enabled since=%d err=%v", since, err)
	}
	enabled = 0
	if since, err := d.insightsActivation(context.Background()); err != nil || since != 0 {
		t.Fatalf("inactive since=%d err=%v", since, err)
	}
	status = 503
	if since, err := d.insightsActivation(context.Background()); err != nil || since != 0 {
		t.Fatalf("cached inactive since=%d err=%v", since, err)
	}
}

func TestInsightsNativeImageOriginal(t *testing.T) {
	root := os.Getenv("MULTICA_INSIGHTS_NATIVE_FIXTURE")
	if root == "" {
		t.Skip("requires explicit native DSH image fixture")
	}
	frames, err := os.ReadFile(filepath.Join(root, "image.frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "image-work/image.png"))
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{}
	d.cfg.WorkspacesRoot = t.TempDir()
	spool, err := d.newInsightsSpool("native-image")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.file.Close()
	seen := map[string]bool{}
	for _, frame := range bytes.Split(bytes.TrimSpace(frames), []byte("\n")) {
		var value map[string]any
		if json.Unmarshal(frame, &value) != nil {
			t.Fatal("invalid native frame")
		}
		if value["type"] == "archive" || value["type"] == "insight_context" {
			if err = spool.capture(frame, filepath.Join(root, "home/attachments/v1/objects"), seen); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := os.ReadFile(spool.path)
	if err != nil {
		t.Fatal(err)
	}
	captured := 0
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var frame struct {
			Type    string `json:"type"`
			Content []byte `json:"content"`
			ID      string `json:"attachment_id"`
		}
		json.Unmarshal(line, &frame)
		if frame.Type == "insight_attachment" {
			captured++
			sum := sha256.Sum256(original)
			if !bytes.Equal(original, frame.Content) || frame.ID != "sha256:"+hex.EncodeToString(sum[:]) {
				t.Fatal("native original not preserved")
			}
		}
	}
	if captured != 1 {
		t.Fatalf("captured=%d", captured)
	}
}
