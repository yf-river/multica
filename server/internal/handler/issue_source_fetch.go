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
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AutoFetch {
		fetched, err := h.autoFetchIssueSource(r.Context(), userID, issue, req)
		if err != nil {
			req.Status = "fetch_failed"
			req.Error = err.Error()
			if req.Provider == "" {
				req.Provider = "tapd"
			}
			metadata := parseIssueMetadata(issue.Metadata)
			req.WorkspaceID = firstNonEmpty(req.WorkspaceID, stringFromMetadata(metadata, "tapd_workspace_id"), stringFromMetadata(metadata, "tapd_workspace"))
			req.ResourceType = firstNonEmpty(req.ResourceType, stringFromMetadata(metadata, "tapd_resource_type"))
			req.ResourceID = firstNonEmpty(req.ResourceID, stringFromMetadata(metadata, "tapd_resource_id"), stringFromMetadata(metadata, "tapd_wiki_id"))
			req.URL = firstNonEmpty(req.URL, stringFromMetadata(metadata, "source_url"))
		} else {
			req = fetched
		}
	}
	normalized, ok := normalizeSourceFetchRequest(w, req)
	if !ok {
		return
	}

	metadataUpdates := sourceFetchMetadata(normalized)
	existing := parseIssueMetadata(issue.Metadata)
	newKeyCount := 0
	for key := range metadataUpdates {
		if _, present := existing[key]; !present {
			newKeyCount++
		}
	}
	if normalized.Status == "fetched" {
		if _, present := existing["source_fetch_error"]; present {
			// Deleting an existing stale error does not add a key.
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
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	var updated db.Issue = issue
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
		if _, err := qtx.DeleteIssueMetadataKey(r.Context(), db.DeleteIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         "source_fetch_error",
		}); err == nil {
			latest, _ := qtx.GetIssue(r.Context(), issue.ID)
			if latest.ID.Valid {
				updated = latest
			}
		}
	}

	traceResponse := map[string]any(nil)
	if taskID := strings.TrimSpace(r.Header.Get("X-Task-ID")); taskID != "" {
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
		traceMeta, _ := json.Marshal(map[string]any{
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
			writeError(w, http.StatusInternalServerError, "failed to record source fetch trace")
			return
		}
		traceResponse = map[string]any{
			"id":         uuidToString(ev.ID),
			"event_type": ev.EventType,
			"status":     ev.Status,
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit source fetch record")
		return
	}

	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	metadata := parseIssueMetadata(updated.Metadata)
	h.publish(protocol.EventIssueMetadataChanged, workspaceID, actorType, actorID, map[string]any{
		"issue_id": uuidToString(updated.ID),
		"metadata": metadata,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"metadata":    metadata,
		"trace_event": traceResponse,
	})
}

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

func (h *Handler) autoFetchIssueSource(ctx context.Context, userID string, issue db.Issue, req RecordIssueSourceFetchRequest) (RecordIssueSourceFetchRequest, error) {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(req.Provider, "tapd")))
	if provider != externalCredentialProviderTAPD {
		return req, fmt.Errorf("auto_fetch currently supports tapd only")
	}
	metadata := parseIssueMetadata(issue.Metadata)
	workspaceID := firstNonEmpty(req.WorkspaceID, stringFromMetadata(metadata, "tapd_workspace_id"), stringFromMetadata(metadata, "tapd_workspace"))
	resourceType := firstNonEmpty(req.ResourceType, stringFromMetadata(metadata, "tapd_resource_type"))
	resourceID := firstNonEmpty(req.ResourceID, stringFromMetadata(metadata, "tapd_resource_id"), stringFromMetadata(metadata, "tapd_wiki_id"))
	sourceURL := firstNonEmpty(req.URL, stringFromMetadata(metadata, "source_url"))
	if (resourceType == "" || resourceType == "tapd_resource") && strings.Contains(sourceURL, "/markdown_wikis/") {
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
	token := h.resolveExternalCredentialToken(profile)
	if token == "" {
		return req, fmt.Errorf("TAPD credential profile has no resolvable token")
	}

	started := time.Now()
	doc, err := fetchTAPDSourceDocument(ctx, token, workspaceID, resourceType, resourceID)
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

type tapdSourceDocument struct {
	Title       string
	Summary     string
	BodyExcerpt string
	Version     string
}

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
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

func tapdSourceEndpoint(resourceType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "", "markdown_wiki", "wiki", "tapd_wiki":
		return "tapd_wikis", true
	case "story", "stories":
		return "stories", true
	case "task", "tasks":
		return "tasks", true
	default:
		return "", false
	}
}

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

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

func compactPlainText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, " ")
	value = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

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

func stringFromMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

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
