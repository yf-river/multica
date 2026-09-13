// bot_identity.go：查「机器人在群里的可读名字」并缓存，供入站归一化与群存在性
// 元数据用。
//
// 背景：钉钉机器人消息回调里没有机器人自己的身份字段。为了让入站消息能剥离「@
// 机器人」的提及、以及把「群里看到的 bot 名字」记进产品元数据，需要调钉钉的群
// 机器人列表接口：POST /v1.0/robot/groups/robots/query 一次返回群内全部机器人，
// 我们按 robotCode 精确匹配出那个名字。openConversationId 换名字。
//
// 查询要带 qyapi_chat_manage 权限。拿不到该权限（钉钉按「应用」而非按「群」授
// 权）时，正常态就是把名字留空继续跑，并缓存这次拒绝避免每条入站都去撞一次接口。
package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	groupBotsPath        = "/v1.0/robot/groups/robots/query"
	botNameLookupTimeout = 3 * time.Second
	botNameCacheTTL      = time.Hour
	botNameErrorCacheTTL = time.Minute
	// qyapi_chat_manage 按钉钉应用授予、不按群授予。同一 bot 的缺权限拒绝要跨群
	// 短暂缓存，否则「正常缺权限」状态会变成每条入站消息都打一次 OpenAPI。30 秒
	// 的上限让「补权限后再 @ 一次」的补救路径能及时生效。
	botNamePermissionCacheTTL = 30 * time.Second
	botNameCacheMaxSize       = 4096

	dingTalkPermissionDeniedCode      = "Forbidden.AccessDenied.AccessTokenPermissionDenied"
	botIdentityIssueMissingChatManage = "missing_qyapi_chat_manage"
)

var errMissingChatManagePermission = errors.New("dingtalk: qyapi_chat_manage permission is required")

type groupBotListResponse struct {
	Bots []groupBotListItem `json:"chatbotInstanceVOList"`
}

type groupBotListItem struct {
	RobotCode string `json:"robotCode"`
	Name      string `json:"name"`
}

// botNameInGroup 返回某个精确 robotCode 的机器人在指定群里的可读名字。接口返回
// 群内全部机器人；调用方绝不允许按列表位置猜身份，也不许把别的机器人的元数据落库。
func (c *Client) botNameInGroup(ctx context.Context, appKey, appSecret, robotCode, conversationID string) (string, error) {
	if strings.TrimSpace(robotCode) == "" || strings.TrimSpace(conversationID) == "" {
		return "", errors.New("dingtalk: bot-name lookup requires robot code and conversation id")
	}

	body := map[string]string{"openConversationId": conversationID}
	var out groupBotListResponse
	token, err := c.accessToken(ctx, appKey, appSecret)
	if err != nil {
		return "", err
	}
	err = c.postJSON(ctx, groupBotsPath, token, body, &out)
	if errors.Is(err, errUnauthorized) {
		c.invalidate(appKey)
		if token, err = c.accessToken(ctx, appKey, appSecret); err != nil {
			return "", err
		}
		err = c.postJSON(ctx, groupBotsPath, token, body, &out)
	}
	if err != nil {
		var apiErr *apiRequestError
		if errors.As(err, &apiErr) &&
			apiErr.Code == dingTalkPermissionDeniedCode &&
			strings.Contains(apiErr.Message, "qyapi_chat_manage") {
			return "", fmt.Errorf("%w: %v", errMissingChatManagePermission, err)
		}
		return "", err
	}
	for _, bot := range out.Bots {
		if bot.RobotCode != robotCode {
			continue
		}
		name := strings.TrimSpace(bot.Name)
		if name == "" {
			return "", fmt.Errorf("dingtalk: robot %q has no readable name in group %q", robotCode, conversationID)
		}
		return name, nil
	}
	return "", fmt.Errorf("dingtalk: robot %q is absent from group %q bot list", robotCode, conversationID)
}

// cachedBotName 是一条缓存的查询结果：名字（或错误）+ 过期时刻。
type cachedBotName struct {
	name      string
	err       error
	expiresAt time.Time
}

// botNameFlightResult 是 singleflight 合流后带回的结果，额外记录本次实际查询用的
// 群——成功结果跨群共享时，调用方需要知道它是在哪个群里验出来的。
type botNameFlightResult struct {
	name                 string
	sourceConversationID string
}

// BotNameResolver 通过一个被观察到的群解析机器人真实的钉钉身份。机器人的名字与
// 应用维度的缺权限拒绝跨群缓存；只有群相关的失败才按群分键。成功的名字每小时刷
// 新一次，让改名能收敛；权限拒绝 30 秒后刷新，保证「补权限后再 @」的补救路径
// 仍然灵敏。
type BotNameResolver struct {
	client  *Client
	decrypt Decrypter
	now     func() time.Time
	timeout time.Duration
	maxSize int

	mu    sync.Mutex
	cache map[string]cachedBotName
	group singleflight.Group
}

// NewBotNameResolver 构造解析器；client 缺省指向真实的共享出站 Client。
func NewBotNameResolver(client *Client, decrypt Decrypter) *BotNameResolver {
	if client == nil {
		client = NewClient(nil, "")
	}
	return &BotNameResolver{
		client:  client,
		decrypt: decrypt,
		now:     time.Now,
		timeout: botNameLookupTimeout,
		maxSize: botNameCacheMaxSize,
		cache:   make(map[string]cachedBotName),
	}
}

// Resolve 从安装行里解密出凭据，再按 (安装, 群) 解析机器人的可读名字。
func (r *BotNameResolver) Resolve(ctx context.Context, installation db.ChannelInstallation, conversationID string) (string, error) {
	credentials, err := decodeCredentials(installation.Config, r.decrypt)
	if err != nil {
		return "", err
	}
	return r.resolveCredentials(ctx, credentials, conversationID)
}

// resolveCredentials 是适配器入站摄取的入口。Stream 连接已经持有解密后的凭据，
// 归一化路径因此能直接复用与「群存在性发现」同一套缓存与 OpenAPI 实现，既免掉一
// 次数据库往返、也不需要第二个解析器。
func (r *BotNameResolver) resolveCredentials(ctx context.Context, credentials credentials, conversationID string) (string, error) {
	identityKey := "identity\x00" + credentials.AppKey + "\x00" + credentials.RobotCode
	if cached, ok := r.cached(identityKey); ok {
		return cached.name, cached.err
	}
	groupKey := "group\x00" + credentials.AppKey + "\x00" + credentials.RobotCode + "\x00" + conversationID
	if cached, ok := r.cached(groupKey); ok {
		return cached.name, cached.err
	}

	resolved, err := r.resolveFlight(ctx, identityKey, identityKey, groupKey, credentials, conversationID)
	if err == nil || errors.Is(err, errMissingChatManagePermission) ||
		resolved.sourceConversationID == "" || resolved.sourceConversationID == conversationID {
		return resolved.name, err
	}

	// 成功与 qyapi_chat_manage 拒绝都是「应用」维度的，身份 flight 跨群共享是安全
	// 的。其它失败可能取决于领飞者用的那个群（比如机器人不在那个群里），所以改用
	// 调用方自己的群再查一次，而不是把这个错误原样返回。
	resolved, err = r.resolveFlight(ctx, groupKey, identityKey, groupKey, credentials, conversationID)
	return resolved.name, err
}

func (r *BotNameResolver) resolveFlight(
	ctx context.Context,
	flightKey string,
	identityKey string,
	groupKey string,
	credentials credentials,
	conversationID string,
) (botNameFlightResult, error) {
	result := r.group.DoChan(flightKey, func() (any, error) {
		// 排队等 flight 的功夫里缓存可能已被领飞者填好；直接复用。
		if cached, ok := r.cached(identityKey); ok {
			return botNameFlightResult{name: cached.name}, cached.err
		}
		if cached, ok := r.cached(groupKey); ok {
			return botNameFlightResult{
				name:                 cached.name,
				sourceConversationID: conversationID,
			}, cached.err
		}
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.timeout)
		defer cancel()
		name, lookupErr := r.client.botNameInGroup(
			lookupCtx,
			credentials.AppKey,
			credentials.AppSecret,
			credentials.RobotCode,
			conversationID,
		)
		ttl := botNameCacheTTL
		if lookupErr != nil {
			if errors.Is(lookupErr, errMissingChatManagePermission) {
				// 缺权限是应用维度的：按身份键缓存，所有群共享同一拒绝结论。
				r.store(identityKey, cachedBotName{
					err:       lookupErr,
					expiresAt: r.now().Add(botNamePermissionCacheTTL),
				})
				return botNameFlightResult{sourceConversationID: conversationID}, lookupErr
			}
			// 其它失败按群缓存一分钟——可能只是这个群查不到，换个群会好。
			ttl = botNameErrorCacheTTL
			r.store(groupKey, cachedBotName{name: "", err: lookupErr, expiresAt: r.now().Add(ttl)})
			return botNameFlightResult{sourceConversationID: conversationID}, lookupErr
		}
		// 查到的名字按身份键缓存一小时，让改名在小时内收敛。
		r.store(identityKey, cachedBotName{name: name, expiresAt: r.now().Add(ttl)})
		return botNameFlightResult{name: name, sourceConversationID: conversationID}, nil
	})
	select {
	case <-ctx.Done():
		return botNameFlightResult{}, ctx.Err()
	case resolved := <-result:
		return resolved.Val.(botNameFlightResult), resolved.Err
	}
}

// cached 读一条未过期的缓存条目。
func (r *BotNameResolver) cached(key string) (cachedBotName, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.cache[key]
	if !ok || !value.expiresAt.After(r.now()) {
		return cachedBotName{}, false
	}
	return value, true
}

// store 写入一条缓存，必要时先做容量维护：先清掉已过期的条目，仍超 maxSize 再按
// 「最早过期」淘汰一个（比随机淘汰更贴近 LRU 的效果）。
func (r *BotNameResolver) store(key string, value cachedBotName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cache[key]; exists {
		r.cache[key] = value
		return
	}
	now := r.now()
	for cachedKey, cached := range r.cache {
		if !cached.expiresAt.After(now) {
			delete(r.cache, cachedKey)
		}
	}
	if r.maxSize > 0 && len(r.cache) >= r.maxSize {
		var oldestKey string
		var oldestExpiry time.Time
		for cachedKey, cached := range r.cache {
			if oldestKey == "" || cached.expiresAt.Before(oldestExpiry) {
				oldestKey, oldestExpiry = cachedKey, cached.expiresAt
			}
		}
		delete(r.cache, oldestKey)
	}
	r.cache[key] = value
}
