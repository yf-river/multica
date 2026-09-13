// plugin_token.go —— 插件的两种凭据，方向相反：
// 安装令牌（install token）给“插件的服务器”用来向宿主证明自己，长效、可轮换；
// 回调令牌（callback token）由宿主发给“单次 hook 调用”，几分钟过期、绑定单次
// 调用，让 hook handler 能调用 Action API。存的是哈希；两种令牌都不会进 iframe。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 两种凭据，方向相反。
//
// 两者都永远不会进 iframe：一个表面（surface）仍什么都不持有，只能通过 postMessage
// 问宿主页要 Multica 的能力。有了 hook 之后改变的是——插件现在有了一台“服务器”，
// 而这台服务器需要一种被认出身份的方式。所以对这个系统诚实的描述不再是“插件没有
// 凭据”，而是“插件的凭据只在服务器之间流动”。
//
//	install token  (mpi_…)  插件 -> 宿主，长期有效、可轮换。
//	                        宿主只做验证，因此只存哈希，无法从库中还原出令牌本体。
//	callback token          宿主 -> 插件，几分钟过期、只用于一次 INVOCATION。
//	                        发给 hook handler，让它在不持有长期权限的前提下，
//	                        借助 Action API 完成这次调用的应答。

const (
	installTokenPrefix   = "mpi_"
	callbackTokenPrefix  = "mpc_"
	callbackTokenTTL     = 5 * time.Minute
	callbackTokenEntropy = 32
)

// IssueInstallToken 铸造一枚新的安装令牌，只存它的哈希。
//
// 明文只在返回这一刻给出一次。没有任何端点能把它读回来——管理员弄丢了就轮换、
// 而不是找回，这和产品里其他所有 bearer 凭据是同一个取舍。
func (s *PluginService) IssueInstallToken(ctx context.Context, installationID pgtype.UUID) (string, error) {
	raw := make([]byte, callbackTokenEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", &PluginError{Kind: PluginErrorUnavailable, Message: "generate install token", Err: err}
	}
	token := installTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if err := s.Queries.SetPluginInstallationToken(ctx, db.SetPluginInstallationTokenParams{
		ID:        installationID,
		TokenHash: pgtype.Text{String: hashToken(token), Valid: true},
	}); err != nil {
		return "", &PluginError{Kind: PluginErrorUnavailable, Message: "store install token", Err: err}
	}
	return token, nil
}

// InstallCredentials 是管理员轮换插件“长期凭据”时、只返回一次的响应。hook 签名被
// 禁用时 SigningSecret 为空；那种部署里安装令牌仍可用于 Public API。
type InstallCredentials struct {
	Token         string
	SigningSecret string
}

// RotateInstallCredentials 先把所有要返回的值都准备好，再替换库里存的令牌哈希。
// 这样“可选 hook 签名配置失败”不会在响应已经注定无法完成后、又顺手作废旧令牌。
func (s *PluginService) RotateInstallCredentials(ctx context.Context, installationID pgtype.UUID) (InstallCredentials, error) {
	var signingSecret string
	if len(s.DeploymentKey) > 0 {
		secret, err := s.HookSigningSecret(installationID)
		if err != nil {
			return InstallCredentials{}, err
		}
		signingSecret = secret
	}
	token, err := s.IssueInstallToken(ctx, installationID)
	if err != nil {
		return InstallCredentials{}, err
	}
	return InstallCredentials{Token: token, SigningSecret: signingSecret}, nil
}

// RevokeInstallToken 丢掉已存的哈希，之后任何拿着旧令牌的请求都对不上。轮换就是
// IssueInstallToken——它直接在原位上覆盖。
func (s *PluginService) RevokeInstallToken(ctx context.Context, installationID pgtype.UUID) error {
	if err := s.Queries.SetPluginInstallationToken(ctx, db.SetPluginInstallationTokenParams{
		ID:        installationID,
		TokenHash: pgtype.Text{},
	}); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "revoke install token", Err: err}
	}
	return nil
}

// AuthenticateInstallToken 把出示的令牌解析回它的安装记录。
func (s *PluginService) AuthenticateInstallToken(ctx context.Context, token string) (db.PluginInstallation, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, installTokenPrefix) {
		return db.PluginInstallation{}, pluginErrf(PluginErrorForbidden, "invalid plugin token")
	}
	installation, err := s.Queries.GetPluginInstallationByTokenHash(ctx, pgtype.Text{String: hashToken(token), Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PluginInstallation{}, pluginErrf(PluginErrorForbidden, "invalid plugin token")
	}
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin installation", Err: err}
	}
	if !installation.Enabled {
		return db.PluginInstallation{}, pluginErrf(PluginErrorForbidden, "this Plugin is disabled")
	}
	return installation, nil
}

// hashToken 计算令牌的 SHA-256 十六进制串。库里只存哈希，令牌本体无法从库恢复。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CallbackGrant 是一次被兑换的回调令牌所证明的东西。
//
// 它的范围就是该安装的范围，绝不会更宽：回调的存在是为了让 hook handler 完成叫它
// 来的那件活，而不是让一次带外请求做得比表面（surface）能做的还多。
type CallbackGrant struct {
	InstallationID pgtype.UUID
	WorkspaceID    pgtype.UUID
	HookKey        string
	Trigger        string
	// Actor 是“产生的写入归谁”，在 hook 被分发那一刻就已定下。handler 无法选择
	// 以别人的身份写。
	Actor HookActor
	// IssueID 把事件回调收窄到产生它的那个 issue。调用本身没有 issue 时为零值。
	IssueID   pgtype.UUID
	ExpiresAt time.Time
}

// CallbackTokens 签发并解析“绑定单次调用”的回调令牌。
//
// 作用域是“一次 INVOCATION”而不是“一次请求”——这个区别是跑真 handler 跑出来的，
// 不是靠推理想出来的。单次使用令牌看起来更严，也确实更严：参考 handler 先读 issue、
// 做个判断、再发一条评论，第二调用就死在已花掉的令牌上。任何“拿读到的东西干点事”
// 的 handler，两次调用都是地板价。
//
// 真正要紧的取舍不是“一次调用 vs 多次”，而是“这枚令牌 vs 安装的长期令牌”：一个
// 用回调令牌完不成活的 handler，就会索性改用安装令牌来写——而安装令牌永不过期、
// 也不绑调用。与其把作者往更强的凭据那边推，还不如给他们那枚稍微宽松点、但他们会
// 真的去用的令牌。
//
// 仍然框住它的边界：几分钟的有效期、该安装被授予的范围、分发时定下的 actor、以及
// 这次调用所关心的 issue。
//
// 令牌存在内存里。多实例部署或进程重启后，令牌只会“提前失效”而非“逾期仍有效”，
// 所以故障模式是 handler 看到一个 403——可见且可重试——而不是一笔授权活过它的
// 窗口。
type CallbackTokens struct {
	mu     sync.Mutex
	issued map[string]CallbackGrant
}

func NewCallbackTokens() *CallbackTokens {
	return &CallbackTokens{issued: make(map[string]CallbackGrant)}
}

// Issue 为一次 hook 调用铸造一枚令牌。
func (c *CallbackTokens) Issue(ctx context.Context, invocation HookInvocation) (string, error) {
	raw := make([]byte, callbackTokenEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", &PluginError{Kind: PluginErrorUnavailable, Message: "generate callback token", Err: err}
	}
	token := callbackTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	grant := CallbackGrant{
		InstallationID: invocation.Installation.ID,
		WorkspaceID:    invocation.Installation.WorkspaceID,
		HookKey:        invocation.Hook.Key,
		Trigger:        invocation.Trigger,
		Actor:          invocation.Actor,
		IssueID:        invocation.IssueID,
		ExpiresAt:      time.Now().Add(callbackTokenTTL),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()
	c.issued[hashToken(token)] = grant
	_ = ctx
	return token, nil
}

// Resolve 按令牌查授权。handler 需要几次就能用几次，直到过期；为什么这是对的边界，
// 见类型注释。
func (c *CallbackTokens) Resolve(token string) (CallbackGrant, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, callbackTokenPrefix) {
		return CallbackGrant{}, pluginErrf(PluginErrorForbidden, "invalid callback token")
	}
	key := hashToken(token)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked()
	grant, ok := c.issued[key]
	if !ok {
		return CallbackGrant{}, pluginErrf(PluginErrorForbidden, "callback token is expired or unknown")
	}
	if time.Now().After(grant.ExpiresAt) {
		delete(c.issued, key)
		return CallbackGrant{}, pluginErrf(PluginErrorForbidden, "callback token is expired or unknown")
	}
	return grant, nil
}

// Revoke 在到期前提前丢弃一个授权。hook 调用一结束就调用它，于是令牌在“它为之签发
// 的活”干完那一刻就失效，而不是在余下的窗口里继续躺尸。
func (c *CallbackTokens) Revoke(token string) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.issued, hashToken(strings.TrimSpace(token)))
}

// sweepLocked 清掉已过期的授权，免得长跑服务器上这张 map 无限长大。调用方须已持锁。
func (c *CallbackTokens) sweepLocked() {
	now := time.Now()
	for key, grant := range c.issued {
		if now.After(grant.ExpiresAt) {
			delete(c.issued, key)
		}
	}
}
