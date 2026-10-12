// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace_gongfeng_repo.go）实现 workspace 关联"工蜂
// （git.code.tencent.com）代码仓库"的两个接口：ProbeWorkspaceRepo 探测仓库
// 连通性并列出可用分支，ResolveWorkspaceRepo 在创建绑定前解析出默认分支与
// 最新 commit。两者都要求请求者已配置可用的工蜂账号凭据，地址只接受
// git.code.tencent.com 域名。
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

// workspaceRepoRequest 是探测/解析仓库两个接口的入参：url 为工蜂仓库地址，
// default_branch 是调用方期望的分支（解析接口可留空，留空则用仓库默认分支）。
type workspaceRepoRequest struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

// workspaceRepoProbeResponse 是 ProbeWorkspaceRepo 的返回：除了地址本身，
// 还给出识别出的项目路径、默认分支、全部分支，以及连接/测试状态
// （本实现里成功时恒为 credential_backed / passed）。
type workspaceRepoProbeResponse struct {
	URL              string   `json:"url"`
	Provider         string   `json:"provider"`
	ProjectPath      string   `json:"project_path"`
	DefaultBranch    string   `json:"default_branch"`
	Branches         []string `json:"branches"`
	ConnectionStatus string   `json:"connection_status"`
	TestStatus       string   `json:"test_status"`
}

// parseGongfengRepositoryURL 校验并解析用户填的仓库地址：要求是有效的
// HTTP(S) URL、域名是 git.code.tencent.com，并能从中识别出工蜂项目路径。
// 返回项目路径（provider 侧用的全局唯一路径，形如 group/project）。
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

// gongfengRepositoryCredential 为当前请求者取一份可用的工蜂访问令牌。
// 流程：先要求登录，再读取"可用的工蜂凭据档案"，最后把档案里的凭据解析成
// 实际 token。任一环节失败都会写好 HTTP 错误并返回 ok=false。
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

// loadGongfengProjectDetails 用 token 查询工蜂上某个项目的公开元数据：
// 先按项目路径换算出 API 用的项目 ID，再查项目详情，返回 (项目ID, 默认分支)。
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

// listGongfengBranches 分页拉取项目全部分支，返回按"最后一次提交时间"从新到
// 旧排好的分支名列表。内部先统一收集（跨页去重，因为翻页边界可能重复），
// 再整体排序。
func listGongfengBranches(r *http.Request, token, projectID string) ([]string, error) {
	const pageSize = 100
	const maxPages = 50
	// branchRow 是单条分支的中间形态：名字 + 提交时间 + 出现顺序 index。
	// updatedAt 取三个候选提交时间里最先能解析的那个；index 用于时间缺失或
	// 相同时保持 API 返回顺序（见下方排序）。
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
			// 去重：翻页拉取时同一分支可能在两页边界重复出现。
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
	// 排序主键是最近提交时间（新的在前）；时间解析不出来（零值）的排最后；
	// 时间相同或都为零时退回 API 原始顺序，保证结果稳定可预期。
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

// firstGongfengTime 依次尝试解析传入的时间字符串，返回第一个能解析出非零
// 时间的值。工蜂 API 不同字段可能带不同格式甚至为空，逐个尝试可最大程度拿到
// 有效时间。
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

// loadGongfengBranchHead 查询指定分支当前的 head commit ID，用于记录绑定
// 发生瞬间仓库被锁定的 commit。
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

// ProbeWorkspaceRepo 探测仓库连通性：校验地址 → 读取用户工蜂凭据 → 查询项目
// 与分支列表，一次性返回项目路径、默认分支、全部分支。供前端在绑定表单里
// 展示候选分支、做前置校验。失败按 400 返回具体原因。
func (h *Handler) ProbeWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	var req workspaceRepoRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.HasPrefix(repositoryIdentity(req.URL), "github.com/") {
		h.readGitHubRepository(w, r, req, false)
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

// ResolveWorkspaceRepo 解析出一个可直接落库的仓库引用：在 Probe 的基础上，
// 再选定分支（未指定时用仓库默认分支）并读取其 head commit，随后以
// workspaceRepoRef 形态返回绑定所需全部字段，表示此刻起该仓库被锁定在此
// 分支与 commit。
func (h *Handler) ResolveWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	var req workspaceRepoRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if strings.HasPrefix(repositoryIdentity(req.URL), "github.com/") {
		h.readGitHubRepository(w, r, req, true)
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
		SyncStatus: "metadata_read", TestStatus: "passed", LastTestedAt: now,
	})
}
