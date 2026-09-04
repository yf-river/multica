package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type workspaceRepoRequest struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

type workspaceRepoProbeResponse struct {
	URL              string   `json:"url"`
	Provider         string   `json:"provider"`
	ProjectPath      string   `json:"project_path"`
	DefaultBranch    string   `json:"default_branch"`
	Branches         []string `json:"branches"`
	ConnectionStatus string   `json:"connection_status"`
	TestStatus       string   `json:"test_status"`
}

func parseGongfengRepositoryURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("工蜂仓库地址必须是有效的 HTTP(S) 地址")
	}
	if !strings.EqualFold(parsed.Hostname(), "git.code.tencent.com") {
		return "", errors.New("仅支持 git.code.tencent.com 的工蜂仓库地址")
	}
	projectPath := gongfengProjectPathFromURL(parsed.String())
	if projectPath == "" {
		return "", errors.New("无法从地址中识别工蜂项目路径")
	}
	return projectPath, nil
}

func (h *Handler) gongfengRepositoryCredential(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", false
	}
	profile, found, err := h.loadUsableGongfengCredentialProfile(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取工蜂账号失败")
		return "", false
	}
	if !found {
		writeError(w, http.StatusBadRequest, "请先在设置中配置可用的工蜂账号")
		return "", false
	}
	token, err := h.resolveExternalCredentialToken(profile)
	if err != nil || strings.TrimSpace(token) == "" {
		writeError(w, http.StatusBadRequest, "工蜂账号凭据不可用")
		return "", false
	}
	return token, true
}

func loadGongfengProjectDetails(r *http.Request, token, projectPath string) (string, string, error) {
	projectID, err := resolveGongfengProjectAPIID(r.Context(), token, projectPath)
	if err != nil {
		return "", "", err
	}
	status, body, err := gongfengJSONRequest(r.Context(), http.MethodGet, strings.TrimRight(gongfengAPIBase(), "/")+"/projects/"+url.PathEscape(projectID), token, nil)
	if err != nil {
		return "", "", err
	}
	if status < 200 || status >= 300 {
		return "", "", fmt.Errorf("工蜂项目查询失败，HTTP %d", status)
	}
	var project struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &project); err != nil {
		return "", "", errors.New("工蜂项目响应格式无效")
	}
	return projectID, strings.TrimSpace(project.DefaultBranch), nil
}

func listGongfengBranches(r *http.Request, token, projectID string) ([]string, error) {
	const pageSize = 100
	const maxPages = 50
	type branchRow struct {
		Name   string `json:"name"`
		Commit struct {
			CommittedDate string `json:"committed_date"`
			AuthoredDate  string `json:"authored_date"`
			CreatedAt     string `json:"created_at"`
		} `json:"commit"`
		index     int
		updatedAt time.Time
	}
	rows := make([]branchRow, 0)
	seen := make(map[string]struct{})
	for page := 1; page <= maxPages; page++ {
		target := fmt.Sprintf("%s/projects/%s/repository/branches?per_page=%d&page=%d", strings.TrimRight(gongfengAPIBase(), "/"), url.PathEscape(projectID), pageSize, page)
		status, body, err := gongfengJSONRequest(r.Context(), http.MethodGet, target, token, nil)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("工蜂分支查询失败，HTTP %d", status)
		}
		var batch []branchRow
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, errors.New("工蜂分支响应格式无效")
		}
		for _, row := range batch {
			row.Name = strings.TrimSpace(row.Name)
			if row.Name == "" {
				continue
			}
			if _, exists := seen[row.Name]; exists {
				continue
			}
			seen[row.Name] = struct{}{}
			row.index = len(rows)
			row.updatedAt = firstGongfengTime(row.Commit.CommittedDate, row.Commit.AuthoredDate, row.Commit.CreatedAt)
			rows = append(rows, row)
		}
		if len(batch) < pageSize {
			break
		}
		if page == maxPages {
			return nil, fmt.Errorf("工蜂分支超过 %d 页，请先清理无用分支", maxPages)
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("工蜂仓库没有可用分支")
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if !left.updatedAt.IsZero() && !right.updatedAt.IsZero() && !left.updatedAt.Equal(right.updatedAt) {
			return left.updatedAt.After(right.updatedAt)
		}
		if left.updatedAt.IsZero() != right.updatedAt.IsZero() {
			return !left.updatedAt.IsZero()
		}
		return left.index < right.index
	})
	branches := make([]string, 0, len(rows))
	for _, row := range rows {
		branches = append(branches, row.Name)
	}
	return branches, nil
}

func firstGongfengTime(values ...string) time.Time {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

func loadGongfengBranchHead(r *http.Request, token, projectID, branch string) (string, error) {
	status, body, err := gongfengJSONRequest(r.Context(), http.MethodGet, strings.TrimRight(gongfengAPIBase(), "/")+"/projects/"+url.PathEscape(projectID)+"/repository/branches/"+url.PathEscape(branch), token, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("工蜂分支查询失败，HTTP %d", status)
	}
	var row struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &row); err != nil {
		return "", errors.New("工蜂分支响应格式无效")
	}
	if strings.TrimSpace(row.Commit.ID) == "" {
		return "", errors.New("工蜂分支响应缺少提交 ID")
	}
	return strings.TrimSpace(row.Commit.ID), nil
}

func (h *Handler) ProbeWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	var req workspaceRepoRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	projectPath, err := parseGongfengRepositoryURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, ok := h.gongfengRepositoryCredential(w, r)
	if !ok {
		return
	}
	projectID, defaultBranch, err := loadGongfengProjectDetails(r, token, projectPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	branches, err := listGongfengBranches(r, token, projectID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workspaceRepoProbeResponse{
		URL: strings.TrimSpace(req.URL), Provider: "gongfeng", ProjectPath: projectPath,
		DefaultBranch: defaultBranch, Branches: branches,
		ConnectionStatus: "credential_backed", TestStatus: "passed",
	})
}

func (h *Handler) ResolveWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	var req workspaceRepoRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	projectPath, err := parseGongfengRepositoryURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, ok := h.gongfengRepositoryCredential(w, r)
	if !ok {
		return
	}
	projectID, defaultBranch, err := loadGongfengProjectDetails(r, token, projectPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	branch := strings.TrimSpace(req.DefaultBranch)
	if branch == "" {
		branch = defaultBranch
	}
	if branch == "" {
		writeError(w, http.StatusBadRequest, "工蜂仓库没有可用的默认分支")
		return
	}
	head, err := loadGongfengBranchHead(r, token, projectID, branch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, workspaceRepoRef{
		URL: strings.TrimSpace(req.URL), Provider: "gongfeng", ProjectPath: projectPath,
		DefaultBranch: branch, Ref: branch, HeadCommit: head, ConnectionStatus: "credential_backed",
		SyncStatus: "synced", TestStatus: "passed", LastTestedAt: now, LastSyncedAt: now,
	})
}
