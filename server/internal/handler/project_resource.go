// 本文件实现「项目资源」（project_resource）相关的全部 HTTP handler 与配套校验逻辑。
//
// 项目资源是一条把“一个仓库或本地目录”挂到某个项目上的记录，支持三种类型：
// github_repo（GitHub 仓库）、gongfeng_repo（腾讯工蜂仓库）、local_directory（某台
// 用户机器上的本地目录）。本文件还承担几件核心工作：
//   - 创建/更新时按类型校验并规范化 ref（见 validateAndNormalizeResourceRef），未知类型
//     在 API 边界就被拒绝；
//   - 处理 local_directory 特有的新旧两代客户端「双轨命名」（顶层 label 列 vs ref 内
//     label）的收敛，以及 worktree 执行模式对 daemon 版本的能力校验；
//   - 在 daemon 认领任务（claim）时，把项目身份与资源解析成 AgentTaskResponse 里的
//     项目上下文（见 resolveClaimProjectContext）。
//
// 所有读写都以 workspace 为范围做归属校验；写操作与领域事件在同一个事务里提交。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectResourceResponse 是项目资源 API 返回给前端的 JSON 结构。
// ResourceRef 保留原始 JSON（不同资源类型结构不同），Label 可空表示未命名。
type ProjectResourceResponse struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	WorkspaceID  string          `json:"workspace_id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     int32           `json:"position"`
	CreatedAt    string          `json:"created_at"`
	CreatedBy    *string         `json:"created_by"`
}

// projectResourceToResponse 把数据库行 db.ProjectResource 转成对外 JSON 结构。
// resource_ref 为空时补一个空对象 {}，避免前端拿到 null 无法按对象解析。
func projectResourceToResponse(r db.ProjectResource) ProjectResourceResponse {
	ref := json.RawMessage(r.ResourceRef)
	if len(ref) == 0 {
		ref = json.RawMessage("{}")
	}
	return ProjectResourceResponse{
		ID:           uuidToString(r.ID),
		ProjectID:    uuidToString(r.ProjectID),
		WorkspaceID:  uuidToString(r.WorkspaceID),
		ResourceType: r.ResourceType,
		ResourceRef:  ref,
		Label:        textToPtr(r.Label),
		Position:     r.Position,
		CreatedAt:    timestampToString(r.CreatedAt),
		CreatedBy:    uuidToPtr(r.CreatedBy),
	}
}

// CreateProjectResourceRequest 是 POST /api/projects/{id}/resources 的请求体。
// ResourceType 指明资源类型；ResourceRef 是随类型变化的原始 JSON；
// Position 不传时默认追加到列表末尾。
type CreateProjectResourceRequest struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     *int32          `json:"position"`
}

// UpdateProjectResourceRequest 是 PUT /api/projects/{id}/resources/{resourceId} 的请求体。
// resource_type 一旦创建便不可修改——想换类型需删除后重建。所有字段都可选；
// 未传的字段保持原值。
type UpdateProjectResourceRequest struct {
	ResourceRef json.RawMessage `json:"resource_ref"`
	Label       *string         `json:"label"`
	Position    *int32          `json:"position"`
}

// validateAndNormalizeResourceRef 校验并规范化 resource_ref：先保证非空，再按
// resource_type 分派到对应类型的校验函数（去空格、补默认值、重序列化），确保
// 落库形态统一。新增资源类型只需在此登记一个 case，无需数据库迁移；未知类型
// 在 API 边界直接拒绝，防止拼写错误漏进库里、产生 daemon/UI 都不认识的数据。
func validateAndNormalizeResourceRef(resourceType string, ref json.RawMessage) (json.RawMessage, error) {
	if len(ref) == 0 {
		return nil, errors.New("resource_ref is required")
	}
	switch resourceType {
	case "github_repo":
		return validateGithubRepoRef(ref)
	case "gongfeng_repo":
		return validateGongfengRepoRef(ref)
	case "local_directory":
		return validateLocalDirectoryRef(ref)
	default:
		return nil, fmt.Errorf("unknown resource_type %q", resourceType)
	}
}

// gongfengRepoRef 是 gongfeng_repo 类型资源落库的 JSONB 结构：
// Provider 固定为 "gongfeng"；URL 是仓库地址；ProjectPath 是工蜂内的项目路径；
// ResourceKind 表示资源种类（缺省为 project）；Ref 为可选的检出分支/提交。
type gongfengRepoRef struct {
	Provider     string `json:"provider"`
	URL          string `json:"url"`
	ProjectPath  string `json:"project_path"`
	ResourceKind string `json:"resource_kind"`
	Ref          string `json:"ref,omitempty"`
}

// validateGongfengRepoRef 校验并规范化 gongfeng_repo 引用：URL 必须是合法的
// http(s)/ssh git 地址；Provider 强制写死为 "gongfeng"；ProjectPath 去掉首尾
// 斜杠；ResourceKind 缺省补为 "project"。清洗完重新序列化返回。
func validateGongfengRepoRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload gongfengRepoRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid gongfeng_repo payload: %w", err)
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" || !isValidGitRepoURL(payload.URL) {
		return nil, errors.New("gongfeng_repo: url must be a valid http(s) or ssh git URL")
	}
	payload.Provider = "gongfeng"
	payload.ProjectPath = strings.Trim(strings.TrimSpace(payload.ProjectPath), "/")
	payload.ResourceKind = firstNonEmpty(strings.TrimSpace(payload.ResourceKind), "project")
	payload.Ref = strings.TrimSpace(payload.Ref)
	return json.Marshal(payload)
}

// githubRepoRef 是 github_repo 类型资源落库的 JSONB 结构：URL 为仓库地址；
// DefaultBranchHint 是可选的默认分支提示（仅 UI 展示用，不影响检出逻辑）；
// Ref 为可选的检出分支/提交。
type githubRepoRef struct {
	URL               string `json:"url"`
	DefaultBranchHint string `json:"default_branch_hint,omitempty"`
	Ref               string `json:"ref,omitempty"`
}

// validateGithubRepoRef 校验并规范化 github_repo 引用：URL 必须非空且是合法的
// http(s)/ssh git 地址，并去除各字段首尾空白。
func validateGithubRepoRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload githubRepoRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid github_repo payload: %w", err)
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return nil, errors.New("github_repo: url is required")
	}
	if !isValidGitRepoURL(payload.URL) {
		return nil, errors.New("github_repo: url must be a valid http(s) or ssh git URL")
	}
	payload.DefaultBranchHint = strings.TrimSpace(payload.DefaultBranchHint)
	payload.Ref = strings.TrimSpace(payload.Ref)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resource_type=local_directory 的执行模式。字段缺省（零值）等价于 in_place，
// 所以 worktree 模式上线前创建的资源无需数据迁移，仍保持原有串行行为。
const (
	// localDirectoryModeInPlace：agent 直接在用户的目录里运行，由 daemon 端按
	// 路径互斥锁串行化——同一时刻只跑一个任务，改动直接落到用户工作区。
	localDirectoryModeInPlace = "in_place"
	// localDirectoryModeWorktree：每个任务在用户仓库的独立 git worktree 中运行
	// （worktree 建在 daemon 的 env root 内）。同一目录下的任务可以并发执行，
	// 最终以分支形式交付。仅当目录是 git 工作树时有效——服务器看不到文件系统，
	// 该条件由 daemon 在任务运行时验证。
	localDirectoryModeWorktree = "worktree"
)

// localDirectoryRef 是 resource_type=local_directory 落库的 JSONB 结构：把一个
// 项目钉到某台具体机器上的一个现有目录。DaemonID 把路径作用域限定在某个 daemon
// 注册之内——不同机器上的同一字符串路径是两个不同的资源。Label 是给 UI 看的
// 可读提示，与行级 project_resource.label 通用列并存。
//
// ExecutionMode 决定任务如何共享该目录：in_place（默认）保持历史上「同一时刻
// 一个任务」的串行行为；worktree 给每个任务一个隔离的 git worktree 以便并发。
type localDirectoryRef struct {
	LocalPath     string `json:"local_path"`
	DaemonID      string `json:"daemon_id"`
	Label         string `json:"label,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
}

// requireWorktreeCapableDaemon 在保存 local_directory 资源时把关：若请求要求
// execution_mode=worktree，而持有该路径的 daemon 版本太老、无法实现此模式，就
// 拒绝保存。老 daemon 不知道这个字段存在：会像 JSON 里没这个键一样把它跳过，
// 直接以 in_place 方式跑在用户明确要求隔离的工作副本上——而老版本 daemon 又缺少
// 端侧的“未知模式拒绝”保护，所以只有服务器能拦住它。放在保存时拦截，可以把
// 失败暴露在用户还能采取行动的时刻（升级 daemon），而不是事后某个任务悄悄跑错。
//
// 遗留缺口（暂时接受）：资源保存之后才被降级的 daemon 在这里拦不住；要堵上它
// 需要在任务认领（claim）时再加一道闸。
//
// 返回 true 表示放行；返回 false 表示已写出 422 响应，code 复用与 quick-create
// 网关相同的 daemon_version_unsupported，方便客户端分支处理。
func (h *Handler) requireWorktreeCapableDaemon(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, resourceType string, normalizedRef json.RawMessage) bool {
	if resourceType != "local_directory" {
		return true
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(normalizedRef, &ref); err != nil || ref.ExecutionMode != localDirectoryModeWorktree {
		return true
	}

	// 一台机器按 provider 各注册一行 runtime，都由同一个 daemon 二进制注册。
	// 一个 workspace 里的 runtime 就那么几个，不筛直接全量读很小、很快。
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check runtime capabilities")
		return false
	}
	// 与认领时的闸门用同一个信号：daemon 在注册时把能力写进自己的 runtime 行。
	// 版本号回答不了这个问题——开发自建的 daemon 报的是 git-describe 字符串，
	// 版本下限刻意放过了它（MUL-5707）。
	if daemonAdvertisesWorktree(runtimes, ref.DaemonID) {
		return true
	}
	// 没有一行 runtime 声明支持时一律失败关闭（fail closed）——包括 daemon_id
	// 压根没有对应注册行的情况：一个永远无法正确下发的 worktree 资源，比保存时
	// 报错更糟。
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error": fmt.Sprintf(
			"local_directory: %q is set to parallel (worktree) mode, but the Multica runtime on that machine does not support it. Update the Multica app on that machine to the latest version, or keep the resource on in_place.",
			ref.LocalPath),
		"code":            "daemon_version_unsupported",
		"current_version": latestDaemonCLIVersion(runtimes, ref.DaemonID),
		"min_version":     agentpkg.MinLocalWorktreeCLIVersion,
		"daemon_id":       ref.DaemonID,
	})
	return false
}

// daemonAdvertisesWorktree 判断该 daemon「最近一次见过」的 runtime 行是否声明
// 支持 worktree。
//
// 刻意不用「只要有任意一行声明过就算」：注销 runtime 只会把行置为离线，元数据
// 仍然保留，而 ListAgentRuntimes 返回所有行。因此一台曾跑过有能力 daemon、之后
// 又降级的机器，旧的“有能力”行会和新“没能力”行并存，若任意命中就会永远说
// 支持。取最新一行，读到的是这台机器当前二进制的能力——那才是被问的问题。
//
// 不带能力字段的行绝不会被跳过：作为最新一行它就有权威性，而不是因为它给出的
// 答案顺眼。
func daemonAdvertisesWorktree(runtimes []db.AgentRuntime, daemonID string) bool {
	if strings.TrimSpace(daemonID) == "" {
		return false
	}
	var newest *db.AgentRuntime
	for i := range runtimes {
		rt := &runtimes[i]
		if !rt.DaemonID.Valid || rt.DaemonID.String != daemonID {
			continue
		}
		if newest == nil || runtimeSeenAfter(rt, newest) {
			newest = rt
		}
	}
	if newest == nil {
		return false
	}
	return runtimeHasCapability(newest.Metadata, protocol.DaemonCapabilityLocalWorktreeV1)
}

// runtimeSeenAfter 按 last_seen_at 比较同一 daemon 的两行。从未上报过（NULL）
// 的行排最旧，保证活跃行总能胜过从未签到过的行。
func runtimeSeenAfter(candidate, current *db.AgentRuntime) bool {
	if !candidate.LastSeenAt.Valid {
		return false
	}
	if !current.LastSeenAt.Valid {
		return true
	}
	return candidate.LastSeenAt.Time.After(current.LastSeenAt.Time)
}

// latestDaemonCLIVersion 返回 daemonID 名下最新上报的 runtime 行的 cli_version，
// 没有任一行携带版本号时返回空串。不带版本的行直接跳过、不当作权威：同一台机器
// 按 provider 各注册一行，只有最新带版本的那行才反映当前真正在跑的二进制。
func latestDaemonCLIVersion(runtimes []db.AgentRuntime, daemonID string) string {
	current := ""
	var currentSeen pgtype.Timestamptz
	for _, rt := range runtimes {
		if !rt.DaemonID.Valid || rt.DaemonID.String != daemonID {
			continue
		}
		v := readRuntimeCLIVersion(rt.Metadata)
		if v == "" {
			continue
		}
		if current == "" || (rt.LastSeenAt.Valid && rt.LastSeenAt.Time.After(currentSeen.Time)) {
			current = v
			currentSeen = rt.LastSeenAt
		}
	}
	return current
}

// validateLocalDirectoryRef 校验并规范化 local_directory 引用：local_path 必填且
// 必须是绝对路径（POSIX 或 Windows 形态）；daemon_id 必填；execution_mode 只能是
// 缺省、in_place 或 worktree，其余值一律拒绝。清洗完重新序列化返回。
func validateLocalDirectoryRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload localDirectoryRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid local_directory payload: %w", err)
	}
	payload.LocalPath = strings.TrimSpace(payload.LocalPath)
	if payload.LocalPath == "" {
		return nil, errors.New("local_directory: local_path is required")
	}
	if !isAbsoluteLocalPath(payload.LocalPath) {
		return nil, errors.New("local_directory: local_path must be an absolute path")
	}
	payload.DaemonID = strings.TrimSpace(payload.DaemonID)
	if payload.DaemonID == "" {
		return nil, errors.New("local_directory: daemon_id is required")
	}
	payload.Label = strings.TrimSpace(payload.Label)
	payload.ExecutionMode = strings.TrimSpace(payload.ExecutionMode)
	switch payload.ExecutionMode {
	case "", localDirectoryModeInPlace, localDirectoryModeWorktree:
	default:
		return nil, fmt.Errorf("local_directory: execution_mode must be %q or %q, got %q",
			localDirectoryModeInPlace, localDirectoryModeWorktree, payload.ExecutionMode)
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// localDirectoryRefLabel 取出 ref 内嵌的 label（已去首尾空白）。label 缺失或 ref
// 无法解析时返回空串——调用方只比较 label，所以一份读不了的 ref 按“没有名字”
// 处理即可，不必让写入失败。
func localDirectoryRefLabel(ref json.RawMessage) string {
	var payload localDirectoryRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Label)
}

// localDirectoryRefDiffersOnlyByLabel 判断两份 ref 撇开 label 之后是否完全一致。
//
// 它用来区分「≤ v0.4.28 客户端改了文件夹名」与「客户端拿着别人改名之前的旧 ref
// 又发了一遍」。两种情况都表现为：整份 ref 与库里的不同，且差异只剩 label——
// 但只有前者是真正的改名。模式对话框打开时会整份快照 ref；若期间另一台设备改了
// 名，接下来任何人保存一次执行模式，就会把名字悄悄回滚掉，除非能把这两种情况分开。
//
// 用解码后的值而不是字节串比较：库里存的 ref 可能是另一个构建版本写下的，键顺序
// 和空格说明不了任何问题。未知键也算差异——这份二进制看不懂的差别依然是差别，
// 把这样的请求称作“纯改名”只是猜测。
func localDirectoryRefDiffersOnlyByLabel(a, b json.RawMessage) bool {
	strip := func(raw json.RawMessage) (map[string]any, bool) {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, false
		}
		delete(fields, "label")
		return fields, true
	}
	left, ok := strip(a)
	if !ok {
		return false
	}
	right, ok := strip(b)
	if !ok {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// withLocalDirectoryRefLabel 只改写 ref 中的 "label" 键（label 为 NULL 时删除该
// 键），其余所有键原样保留。
//
// 背景：local_directory 的显示名有两个存放处——v0.4.28 及更早的桌面版改名是重写
// resource_ref.label；新版客户端改的是顶层列，正是为了不再整份重发 ref（旧服务器
// 会对重发的 ref 重新规范化，静默丢掉它不认识的字段，见 project-resources-section.tsx
// 里的 handleRenameLocalDirectory）。本服务器是唯一能同时见到两代客户端的写入方，
// 所以每次写入都把这两份拷贝对齐；不对齐的话，各代客户端各改各的拷贝，同一行在
// 不同设备上会显示不同名字。
//
// 直接打补丁到原始 map，而不是把 localDirectoryRef 解码再重编码：库里存的 ref 可能
// 带着更新的服务器写下的字段，用本二进制的结构体重编码会把它们丢掉——这正是本
// PR 要堵住的失败模式。键顺序与空白不保证保留（JSONB 本就不保证），但每个键值
// 都保留。
func withLocalDirectoryRefLabel(ref json.RawMessage, label pgtype.Text) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ref, &fields); err != nil {
		return nil, err
	}
	if label.Valid {
		encoded, err := json.Marshal(label.String)
		if err != nil {
			return nil, err
		}
		fields["label"] = encoded
	} else {
		delete(fields, "label")
	}
	return json.Marshal(fields)
}

// isAbsoluteLocalPath 判断路径在 POSIX 或 Windows daemon 上看起来是否像绝对路径。
// 服务器无法知道 daemon 跑在哪个操作系统，所以接受两者并集：以 / 开头（POSIX）、
// UNC 前缀 "\\"、或盘符形如 "C:\" / "C:/"。daemon 在运行时仍会验证路径真实存在
// ——这里只是防拼写错误的护栏，不是文件系统检查。
func isAbsoluteLocalPath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' {
		return true
	}
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isValidGitRepoURL 接受用户从 GitHub 的 Code 菜单可能贴出的三种形态：https://、
// 带显式协议的 ssh://、以及 scp 风格简写 git@host:owner/repo.git。检查刻意保持
// 宽松——我们防的是“not-a-url”这类粘贴垃圾，而不是强制严格文法——因为真正的
// 拉取由客户端侧 git clone 完成，git 给用户的报错比我们给的更清楚。
func isValidGitRepoURL(s string) bool {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		switch u.Scheme {
		case "http", "https", "ssh", "git":
			return true
		}
	}
	// scp 风格 ssh 简写：[user@]host:path，host 与 path 都非空且不能含空格。
	// 凡长得像带协议头的 URL 一律拒掉（那些应走上面的 url.Parse 分支）。
	if strings.Contains(s, " ") || strings.Contains(s, "://") {
		return false
	}
	colon := strings.Index(s, ":")
	if colon <= 0 || colon == len(s)-1 {
		return false
	}
	// scp 风格简写 [user@]host:path 中，@ 只在第一个 ':' 之前作为用户分隔符才有意义。
	// 若 @ 出现在冒号处或之后，它不是用户分隔符——与其瞎猜，不如直接拒绝（同时也
	// 避免盲目切片造成越界 panic）。
	at := strings.Index(s, "@")
	if at >= colon {
		return false
	}
	hostStart := 0
	if at >= 0 {
		hostStart = at + 1
	}
	host := s[hostStart:colon]
	path := s[colon+1:]
	if host == "" || path == "" {
		return false
	}
	return true
}

// loadProjectForResource 解析项目 ID、校验其确实属于当前 workspace，并返回项目
// 数据库行。项目不存在或不属于该 workspace 时，已写好 4xx 响应并返回 ok=false。
// 所有 project_resource handler 共用此装载入口。
func (h *Handler) loadProjectForResource(w http.ResponseWriter, r *http.Request, projectIDParam string) (db.Project, bool) {
	projectUUID, ok := parseUUIDOrBadRequest(w, projectIDParam, "project id")
	if !ok {
		return db.Project{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, false
	}
	return project, true
}

// ListProjectResources 返回挂在某项目下的资源列表（GET /api/projects/{id}/resources）。
func (h *Handler) ListProjectResources(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resources, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project resources")
		return
	}
	resp := make([]ProjectResourceResponse, len(resources))
	for i, res := range resources {
		resp[i] = projectResourceToResponse(res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resp, "total": len(resp)})
}

// CreateProjectResource 给项目新增一个资源（POST）。流程：装载项目 → 校验请求体 →
// 按类型校验/规范化 ref → 做 local_directory 查重与 worktree 能力把关 → 在事务里
// 落库并记录领域事件 → 提交后发布事件、返回 201。
func (h *Handler) CreateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateProjectResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	if req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "resource_type is required")
		return
	}
	normalizedRef, err := validateAndNormalizeResourceRef(req.ResourceType, req.ResourceRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if conflict, err := h.findLocalDirectoryConflict(r.Context(), project.ID, req.ResourceType, normalizedRef, pgtype.UUID{}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, "this daemon already has a local_directory attached to the project; remove it before adding another")
		return
	}

	if !h.requireWorktreeCapableDaemon(w, r, project.WorkspaceID, req.ResourceType, normalizedRef) {
		return
	}

	var label pgtype.Text
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = pgtype.Text{String: strings.TrimSpace(*req.Label), Valid: true}
	}
	var position int32
	if req.Position != nil {
		position = *req.Position
	} else {
		// 默认插到列表末尾：现有资源数就是新资源的下标。
		count, _ := h.Queries.CountProjectResources(r.Context(), project.ID)
		position = int32(count)
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start project resource transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	resource, err := qtx.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  project.WorkspaceID,
		ResourceType: req.ResourceType,
		ResourceRef:  normalizedRef,
		Label:        label,
		Position:     position,
		CreatedBy:    creator,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this resource is already attached to the project")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create project resource")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectResourceDomainEvent(
		protocol.EventProjectResourceCreated, resource, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project resource event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project resource")
		return
	}

	resp := projectResourceToResponse(resource)
	h.publishEvent(event)
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateProjectResource 修改已有资源的 ref/label/position（PUT）。
// resource_type 不可变——把资源改指另一种类型几乎总是另一个概念实体，应删除重建。
// 未传的字段保持原值；其中 label 区分 JSON null 与字段缺失：缺失=保留原值，
// 显式传 ""（或 null）= 清空。
func (h *Handler) UpdateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	existing, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(existing.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}

	// 先把请求体解成原始 map，才能区分「字段没传」和「字段传了零值」——
	// 尤其是 label 的清空语义全靠这个区分。
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	nextRef := json.RawMessage(existing.ResourceRef)
	rawRef, refProvided := raw["resource_ref"]
	if refProvided {
		normalized, err := validateAndNormalizeResourceRef(existing.ResourceType, rawRef)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		nextRef = normalized
	}

	if conflict, err := h.findLocalDirectoryConflict(r.Context(), project.ID, existing.ResourceType, nextRef, existing.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, "another local_directory on this daemon is already attached to the project")
		return
	}

	// ≤ v0.4.28 的客户端靠重发整份 ref 来改名，所以「ref 被发过来了」并不代表
	// 「执行模式被动过」；反之，ref 有任何其它变化，才可能动过模式。
	refRenameOnly := refProvided &&
		existing.ResourceType == "local_directory" &&
		localDirectoryRefDiffersOnlyByLabel(nextRef, existing.ResourceRef)

	// 只在调用方确实要改「会影响怎么跑」的东西时才把关：单纯改 label 或 position
	// ——包括老客户端带着 ref 的改名——不能因为 daemon 注册信息漂移就开始报错，
	// 毕竟该行早已合法保存为 worktree。真正阻止它跑到不支持的地方去的是认领闸。
	if refProvided && !refRenameOnly {
		if !h.requireWorktreeCapableDaemon(w, r, project.WorkspaceID, existing.ResourceType, nextRef) {
			return
		}
	}

	nextLabel := existing.Label
	// 追踪「显式清空」与「该列从未设置过」的区别。只有前者才允许顺带删掉 ref 里
	// 的旧版 label 拷贝：老客户端建的行只有名字放在 ref 里，一次与名字无关的更新
	// 不能因为列是 NULL 就把名字扒掉。
	labelCleared := false
	if rawLabel, ok := raw["label"]; ok {
		var labelStr *string
		if err := json.Unmarshal(rawLabel, &labelStr); err != nil {
			writeError(w, http.StatusBadRequest, "label must be a string or null")
			return
		}
		if labelStr == nil || strings.TrimSpace(*labelStr) == "" {
			nextLabel = pgtype.Text{}
			labelCleared = true
		} else {
			nextLabel = pgtype.Text{String: strings.TrimSpace(*labelStr), Valid: true}
		}
	} else if refRenameOnly {
		// 没有传 label 字段，且 ref 的差异只在它内嵌的 label：这正是 v0.4.28 及更早
		// 桌面版的改名方式——它们改 ref.label、从不发列字段。要把这次改名跟进到列里，
		// 否则新版客户端（优先读列）会一直显示改名前的旧名字。
		//
		// 反过来，ref 还动了别的东西就不算改名——哪怕它的 label 看起来不同：模式
		// 对话框打开时会整份快照 ref，若期间另一台设备改了名，谁再保存一次执行模式
		// 就会把那次改名抹掉。
		if refLabel := localDirectoryRefLabel(nextRef); refLabel != localDirectoryRefLabel(existing.ResourceRef) {
			if refLabel == "" {
				nextLabel = pgtype.Text{}
				labelCleared = true
			} else {
				nextLabel = pgtype.Text{String: refLabel, Valid: true}
			}
		}
	}

	nextPosition := existing.Position
	if rawPos, ok := raw["position"]; ok {
		var pos *int32
		if err := json.Unmarshal(rawPos, &pos); err != nil {
			writeError(w, http.StatusBadRequest, "position must be an integer")
			return
		}
		if pos != nil {
			nextPosition = *pos
		}
	}

	// 把最终 label 镜像进 ref 的旧版拷贝，让两代客户端读到同一个名字，显式清空时
	// 也要同步删掉——否则展示会回退到用户刚删掉的那个名字。两拷贝时代遗留的不一致
	// 行，在第一次写入这里时收敛。从未设置过的 NULL 列不进 ref：对老客户端建的行，
	// ref 里的拷贝本来就是名字本体。
	if existing.ResourceType == "local_directory" {
		// 本次请求有权写入的名字。只有改名或显式传 label 字段才能改它；其它任何
		// 操作都保留这行现在的叫法——不管这名字如今存在哪一份拷贝里——防止一份
		// 过期的 ref 快照借一次无关编辑把旧名字带回来。
		name := nextLabel
		if !nextLabel.Valid && !labelCleared {
			if stored := localDirectoryRefLabel(existing.ResourceRef); stored != "" {
				name = pgtype.Text{String: stored, Valid: true}
			}
		}
		// 从来没起过名字的行保持原样；没碰到 ref 的更新也保持原样：一次无关的
		// position 调整不该重写 ref——对老行来说，ref 里的拷贝就是名字本体。
		if refProvided || nextLabel.Valid || labelCleared {
			synced, err := withLocalDirectoryRefLabel(nextRef, name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update project resource")
				return
			}
			nextRef = synced
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start project resource transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdateProjectResource(r.Context(), db.UpdateProjectResourceParams{
		ID:          existing.ID,
		ResourceRef: nextRef,
		Label:       nextLabel,
		Position:    nextPosition,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this resource is already attached to the project")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project resource")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectResourceDomainEvent(
		protocol.EventProjectResourceUpdated, updated, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project resource event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project resource")
		return
	}

	resp := projectResourceToResponse(updated)
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, resp)
}

// findLocalDirectoryConflict 保证「每个 (project, daemon) 至多挂一个 local_directory
// 资源」。daemon 从任务的资源里挑第一个 daemon_id 匹配的行来分配目录（见
// findLocalDirectoryAssignment）；若允许同一 daemon 在一个项目里挂两行，agent 就会
// 静默写进碰巧先返回的那个目录——对直接操作真实工作目录的功能这是安全隐患。
//
// 仅靠数据库层 UNIQUE(project_id, resource_type, resource_ref) 约束不够：它只在整份
// ref JSON 完全相等时才触发，换个 local_path、甚至同一 daemon 上 label 打错字都能
// 溜过去，所以在这里用应用层代码做 daemon 维度的查重。
//
// excludeID 让更新路径可以忽略正在编辑的那一行本身。
func (h *Handler) findLocalDirectoryConflict(ctx context.Context, projectID pgtype.UUID, resourceType string, normalizedRef json.RawMessage, excludeID pgtype.UUID) (bool, error) {
	if resourceType != "local_directory" {
		return false, nil
	}
	var incoming localDirectoryRef
	if err := json.Unmarshal(normalizedRef, &incoming); err != nil {
		return false, err
	}
	rows, err := h.Queries.ListProjectResources(ctx, projectID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ResourceType != "local_directory" {
			continue
		}
		if excludeID.Valid && uuidToString(row.ID) == uuidToString(excludeID) {
			continue
		}
		var existing localDirectoryRef
		if err := json.Unmarshal(row.ResourceRef, &existing); err != nil {
			continue
		}
		// daemon 维度唯一：每个 (daemon, project) 至多一个 local_directory。
		// 不同 daemon（对应不同用户设备）可以各挂一行；daemon 端解析器按
		// daemon_id 把每个 daemon 路由到它自己的那一行。
		if existing.DaemonID == incoming.DaemonID {
			return true, nil
		}
	}
	return false, nil
}

// DeleteProjectResource 从项目上移除一个资源（DELETE）。同样先校验项目与资源的
// workspace 归属，再在同一事务里删除并记录领域事件，成功返回 204。
func (h *Handler) DeleteProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	resource, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(resource.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start project resource transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteProjectResource(r.Context(), resource.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project resource")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildProjectResourceDomainEvent(
		protocol.EventProjectResourceDeleted, resource, "member", userID, nil,
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record project resource event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project resource")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// parseUserUUIDOrZero 把用户 ID 字符串转成 pgtype.UUID；任何解析失败都返回零值，
// 让调用方在「已认证身份不是 workspace 成员」（例如服务器内部调用）时能把
// created_by 存成 NULL。
func (h *Handler) parseUserUUIDOrZero(userID string) (pgtype.UUID, bool) {
	if userID == "" {
		return pgtype.UUID{}, false
	}
	u, err := parseUUIDLoose(userID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

// parseUUIDLoose 与 util.ParseUUID 行为一致，只是放在本文件，避免为这么个一次性
// 小工具引入 util 依赖。保持函数体尽量精简。
func parseUUIDLoose(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// claimProjectContext 是一次 daemon 认领（claim）能暴露给 agent 的项目背景：
// ProjectID/Title/Description 是 prompt 里指名的项目身份；Resources 会被 execenv
// 物化成 .multica/project/resources.json；Repos 是 `multica repo checkout` 读取的
// 仓库清单。
type claimProjectContext struct {
	ProjectID   string
	Title       string
	Description string
	Resources   []ProjectResourceData
	Repos       []RepoData
}

// applyTo 把解析好的背景整体拷到 claim 响应上。调用方要么全量赋值、要么完全不赋，
// 所以一次 claim 不可能只带项目标题却没有它的资源。
func (c claimProjectContext) applyTo(resp *AgentTaskResponse) {
	resp.ProjectID = c.ProjectID
	resp.ProjectTitle = c.Title
	resp.ProjectDescription = c.Description
	if len(c.Resources) > 0 {
		resp.ProjectResources = c.Resources
	}
	resp.Repos = c.Repos
}

// resolveClaimProjectContext 为一次 daemon 认领加载项目背景。
//
// 所有认领路径（issue、chat、autopilot、quick-create）都要从一份软项目引用解析出
// 同样的东西，所以租户边界与失败规则集中写在这里，而不是每条路径各抄一份：
//
//   - 两处读取都限定在 workspace 内。project_resource 自带 workspace_id，因此一份
//     被污染的项目引用不可能把别的租户的仓库 URL 或本地路径带进 claim。
//   - 读取失败 ≠ 项目不存在。此时返回 error，调用方可以保留任务等待重投；若把它
//     当成「没有项目」而降级到 workspace 仓库兜底，一次瞬时 DB 错误就会让 agent
//     静默地对着错误的仓库跑起来（与 chat-input 加载遵循同一条规则，MUL-4351）。
//   - 项目解析不到任何行才算「没有项目」：引用已过期、被删或指向 workspace 之外，
//     此时 claim 降级为 workspace 背景。
//
// 仓库优先级：项目绑定 github_repo 资源存在时，覆盖 workspace 仓库。两者混在一起
// 只会让 agent 困惑——项目既然显式挂了仓库，那组就是权威集合。没有项目、没有
// github_repo 资源、或引用已过期时，退回 workspace 仓库。
func (h *Handler) resolveClaimProjectContext(ctx context.Context, projectID, workspaceID pgtype.UUID) (claimProjectContext, error) {
	var out claimProjectContext

	if projectID.Valid {
		project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: workspaceID,
		})
		switch {
		case err == nil:
			out.ProjectID = uuidToString(project.ID)
			out.Title = project.Title
			out.Description = project.Description.String

			rows, resErr := h.Queries.ListProjectResourcesInWorkspace(ctx, db.ListProjectResourcesInWorkspaceParams{
				ProjectID:   project.ID,
				WorkspaceID: workspaceID,
			})
			if resErr != nil {
				return claimProjectContext{}, fmt.Errorf("list project resources: %w", resErr)
			}
			out.Resources, out.Repos = projectResourcesForClaim(rows)
		case errors.Is(err, pgx.ErrNoRows):
			// 引用过期/被删/指向别处：降级为 workspace 背景。
		default:
			return claimProjectContext{}, fmt.Errorf("get project: %w", err)
		}
	}

	if len(out.Repos) > 0 {
		return out, nil
	}

	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return claimProjectContext{}, fmt.Errorf("get workspace: %w", err)
	}
	if ws.Repos != nil {
		var repos []RepoData
		if jsonErr := json.Unmarshal(ws.Repos, &repos); jsonErr != nil {
			// 库里 JSON 损坏不是瞬时问题：在这里让认领失败，会把整个 workspace 的
			// 认领都卡死，直到有人修复该行。改为降级成「没有仓库」并留下日志线索。
			slog.Error("claim project context: workspace repos are not valid JSON; claiming without repos",
				"workspace_id", uuidToString(workspaceID), "error", jsonErr)
		} else if len(repos) > 0 {
			out.Repos = repos
		}
	}
	return out, nil
}

// projectResourcesForClaim 把资源行映射成认领协议里的线上结构，并把仓库类资源提升
// 进 repo 列表，让 `multica repo checkout` 和 meta-skill 把它们当成任务仓库渲染。
// github_repo 直接取 url；gongfeng_repo 经 canonicalGongfengCloneURL 拼出可克隆
// 地址后再进列表。
func projectResourcesForClaim(rows []db.ProjectResource) ([]ProjectResourceData, []RepoData) {
	if len(rows) == 0 {
		return nil, nil
	}
	resources := make([]ProjectResourceData, 0, len(rows))
	var repos []RepoData
	for _, row := range rows {
		label := ""
		if row.Label.Valid {
			label = row.Label.String
		}
		ref := json.RawMessage(row.ResourceRef)
		if len(ref) == 0 {
			ref = json.RawMessage("{}")
		}
		resources = append(resources, ProjectResourceData{
			ID:           uuidToString(row.ID),
			ResourceType: row.ResourceType,
			ResourceRef:  ref,
			Label:        label,
		})
		switch row.ResourceType {
		case "github_repo":
			var payload struct {
				URL string `json:"url"`
				Ref string `json:"ref,omitempty"`
			}
			if json.Unmarshal(row.ResourceRef, &payload) == nil && payload.URL != "" {
				repos = append(repos, RepoData{URL: payload.URL, Ref: strings.TrimSpace(payload.Ref)})
			}
		case "gongfeng_repo":
			var payload struct {
				URL         string `json:"url"`
				ProjectPath string `json:"project_path"`
				Ref         string `json:"ref,omitempty"`
				Branch      string `json:"branch,omitempty"`
			}
			if json.Unmarshal(row.ResourceRef, &payload) == nil {
				if cloneURL := canonicalGongfengCloneURL(payload.URL, payload.ProjectPath); cloneURL != "" {
					repos = append(repos, RepoData{
						URL: cloneURL,
						Ref: firstNonEmpty(strings.TrimSpace(payload.Branch), strings.TrimSpace(payload.Ref)),
					})
				}
			}
		}
	}
	return resources, repos
}

// canonicalGongfengCloneURL 把 gongfeng_repo 拼成规范的可克隆 https URL：去掉
// project_path 首尾斜杠与 .git 后缀差异，统一为 https://git.code.tencent.com/<path>.git。
// project_path 为空时先从 rawURL 反推（gongfengProjectPathFromURL）；仍反推不出就
// 返回空串，表示该行无法形成仓库。
func canonicalGongfengCloneURL(rawURL, projectPath string) string {
	projectPath = strings.Trim(strings.TrimSpace(projectPath), "/")
	if projectPath == "" {
		projectPath = gongfengProjectPathFromURL(rawURL)
	}
	if projectPath == "" {
		return ""
	}
	return "https://git.code.tencent.com/" + strings.TrimSuffix(projectPath, ".git") + ".git"
}
