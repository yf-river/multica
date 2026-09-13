// plugin_action.go —— Action API 授权与上下文装配：核实安装/作用域并构造 plugin 上下文载荷。
package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// PluginActionCaller 是一次已授权的 Action API 调用：哪个安装在说、在哪个工作区、代表谁。
//
// 插件从不持有凭据。面板经 postMessage 与宿主页对话，宿主用已登录用户自己的会话重发调用，因此这里的身份
// 永远是真实成员——插件只能以正在使用它的人行事。
type PluginActionCaller struct {
	Installation db.PluginInstallation
	WorkspaceID  pgtype.UUID
	UserID       pgtype.UUID
	// Scopes 是已同意的作用域集合，从安装行读取而非源 URL 今天提供的 manifest。
	Scopes []string
	// IssueScope 一旦设置，就是本调用方可触碰的【唯一】issue。
	//
	// 为针对某具体 issue 签发的回调 token 设置。围绕一个 issue 被调用的 hook 没道理够到另一个：没有它，授权在
	// 存续的整个五分钟内等于该 actor 可见的工作区里每个 issue。零值表示不受限——会话调用方、或无 issue 的调用——
	// 普通的工作区与成员检查仍叠加生效。
	IssueScope pgtype.UUID
}

// AuthorizePluginAction 执行属于插件的两道检查：安装真实且已启用，且持有本次调用所需的作用域。
//
// 它刻意不决定“调用方能否触碰该资源”。那是第三道检查，留在普通资源加载器处，使插件恰好继承背后用户的
// 权限——不多一分，也不存在一份会漂移的权限规则副本。
func (s *PluginService) AuthorizePluginAction(ctx context.Context, installationID string, userID pgtype.UUID, scope string) (PluginActionCaller, error) {
	if installationID == "" {
		return PluginActionCaller{}, pluginErrf(PluginErrorInvalid, "plugin installation is required")
	}
	parsed, err := parseInstallationID(installationID)
	if err != nil {
		return PluginActionCaller{}, err
	}
	installation, err := s.Queries.GetPluginInstallation(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return PluginActionCaller{}, pluginErrf(PluginErrorNotFound, "plugin installation not found")
	}
	if err != nil {
		return PluginActionCaller{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin installation", Err: err}
	}
	// 被禁用的插件是“关掉”，不只是“隐藏”：陈旧标签页里还开着的 iframe 不得在管理员禁用后继续工作。
	if !installation.Enabled {
		return PluginActionCaller{}, pluginErrf(PluginErrorForbidden, "this Plugin is disabled")
	}

	scopes := decodeScopes(installation.GrantedScopes)
	if scope != "" && !hasScope(scopes, scope) {
		return PluginActionCaller{}, &PluginError{
			Kind:    PluginErrorForbidden,
			Message: "this Plugin was not granted the " + scope + " scope",
		}
	}

	return PluginActionCaller{
		Installation: installation,
		WorkspaceID:  installation.WorkspaceID,
		UserID:       userID,
		Scopes:       scopes,
	}, nil
}

// hasScope 判断作用域列表是否包含目标作用域。
func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// parseInstallationID 解析安装 ID；解析失败统一报 not-found（避免泄露 ID 形状）。
func parseInstallationID(value string) (pgtype.UUID, error) {
	parsed, err := parseUUIDValue(value)
	if err != nil {
		return pgtype.UUID{}, pluginErrf(PluginErrorNotFound, "plugin installation not found")
	}
	return parsed, nil
}

// PluginContext 是面板在请求任何东西之前拿到的东西：谁在看、在哪、面板挂在哪个 issue 上。它不需要作用域，
// 因为它不含 iframe 前的用户在页面周边看不到的任何内容。
type PluginContext struct {
	Workspace PluginContextWorkspace `json:"workspace"`
	// 调用方是插件本身时 User 缺席——事件 hook 或常驻安装 token 背后没有人，在此编一个会骗 handler 以为自己在
	// 替某人行事。
	User        *PluginContextUser  `json:"user,omitempty"`
	Issue       *PluginContextIssue `json:"issue,omitempty"`
	Config      map[string]any      `json:"config"`
	GrantedURLs []string            `json:"granted_net_domains"`
	// Actor 标明是两者中的哪一个，handler 无需从缺失字段反推即可分支。
	Actor string `json:"actor"`
}

type PluginContextWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type PluginContextUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Email 刻意缺席：需要向后端标识成员的面板应使用不透明 id，而非邮箱。
}

type PluginContextIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// BuildPluginContext 装配上下文载荷。Config 只带非机密安装值——机密在自己的表里且没有读路径，
// 因此不可能有一条到达 iframe。
func (s *PluginService) BuildPluginContext(caller PluginActionCaller, workspace db.Workspace, user *db.User, issue *db.Issue) PluginContext {
	config := map[string]any{}
	if len(caller.Installation.Config) > 0 {
		_ = json.Unmarshal(caller.Installation.Config, &config)
	}

	payload := PluginContext{
		Workspace: PluginContextWorkspace{
			ID:   uuidString(workspace.ID),
			Name: workspace.Name,
			Slug: workspace.Slug,
		},
		Config:      config,
		GrantedURLs: plugincontract.NetDomains(caller.Scopes),
		Actor:       "plugin",
	}
	if user != nil {
		payload.Actor = "member"
		payload.User = &PluginContextUser{
			ID:   uuidString(user.ID),
			Name: user.Name,
		}
	}
	if issue != nil {
		payload.Issue = &PluginContextIssue{
			ID:         uuidString(issue.ID),
			Identifier: IssueIdentifier(workspace.IssuePrefix, issue.Number),
			Title:      issue.Title,
		}
	}
	return payload
}
