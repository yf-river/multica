package handler

// Workspace knowledge base proxy.
//
// OpenViking is the source of truth; Multica only scopes requests to a workspace
// and keeps its credentials server-side. The browser never talks to OpenViking
// directly. This first slice is read-only (status, browse, search); ingestion
// and deletion follow on the same client.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	openVikingDefaultURL     = "http://127.0.0.1:1933"
	openVikingRequestTimeout = 15 * time.Second
)

type knowledgeClient struct {
	baseURL string
	apiKey  string
}

func newKnowledgeClient() *knowledgeClient {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_OPENVIKING_URL")), "/")
	if base == "" {
		base = openVikingDefaultURL
	}
	return &knowledgeClient{baseURL: base, apiKey: strings.TrimSpace(os.Getenv("MULTICA_OPENVIKING_API_KEY"))}
}

func (c *knowledgeClient) do(ctx context.Context, method, path string, query url.Values, body any, workspaceID string) (json.RawMessage, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Dev auth (the current deployment) trusts identity headers; api-key mode
	// additionally needs the workspace-scoped key.
	req.Header.Set("X-OpenViking-User", workspaceID)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	client := &http.Client{Timeout: openVikingRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("openviking returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.RawMessage(raw), resp.StatusCode, nil
}

// GetKnowledgeStatus reports whether the workspace knowledge base is reachable.
// GET /api/workspaces/{id}/knowledge
func (h *Handler) GetKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	client := newKnowledgeClient()
	raw, _, err := client.do(r.Context(), http.MethodGet, "/health", nil, nil, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true, "healthy": false, "base_url": client.baseURL, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "healthy": true, "base_url": client.baseURL, "health": raw,
	})
}

// ListKnowledgeResources lists a workspace knowledge directory.
// GET /api/workspaces/{id}/knowledge/resources?uri=...
func (h *Handler) ListKnowledgeResources(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	if uri == "" {
		uri = "viking://resources/workspaces/" + workspaceID
	}
	if !strings.HasPrefix(uri, "viking://resources/workspaces/"+workspaceID) {
		writeError(w, http.StatusForbidden, "knowledge path is outside this workspace")
		return
	}
	client := newKnowledgeClient()
	root := "viking://resources/workspaces/" + workspaceID
	// The workspace namespace is created on first access; mkdir is idempotent and
	// a "already exists" failure must not block browsing.
	_, _, _ = client.do(r.Context(), http.MethodPost, "/api/v1/fs/mkdir", nil, map[string]any{"uri": root}, workspaceID)
	_ = root
	raw, _, err := client.do(r.Context(), http.MethodGet, "/api/v1/fs/ls", url.Values{"uri": {uri}, "output": {"agent"}}, nil, workspaceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uri": uri, "result": raw})
}

// SearchKnowledge runs a semantic search scoped to the workspace namespace.
// POST /api/workspaces/{id}/knowledge/search {"query": "...", "limit": 10}
func (h *Handler) SearchKnowledge(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
		Path  string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if body.Limit <= 0 || body.Limit > 50 {
		body.Limit = 10
	}
	target := "viking://resources/workspaces/" + workspaceID
	if p := strings.TrimSpace(body.Path); p != "" {
		if !strings.HasPrefix(p, target) {
			writeError(w, http.StatusForbidden, "knowledge path is outside this workspace")
			return
		}
		target = p
	}
	client := newKnowledgeClient()
	payload := map[string]any{"query": query, "limit": body.Limit, "target_uri": target, "include_provenance": true}
	raw, _, err := client.do(r.Context(), http.MethodPost, "/api/v1/search/find", nil, payload, workspaceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "target_uri": target, "result": raw})
}

// ReadKnowledge reads one file from the workspace knowledge base.
// GET /api/workspaces/{id}/knowledge/read?uri=...&offset=0&limit=-1
func (h *Handler) ReadKnowledge(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	prefix := "viking://resources/workspaces/" + workspaceID
	if uri == "" || !strings.HasPrefix(uri, prefix) {
		writeError(w, http.StatusForbidden, "knowledge path is outside this workspace")
		return
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	limit := -1
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed != 0 && parsed <= 2000 {
			limit = parsed
		}
	}
	client := newKnowledgeClient()
	raw, _, err := client.do(r.Context(), http.MethodGet, "/api/v1/content/read", url.Values{
		"uri": {uri}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)},
	}, nil, workspaceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uri": uri, "result": raw})
}

// UploadKnowledgeResource ingests one file into the workspace knowledge base.
// POST /api/workspaces/{id}/knowledge/resources (multipart field "file")
func (h *Handler) UploadKnowledgeResource(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 10<<20+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	if len(data) > 10<<20 {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds 10 MiB")
		return
	}
	name := filepath.Base(header.Filename)
	client := newKnowledgeClient()
	uploaded, _, err := client.upload(r.Context(), workspaceID, name, data)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var envelope struct {
		Result struct {
			TempFileID string `json:"temp_file_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(uploaded, &envelope); err != nil || envelope.Result.TempFileID == "" {
		writeError(w, http.StatusBadGateway, "openviking did not return a temp_file_id")
		return
	}
	root := "viking://resources/workspaces/" + workspaceID
	payload := map[string]any{
		"temp_file_id": envelope.Result.TempFileID,
		"parent":       root,
		"reason":       "Uploaded from Multica knowledge",
		"wait":         false,
	}
	added, _, err := client.do(r.Context(), http.MethodPost, "/api/v1/resources", nil, payload, workspaceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filename": name, "result": added})
}

// upload posts one file to OpenViking's temp-upload endpoint.
func (c *knowledgeClient) upload(ctx context.Context, workspaceID, filename string, data []byte) (json.RawMessage, int, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, 0, err
	}
	if _, err = part.Write(data); err != nil {
		return nil, 0, err
	}
	if err = writer.Close(); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/resources/temp_upload", &body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-OpenViking-User", workspaceID)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := (&http.Client{Timeout: openVikingRequestTimeout}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("openviking upload returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.RawMessage(raw), resp.StatusCode, nil
}

// DeleteKnowledgeResource removes one file or directory from the workspace
// knowledge base. Workspace admins own destructive curation.
// DELETE /api/workspaces/{id}/knowledge/resources?uri=...&recursive=false
func (h *Handler) DeleteKnowledgeResource(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if member.Role != "owner" && member.Role != "admin" {
		writeError(w, http.StatusForbidden, "only workspace admins can delete knowledge resources")
		return
	}
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	prefix := "viking://resources/workspaces/" + workspaceID
	if uri == "" || !strings.HasPrefix(uri, prefix) {
		writeError(w, http.StatusForbidden, "knowledge path is outside this workspace")
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	client := newKnowledgeClient()
	raw, _, err := client.do(r.Context(), http.MethodDelete, "/api/v1/fs", url.Values{
		"uri": {uri}, "recursive": {strconv.FormatBool(recursive)},
	}, nil, workspaceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uri": uri, "result": raw})
}
