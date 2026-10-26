package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type knowledgeImportResponse struct {
	ID          string    `json:"id"`
	SourceURL   string    `json:"source_url"`
	ResourceURI string    `json:"resource_uri"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func knowledgeImportJSON(row db.KnowledgeImport) knowledgeImportResponse {
	return knowledgeImportResponse{ID: uuidToString(row.ID), SourceURL: row.SourceUrl, ResourceURI: row.ResourceUri, Status: row.Status, Error: row.Error, CreatedAt: row.CreatedAt.Time}
}

func (h *Handler) ImportKnowledgeURL(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var body struct {
		URL       string `json:"url"`
		RequestID string `json:"request_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body) != nil {
		writeError(w, 400, "导入请求无效")
		return
	}
	u, err := parseKnowledgeURL(body.URL)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if body.RequestID == "" {
		body.RequestID = dbid.NewV7().String()
	}
	id, ok := parseUUIDOrBadRequest(w, body.RequestID, "request_id")
	if !ok {
		return
	}
	creator, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user_id")
	if !ok {
		return
	}
	row, err := h.Queries.CreateKnowledgeImport(r.Context(), db.CreateKnowledgeImportParams{ID: id, WorkspaceID: parseUUID(workspaceID), CreatedBy: creator, SourceUrl: u.String(), ResourceUri: "viking://resources/workspaces/" + workspaceID + "/url-" + body.RequestID})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, e := h.Queries.GetKnowledgeImport(r.Context(), db.GetKnowledgeImportParams{ID: id, WorkspaceID: parseUUID(workspaceID)})
		if e != nil {
			writeError(w, 409, "导入请求编号不可用")
			return
		}
		if existing.SourceUrl != u.String() {
			writeError(w, 409, "同一导入请求不能更换地址")
			return
		}
		writeJSON(w, 200, knowledgeImportJSON(existing))
		return
	}
	if err != nil {
		writeError(w, 500, "无法保存导入任务")
		return
	}
	fail := func(message string) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer cancel()
		updated, e := h.Queries.UpdateKnowledgeImport(ctx, db.UpdateKnowledgeImportParams{ID: row.ID, WorkspaceID: row.WorkspaceID, Status: "failed", Error: message})
		if e != nil {
			writeError(w, 500, "导入中断，请刷新查看状态")
			return
		}
		writeJSON(w, http.StatusAccepted, knowledgeImportJSON(updated))
	}
	data, extension, err := newKnowledgeURLFetcher().fetch(r.Context(), u.String())
	if err != nil {
		fail(err.Error())
		return
	}
	client := newKnowledgeClient()
	uploaded, _, err := client.upload(r.Context(), workspaceID, "url-"+body.RequestID+extension, data)
	if err != nil {
		fail("知识库文件上传失败，请稍后重试")
		return
	}
	var upload struct {
		Result struct {
			TempFileID string `json:"temp_file_id"`
		} `json:"result"`
	}
	if json.Unmarshal(uploaded, &upload) != nil || upload.Result.TempFileID == "" {
		fail("知识库未返回文件上传凭据")
		return
	}
	added, _, err := client.do(r.Context(), http.MethodPost, "/api/v1/resources", nil, map[string]any{
		"temp_file_id": upload.Result.TempFileID, "to": row.ResourceUri, "create_parent": true, "reason": "Imported from Multica URL", "wait": false, "directly_upload_media": false,
	}, workspaceID)
	if err != nil {
		fail("知识库未确认导入，请检查资源列表后重试")
		return
	}
	var result struct {
		Result struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.Unmarshal(added, &result) != nil || result.Result.TaskID == "" {
		fail("知识库未返回导入任务编号，请检查资源列表")
		return
	}
	row, err = h.Queries.UpdateKnowledgeImport(r.Context(), db.UpdateKnowledgeImportParams{ID: row.ID, WorkspaceID: row.WorkspaceID, Status: "indexing", UpstreamTaskID: result.Result.TaskID})
	if err != nil {
		writeError(w, 500, "导入已提交，状态保存中断，请刷新查看")
		return
	}
	writeJSON(w, http.StatusAccepted, knowledgeImportJSON(row))
}

// 状态查询只向当前工作区身份查询自己的 OpenViking task，不返回上游原始载荷。
func (h *Handler) refreshKnowledgeImport(ctx context.Context, row db.KnowledgeImport) db.KnowledgeImport {
	if row.Status != "indexing" || row.UpstreamTaskID == "" {
		return row
	}
	workspaceID := uuidToString(row.WorkspaceID)
	raw, _, err := newKnowledgeClient().do(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(row.UpstreamTaskID), nil, nil, workspaceID)
	if err != nil {
		return row
	}
	var result struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return row
	}
	status, message := "", ""
	switch result.Result.Status {
	case "completed":
		status = "completed"
	case "failed", "cancelled":
		status = "failed"
		message = "知识库处理失败，请检查来源内容后重试"
	default:
		return row
	}
	updated, err := h.Queries.UpdateKnowledgeImport(ctx, db.UpdateKnowledgeImportParams{ID: row.ID, WorkspaceID: row.WorkspaceID, Status: status, UpstreamTaskID: row.UpstreamTaskID, Error: message})
	if err == nil {
		return updated
	}
	return row
}

func (h *Handler) ListKnowledgeImports(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	if err := h.Queries.ExpireInterruptedKnowledgeImports(r.Context(), parseUUID(workspaceID)); err != nil {
		writeError(w, 500, "无法读取导入状态")
		return
	}
	rows, err := h.Queries.ListKnowledgeImports(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, 500, "无法读取导入记录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	response := make([]knowledgeImportResponse, 0, len(rows))
	for _, row := range rows {
		response = append(response, knowledgeImportJSON(h.refreshKnowledgeImport(ctx, row)))
	}
	writeJSON(w, 200, map[string]any{"imports": response})
}

func (h *Handler) GetKnowledgeImport(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "importId"), "import_id")
	if !ok {
		return
	}
	if err := h.Queries.ExpireInterruptedKnowledgeImports(r.Context(), parseUUID(workspaceID)); err != nil {
		writeError(w, 500, "无法读取导入状态")
		return
	}
	row, err := h.Queries.GetKnowledgeImport(r.Context(), db.GetKnowledgeImportParams{ID: id, WorkspaceID: parseUUID(workspaceID)})
	if err != nil {
		writeError(w, 404, "导入记录不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	writeJSON(w, 200, knowledgeImportJSON(h.refreshKnowledgeImport(ctx, row)))
}

// URI 按完整路径段校验；相似前缀、编码斜杠和路径回退都不能跨工作区。
func knowledgeURIAllowed(workspaceID, raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "viking" || u.Host != "resources" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.Contains(decoded, "\\") {
		return false
	}
	parts := strings.Split(decoded, "/")
	for _, part := range parts {
		if part == ".." || part == "." || strings.Contains(part, "%") {
			return false
		}
	}
	prefix := "/workspaces/" + workspaceID
	return decoded == prefix || strings.HasPrefix(decoded, prefix+"/")
}
