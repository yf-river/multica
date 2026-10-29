package handler

// 知识内容由 OpenViking 管理；这些端点服务现有 CLI，Studio 使用原生接口。

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/openviking"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (h *Handler) knowledgeUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := h.workspaceMember(w, r, h.resolveWorkspaceID(r)); !ok {
		return "", false
	}
	id := r.Header.Get("X-Agent-ID")
	if id == "" {
		return "default", true
	}
	agent, err := h.Queries.GetAgent(r.Context(), parseUUID(id))
	if err != nil {
		writeError(w, 404, "Agent not found")
		return "", false
	}
	var env map[string]string
	if len(agent.CustomEnv) > 0 {
		if err = json.Unmarshal(agent.CustomEnv, &env); err != nil {
			writeError(w, 500, "invalid Agent environment")
			return "", false
		}
	}
	user, err := openviking.User(id, env["MULTICA_OPENVIKING_USER"])
	if err != nil {
		writeError(w, 400, err.Error())
		return "", false
	}
	return user, true
}

func knowledgePathAllowed(r *http.Request, user, uri string) bool {
	if r.Header.Get("X-Agent-ID") == "" {
		return strings.HasPrefix(uri, "viking://")
	}
	for _, root := range []string{"viking://resources", "viking://agent/skills", "viking://user/" + user} {
		if uri == root || strings.HasPrefix(uri, root+"/") {
			return true
		}
	}
	return false
}

func (h *Handler) GetKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.knowledgeUser(w, r); !ok {
		return
	}
	c := openviking.FromEnv()
	err := c.Health(r.Context())
	sync := openviking.Status(filepath.Join(os.Getenv("MULTICA_OPENVIKING_ARCHIVE_ROOT"), "runs"))
	if openviking.Enabled() {
		var pending int
		if queryErr := h.DB.QueryRow(r.Context(), `SELECT count(*) FROM openviking_event WHERE delivered_at IS NULL`).Scan(&pending); queryErr == nil {
			sync.Pending = sync.Pending - sync.PlatformPending + pending
		}
	}
	status := map[string]any{"enabled": openviking.Enabled(), "healthy": err == nil, "studio_url": "/studio/", "sync": sync}
	if err != nil {
		status["error"] = err.Error()
	}
	writeJSON(w, 200, status)
}

func (h *Handler) ListKnowledgeResources(w http.ResponseWriter, r *http.Request) {
	user, ok := h.knowledgeUser(w, r)
	if !ok {
		return
	}
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	if uri == "" {
		uri = "viking://resources"
	}
	if !knowledgePathAllowed(r, user, uri) {
		writeError(w, 403, "knowledge path is outside this Agent's scope")
		return
	}
	var result json.RawMessage
	if err := openviking.FromEnv().JSON(r.Context(), user, "GET", "/api/v1/fs/ls?uri="+url.QueryEscape(uri), nil, &result); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"uri": uri, "result": map[string]any{"result": result}})
}

func (h *Handler) SearchKnowledge(w http.ResponseWriter, r *http.Request) {
	user, ok := h.knowledgeUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
		Path  string `json:"path"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil || strings.TrimSpace(body.Query) == "" {
		writeError(w, 400, "query is required")
		return
	}
	if body.Limit <= 0 || body.Limit > 50 {
		body.Limit = 10
	}
	if body.Path == "" {
		body.Path = "viking://resources"
	}
	if !knowledgePathAllowed(r, user, body.Path) {
		writeError(w, 403, "knowledge path is outside this Agent's scope")
		return
	}
	var result json.RawMessage
	if err := openviking.FromEnv().JSON(r.Context(), user, "POST", "/api/v1/search/find", map[string]any{"query": body.Query, "limit": body.Limit, "target_uri": body.Path}, &result); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"query": body.Query, "target_uri": body.Path, "result": map[string]any{"result": result}})
}

func (h *Handler) ReadKnowledge(w http.ResponseWriter, r *http.Request) {
	user, ok := h.knowledgeUser(w, r)
	if !ok {
		return
	}
	uri := r.URL.Query().Get("uri")
	if uri == "" || !knowledgePathAllowed(r, user, uri) {
		writeError(w, 403, "knowledge path is outside this Agent's scope")
		return
	}
	query := r.URL.Query()
	query.Set("uri", uri)
	var result json.RawMessage
	if err := openviking.FromEnv().JSON(r.Context(), user, "GET", "/api/v1/content/read?"+query.Encode(), nil, &result); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"uri": uri, "result": map[string]any{"result": result}})
}

func (h *Handler) UploadKnowledgeResource(w http.ResponseWriter, r *http.Request) {
	// 原生 Studio 负责资料导入；旧客户端获得明确的入口，不再维护另一份上传实现。
	if _, ok := h.knowledgeUser(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "Upload resources in OpenViking Studio at /studio/")
}

func (h *Handler) DeleteKnowledgeResource(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.knowledgeUser(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "Manage resources in OpenViking Studio at /studio/")
}
