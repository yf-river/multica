// byo_install.go：BYO（自带应用）安装校验与落库——管理员把 bot 令牌（xoxb-）
// 和应用级令牌（xapp-）贴进 Multica 时，先验活、验同源，再加密入库。
//
// 与 OAuth 授权码安装不同，BYO 没有「点授权」交换这一步：用户自建 Slack 应用、
// 装进自己的工作区，然后把两个令牌直接贴过来。入库前本文件做四件事：
// 1) auth.test 实时校验 bot 令牌，顺带拿到 team id 与 bot 用户 id；
// 2) 用 bots.info 证明 bot 令牌与应用级令牌属于【同一个】 Slack 应用；
// 3) apps.connections.open 探活应用级令牌（确认 Socket Mode 真能拨通）；
// 4) 把两个令牌各自加密后随配置落库。
//
// 因为每个 BYO 应用都是独立的 Slack 应用（独立 bot 身份），同一个 Slack 工作
// 区可以按 agent 挂多个。专属的 Socket Mode 长连接在 slack_channel.go 消费
// 存好的 app 令牌；本文件只管校验与持久化。
package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrInvalidBotToken / ErrInvalidAppToken 由 RegisterBYO 在贴来的令牌格式不对
// 时返回（前缀错，或应用级令牌里解析不出 app id）。handler 把它们映射成 400，
// 让安装弹窗能给出精确提示而不是一句笼统的失败。
var (
	ErrInvalidBotToken = errors.New("slack: bot token must start with xoxb-")
	ErrInvalidAppToken = errors.New("slack: app-level token must start with xapp- and embed an app id")
	// ErrTokenAppMismatch：贴来的 bot 令牌和应用级令牌属于【不同的】 Slack 应用。
	// 把这种配对存库等于「假连通」：入站事件挂在应用级令牌那条 socket 上（按它
	// 的 app id 路由），而 @ 提及识别和出站却用 bot 令牌的身份，两者对不上就废了。
	ErrTokenAppMismatch = errors.New("slack: the bot token and app-level token are from different Slack apps")
)

// RegisterBYOParams 是一次 BYO 安装的入参：这个 bot 代表哪个 agent、谁在安装、
// 以及用户从自己的 Slack 应用里贴来的两个令牌。
type RegisterBYOParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	BotToken    string // xoxb-…，出站 Web API（chat.postMessage）用
	AppToken    string // xapp-…，本应用自己的 Socket Mode 连接（入站）用
}

// RegisterBYO 把用户自带的（BYO）Slack 应用装到一个 agent 名下。
//
// 流程：用户自建 Slack 应用并装进工作区后，把它的 bot 令牌（xoxb-）和应用级
// 令牌（xapp-）贴进来。这里没有 OAuth 授权码交换——我们通过 auth.test 实时
// 校验 bot 令牌（顺带拿到 team id 与 bot 用户 id），证明 bot 与应用级令牌属于
// 同一个应用，从应用级令牌里解析出真实 Slack app id，把两个令牌各自加密落盘，
// 最后持久化安装记录。
//
// 由于每个 BYO 应用都是独立 Slack 应用（独立 bot 身份），同一个 Slack 工作区
// 可以并存多个（每个 agent 一个）。存下的 config 携带真实 app id 供入站路由；
// persistInstall 以 (workspace, agent) 为主键落行，若该 app id 已被别的
// agent/工作区连走则拒绝这对组合。消费所存 app 令牌的 Socket Mode 连接在
// slack_channel.go；本方法只负责持久化安装记录。
func (s *InstallService) RegisterBYO(ctx context.Context, p RegisterBYOParams) (db.ChannelInstallation, error) {
	botToken := strings.TrimSpace(p.BotToken)
	appToken := strings.TrimSpace(p.AppToken)
	if !strings.HasPrefix(botToken, "xoxb-") {
		return db.ChannelInstallation{}, ErrInvalidBotToken
	}
	appID, err := parseSlackAppID(appToken)
	if err != nil {
		return db.ChannelInstallation{}, err
	}

	// 用 bot 令牌实时校验并获取 team 与 bot 用户 id。auth.test 拿着 bot 令牌
	// 认证，返回 bot 自己的用户 id——入站翻译里剥掉的 @ 提及身份就是它。
	auth, err := s.authTest(ctx, botToken)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("slack auth.test: %w", err)
	}
	if auth.TeamID == "" || auth.UserID == "" || auth.BotID == "" {
		return db.ChannelInstallation{}, errors.New("slack auth.test: response missing team_id / user_id / bot_id")
	}

	// 证明两个令牌属于【同一个】 Slack 应用：用 auth.test 返回的 bot id 调
	// bots.info 解析出 bot 的「所属」应用 id，并要求它与应用级令牌里嵌的那个
	// app id 相等。不做这步的话，贴 A 应用的 bot 令牌 + B 应用的 app 令牌也能
	// 「连通」但实为残废——入站走 B 的 socket（按 api_app_id=B 路由），@ 提及
	// 识别和出站却用 A 的 bot 身份/令牌（Niko review）。
	botAppID, err := s.botAppID(ctx, botToken, auth.BotID)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("slack bots.info: %w", err)
	}
	if botAppID != appID {
		return db.ChannelInstallation{}, ErrTokenAppMismatch
	}

	// 探活应用级令牌（确认 Socket Mode 真能拨通），绝不落一个将来永远收不到
	// 事件的死令牌。
	if err := s.validateAppToken(ctx, appToken); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("slack apps.connections.open: %w", err)
	}

	sealedBot, err := s.box.Seal([]byte(botToken))
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encrypt slack bot token: %w", err)
	}
	sealedApp, err := s.box.Seal([]byte(appToken))
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encrypt slack app token: %w", err)
	}
	cfgJSON, err := json.Marshal(installConfig{
		AppID:             appID,
		TeamID:            auth.TeamID,
		BotUserID:         auth.UserID,
		BotTokenEncrypted: base64.StdEncoding.EncodeToString(sealedBot),
		AppTokenEncrypted: base64.StdEncoding.EncodeToString(sealedApp),
	})
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encode slack installation config: %w", err)
	}

	// 每个 agent 只落一条 bot（行以 workspace + agent 为主键）。config 里带的
	// 真实 app id 供入站路由；persistInstall 若发现该 app 已被别的 agent/工作区
	// 连走，会拒绝这对组合。
	return s.persistInstall(ctx, installPersist{
		wsID:        p.WorkspaceID,
		agentID:     p.AgentID,
		installerID: p.InitiatorID,
		appIDKey:    appID,
		configJSON:  cfgJSON,
	})
}

// slackOpts 组装安装期 Web API 调用共用的 slack.Client 选项；支持 apiURL 覆盖，
// 这样测试能把请求指向 httptest server。Slack SDK 会把方法名拼到 endpoint 末尾，
// 所以 base URL 必须以斜杠结尾。每次调用都返回全新切片（可以安全往里 append）。
func (s *InstallService) slackOpts() []slack.Option {
	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	opts := []slack.Option{slack.OptionHTTPClient(httpClient)}
	if s.apiURL != "" {
		base := s.apiURL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		opts = append(opts, slack.OptionAPIURL(base))
	}
	return opts
}

// authTest 拿 bot 令牌调 Slack auth.test：既校验令牌有效，又返回 team id、bot
// 自己的用户 id 以及 bot id（后者供 bots.info 查询用）。
func (s *InstallService) authTest(ctx context.Context, botToken string) (*slack.AuthTestResponse, error) {
	return slack.New(botToken, s.slackOpts()...).AuthTestContext(ctx)
}

// botAppID 通过 bots.info（传入 auth.test 返回的 bot id）解析出 bot 的【所属】
// Slack 应用 id。对 bot 令牌来说这是唯一的 token->app_id 路径，正是靠它证明
// 贴来的 bot + 应用级令牌同属一个应用。
func (s *InstallService) botAppID(ctx context.Context, botToken, botID string) (string, error) {
	bot, err := slack.New(botToken, s.slackOpts()...).GetBotInfoContext(ctx, slack.GetBotInfoParameters{Bot: botID})
	if err != nil {
		return "", err
	}
	return bot.AppID, nil
}

// validateAppToken 通过 apps.connections.open 确认应用级令牌能拨通一条 Socket
// Mode 连接——实时验证这条 xapp 对【当前这个应用】有效，避免存下一个将来收不
// 到任何事件的静默令牌。
func (s *InstallService) validateAppToken(ctx context.Context, appToken string) error {
	api := slack.New("", append(s.slackOpts(), slack.OptionAppLevelToken(appToken))...)
	_, _, err := api.StartSocketModeContext(ctx)
	return err
}

// parseSlackAppID 从应用级令牌文本里抽出真实 Slack app id。令牌格式是
// `xapp-1-<APP_ID>-<gen>-<secret>`（例：xapp-1-A0BCXGVCS7R-…），app id 是
// 第三段。它是按应用存/路由的键，让同一个 Slack 工作区里多个 BYO 应用能共存。
func parseSlackAppID(appToken string) (string, error) {
	if !strings.HasPrefix(appToken, "xapp-") {
		return "", ErrInvalidAppToken
	}
	parts := strings.SplitN(appToken, "-", 5)
	// Slack 的 app id 恒以大写 A 打头，这是格式本身的规律：拿它当第二道校验，
	// 能拦下「凑巧以 xapp- 开头但根本不是合法 app id」的噪音输入。
	if len(parts) < 4 || parts[2] == "" || !strings.HasPrefix(parts[2], "A") {
		return "", ErrInvalidAppToken
	}
	return parts[2], nil
}
