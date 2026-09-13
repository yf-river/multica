// 职责定位：记录 issue 的「来源抓取」（source fetch）事实——agent/服务从外部系统（当前支持
// TAPD）抓取原始材料后回写元数据与不可变快照，供 issue 详情展示与溯源。也处理 TAPD 链接的
// 自动补抓。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RecordIssueSourceFetchRequest 是一次来源抓取结果的记录请求。
//
// 几个非显然字段的语义：
//   - Provider：外部系统类型（tapd / gongfeng），缺省按 tapd 处理；
//   - FetchProvider：本次抓取实际走的通道，缺省派生为 <provider>_mcp；
//   - Status：抓取结论，fetched 或 fetch_failed；
//   - WorkspaceID / ResourceType / ResourceID：定位「外部资源」自身的三元组——对 TAPD 指 TAPD
//     侧的 workspace、story / markdown_wiki、ID，与 issue 所在的工作区是两个概念；
//   - AutoFetch：为 true 时由服务端用当前用户的 TAPD 凭证代抓（当前仅支持 tapd），成功后本
//     请求里其它抓取字段会被抓取结果覆盖。
type RecordIssueSourceFetchRequest struct {
	Provider      string `json:"provider"`
	FetchProvider string `json:"fetch_provider"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	WorkspaceID   string `json:"workspace_id"`
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	BodyExcerpt   string `json:"body_excerpt"`
	Version       string `json:"version"`
	Error         string `json:"error"`
	DurationMs    int64  `json:"duration_ms"`
	AutoFetch     bool   `json:"auto_fetch"`
}

// tapdSourceRef 是从 TAPD URL 里解析出的资源引用。
type tapdSourceRef struct {
	WorkspaceID  string
	ResourceType string
	ResourceID   string
	URL          string
}

// parseTAPDSourceURL 识别并解析 TAPD 的 story / markdown_wiki 页面 URL 成结构化引用。
func parseTAPDSourceURL(value string) (tapdSourceRef, bool) {
	parsed, err := url.Parse(strings.Join(strings.Fields(value), ""))
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Host), "tapd.cn") {
		return tapdSourceRef{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "tapd_fe" && parts[2] == "story" {
		id := strings.TrimSpace(parsed.Query().Get("dialog_preview_id"))
		id = strings.TrimPrefix(id, "story_")
		if id != "" {
			return tapdSourceRef{WorkspaceID: parts[1], ResourceType: "story", ResourceID: id, URL: parsed.String()}, true
		}
	}
	for i, part := range parts {
		if part != "markdown_wikis" || i == 0 {
			continue
		}
		resourceID := strings.TrimSpace(parsed.Fragment)
		if resourceID == "" && i+1 < len(parts) && parts[len(parts)-1] != "show" {
			resourceID = parts[len(parts)-1]
		}
		if resourceID != "" {
			parsed.Fragment = resourceID
			return tapdSourceRef{WorkspaceID: parts[i-1], ResourceType: "markdown_wiki", ResourceID: resourceID, URL: parsed.String()}, true
		}
	}
	return tapdSourceRef{}, false
}

// RecordIssueSourceFetch 处理 POST .../issues/:id/source-fetch：记录一次来源抓取（agent / 服务从
// 外部系统抓取原始材料）的结果——把 provider/URL/摘要等元数据写进 issue.metadata，并在调用方
// 通过 X-Task-ID 声明 agent 任务时，以幂等键沉淀一条 task trace 事件。AutoFetch 时服务端代抓。
func (h *Handler) RecordIssueSourceFetch(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req RecordIssueSourceFetchRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	existing := util.JSONObjectOrEmpty(issue.Metadata)
	// 记录客户端原始请求快照，用于随后计算幂等指纹：auto_fetch 会改写 req，指纹必须基于客户端
	// 真正提交的载荷，否则同一请求重发会算出不同指纹、绕过幂等。
	requestFingerprint := req
	if req.AutoFetch {
		fetched, err := h.autoFetchTAPDSource(r.Context(), userID, req, existing)
		if err != nil {
			req.Status = "fetch_failed"
			req.Error = err.Error()
			if req.Provider == "" {
				req.Provider = "tapd"
			}
			req.WorkspaceID = firstNonEmpty(req.WorkspaceID, stringFromMetadata(existing, "tapd_workspace_id"))
			req.ResourceType = firstNonEmpty(req.ResourceType, stringFromMetadata(existing, "tapd_resource_type"))
			req.ResourceID = firstNonEmpty(req.ResourceID, stringFromMetadata(existing, "tapd_resource_id"))
			req.URL = firstNonEmpty(req.URL, stringFromMetadata(existing, "source_url"))
		} else {
			req = fetched
		}
	}
	normalized, ok := normalizeSourceFetchRequest(w, req)
	if !ok {
		return
	}

	metadataUpdates := sourceFetchMetadata(normalized)
	// metadata 会整体作为事件 payload 广播，须受总 key 数上限约束：统计本次将要新增的 key 数，
	// 与现有 key 相加超过上限就直接拒绝请求。
	newKeyCount := 0
	for key := range metadataUpdates {
		if _, present := existing[key]; !present {
			newKeyCount++
		}
	}
	if len(existing)+newKeyCount > maxIssueMetadataKeys {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("metadata cannot exceed %d keys", maxIssueMetadataKeys))
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin source fetch transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	updated := issue
	for key, value := range metadataUpdates {
		buf, err := json.Marshal(value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode source fetch metadata")
			return
		}
		updated, err = qtx.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         key,
			Value:       buf,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record source fetch metadata")
			return
		}
	}
	if normalized.Status == "fetched" {
		// 抓取成功：清掉上次失败残留的 source_fetch_error 键，并重读最新行——后续 metadata
		// 变更事件要基于清过脏值后的权威状态构造。
		if _, err := qtx.DeleteIssueMetadataKey(r.Context(), db.DeleteIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         "source_fetch_error",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear source fetch error")
			return
		}
		latest, err := qtx.GetIssue(r.Context(), issue.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload source fetch metadata")
			return
		}
		updated = latest
	}

	// 只有调用方通过 X-Task-ID 声明这次抓取属于某条 agent 任务时，才把抓取沉淀为 task trace
	// 事件（ID 用幂等键，重复提交返回 409）；普通来源的写入不产生 trace。
	traceResponse := map[string]any(nil)
	if taskID := strings.TrimSpace(r.Header.Get("X-Task-ID")); taskID != "" {
		idempotencyKey, ok := requireIdempotencyKey(w, r)
		if !ok {
			return
		}
		taskUUID, err := util.ParseUUID(taskID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "X-Task-ID must be a UUID")
			return
		}
		task, err := qtx.GetAgentTask(r.Context(), taskUUID)
		if err != nil || !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "X-Task-ID must belong to this issue")
			return
		}
		requestHash, err := hashRequestFingerprint(requestFingerprint)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fingerprint source fetch request")
			return
		}
		traceMeta, _ := json.Marshal(map[string]any{
			"request_hash":   requestHash,
			"provider":       normalized.Provider,
			"fetch_provider": normalized.FetchProvider,
			"workspace_id":   normalized.WorkspaceID,
			"resource_type":  normalized.ResourceType,
			"resource_id":    normalized.ResourceID,
			"title":          normalized.Title,
			"url":            normalized.URL,
			"version":        normalized.Version,
			"body_excerpt":   normalized.BodyExcerpt,
		})
		duration := pgtype.Int8{}
		if normalized.DurationMs > 0 {
			duration = pgtype.Int8{Int64: normalized.DurationMs, Valid: true}
		}
		eventName := "Source fetch recorded"
		failureReason := ""
		errorType := ""
		if normalized.Status == "fetch_failed" {
			eventName = "Source fetch failed"
			failureReason = normalized.Error
			errorType = "source_fetch_failed"
		}
		ev, err := qtx.CreateTaskTraceEvent(r.Context(), db.CreateTaskTraceEventParams{
			ID:            idempotencyKey,
			WorkspaceID:   issue.WorkspaceID,
			TaskID:        task.ID,
			IssueID:       issue.ID,
			AgentID:       task.AgentID,
			RuntimeID:     task.RuntimeID,
			ProjectID:     issue.ProjectID,
			Source:        "issue",
			EventType:     "source.fetch",
			EventName:     eventName,
			Status:        normalized.Status,
			Attempt:       task.Attempt,
			DurationMs:    duration,
			FailureReason: failureReason,
			ErrorType:     errorType,
			Metadata:      traceMeta,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusConflict, "idempotency key already used for a different source fetch")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to record source fetch trace")
			return
		}
		traceResponse = map[string]any{
			"id":         uuidToString(ev.ID),
			"event_type": ev.EventType,
			"status":     ev.Status,
		}
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	metadata := parseIssueMetadata(updated.Metadata)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventIssueMetadataChanged,
		IdempotencyKey: "issue_source_fetch:" + uuidToString(updated.ID) + ":" + fmt.Sprint(updated.Revision),
		StreamKey:      "issue:" + uuidToString(updated.ID),
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"issue_id": uuidToString(updated.ID), "metadata": metadata, "issue_revision": updated.Revision},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record source fetch event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit source fetch record")
		return
	}

	// 事务提交之后才广播 metadata 变更事件，让其它端刷新 issue 详情。广播属尽力而为——此后任何
	// 一步出错都不能回滚已落库的抓取事实。
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, map[string]any{
		"metadata":    metadata,
		"trace_event": traceResponse,
	})
}

// normalizeSourceFetchRequest 归一化并校验抓取记录请求：小写化 provider、补缺省值、按 status 强制
// 必填项、逐字段限长。非法输入直接写 4xx 并返回 ok=false。
func normalizeSourceFetchRequest(w http.ResponseWriter, req RecordIssueSourceFetchRequest) (RecordIssueSourceFetchRequest, bool) {
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Provider == "" {
		req.Provider = "tapd"
	}
	if req.Provider != "tapd" && req.Provider != "gongfeng" {
		writeError(w, http.StatusBadRequest, "provider must be tapd or gongfeng")
		return req, false
	}
	req.FetchProvider = strings.TrimSpace(req.FetchProvider)
	if req.FetchProvider == "" {
		req.FetchProvider = req.Provider + "_mcp"
	}
	req.Status = strings.TrimSpace(req.Status)
	if req.Status != "fetched" && req.Status != "fetch_failed" {
		writeError(w, http.StatusBadRequest, "status must be fetched or fetch_failed")
		return req, false
	}
	req.URL = strings.TrimSpace(req.URL)
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	req.ResourceID = strings.TrimSpace(req.ResourceID)
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	req.BodyExcerpt = strings.TrimSpace(req.BodyExcerpt)
	req.Version = strings.TrimSpace(req.Version)
	req.Error = strings.TrimSpace(req.Error)
	if req.Status == "fetched" && req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required when status is fetched")
		return req, false
	}
	if req.Status == "fetch_failed" && req.Error == "" {
		writeError(w, http.StatusBadRequest, "error is required when status is fetch_failed")
		return req, false
	}
	for label, value := range map[string]string{
		"url":           req.URL,
		"workspace_id":  req.WorkspaceID,
		"resource_type": req.ResourceType,
		"resource_id":   req.ResourceID,
		"title":         req.Title,
		"version":       req.Version,
		"error":         req.Error,
	} {
		if len(value) > 2048 {
			writeError(w, http.StatusBadRequest, label+" is too long")
			return req, false
		}
	}
	if len(req.Summary) > 2000 {
		writeError(w, http.StatusBadRequest, "summary is too long")
		return req, false
	}
	if len(req.BodyExcerpt) > 4000 {
		writeError(w, http.StatusBadRequest, "body_excerpt is too long")
		return req, false
	}
	if req.DurationMs < 0 {
		writeError(w, http.StatusBadRequest, "duration_ms must be non-negative")
		return req, false
	}
	return req, true
}

// autoFetchTAPDSource 用当前用户账户级的 TAPD 凭证替客户端代抓来源文档：先从请求或 issue 既有
// metadata 里解析出 TAPD 资源三元组（必要时用 URL 反推），再取默认凭证 profile、解析 token 抓取。
// 返回填充好抓取字段的请求；任何一步失败都以带可读原因的错误返回（上层会据此记 fetch_failed）。
func (h *Handler) autoFetchTAPDSource(ctx context.Context, userID string, req RecordIssueSourceFetchRequest, metadata map[string]any) (RecordIssueSourceFetchRequest, error) {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(req.Provider, "tapd")))
	if provider != externalCredentialProviderTAPD {
		return req, fmt.Errorf("auto_fetch currently supports tapd only")
	}
	workspaceID := firstNonEmpty(req.WorkspaceID, stringFromMetadata(metadata, "tapd_workspace_id"))
	resourceType := firstNonEmpty(req.ResourceType, stringFromMetadata(metadata, "tapd_resource_type"))
	resourceID := firstNonEmpty(req.ResourceID, stringFromMetadata(metadata, "tapd_resource_id"))
	sourceURL := firstNonEmpty(req.URL, stringFromMetadata(metadata, "source_url"))
	if ref, ok := parseTAPDSourceURL(sourceURL); ok {
		sourceURL = ref.URL
		workspaceID = firstNonEmpty(workspaceID, ref.WorkspaceID)
		resourceID = firstNonEmpty(resourceID, ref.ResourceID)
		if resourceType == "" {
			resourceType = ref.ResourceType
		}
	}
	if resourceType == "" && strings.Contains(sourceURL, "/markdown_wikis/") {
		resourceType = "markdown_wiki"
	}
	if workspaceID == "" || resourceID == "" {
		return req, fmt.Errorf("tapd auto_fetch requires workspace_id and resource_id")
	}

	profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
		UserID:   parseUUID(userID),
		Provider: externalCredentialProviderTAPD,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return req, fmt.Errorf("no account-level TAPD credential profile for current user")
	}
	if err != nil {
		return req, fmt.Errorf("load TAPD credential profile: %w", err)
	}
	if strings.EqualFold(profile.Status, "disabled") {
		return req, fmt.Errorf("TAPD credential profile is disabled")
	}
	token, err := h.resolveExternalCredentialToken(profile)
	if err != nil {
		return req, fmt.Errorf("resolve TAPD credential token: %w", err)
	}
	if token == "" {
		return req, fmt.Errorf("TAPD credential profile has no resolvable token")
	}

	started := time.Now()
	doc, err := fetchTAPDSourceDocumentWithRetry(ctx, token, workspaceID, resourceType, resourceID)
	durationMs := time.Since(started).Milliseconds()
	if err != nil {
		return req, err
	}
	req.Provider = externalCredentialProviderTAPD
	req.FetchProvider = "tapd_mcp"
	req.Status = "fetched"
	req.URL = sourceURL
	req.WorkspaceID = workspaceID
	req.ResourceType = firstNonEmpty(resourceType, "markdown_wiki")
	req.ResourceID = resourceID
	req.Title = doc.Title
	req.Summary = doc.Summary
	req.BodyExcerpt = doc.BodyExcerpt
	req.Version = doc.Version
	req.DurationMs = durationMs
	return req, nil
}

// tapdSourceDocument 是 TAPD 源文档在本地加工后的展示形态：标题 + 一段摘要 + 正文摘录 + 版本戳。
type tapdSourceDocument struct {
	Title       string
	Summary     string
	BodyExcerpt string
	Version     string
}

// tapdAutoFetchMaxAttempts 是代抓 TAPD 的重试上限（含首次尝试）。网络类瞬时错误（见
// isTransientTAPDAutoFetchError）才会触发重试，指数式退避等待。
const tapdAutoFetchMaxAttempts = 3

// fetchTAPDSourceDocumentWithRetry 带重试地抓取 TAPD 源文档：遇到瞬时错误（网络抖动、连接被
// 重置、上游限流/5xx 等）按退避重试，最多 tapdAutoFetchMaxAttempts 次；上下文取消则立即返回
// 取消错误。重试耗尽后返回最后一次的原始错误。
func fetchTAPDSourceDocumentWithRetry(ctx context.Context, token, workspaceID, resourceType, resourceID string) (tapdSourceDocument, error) {
	var lastErr error
	for attempt := 1; attempt <= tapdAutoFetchMaxAttempts; attempt++ {
		doc, err := fetchTAPDSourceDocument(ctx, token, workspaceID, resourceType, resourceID)
		if err == nil {
			return doc, nil
		}
		lastErr = err
		if attempt == tapdAutoFetchMaxAttempts || !isTransientTAPDAutoFetchError(err) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return tapdSourceDocument{}, ctx.Err()
		case <-timer.C:
		}
	}
	return tapdSourceDocument{}, lastErr
}

// isTransientTAPDAutoFetchError 判断错误是否属「值得重试」的瞬时故障。判断方式是做错误消息的
// 关键词匹配：连接层/超时/限流/TAPD 5xx 等提示重发可能成功；TAPD 401/403（凭证或权限问题）与
// 4xx 一律不在名单里，重试多少次结果都一样。
func isTransientTAPDAutoFetchError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"eof",
		"connection reset",
		"connection refused",
		"timeout",
		"temporary",
		"tls handshake",
		"tapd auto_fetch http 429",
		"tapd auto_fetch http 500",
		"tapd auto_fetch http 502",
		"tapd auto_fetch http 503",
		"tapd auto_fetch http 504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// fetchTAPDSourceDocument 不带重试地抓取一次 TAPD 源文档：按 resource_type 选端点、以 Bearer
// token 请求对应 API，解析并校验响应（必须有标题与正文），再折叠成本地文档形态。响应体被
// LimitReader 限制在 2 MiB 以内，防止上游异常返回海量 body 撑爆内存。
func fetchTAPDSourceDocument(ctx context.Context, token, workspaceID, resourceType, resourceID string) (tapdSourceDocument, error) {
	endpoint, ok := tapdSourceEndpoint(resourceType)
	if !ok {
		return tapdSourceDocument{}, fmt.Errorf("unsupported TAPD resource_type for auto_fetch: %s", resourceType)
	}
	base := strings.TrimRight(firstNonEmpty(os.Getenv("TAPD_API_BASE_URL"), "https://api.tapd.cn"), "/")
	u, err := url.Parse(base + "/" + endpoint)
	if err != nil {
		return tapdSourceDocument{}, fmt.Errorf("invalid TAPD API base URL")
	}
	q := u.Query()
	q.Set("workspace_id", workspaceID)
	q.Set("id", resourceID)
	q.Set("page", "1")
	q.Set("limit", "1")
	q.Set("s", "mcp")
	if endpoint == "stories" || endpoint == "tasks" {
		q.Set("fields", "id,name,description,modified")
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return tapdSourceDocument{}, fmt.Errorf("build TAPD request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Via", "mcp")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return tapdSourceDocument{}, fmt.Errorf("TAPD auto_fetch request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return tapdSourceDocument{}, fmt.Errorf("TAPD credential is invalid or expired (HTTP 401)")
		case http.StatusForbidden:
			return tapdSourceDocument{}, fmt.Errorf("TAPD credential does not have permission to read this source (HTTP 403)")
		}
		return tapdSourceDocument{}, fmt.Errorf("TAPD auto_fetch HTTP %d", resp.StatusCode)
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return tapdSourceDocument{}, fmt.Errorf("decode TAPD auto_fetch response: %w", err)
	}
	doc := tapdDocumentFromPayload(payload)
	if doc.Title == "" {
		return tapdSourceDocument{}, fmt.Errorf("TAPD auto_fetch response did not contain a title")
	}
	if doc.BodyExcerpt == "" {
		return tapdSourceDocument{}, fmt.Errorf("TAPD auto_fetch response did not contain body content")
	}
	return doc, nil
}

// tapdSourceEndpoint 把本地 resource_type 映射到 TAPD API 端点名；不支持的资源类型返回 ok=false。
// markdown_wiki 落到 tapd_wikis 接口，story / task 各落其接口，空类型默认按 markdown_wiki 处理。
func tapdSourceEndpoint(resourceType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "", "markdown_wiki":
		return "tapd_wikis", true
	case "story":
		return "stories", true
	case "task":
		return "tasks", true
	default:
		return "", false
	}
}

// tapdDocumentFromPayload 从 TAPD API 返回的任意结构 JSON 中提取标题/正文/版本：先逐层剥掉
// 外层包装（data 字段、结果数组第一项），再把整棵树压平成「小写键 -> 首个非空字符串」的字典，
// 最后按多个可能的键名顺序取值。这样可容忍 TAPD 各端点字段名的差异。
func tapdDocumentFromPayload(payload any) tapdSourceDocument {
	if root, ok := payload.(map[string]any); ok {
		payload = root["data"]
	}
	if items, ok := payload.([]any); ok && len(items) > 0 {
		payload = items[0]
	}
	fields := flattenJSONStrings(payload)
	body := firstNonEmpty(fields["markdown_description"], fields["description"], fields["content"], fields["body"])
	body = compactPlainText(body)
	return tapdSourceDocument{
		Title:       firstNonEmpty(fields["name"], fields["title"]),
		Summary:     truncateRunes(body, 500),
		BodyExcerpt: truncateRunes(body, 2000),
		Version:     firstNonEmpty(fields["modified"], fields["modified_date"], fields["updated"], fields["updated_at"], fields["version"]),
	}
}

// flattenJSONStrings 深度遍历任意 JSON 值，把树中所有「键 -> 非空字符串」对收进小写键字典。
// 深层的标量/对象字段因此能被 tapdDocumentFromPayload 用统一键名取到，无需知道响应精确形状。
func flattenJSONStrings(value any) map[string]string {
	out := map[string]string{}
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case map[string]any:
			for key, child := range typed {
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					out[strings.ToLower(key)] = strings.TrimSpace(text)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

// htmlTagPattern 匹配尖括号 HTML/XML 标签，用于剥掉 TAPD 正文里的富文本标记。
var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

// compactPlainText 把带 HTML 标记的正文压成一段无标签、无多余空白的纯文本：标签换成空格、
// 常见 HTML 实体还原成字符，再按空白折叠。供展示用的摘要/摘录需要的是干净文本而不是原始 HTML。
func compactPlainText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, " ")
	value = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

// truncateRunes 按「字符数」截断文本到 limit 以内（超出直接切掉，不加省略号），空串或 limit<=0
// 返回空串。摘要/摘录的存储有长度上限，按 rune 计数避免把多字节中文从中间切开。
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// stringFromMetadata 从 issue.metadata 里读指定 key 的字符串值（类型不符或缺键都返回空串）。
func stringFromMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// sourceFetchMetadata 把归一化后的抓取结果展开成要写进 issue.metadata 的一组键值。provider/
// status/观测时间必写；其余字段只在非空时写，避免用空串污染 metadata（source_fetch_error 只在
// fetch_failed 时有值）。
func sourceFetchMetadata(req RecordIssueSourceFetchRequest) map[string]any {
	out := map[string]any{
		"source_fetch_provider":    req.FetchProvider,
		"source_fetch_status":      req.Status,
		"source_fetch_observed_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	set := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	set("source_fetch_url", req.URL)
	set("source_fetch_workspace_id", req.WorkspaceID)
	set("source_fetch_resource_type", req.ResourceType)
	set("source_fetch_resource_id", req.ResourceID)
	set("source_fetch_title", req.Title)
	set("source_fetch_summary", req.Summary)
	set("source_fetch_body_excerpt", req.BodyExcerpt)
	set("source_fetch_version", req.Version)
	set("source_fetch_error", req.Error)
	if req.DurationMs > 0 {
		out["source_fetch_duration_ms"] = req.DurationMs
	}
	return out
}
