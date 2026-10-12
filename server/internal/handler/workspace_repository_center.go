package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// repositoryIdentity equates clone protocols without folding case-sensitive Git paths.
func repositoryIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		if at := strings.Index(raw, "@"); at >= 0 {
			raw = raw[at+1:]
		}
		if colon := strings.Index(raw, ":"); colon > 0 {
			raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if path == "" {
		return ""
	}
	if strings.EqualFold(u.Hostname(), "github.com") {
		path = strings.ToLower(path)
	}
	return strings.ToLower(u.Hostname()) + "/" + path
}

type repositoryProject struct {
	ID        string                    `json:"id"`
	Title     string                    `json:"title"`
	Resources []ProjectResourceResponse `json:"resources"`
}

type repositoryLocation struct {
	ProjectID   string  `json:"project_id"`
	TaskID      string  `json:"task_id"`
	RuntimeID   string  `json:"runtime_id"`
	DaemonID    string  `json:"daemon_id"`
	RuntimeName string  `json:"runtime_name"`
	Provider    string  `json:"provider"`
	WorkDir     string  `json:"work_dir"`
	Branch      string  `json:"branch"`
	Status      string  `json:"status"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

// GetWorkspaceRepositoryCenter reads confirmed project resources and task directories.
// A recorded directory is execution evidence, not a successful Git pull assertion.
func (h *Handler) GetWorkspaceRepositoryCenter(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, 404, "workspace not found")
		return
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{WorkspaceID: id})
	if err != nil {
		writeError(w, 500, "读取项目失败")
		return
	}
	ids := make([]pgtype.UUID, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	resources, err := h.Queries.ListProjectResourcesForProjects(r.Context(), ids)
	if err != nil {
		writeError(w, 500, "读取项目资源失败")
		return
	}
	out := make([]repositoryProject, 0, len(projects))
	for _, p := range projects {
		item := repositoryProject{ID: uuidToString(p.ID), Title: p.Title, Resources: []ProjectResourceResponse{}}
		for _, res := range resources {
			if res.WorkspaceID == id && res.ProjectID == p.ID {
				item.Resources = append(item.Resources, projectResourceToResponse(res))
			}
		}
		out = append(out, item)
	}
	rows, err := h.DB.Query(r.Context(), `SELECT i.project_id, t.id, COALESCE(t.runtime_id::text,''), COALESCE(rt.daemon_id,''), COALESCE(NULLIF(rt.custom_name,''), rt.name,''), COALESCE(rt.provider,''), COALESCE(NULLIF(t.durable_work_dir,''),t.work_dir,''), COALESCE(t.branch_name,''), t.status, t.started_at, t.completed_at
 FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id AND i.workspace_id=$1
 JOIN project p ON p.id=i.project_id AND p.workspace_id=$1
 LEFT JOIN agent_runtime rt ON rt.id=t.runtime_id AND rt.workspace_id=$1
 WHERE COALESCE(t.work_dir,'')<>'' ORDER BY t.created_at DESC LIMIT 200`, id)
	if err != nil {
		writeError(w, 500, "读取运行位置失败")
		return
	}
	defer rows.Close()
	locations := []repositoryLocation{}
	for rows.Next() {
		var pID, tID pgtype.UUID
		var started, completed pgtype.Timestamptz
		var x repositoryLocation
		if err := rows.Scan(&pID, &tID, &x.RuntimeID, &x.DaemonID, &x.RuntimeName, &x.Provider, &x.WorkDir, &x.Branch, &x.Status, &started, &completed); err != nil {
			writeError(w, 500, "读取运行位置失败")
			return
		}
		x.ProjectID = uuidToString(pID)
		x.TaskID = uuidToString(tID)
		if started.Valid {
			s := timestampToString(started)
			x.StartedAt = &s
		}
		if completed.Valid {
			s := timestampToString(completed)
			x.CompletedAt = &s
		}
		locations = append(locations, x)
	}
	if rows.Err() != nil {
		writeError(w, 500, "读取运行位置失败")
		return
	}
	writeJSON(w, 200, map[string]any{"repos": json.RawMessage(ws.Repos), "projects": out, "locations": locations})
}

// checkRemovedRepositoryBindings runs inside the workspace update transaction.
func checkRemovedRepositoryBindings(current, next []byte, resources []db.ProjectResource) bool {
	var before, after []workspaceRepoRef
	if json.Unmarshal(current, &before) != nil || json.Unmarshal(next, &after) != nil {
		return false
	}
	kept := map[string]bool{}
	for _, repo := range after {
		kept[repositoryIdentity(repo.URL)] = true
	}
	removed := map[string]bool{}
	for _, repo := range before {
		key := repositoryIdentity(repo.URL)
		if key != "" && !kept[key] {
			removed[key] = true
		}
	}
	for _, res := range resources {
		if res.ResourceType != "github_repo" && res.ResourceType != "gongfeng_repo" {
			continue
		}
		var ref struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(res.ResourceRef, &ref) == nil && removed[repositoryIdentity(ref.URL)] {
			return true
		}
	}
	return false
}
