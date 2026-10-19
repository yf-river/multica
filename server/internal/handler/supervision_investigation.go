package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/investigation"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func (h *Handler) supervisionRoots(ctx context.Context, ws, batch, runtime string) ([]investigation.Root, error) {
	roots := []investigation.Root{}
	rows, err := h.DB.Query(ctx, `SELECT 'run:'||t.id::text,t.work_dir,t.id::text,'运行目录当前内容' FROM supervision_batch b
 JOIN agent_task_queue t ON t.issue_id=b.issue_id JOIN agent_runtime original ON original.id=t.runtime_id
 JOIN agent_runtime current ON current.id=$3::uuid AND original.daemon_id=current.daemon_id
 WHERE b.workspace_id=$1 AND b.id=$2 AND COALESCE(t.work_dir,'')<>'' ORDER BY t.created_at DESC LIMIT 12`, ws, batch, runtime)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var root investigation.Root
		if err = rows.Scan(&root.ID, &root.Path, &root.RunID, &root.Label); err != nil {
			rows.Close()
			return nil, err
		}
		roots = append(roots, root)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = h.DB.Query(ctx, `SELECT p.id::text,p.resource_ref,COALESCE(p.label,'关联项目目录') FROM supervision_batch b JOIN issue i ON i.id=b.issue_id AND i.workspace_id=b.workspace_id
 JOIN project_resource p ON p.project_id=i.project_id AND p.workspace_id=b.workspace_id
 JOIN agent_runtime r ON r.id=$3::uuid WHERE b.workspace_id=$1 AND b.id=$2 AND p.resource_type='local_directory' AND p.resource_ref->>'daemon_id'=r.daemon_id`, ws, batch, runtime)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var ref localDirectoryRef
		var raw []byte
		var label string
		if err = rows.Scan(&id, &raw, &label); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &ref); err != nil {
			rows.Close()
			return nil, err
		}
		roots = append(roots, investigation.Root{ID: "resource:" + id, Path: ref.LocalPath, Label: label})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = h.DB.Query(ctx, `SELECT 'resource:'||p.id::text,l.local_path,COALESCE(p.label,'项目仓库') FROM supervision_batch b JOIN issue i ON i.id=b.issue_id AND i.workspace_id=b.workspace_id
 JOIN project_resource p ON p.project_id=i.project_id AND p.workspace_id=b.workspace_id
 JOIN repository_location l ON l.workspace_id=b.workspace_id AND l.repository_id::text=p.resource_ref->>'repository_id'
 JOIN agent_runtime r ON r.id=$3::uuid AND r.daemon_id=l.daemon_id
 WHERE b.workspace_id=$1 AND b.id=$2 AND p.resource_type IN('github_repo','gongfeng_repo')`, ws, batch, runtime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var root investigation.Root
		if err = rows.Scan(&root.ID, &root.Path, &root.Label); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

type supervisionInvestigationScope struct{ Workspace, Issue, Batch, Task, Runtime string }

func (h *Handler) investigationScope(w http.ResponseWriter, r *http.Request) (supervisionInvestigationScope, bool) {
	var scope supervisionInvestigationScope
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, 403, "需要有效的监督运行身份")
		return scope, false
	}
	task, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return scope, false
	}
	var owner, agent string
	err := h.DB.QueryRow(r.Context(), `SELECT b.workspace_id::text,b.issue_id::text,b.id::text,t.id::text,t.runtime_id::text,c.owner_id::text,t.agent_id::text
 FROM supervision_batch b JOIN supervision_config c USING(workspace_id) JOIN agent_task_queue t ON t.id=b.task_id
 WHERE t.id=$1 AND t.status='running' AND b.status='running' AND c.enabled AND c.investigation_enabled AND c.agent_id=t.agent_id
 AND t.context->>'type'='quality_supervision' AND t.context->>'batch_id'=b.id::text`, task).Scan(&scope.Workspace, &scope.Issue, &scope.Batch, &scope.Task, &scope.Runtime, &owner, &agent)
	if err != nil || agent != r.Header.Get("X-Agent-ID") || owner != requestUserID(r) || scope.Workspace != h.resolveWorkspaceID(r) || !h.CanUseSupervisionAgent(r.Context(), scope.Workspace, owner, agent) {
		writeError(w, 403, "监督批次或调用权限已失效")
		return scope, false
	}
	return scope, true
}

// storeInvestigation 锁定批次后累计预算，重启或并发工具调用也不能重置预算。
func (h *Handler) storeInvestigation(ctx context.Context, scope supervisionInvestigationScope, req investigation.Request, result investigation.Result) (string, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT status='running' AND task_id=$3 FROM supervision_batch WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, scope.Workspace, scope.Batch, scope.Task).Scan(&active); err != nil || !active {
		return "", fmt.Errorf("审查运行已停止")
	}
	var count, chars int
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(length(result->>'text')),0) FROM supervision_investigation WHERE workspace_id=$1 AND batch_id=$2`, scope.Workspace, scope.Batch).Scan(&count, &chars); err != nil {
		return "", err
	}
	if count >= 40 || chars+len([]rune(result.Text)) > 240000 {
		return "", fmt.Errorf("本批次调查预算已用完")
	}
	id := uuidToString(dbid.NewV7())
	result.ID = id
	result.Kind = "investigation"
	result.ObservedAt = time.Now().UTC()
	result.Text = redact.Text(result.Text)
	result.Error = redact.Text(result.Error)
	a, _ := json.Marshal(req)
	b, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `INSERT INTO supervision_investigation(id,workspace_id,issue_id,batch_id,task_id,request,result) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, scope.Workspace, scope.Issue, scope.Batch, scope.Task, a, b); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func (h *Handler) SaveSupervisionInvestigation(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.investigationScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Request investigation.Request `json:"request"`
		Result  investigation.Result  `json:"result"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body) != nil || strings.TrimSpace(body.Request.Reason) == "" || len([]rune(body.Result.Text)) > investigation.PageSize {
		writeError(w, 400, "调查结果无效")
		return
	}
	if body.Result.Tool != "supervision_read" && body.Result.Tool != "supervision_search" && body.Result.Tool != "supervision_git" {
		writeError(w, 400, "调查工具无效")
		return
	}
	roots, err := h.supervisionRoots(r.Context(), scope.Workspace, scope.Batch, scope.Runtime)
	if err != nil {
		writeError(w, 503, "调查来源不可用")
		return
	}
	authorized := false
	for _, root := range roots {
		identity := sha256.Sum256([]byte(root.Path))
		if root.ID == body.Request.Root && body.Result.Root == root.ID && body.Result.RunID == root.RunID && body.Result.RootSHA256 == hex.EncodeToString(identity[:]) {
			body.Result.SourceLabel = root.Label
			authorized = true
			break
		}
	}
	if !authorized {
		writeError(w, 403, "调查来源不属于当前任务")
		return
	}
	id, err := h.storeInvestigation(r.Context(), scope, body.Request, body.Result)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"id": id})
}

func (h *Handler) ReadSupervisionInvestigationMaterial(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.investigationScope(w, r)
	if !ok {
		return
	}
	kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
	if kind == "index" {
		offset, e := strconv.Atoi(r.URL.Query().Get("offset"))
		reason := r.URL.Query().Get("reason")
		if e != nil || offset < 0 || strings.TrimSpace(reason) == "" {
			writeError(w, 400, "需要非负索引位置和调查原因")
			return
		}
		var body string
		var total int
		e = h.DB.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2),COALESCE((SELECT jsonb_agg(x)::text FROM (SELECT kind,source_id id,run_id,changed_at FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2 ORDER BY changed_at,kind,source_id OFFSET $3 LIMIT 20)x),'[]')`, scope.Workspace, scope.Issue, offset).Scan(&total, &body)
		if e != nil {
			writeError(w, 503, "历史索引不可用")
			return
		}
		out := investigation.Result{Kind: "investigation", Root: "materials", Tool: "supervision_material", Path: "index", Text: body, Offset: offset, End: min(offset+20, total), Length: total, Truncated: offset+20 < total}
		out.ID, e = h.storeInvestigation(r.Context(), scope, investigation.Request{Root: "materials", Path: "index", Offset: offset, Reason: reason}, out)
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if _, ok = parseUUIDOrBadRequest(w, id, "source_id"); !ok {
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		writeError(w, 400, "读取位置无效")
		return
	}
	page, err := h.Supervision.ReadEvidence(r.Context(), scope.Workspace, kind, id, offset)
	if err != nil || page.IssueID != scope.Issue {
		writeError(w, 404, "本任务材料不存在")
		return
	}
	out := investigation.Result{Kind: "investigation", Root: "materials", Tool: "supervision_material", Path: kind + "/" + id, RunID: page.RunID, Text: page.Text, Offset: page.Offset, End: page.End, Length: page.Length, Truncated: page.End < page.Length}
	if page.Deleted {
		out.Error = "原始材料已删除"
	}
	req := investigation.Request{Root: "materials", Path: out.Path, Offset: offset, Reason: r.URL.Query().Get("reason")}
	if strings.TrimSpace(req.Reason) == "" {
		writeError(w, 400, "需要调查原因")
		return
	}
	out.ID, err = h.storeInvestigation(r.Context(), scope, req, out)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
