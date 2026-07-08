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
	output := "I collected the context.\n\n---\n# 02-design.md\n\n## 技术方案\n" + strings.Repeat("- 方案内容\n", 80)
	result := TaskResult{Status: "completed", Comment: output}

	d := &Daemon{}
	opts := d.persistFinalOutputArtifactIfNeeded(task, result, workDir, artifactDir, slog.New(slog.NewTextHandler(io.Discard, nil)))

	data, err := os.ReadFile(filepath.Join(artifactDir, "02-design.md"))
	if err != nil {
		t.Fatalf("read persisted final output artifact: %v", err)
	}
	if strings.Contains(string(data), "I collected the context") {
		t.Fatalf("artifact content should drop preamble:\n%s", string(data))
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "# 02-design.md") {
		t.Fatalf("artifact content mismatch:\n%s", string(data))
	}
	if strings.Contains(opts.Summary, "评论只保留摘要") || !strings.Contains(opts.Summary, "已上传阶段产物：02-design.md") || !strings.Contains(opts.Summary, "摘要：方案内容") || !strings.Contains(opts.Summary, "完整内容见附件") {
		t.Fatalf("summary = %q", opts.Summary)
	}
}

func TestSummarizeFinalOutputForArtifactCommentKeepsCommentCompact(t *testing.T) {
	t.Parallel()

	output := "I read the context and will now provide the artifact.\n\n---\n## 03-task-split.md\n\n" + strings.Repeat("- detail\n", 20)

	got := summarizeFinalOutputForArtifactComment(output)

	if !strings.Contains(got, "已上传阶段产物：03-task-split.md") {
		t.Fatalf("summary missing heading: %q", got)
	}
	if strings.Contains(got, "评论只保留摘要") || strings.Contains(got, "## 03-task-split.md") {
		t.Fatalf("summary should be user-facing plain text: %q", got)
	}
	if strings.Contains(got, "I read the context") || strings.Contains(got, "- detail") {
		t.Fatalf("summary should not include preamble or body details: %q", got)
	}
	if !strings.Contains(got, "摘要：detail") || !strings.Contains(got, "完整内容见附件") {
		t.Fatalf("summary missing useful body summary: %q", got)
	}
}

func TestSummarizeFinalOutputForArtifactCommentUsesReadableStageSummary(t *testing.T) {
	t.Parallel()

	output := `Now I have all the context.

---

## 03-任务拆分：增强密码强度 [IDA-79]

### 一、变更范围确认

基于 02-design.md 产物，本次变更覆盖 user-center 单仓库内的密码强度校验。
`

	got := summarizeFinalOutputForArtifactComment(output)

	if want := "已上传阶段产物：03-任务拆分：增强密码强度 [IDA-79]"; !strings.Contains(got, want) {
		t.Fatalf("summary missing stage title %q: %q", want, got)
	}
	if want := "摘要：基于 02-design.md 产物，本次变更覆盖 user-center 单仓库内的密码强度校验。"; !strings.Contains(got, want) {
		t.Fatalf("summary missing first body paragraph %q: %q", want, got)
	}
}

func TestSummarizeFinalOutputForArtifactCommentSummarizesTwoColumnTable(t *testing.T) {
	t.Parallel()

	output := `# 05-验证测试：增强密码强度 [IDA-79]

## 一、验证摘要

| 项目 | 结果 |
|------|------|
| 目标项目 | user-center |
| MR | !122 |
| child issue | 0 |
`

	got := summarizeFinalOutputForArtifactComment(output)

	if want := "摘要：目标项目：user-center；MR：!122；child issue：0"; !strings.Contains(got, want) {
		t.Fatalf("summary missing table digest %q: %q", want, got)
	}
}

func TestSummarizeFinalOutputForArtifactCommentIncludesClarificationQuestions(t *testing.T) {
	t.Parallel()

	output := `# 01-clarify.md

## 摘要

已上传阶段产物：三、验收口径。

## 待确认项

1. 作用范围：注册、重置密码、管理员重置是否统一适用？
2. 特殊字符白名单是否包含空格？
3. 是否需要禁止与最近 N 次密码相同？
4. 这一条不应进入评论。

## 下一步

- 等待用户确认后进入 02-方案设计。
`

	got := summarizeFinalOutputForArtifactComment(output)

	for _, want := range []string{
		"已上传阶段产物：01-clarify.md",
		"摘要：已上传阶段产物：三、验收口径。",
		"待确认：\n- 作用范围：注册、重置密码、管理员重置是否统一适用？",
		"- 特殊字符白名单是否包含空格？",
		"- 是否需要禁止与最近 N 次密码相同？",
		"下一步：\n- 等待用户确认后进入 02-方案设计。",
		"完整内容见附件。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "这一条不应进入评论") {
		t.Fatalf("summary should cap section items:\n%s", got)
	}
}

func TestSummarizeFinalOutputForArtifactCommentIncludesVerificationCases(t *testing.T) {
	t.Parallel()

	output := `# 05-verify.md

## 验证摘要

验证通过。

## 测试列表

| 用例 | 结果 |
|------|------|
| AC-01 长度校验 | 通过 |
| AC-02 字符类型校验 | 通过 |
| AC-03 特殊字符白名单 | 通过 |

## 代码块不应进入评论

` + "```" + `
go test ./...
` + "```" + `
`

	got := summarizeFinalOutputForArtifactComment(output)

	for _, want := range []string{
		"摘要：验证通过。",
		"验收口径：\n- AC-01 长度校验：通过",
		"- AC-02 字符类型校验：通过",
		"- AC-03 特殊字符白名单：通过",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "go test ./...") {
		t.Fatalf("summary should not include code block:\n%s", got)
	}
}

func TestTaskArtifactCommentContentIncludesSummary(t *testing.T) {
	t.Parallel()

	content := taskArtifactCommentContent([]uploadedTaskArtifact{{
		ID:          "att-1",
		Filename:    "02-design.md",
		MarkdownURL: "/api/attachments/att-1/download",
	}}, taskArtifactCommentOptions{Summary: "已上传阶段产物：02-design.md\n\n摘要：方案已完成。\n\n完整内容见附件。"})

	if !strings.HasPrefix(content, "已上传阶段产物：02-design.md") {
		t.Fatalf("content missing summary prefix: %q", content)
	}
	if strings.Contains(content, "评论只保留摘要") {
		t.Fatalf("content contains internal implementation wording: %q", content)
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
