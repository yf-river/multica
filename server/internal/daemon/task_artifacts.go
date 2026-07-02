package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const taskArtifactMaxBytes = 100 << 20 // Keep aligned with handler maxUploadSize.

var taskArtifactDirs = []string{
	filepath.Join("artifacts", "multica"),
	filepath.Join("artifacts", "ai-studio"),
	filepath.Join(".multica", "artifacts"),
}

type taskMarkdownArtifact struct {
	Path        string
	DisplayName string
	SizeBytes   int64
}

type uploadedTaskArtifact struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	MarkdownURL string `json:"markdown_url"`
	DownloadURL string `json:"download_url"`
}

func collectTaskMarkdownArtifacts(workDir string) ([]taskMarkdownArtifact, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil, nil
	}
	workDirAbs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(workDirAbs); err != nil || !st.IsDir() {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("workdir is not a directory: %s", workDirAbs)
	}

	seen := map[string]struct{}{}
	artifacts := make([]taskMarkdownArtifact, 0)
	for _, relRoot := range taskArtifactDirs {
		root := filepath.Join(workDirAbs, relRoot)
		rootInfo, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !rootInfo.IsDir() {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				name := entry.Name()
				if name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(entry.Name()) != ".md" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > taskArtifactMaxBytes {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if _, ok := seen[abs]; ok {
				return nil
			}
			seen[abs] = struct{}{}
			displayName, err := filepath.Rel(root, abs)
			if err != nil || strings.HasPrefix(displayName, "..") {
				displayName = filepath.Base(abs)
			}
			artifacts = append(artifacts, taskMarkdownArtifact{
				Path:        abs,
				DisplayName: filepath.ToSlash(displayName),
				SizeBytes:   info.Size(),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].DisplayName < artifacts[j].DisplayName
	})
	return artifacts, nil
}

func (d *Daemon) collectAndPostTaskArtifacts(ctx context.Context, task Task, workDir string, taskLog *slog.Logger) {
	if task.IssueID == "" || task.WorkspaceID == "" || task.AgentID == "" || task.ID == "" {
		return
	}
	artifacts, err := collectTaskMarkdownArtifacts(workDir)
	if err != nil {
		taskLog.Warn("collect task artifacts failed", "error", err, "work_dir", workDir)
		return
	}
	if len(artifacts) == 0 {
		return
	}
	token, err := taskScopedAuthToken(task)
	if err != nil {
		taskLog.Warn("collect task artifacts skipped: task auth token invalid", "error", err)
		return
	}

	uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	uploaded := make([]uploadedTaskArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		resp, err := d.client.UploadTaskArtifact(uploadCtx, token, task.WorkspaceID, task.AgentID, task.ID, task.IssueID, artifact)
		if err != nil {
			taskLog.Warn("upload task artifact failed",
				"error", err,
				"path", artifact.Path,
				"issue_id", task.IssueID,
				"task_id", task.ID,
			)
			continue
		}
		if resp.ID == "" {
			taskLog.Warn("upload task artifact returned empty attachment id", "path", artifact.Path)
			continue
		}
		uploaded = append(uploaded, resp)
	}
	if len(uploaded) == 0 {
		return
	}

	content := taskArtifactCommentContent(uploaded)
	attachmentIDs := make([]string, 0, len(uploaded))
	for _, att := range uploaded {
		attachmentIDs = append(attachmentIDs, att.ID)
	}
	if err := d.client.CreateTaskArtifactComment(uploadCtx, token, task.WorkspaceID, task.AgentID, task.ID, task.IssueID, task.TriggerCommentID, content, attachmentIDs); err != nil {
		taskLog.Warn("create task artifact comment failed", "error", err, "issue_id", task.IssueID, "task_id", task.ID)
		return
	}
	taskLog.Info("task artifacts uploaded and linked to issue comment",
		"issue_id", task.IssueID,
		"task_id", task.ID,
		"count", len(uploaded),
	)
}

func taskArtifactCommentContent(attachments []uploadedTaskArtifact) string {
	var b strings.Builder
	b.WriteString("Agent 产物已自动收拢到平台。\n\n")
	for _, att := range attachments {
		label := escapeMarkdownLabel(firstNonEmptyString(att.Filename, att.ID))
		href := firstNonEmptyString(att.MarkdownURL, att.DownloadURL, "/api/attachments/"+att.ID+"/download")
		fmt.Fprintf(&b, "!file[%s](%s)\n\n", label, href)
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func escapeMarkdownLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `]`, `\]`)
	return value
}

func (c *Client) UploadTaskArtifact(ctx context.Context, token, workspaceID, agentID, taskID, issueID string, artifact taskMarkdownArtifact) (uploadedTaskArtifact, error) {
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		return uploadedTaskArtifact{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(artifact.Path))
	if err != nil {
		return uploadedTaskArtifact{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return uploadedTaskArtifact{}, fmt.Errorf("write form file: %w", err)
	}
	if issueID != "" {
		if err := writer.WriteField("issue_id", issueID); err != nil {
			return uploadedTaskArtifact{}, fmt.Errorf("write issue_id field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return uploadedTaskArtifact{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/upload-file", &body)
	if err != nil {
		return uploadedTaskArtifact{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setTaskHeaders(req, token, workspaceID, agentID, taskID)

	resp, err := c.client.Do(req)
	if err != nil {
		return uploadedTaskArtifact{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return uploadedTaskArtifact{}, &requestError{Method: http.MethodPost, Path: "/api/upload-file", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var out uploadedTaskArtifact
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uploadedTaskArtifact{}, fmt.Errorf("decode upload response: %w", err)
	}
	return out, nil
}

func (c *Client) CreateTaskArtifactComment(ctx context.Context, token, workspaceID, agentID, taskID, issueID, parentID, content string, attachmentIDs []string) error {
	body := map[string]any{
		"content":        content,
		"attachment_ids": attachmentIDs,
	}
	if parentID != "" {
		body["parent_id"] = parentID
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	path := "/api/issues/" + issueID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setTaskHeaders(req, token, workspaceID, agentID, taskID)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) setTaskHeaders(req *http.Request, token, workspaceID, agentID, taskID string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}
	if taskID != "" {
		req.Header.Set("X-Task-ID", taskID)
	}
	c.setIdentityHeaders(req)
}
