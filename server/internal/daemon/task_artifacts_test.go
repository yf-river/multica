package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectTaskMarkdownArtifactsOnlyScansContractDirs(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "02-design.md"), "# design")
	writeArtifactTestFile(t, filepath.Join(workDir, ".multica", "artifacts", "01-clarify.md"), "# clarify")
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "acceptance", "ignore.md"), "# ignore")
	writeArtifactTestFile(t, filepath.Join(workDir, "docs", "ignore.md"), "# ignore")
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "empty.md"), "")
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "note.txt"), "ignore")

	got, err := collectTaskMarkdownArtifacts(workDir)
	if err != nil {
		t.Fatalf("collectTaskMarkdownArtifacts: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, artifact := range got {
		names = append(names, artifact.DisplayName)
	}
	want := []string{"01-clarify.md", "02-design.md"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("artifact names = %#v, want %#v", names, want)
	}
}

func TestCollectAndPostTaskArtifactsUploadsAndLinksCommentAsTask(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "05-verify.md"), "# verify")

	var uploaded bool
	var commented bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer mat_task-token" {
			t.Errorf("Authorization = %q, want task token", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "ws-1" {
			t.Errorf("X-Workspace-ID = %q", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got != "agent-1" {
			t.Errorf("X-Agent-ID = %q", got)
		}
		if got := r.Header.Get("X-Task-ID"); got != "task-1" {
			t.Errorf("X-Task-ID = %q", got)
		}

		switch r.URL.Path {
		case "/api/upload-file":
			uploaded = true
			if err := r.ParseMultipartForm(taskArtifactMaxBytes); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			if got := r.FormValue("issue_id"); got != "issue-1" {
				t.Errorf("issue_id = %q", got)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("FormFile: %v", err)
			}
			defer file.Close()
			if header.Filename != "05-verify.md" {
				t.Errorf("filename = %q", header.Filename)
			}
			data, _ := io.ReadAll(file)
			if string(data) != "# verify" {
				t.Errorf("file content = %q", data)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(uploadedTaskArtifact{
				ID:          "att-1",
				Filename:    "05-verify.md",
				MarkdownURL: "/api/attachments/att-1/download",
				DownloadURL: "/api/attachments/att-1/download",
			})
		case "/api/issues/issue-1/comments":
			commented = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode comment body: %v", err)
			}
			if got := body["parent_id"]; got != "comment-1" {
				t.Errorf("parent_id = %#v", got)
			}
			content, _ := body["content"].(string)
			if !strings.Contains(content, "Agent 产物已自动收拢到平台") || !strings.Contains(content, "!file[05-verify.md](/api/attachments/att-1/download)") {
				t.Errorf("unexpected content: %q", content)
			}
			ids, ok := body["attachment_ids"].([]any)
			if !ok || len(ids) != 1 || ids[0] != "att-1" {
				t.Errorf("attachment_ids = %#v", body["attachment_ids"])
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	d := &Daemon{client: NewClient(srv.URL)}
	d.collectAndPostTaskArtifacts(context.Background(), Task{
		ID:               "task-1",
		AgentID:          "agent-1",
		IssueID:          "issue-1",
		WorkspaceID:      "ws-1",
		TriggerCommentID: "comment-1",
		AuthToken:        "mat_task-token",
	}, workDir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if !uploaded || !commented {
		t.Fatalf("uploaded=%v commented=%v, want both true", uploaded, commented)
	}
}

func writeArtifactTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
