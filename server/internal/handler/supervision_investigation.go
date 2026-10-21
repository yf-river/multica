package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/supervision"
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var identifier string
	if err = h.DB.QueryRow(ctx, `SELECT w.issue_prefix||'-'||i.number FROM supervision_batch b JOIN issue i ON i.id=b.issue_id JOIN workspace w ON w.id=b.workspace_id WHERE b.workspace_id=$1 AND b.id=$2`, ws, batch).Scan(&identifier); err != nil {
		return nil, err
	}
	for _, root := range append([]investigation.Root(nil), roots...) {
		if strings.HasPrefix(root.ID, "resource:") {
			roots = append(roots, investigation.Root{ID: "evidence:" + strings.TrimPrefix(root.ID, "resource:"), Path: filepath.Join(root.Path, ".run", "evidence"), Label: "本任务验收材料", ArtifactPrefix: identifier})
		}
	}
	return distinctSupervisionRoots(roots), nil
}

// 同一目录在多个运行及项目绑定中出现时，仅提供一份当前内容来源。
// 不合并不同验收范围，也不把同目录的不同运行误当成历史版本。
func distinctSupervisionRoots(roots []investigation.Root) []investigation.Root {
	out := make([]investigation.Root, 0, len(roots))
	positions := map[string]int{}
	for _, root := range roots {
		key := filepath.Clean(root.Path) + "\x00" + root.ArtifactPrefix
		if i, exists := positions[key]; exists {
			if strings.HasPrefix(root.ID, "resource:") && !strings.HasPrefix(out[i].ID, "resource:") {
				out[i] = root
			}
			continue
		}
		positions[key] = len(out)
		out = append(out, root)
	}
	return out
}

type supervisionInvestigationScope struct{ Workspace, Issue, Batch, Task, Runtime string }

func (h *Handler) investigationScope(w http.ResponseWriter, r *http.Request) (supervisionInvestigationScope, bool) {
	return h.supervisionRunScope(w, r, true)
}

func (h *Handler) supervisionRunScope(w http.ResponseWriter, r *http.Request, requireInvestigation bool) (supervisionInvestigationScope, bool) {
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
 WHERE t.id=$1 AND t.status='running' AND b.status='running' AND (NOT $2::boolean OR c.investigation_enabled) AND c.agent_id=t.agent_id
 AND t.context->>'type'='quality_supervision' AND t.context->>'batch_id'=b.id::text`, task, requireInvestigation).Scan(&scope.Workspace, &scope.Issue, &scope.Batch, &scope.Task, &scope.Runtime, &owner, &agent)
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
	if strings.HasPrefix(req.Root, "evidence:") && result.Tool == "supervision_read" && result.Error == "" && result.Text != "" {
		if h.Storage == nil {
			return "", fmt.Errorf("验收材料附件存储不可用")
		}
		sum := sha256.Sum256([]byte(req.Root + "\n" + req.Path + "\n" + result.SHA256 + fmt.Sprint(result.Offset, ":", result.End)))
		filename := "supervision-evidence-" + hex.EncodeToString(sum[:]) + ".txt"
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachment WHERE workspace_id=$1 AND issue_id=$2 AND filename=$3)`, scope.Workspace, scope.Issue, filename).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			data := []byte(fmt.Sprintf("审查时的验收材料片段，非完整文件\n来源：%s / %s\n原文SHA256：%s\n字符范围：%d-%d / %d\n观察时间：%s\n\n%s", result.SourceLabel, req.Path, result.SHA256, result.Offset, result.End, result.Length, result.ObservedAt.Format(time.RFC3339), result.Text))
			key := "private/supervision/" + scope.Workspace + "/" + scope.Issue + "/" + filename
			url, e := h.Storage.Upload(ctx, key, data, "text/plain; charset=utf-8", filename)
			if e != nil {
				return "", fmt.Errorf("保存验收附件失败：%w", e)
			}
			_, err = tx.Exec(ctx, `INSERT INTO attachment(id,workspace_id,issue_id,task_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES($1,$2,$3,$4,'agent',(SELECT agent_id FROM agent_task_queue WHERE id=$4),$5,$6,'text/plain',$7)`, dbid.NewV7(), scope.Workspace, scope.Issue, scope.Task, filename, url, len(data))
			if err != nil {
				return "", err
			}
		}
	}
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
			if !root.Allows(body.Request.Path, body.Result.Tool) {
				continue
			}
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
	if kind == "index" || kind == "attachments" {
		offset, e := strconv.Atoi(r.URL.Query().Get("offset"))
		reason := r.URL.Query().Get("reason")
		if e != nil || offset < 0 || strings.TrimSpace(reason) == "" {
			writeError(w, 400, "需要非负索引位置和调查原因")
			return
		}
		var body string
		var total int
		e = h.DB.QueryRow(r.Context(), `WITH sources AS (
 SELECT kind,source_id id,run_id,changed_at,'原始材料' label FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2
 UNION ALL SELECT 'attachment',a.id,a.task_id,a.created_at,a.filename FROM attachment a WHERE a.workspace_id=$1 AND (a.issue_id=$2 OR a.comment_id IN(SELECT id FROM comment WHERE issue_id=$2) OR a.task_id IN(SELECT id FROM agent_task_queue WHERE issue_id=$2))
 ) SELECT (SELECT count(*) FROM sources WHERE NOT $4::boolean OR kind='attachment'),COALESCE((SELECT jsonb_agg(x)::text FROM (SELECT * FROM sources WHERE NOT $4::boolean OR kind='attachment' ORDER BY changed_at,kind,id OFFSET $3 LIMIT 20)x),'[]')`, scope.Workspace, scope.Issue, offset, kind == "attachments").Scan(&total, &body)
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
	if kind == "attachment" {
		h.readSupervisionAttachment(w, r, scope, id, offset)
		return
	}
	page, err := h.Supervision.ReadEvidence(r.Context(), scope.Workspace, kind, id, offset)
	if err != nil || page.IssueID != scope.Issue {
		writeError(w, 404, "本任务材料不存在")
		return
	}
	out := investigation.Result{Kind: "investigation", Root: "materials", Tool: "supervision_material", Path: kind + "/" + id, RunID: page.RunID, Text: page.Text, Offset: page.Offset, End: page.End, Length: page.Length, Generation: page.Generation, SHA256: page.SHA256, Truncated: page.End < page.Length}
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

// ValidateSupervisionResult 不保存结果或推进进度，仅返回与最终提交一致的校验反馈。
func (h *Handler) ValidateSupervisionResult(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.supervisionRunScope(w, r, false)
	if !ok {
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&raw); err != nil {
		writeError(w, 400, "结果需要有效JSON对象且不超过64KiB")
		return
	}
	result, err := supervision.ParseResult(string(raw))
	if err == nil {
		err = h.Supervision.ValidateBatch(r.Context(), scope.Workspace, scope.Batch, scope.Task, result)
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"valid": true})
}

func (h *Handler) readSupervisionAttachment(w http.ResponseWriter, r *http.Request, scope supervisionInvestigationScope, id string, offset int) {
	var url, filename string
	err := h.DB.QueryRow(r.Context(), `SELECT url,filename FROM attachment a WHERE a.workspace_id=$1 AND a.id=$2 AND (a.issue_id=$3 OR a.comment_id IN(SELECT id FROM comment WHERE issue_id=$3) OR a.task_id IN(SELECT id FROM agent_task_queue WHERE issue_id=$3))`, scope.Workspace, id, scope.Issue).Scan(&url, &filename)
	if err != nil || h.Storage == nil {
		writeError(w, 404, "本任务附件不可用")
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), h.Storage.KeyFromURL(url))
	if err != nil {
		writeError(w, 404, "附件对象已不可用")
		return
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, 16*1024*1024+1))
	if err != nil || len(data) > 16*1024*1024 || !utf8.Valid(data) || bytesContainNUL(data) {
		writeError(w, 400, "仅支持16MiB以内文本附件；图片和二进制内容需要人工核验")
		return
	}
	body := []rune(redact.Text(string(data)))
	offset = min(offset, len(body))
	end := min(offset+investigation.PageSize, len(body))
	digest := sha256.Sum256(data)
	result := investigation.Result{Root: "materials", Path: "attachment/" + id, Tool: "supervision_material", SourceLabel: filename, SHA256: hex.EncodeToString(digest[:]), Text: string(body[offset:end]), Offset: offset, End: end, Length: len(body), Truncated: end < len(body)}
	reason := r.URL.Query().Get("reason")
	if strings.TrimSpace(reason) == "" {
		writeError(w, 400, "需要调查原因")
		return
	}
	result.ID, err = h.storeInvestigation(r.Context(), scope, investigation.Request{Root: "materials", Path: result.Path, Offset: offset, Reason: reason}, result)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}
func bytesContainNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
