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
	"time"
)

func TestCollectTaskMarkdownArtifactsScansArtifactsTree(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "02-design.md"), "# design")
	writeArtifactTestFile(t, filepath.Join(workDir, ".multica", "artifacts", "01-clarify.md"), "# clarify")
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "acceptance", "01-clarify.md"), "# acceptance")
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
	want := []string{"01-clarify.md", "acceptance/01-clarify.md", "multica/02-design.md"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("artifact names = %#v, want %#v", names, want)
	}
}

func TestCollectTaskMarkdownArtifactsFromDirsIncludesIssueArtifactDir(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	artifactDir := t.TempDir()
	writeArtifactTestFile(t, filepath.Join(workDir, "artifacts", "multica", "02-design.md"), "# design")
	writeArtifactTestFile(t, filepath.Join(artifactDir, "05-verify.md"), "# verify")

	got, err := collectTaskMarkdownArtifactsFromDirs(workDir, artifactDir)
	if err != nil {
		t.Fatalf("collectTaskMarkdownArtifactsFromDirs: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, artifact := range got {
		names = append(names, artifact.DisplayName)
	}
	want := []string{"05-verify.md", "multica/02-design.md"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("artifact names = %#v, want %#v", names, want)
	}
}

func TestCollectTaskMarkdownArtifactsFromDirsSkipsStaleIssueArtifacts(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	artifactDir := t.TempDir()
	oldPath := filepath.Join(artifactDir, "01-clarify.md")
	newPath := filepath.Join(artifactDir, "02-design.md")
	writeArtifactTestFile(t, oldPath, "# clarify")
	cutoff := time.Now()
	if err := os.Chtimes(oldPath, cutoff.Add(-2*time.Minute), cutoff.Add(-2*time.Minute)); err != nil {
		t.Fatalf("Chtimes old artifact: %v", err)
	}
	writeArtifactTestFile(t, newPath, "# design")
	if err := os.Chtimes(newPath, cutoff.Add(time.Second), cutoff.Add(time.Second)); err != nil {
		t.Fatalf("Chtimes new artifact: %v", err)
	}

	got, err := collectTaskMarkdownArtifactsFromDirsSince(workDir, artifactDir, cutoff)
	if err != nil {
		t.Fatalf("collectTaskMarkdownArtifactsFromDirsSince: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, artifact := range got {
		names = append(names, artifact.DisplayName)
	}
	want := []string{"02-design.md"}
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
			if strings.Contains(content, "Agent 产物已自动收拢到平台") || !strings.Contains(content, "!file[05-verify.md](/api/attachments/att-1/download)") {
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
	}, workDir, "", time.Time{}, slog.New(slog.NewTextHandler(io.Discard, nil)), taskArtifactCommentOptions{})

	if !uploaded || !commented {
		t.Fatalf("uploaded=%v commented=%v, want both true", uploaded, commented)
	}
}

func TestPersistFinalOutputArtifactForReadOnlyStage(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	artifactDir := t.TempDir()
	task := Task{
		ID:      "task-1",
		IssueID: "issue-1",
		ExecutionPolicy: &TaskExecutionPolicy{
			RoleKey:     "02-design",
			RoleKind:    "planning_stage",
			CanEditRepo: false,
		},
	}
	output := "# 02-design.md\n\n## 技术方案\n" + strings.Repeat("- 方案内容\n", 80)
	result := TaskResult{Status: "completed", Comment: output}

	d := &Daemon{}
	opts := d.persistFinalOutputArtifactIfNeeded(task, result, workDir, artifactDir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	data, err := os.ReadFile(filepath.Join(artifactDir, "02-design.md"))
	if err != nil {
		t.Fatalf("read persisted final output artifact: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(output) {
		t.Fatalf("artifact content mismatch:\n%s", string(data))
	}
	if !strings.Contains(opts.Summary, "阶段产物已上传") || !strings.Contains(opts.Summary, "# 02-design.md") {
		t.Fatalf("summary = %q", opts.Summary)
	}
}

func TestSummarizeFinalOutputForArtifactCommentKeepsCommentCompact(t *testing.T) {
	t.Parallel()

	output := "I read the context and will now provide the artifact.\n\n---\n## 03-task-split.md\n\n" + strings.Repeat("- detail\n", 20)

	got := summarizeFinalOutputForArtifactComment(output)

	if !strings.Contains(got, "## 03-task-split.md") {
		t.Fatalf("summary missing heading: %q", got)
	}
	if strings.Contains(got, "I read the context") || strings.Contains(got, "- detail") {
		t.Fatalf("summary should not include preamble or body details: %q", got)
	}
}

func TestTaskArtifactCommentContentIncludesSummary(t *testing.T) {
	t.Parallel()

	content := taskArtifactCommentContent([]uploadedTaskArtifact{{
		ID:          "att-1",
		Filename:    "02-design.md",
		MarkdownURL: "/api/attachments/att-1/download",
	}}, taskArtifactCommentOptions{Summary: "阶段产物已上传，评论只保留摘要。\n\n# 02-design.md"})

	if !strings.HasPrefix(content, "阶段产物已上传，评论只保留摘要。") {
		t.Fatalf("content missing summary prefix: %q", content)
	}
	if !strings.Contains(content, "!file[02-design.md](/api/attachments/att-1/download)") {
		t.Fatalf("content missing file card: %q", content)
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
