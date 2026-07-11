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
	"unicode"
)

const taskArtifactMaxBytes = 100 << 20 // Keep aligned with handler maxUploadSize.

var taskArtifactDirs = []string{
	"artifacts",
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

type taskArtifactCommentOptions struct {
	Summary string
}

func collectTaskMarkdownArtifacts(workDir string) ([]taskMarkdownArtifact, error) {
	return collectTaskMarkdownArtifactsSince(workDir, time.Time{})
}

func collectTaskMarkdownArtifactsSince(workDir string, minModTime time.Time) ([]taskMarkdownArtifact, error) {
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
		artifacts, err = appendTaskMarkdownArtifactsFromRoot(artifacts, root, seen, minModTime)
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].DisplayName < artifacts[j].DisplayName
	})
	return artifacts, nil
}

func collectTaskMarkdownArtifactsFromDirs(workDir, artifactDir string) ([]taskMarkdownArtifact, error) {
	return collectTaskMarkdownArtifactsFromDirsSince(workDir, artifactDir, time.Time{})
}

func collectTaskMarkdownArtifactsFromDirsSince(workDir, artifactDir string, minModTime time.Time) ([]taskMarkdownArtifact, error) {
	artifacts, err := collectTaskMarkdownArtifactsSince(workDir, minModTime)
	if err != nil {
		return nil, err
	}
	artifactDir = strings.TrimSpace(artifactDir)
	if artifactDir == "" {
		return artifacts, nil
	}
	root, err := filepath.Abs(artifactDir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if abs, err := filepath.Abs(artifact.Path); err == nil {
			seen[abs] = struct{}{}
		}
	}
	artifacts, err = appendTaskMarkdownArtifactsFromRoot(artifacts, root, seen, minModTime)
	if err != nil {
		return nil, err
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].DisplayName < artifacts[j].DisplayName
	})
	return artifacts, nil
}

func appendTaskMarkdownArtifactsFromRoot(artifacts []taskMarkdownArtifact, root string, seen map[string]struct{}, minModTime time.Time) ([]taskMarkdownArtifact, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return artifacts, nil
		}
		return nil, err
	}
	if !rootInfo.IsDir() {
		return artifacts, nil
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
		if !minModTime.IsZero() && info.ModTime().Before(minModTime) {
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
	return artifacts, nil
}

func (d *Daemon) persistFinalOutputArtifactIfNeeded(task Task, result TaskResult, workDir string, artifactDir string, taskLog *slog.Logger) taskArtifactCommentOptions {
	output := strings.TrimSpace(result.Comment)
	if !shouldPersistFinalOutputAsArtifact(task, result, output) {
		return taskArtifactCommentOptions{}
	}
	artifactContent := cleanFinalOutputArtifactContent(output)
	root := strings.TrimSpace(artifactDir)
	if root == "" && strings.TrimSpace(workDir) != "" {
		root = filepath.Join(workDir, "artifacts", "multica")
	}
	if root == "" {
		return taskArtifactCommentOptions{}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		taskLog.Warn("create final output artifact dir failed", "error", err, "artifact_dir", root)
		return taskArtifactCommentOptions{}
	}
	filename := finalOutputArtifactFilename(task)
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, []byte(artifactContent+"\n"), 0o644); err != nil {
		taskLog.Warn("write final output artifact failed", "error", err, "path", path)
		return taskArtifactCommentOptions{}
	}
	return taskArtifactCommentOptions{Summary: summarizeFinalOutputForArtifactComment(artifactContent)}
}

func shouldPersistFinalOutputAsArtifact(task Task, result TaskResult, output string) bool {
	if strings.TrimSpace(task.IssueID) == "" || strings.TrimSpace(output) == "" || result.Status != "completed" {
		return false
	}
	if task.ExecutionPolicy == nil || !isBoundedReviewStage(*task.ExecutionPolicy) || task.ExecutionPolicy.CanEditRepo {
		return false
	}
	if strings.HasPrefix(output, "# ") || strings.Contains(output, "\n# ") || strings.Contains(output, "\n## ") {
		return true
	}
	runes := []rune(output)
	return len(runes) >= 700
}

func finalOutputArtifactFilename(task Task) string {
	roleKey := "stage-result"
	if task.ExecutionPolicy != nil && strings.TrimSpace(task.ExecutionPolicy.RoleKey) != "" {
		roleKey = strings.TrimSpace(task.ExecutionPolicy.RoleKey)
	}
	roleKey = sanitizeArtifactFilename(strings.ToLower(roleKey))
	if roleKey == "" {
		roleKey = "stage-result"
	}
	return roleKey + ".md"
}

func sanitizeArtifactFilename(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-._")
}

func summarizeFinalOutputForArtifactComment(output string) string {
	content := cleanFinalOutputArtifactContent(output)
	title, summary := extractArtifactCommentTitleAndSummary(content)
	sections := extractArtifactCommentKeySections(content)
	var b strings.Builder
	if title != "" {
		b.WriteString("已上传阶段产物：")
		b.WriteString(truncateArtifactCommentSummaryLine(title))
	} else {
		b.WriteString("已上传阶段产物。")
	}
	if summary != "" {
		b.WriteString("\n\n摘要：")
		b.WriteString(truncateArtifactCommentSummaryLine(summary))
	}
	for _, section := range sections {
		if len(section.Items) == 0 {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(section.Label)
		b.WriteString("：")
		for _, item := range section.Items {
			b.WriteString("\n- ")
			b.WriteString(truncateArtifactCommentSummaryLine(item))
		}
	}
	b.WriteString("\n\n完整内容见附件。")
	return b.String()
}

func cleanFinalOutputArtifactContent(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "---" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if markdownHeadingText(lines[j]) == "" {
				continue
			}
			return strings.TrimSpace(strings.Join(lines[j:], "\n"))
		}
	}
	return output
}

func extractArtifactCommentTitleAndSummary(output string) (string, string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	titleIdx := -1
	title := ""
	for i, line := range lines {
		if heading := markdownHeadingText(line); heading != "" {
			titleIdx = i
			title = heading
			break
		}
	}
	start := 0
	if titleIdx >= 0 {
		start = titleIdx + 1
	}
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !isArtifactCommentSummaryCandidate(line) {
			continue
		}
		if table := summarizeMarkdownTable(lines, i); table != "" {
			return title, table
		}
		return title, stripMarkdownListMarker(line)
	}
	return title, ""
}

type artifactCommentKeySection struct {
	Label string
	Items []string
}

type artifactCommentSectionRule struct {
	Label    string
	Keywords []string
}

var artifactCommentSectionRules = []artifactCommentSectionRule{
	{Label: "待确认", Keywords: []string{"待确认", "需要确认", "需确认", "待澄清", "需要澄清", "澄清问题", "未决", "风险", "阻塞"}},
	{Label: "验收口径", Keywords: []string{"验收", "测试列表", "测试矩阵", "测试项", "验证项", "验证"}},
	{Label: "下一步", Keywords: []string{"下一步", "进入条件", "后续", "建议", "结论"}},
}

func extractArtifactCommentKeySections(output string) []artifactCommentKeySection {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	sections := make([]artifactCommentKeySection, len(artifactCommentSectionRules))
	for i, rule := range artifactCommentSectionRules {
		sections[i] = artifactCommentKeySection{Label: rule.Label}
	}
	sectionIndex := -1
	inCodeBlock := false
	for i, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		if heading := markdownHeadingText(line); heading != "" {
			sectionIndex = artifactCommentSectionIndex(heading)
			continue
		}
		if sectionIndex < 0 || len(sections[sectionIndex].Items) >= 3 {
			continue
		}
		if strings.HasPrefix(line, "|") && i+1 < len(lines) && isMarkdownTableSeparator(strings.TrimSpace(lines[i+1])) {
			continue
		}
		item, ok := artifactCommentKeySectionItem(line)
		if !ok || artifactCommentSectionHasItem(sections[sectionIndex], item) {
			continue
		}
		sections[sectionIndex].Items = append(sections[sectionIndex].Items, item)
	}
	result := make([]artifactCommentKeySection, 0, 2)
	for _, section := range sections {
		if len(section.Items) == 0 {
			continue
		}
		result = append(result, section)
		if len(result) >= 2 {
			break
		}
	}
	return result
}

func artifactCommentSectionIndex(heading string) int {
	heading = strings.ToLower(strings.TrimSpace(heading))
	if strings.Contains(heading, "摘要") {
		return -1
	}
	for i, rule := range artifactCommentSectionRules {
		for _, keyword := range rule.Keywords {
			if strings.Contains(heading, strings.ToLower(keyword)) {
				return i
			}
		}
	}
	return -1
}

func artifactCommentKeySectionItem(line string) (string, bool) {
	if line == "" || line == "---" || line == "..." || strings.HasPrefix(line, "```") || markdownHeadingText(line) != "" || isMarkdownTableSeparator(line) {
		return "", false
	}
	if strings.HasPrefix(line, "|") {
		cells := markdownTableCells(line)
		if len(cells) < 2 {
			return "", false
		}
		return cells[0] + "：" + cells[1], true
	}
	item := stripMarkdownListMarker(line)
	if item == "" {
		return "", false
	}
	return item, true
}

func artifactCommentSectionHasItem(section artifactCommentKeySection, item string) bool {
	for _, existing := range section.Items {
		if existing == item {
			return true
		}
	}
	return false
}

func markdownHeadingText(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return ""
	}
	hashes := 0
	for hashes < len(trimmed) && trimmed[hashes] == '#' {
		hashes++
	}
	if hashes > 6 || hashes == len(trimmed) || trimmed[hashes] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[hashes:])
}

func isArtifactCommentSummaryCandidate(line string) bool {
	if line == "" || line == "---" || line == "..." || strings.HasPrefix(line, "```") {
		return false
	}
	if markdownHeadingText(line) != "" {
		return false
	}
	if strings.HasPrefix(line, "|") {
		return true
	}
	if isMarkdownTableSeparator(line) {
		return false
	}
	return true
}

func summarizeMarkdownTable(lines []string, start int) string {
	if start+2 >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[start]), "|") || !isMarkdownTableSeparator(strings.TrimSpace(lines[start+1])) {
		return ""
	}
	parts := make([]string, 0, 3)
	for i := start + 2; i < len(lines) && len(parts) < 3; i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := markdownTableCells(line)
		if len(cells) < 2 {
			continue
		}
		parts = append(parts, cells[0]+"："+cells[1])
	}
	return strings.Join(parts, "；")
}

func markdownTableCells(line string) []string {
	raw := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(raw))
	for _, cell := range raw {
		trimmed := strings.TrimSpace(cell)
		if trimmed != "" {
			cells = append(cells, trimmed)
		}
	}
	return cells
}

func isMarkdownTableSeparator(line string) bool {
	line = strings.Trim(line, "| ")
	if line == "" {
		return false
	}
	for _, part := range strings.Split(line, "|") {
		part = strings.TrimSpace(part)
		if len(part) < 3 {
			return false
		}
		for _, r := range part {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}

func stripMarkdownListMarker(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		return strings.TrimSpace(trimmed[2:])
	}
	for i, r := range trimmed {
		if unicode.IsDigit(r) {
			continue
		}
		if i > 0 && (strings.HasPrefix(trimmed[i:], ". ") || strings.HasPrefix(trimmed[i:], ") ")) {
			return strings.TrimSpace(trimmed[i+2:])
		}
		break
	}
	return trimmed
}

func truncateArtifactCommentSummaryLine(line string) string {
	runes := []rune(strings.TrimSpace(line))
	if len(runes) <= 180 {
		return string(runes)
	}
	return string(runes[:180]) + "..."
}

func (d *Daemon) collectAndPostTaskArtifacts(ctx context.Context, task Task, workDir string, artifactDir string, minModTime time.Time, taskLog *slog.Logger, opts taskArtifactCommentOptions) {
	if task.IssueID == "" || task.WorkspaceID == "" || task.AgentID == "" || task.ID == "" {
		return
	}
	artifacts, err := collectTaskMarkdownArtifactsFromDirsSince(workDir, artifactDir, minModTime)
	if err != nil {
		taskLog.Warn("collect task artifacts failed", "error", err, "work_dir", workDir, "artifact_dir", artifactDir)
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

	content := taskArtifactCommentContent(uploaded, opts)
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

func taskArtifactCommentContent(attachments []uploadedTaskArtifact, opts taskArtifactCommentOptions) string {
	var b strings.Builder
	if summary := strings.TrimSpace(opts.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
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
