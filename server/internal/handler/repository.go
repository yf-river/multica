package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 远程仓库探测的响应字段。
type workspaceRepoRef struct {
	URL              string `json:"url"`
	Description      string `json:"description,omitempty"`
	Provider         string `json:"provider,omitempty"`
	ProjectPath      string `json:"project_path,omitempty"`
	DefaultBranch    string `json:"default_branch,omitempty"`
	Ref              string `json:"ref,omitempty"`
	HeadCommit       string `json:"head_commit,omitempty"`
	ConnectionStatus string `json:"connection_status,omitempty"`
	SyncStatus       string `json:"sync_status,omitempty"`
	TestStatus       string `json:"test_status,omitempty"`
	LastTestedAt     string `json:"last_tested_at,omitempty"`
	LastSyncedAt     string `json:"last_synced_at,omitempty"`
}

// 仓库配置只存一份；项目保存 repository_id 和本项目的分支覆盖。
func repositoryConfig(row db.Repository) map[string]any {
	config := map[string]any{}
	_ = json.Unmarshal(row.Config, &config)
	config["id"] = uuidToString(row.ID)
	config["default_for_tasks"] = row.DefaultForTasks
	config["revision"] = row.Revision
	return config
}

func repositoryConfigs(ctx context.Context, q *db.Queries, ws pgtype.UUID, defaultsOnly bool) ([]map[string]any, error) {
	rows, err := q.ListRepositories(ctx, ws)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, row := range rows {
		if defaultsOnly && !row.DefaultForTasks {
			continue
		}
		result = append(result, repositoryConfig(row))
	}
	return result, nil
}

func repositoryTaskConfig(ctx context.Context, q *db.Queries, ws pgtype.UUID) ([]byte, error) {
	rows, err := repositoryConfigs(ctx, q, ws, true)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["ref"] = firstNonEmpty(stringField(row, "ref"), stringField(row, "default_branch"))
	}
	return json.Marshal(rows)
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func normalizeRepositoryConfig(config map[string]any) ([]byte, error) {
	url := stringField(config, "url")
	if strings.Contains(url, "git.code.tencent.com/") && strings.Contains(url, "/-/") {
		url = strings.SplitN(url, "/-/", 2)[0] + ".git"
	}
	if !isValidGitRepoURL(url) || repositoryIdentity(url) == "" {
		return nil, errors.New("仓库地址无效")
	}
	provider := stringField(config, "provider")
	if provider == "" {
		if strings.HasPrefix(repositoryIdentity(url), "github.com/") {
			provider = "github"
		} else if strings.HasPrefix(repositoryIdentity(url), "git.code.tencent.com/") {
			provider = "gongfeng"
		} else {
			return nil, errors.New("请指定 GitHub 或工蜂仓库地址")
		}
	}
	if provider != "github" && provider != "gongfeng" {
		return nil, errors.New("不支持的仓库平台")
	}
	result := map[string]any{"url": url, "provider": provider}
	for _, key := range []string{"description", "project_path", "default_branch", "ref", "head_commit", "connection_status", "test_status", "last_tested_at", "sync_status", "last_synced_at"} {
		if value := stringField(config, key); value != "" {
			result[key] = value
		}
	}
	// 分支交给 Git 作为单独参数；拒绝选项及控制字符。
	for _, key := range []string{"ref", "default_branch"} {
		v := stringField(result, key)
		if strings.HasPrefix(v, "-") || strings.ContainsAny(v, "\x00\r\n") {
			return nil, errors.New("开发分支无效")
		}
	}
	return json.Marshal(result)
}

// 返回绑定的存储形态。URL 新建入口和 ID 选择入口都归入同一个仓库实体。
func bindRepository(ctx context.Context, q *db.Queries, ws pgtype.UUID, kind string, raw []byte) ([]byte, error) {
	if kind != "github_repo" && kind != "gongfeng_repo" {
		return raw, nil
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	var repo db.Repository
	var err error
	if id := stringField(input, "repository_id"); id != "" {
		var parsed pgtype.UUID
		if err = parsed.Scan(id); err != nil {
			return nil, errors.New("repository_id 无效")
		}
		repo, err = q.GetRepository(ctx, db.GetRepositoryParams{WorkspaceID: ws, ID: parsed})
	} else {
		input["provider"] = map[string]string{"github_repo": "github", "gongfeng_repo": "gongfeng"}[kind]
		// 项目分支只作为关联覆盖，不修改其他项目的仓库默认值。
		config := map[string]any{"url": input["url"], "provider": input["provider"]}
		normalized, e := normalizeRepositoryConfig(config)
		if e != nil {
			return nil, e
		}
		repo, err = q.UpsertRepository(ctx, db.UpsertRepositoryParams{WorkspaceID: ws, Identity: repositoryIdentity(stringField(input, "url")), Config: normalized})
	}
	if err != nil {
		return nil, fmt.Errorf("仓库不存在或不属于当前工作区: %w", err)
	}
	config := repositoryConfig(repo)
	if stringField(config, "provider") != map[string]string{"github_repo": "github", "gongfeng_repo": "gongfeng"}[kind] {
		return nil, errors.New("仓库平台与资源类型不一致")
	}
	result := map[string]any{"repository_id": uuidToString(repo.ID)}
	ref := stringField(input, "ref")
	if _, ok := input["project_ref"]; ok {
		ref = stringField(input, "project_ref")
	}
	if ref != "" {
		if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
			return nil, errors.New("项目开发分支无效")
		}
		result["ref"] = ref
	}
	return json.Marshal(result)
}

// 在 API/任务边界解析项目引用；数据库中的项目资源不复制仓库属性。
func resolveRepositoryResources(ctx context.Context, q *db.Queries, ws pgtype.UUID, rows []db.ProjectResource) ([]db.ProjectResource, error) {
	repos, err := q.ListRepositories(ctx, ws)
	if err != nil {
		return nil, err
	}
	index := map[string]db.Repository{}
	for _, repo := range repos {
		index[uuidToString(repo.ID)] = repo
	}
	result := append([]db.ProjectResource(nil), rows...)
	for i, row := range result {
		if row.ResourceType != "github_repo" && row.ResourceType != "gongfeng_repo" {
			continue
		}
		var binding map[string]any
		if err := json.Unmarshal(row.ResourceRef, &binding); err != nil {
			return nil, err
		}
		repo, ok := index[stringField(binding, "repository_id")]
		if !ok {
			return nil, errors.New("项目引用的仓库不存在")
		}
		config := repositoryConfig(repo)
		config["repository_id"] = uuidToString(repo.ID)
		config["project_ref"] = stringField(binding, "ref")
		config["ref"] = firstNonEmpty(stringField(binding, "ref"), stringField(config, "ref"), stringField(config, "default_branch"))
		result[i].ResourceRef, _ = json.Marshal(config)
	}
	return result, nil
}

func (h *Handler) ListRepositories(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	repos, err := repositoryConfigs(r.Context(), h.Queries, ws, false)
	if err != nil {
		writeError(w, 500, "读取仓库失败")
		return
	}
	writeJSON(w, 200, repos)
}

func (h *Handler) CreateRepository(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	config, err := normalizeRepositoryConfig(input)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defaults, _ := input["default_for_tasks"].(bool)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "创建仓库失败")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", ws); err != nil {
		writeError(w, 500, "锁定工作区失败")
		return
	}
	q := h.Queries.WithTx(tx)
	repo, err := q.UpsertRepository(r.Context(), db.UpsertRepositoryParams{WorkspaceID: ws, Identity: repositoryIdentity(stringField(input, "url")), Config: config, DefaultForTasks: defaults})
	if err != nil {
		writeError(w, 500, "创建仓库失败")
		return
	}
	repoEvent, eventErr := recordRepositoryChange(r.Context(), q, ws, requestUserID(r))
	if eventErr != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "保存仓库失败")
		return
	}
	h.publishEvent(repoEvent)
	h.notifyDaemonWorkspacesChanged(requestUserID(r))
	writeJSON(w, 201, repositoryConfig(repo))
}

func (h *Handler) UpdateRepository(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "repositoryId"), "repository id")
	if !ok {
		return
	}
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "更新仓库失败")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", ws); err != nil {
		writeError(w, 500, "锁定工作区失败")
		return
	}
	q := h.Queries.WithTx(tx)
	repo, err := q.GetRepository(r.Context(), db.GetRepositoryParams{WorkspaceID: ws, ID: id})
	if err != nil {
		writeError(w, 404, "仓库不存在")
		return
	}
	config := repositoryConfig(repo)
	if revision, ok := input["revision"].(float64); ok && int32(revision) != repo.Revision {
		writeError(w, 409, "仓库配置已更新，请刷新后重试")
		return
	}
	if url := stringField(input, "url"); url != "" && repositoryIdentity(url) != repo.Identity {
		writeError(w, 400, "更换仓库请新建仓库并重新关联项目")
		return
	}
	if provider := stringField(input, "provider"); provider != "" && provider != stringField(config, "provider") {
		writeError(w, 400, "仓库平台不能更改")
		return
	}
	for key, value := range input {
		config[key] = value
	}
	normalized, err := normalizeRepositoryConfig(config)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defaults, _ := config["default_for_tasks"].(bool)
	repo, err = q.UpdateRepository(r.Context(), db.UpdateRepositoryParams{WorkspaceID: ws, ID: id, Config: normalized, DefaultForTasks: defaults})
	if err != nil {
		writeError(w, 500, "更新仓库失败")
		return
	}
	repoEvent, eventErr := recordRepositoryChange(r.Context(), q, ws, requestUserID(r))
	if eventErr != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "保存仓库失败")
		return
	}
	h.publishEvent(repoEvent)
	h.notifyDaemonWorkspacesChanged(requestUserID(r))
	writeJSON(w, 200, repositoryConfig(repo))
}

func (h *Handler) DeleteRepository(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "repositoryId"), "repository id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "删除仓库失败")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", ws); err != nil {
		writeError(w, 500, "锁定工作区失败")
		return
	}
	q := h.Queries.WithTx(tx)
	if _, err = q.GetRepository(r.Context(), db.GetRepositoryParams{WorkspaceID: ws, ID: id}); err != nil {
		writeError(w, 404, "仓库不存在")
		return
	}
	rows, err := tx.Query(r.Context(), "SELECT p.title FROM project_resource r JOIN project p ON p.id=r.project_id AND p.workspace_id=r.workspace_id WHERE r.workspace_id=$1 AND r.resource_ref->>'repository_id'=$2", ws, uuidToString(id))
	if err != nil {
		writeError(w, 500, "读取关联失败")
		return
	}
	titles := []string{}
	for rows.Next() {
		var title string
		if err = rows.Scan(&title); err != nil {
			rows.Close()
			writeError(w, 500, "读取关联失败")
			return
		}
		titles = append(titles, title)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, 500, "读取关联失败")
		return
	}
	if len(titles) > 0 {
		writeError(w, 409, "仓库仍被项目使用："+strings.Join(titles, "、"))
		return
	}
	if err = q.DeleteRepositoryLocations(r.Context(), db.DeleteRepositoryLocationsParams{WorkspaceID: ws, RepositoryID: id}); err != nil {
		writeError(w, 500, "删除运行位置失败")
		return
	}
	if err = q.DeleteRepository(r.Context(), db.DeleteRepositoryParams{WorkspaceID: ws, ID: id}); err != nil {
		writeError(w, 500, "删除仓库失败")
		return
	}
	repoEvent, eventErr := recordRepositoryChange(r.Context(), q, ws, requestUserID(r))
	if eventErr != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "删除仓库失败")
		return
	}
	h.publishEvent(repoEvent)
	h.notifyDaemonWorkspacesChanged(requestUserID(r))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutRepositoryLocation(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "repositoryId"), "repository id")
	if !ok {
		return
	}
	var input localDirectoryRef
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, 400, "请求格式无效")
		return
	}
	if input.ExecutionMode == "" {
		input.ExecutionMode = "worktree"
	}
	raw, _ := json.Marshal(input)
	normalized, err := validateLocalDirectoryRef(raw)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if !h.requireWorktreeCapableDaemon(w, r, ws, "local_directory", normalized) {
		return
	}
	_ = json.Unmarshal(normalized, &input)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "保存运行位置失败")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", ws); err != nil {
		writeError(w, 500, "锁定工作区失败")
		return
	}
	q := h.Queries.WithTx(tx)
	if _, err = q.GetRepository(r.Context(), db.GetRepositoryParams{WorkspaceID: ws, ID: id}); err != nil {
		writeError(w, 404, "仓库不存在")
		return
	}
	location, err := q.UpsertRepositoryLocation(r.Context(), db.UpsertRepositoryLocationParams{WorkspaceID: ws, RepositoryID: id, DaemonID: input.DaemonID, LocalPath: input.LocalPath, ExecutionMode: input.ExecutionMode})
	if err != nil {
		writeError(w, 500, "保存运行位置失败")
		return
	}
	repoEvent, eventErr := recordRepositoryChange(r.Context(), q, ws, requestUserID(r))
	if eventErr != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "保存运行位置失败")
		return
	}
	h.publishEvent(repoEvent)
	h.notifyDaemonWorkspacesChanged(requestUserID(r))
	writeJSON(w, 200, location)
}

func (h *Handler) DeleteRepositoryLocation(w http.ResponseWriter, r *http.Request) {
	ws, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	repo, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "repositoryId"), "repository id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "locationId"), "location id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "删除运行位置失败")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", ws); err != nil {
		writeError(w, 500, "锁定工作区失败")
		return
	}
	q := h.Queries.WithTx(tx)
	if err = q.DeleteRepositoryLocation(r.Context(), db.DeleteRepositoryLocationParams{WorkspaceID: ws, RepositoryID: repo, ID: id}); err != nil {
		writeError(w, 500, "删除运行位置失败")
		return
	}
	event, err := recordRepositoryChange(r.Context(), q, ws, requestUserID(r))
	if err != nil {
		writeError(w, 500, "记录仓库变更失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "删除运行位置失败")
		return
	}
	h.publishEvent(event)
	h.notifyDaemonWorkspacesChanged(requestUserID(r))
	w.WriteHeader(204)
}

func recordRepositoryChange(ctx context.Context, q *db.Queries, ws pgtype.UUID, actor string) (events.Event, error) {
	workspace, err := q.UpdateWorkspace(ctx, db.UpdateWorkspaceParams{ID: ws})
	if err != nil {
		return events.Event{}, err
	}
	repos, err := repositoryConfigs(ctx, q, ws, false)
	if err != nil {
		return events.Event{}, err
	}
	workspace.Repos, err = json.Marshal(repos)
	if err != nil {
		return events.Event{}, err
	}
	return service.RecordDurableEventTx(ctx, q, buildWorkspaceDomainEvent(protocol.EventWorkspaceUpdated, workspace, "member", actor, map[string]any{"action": "repositories_updated"}))
}
