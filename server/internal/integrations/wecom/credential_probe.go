// credential_probe.go —— 证明「安装者真的掌控这个机器人」。
//
// 过去 BYO（自带）安装会把对话框里粘进去的任意一对凭据直接持久化。机器人 id
// 并不是秘密：它印在企业微信管理后台，而且任何工作区成员都能从
// GET /wecom/installations 读出来。只有 secret 才能证明什么，而当初没有任何
// 东西校验过它。
//
// 于是出了两个问题，两个都不需要内鬼才能干：
//
// 其一，路由槽是全局的。channel_installation 在 (channel_type,
// config->>'app_id') 上的唯一索引不包含工作区，所以部署上任何工作区的管理员
// 都能写一行、拿别人的机器人 id 配一个垃圾 secret，无限期占住这个槽。真正的主
// 人随后会撞上一个他根本看不见的工作区名字的冲突。
//
// 其二，更糟，回收（reclaim）是按同一个未经证明的 id 走的。真正主人只是
// DISCONNECT 过的机器人会处于 revoked，而 revoked 行正是
// ReclaimDeadChannelInstallationByAppID 会硬删的东西——连同它底下每个用户绑定、
// 会话绑定、挂起的 token 一起。所以局外人只要贴一个他从列表上抄来的 bot id，
// 就能毁掉别的工作区的绑定、逼着那边所有人重新绑定。
//
// 修复方式就是另外两个兄弟包已经用起来的做法：先证明控制权，再持久化。钉钉铸
// access token（dingtalk/byo_install.go），Slack 调 auth.test
// （slack/byo_install.go）。企微没有 REST 面，所以证明就用协议自己的握手——
// 拨号、aibot_subscribe、读裁定、挂断。密钥错会被非零 errcode 拒绝，这正是
// 我们要的信号。
//
// 探测绝不会打到别人正在连着的机器人上。企微每个机器人只允许一个在线订阅者，
// 所以对一个活的槽做探测会把真正主人的连接挤掉，主人的 supervisor 又会重连、
// 反过来把探测挤掉——而如果请求接着被拒绝，一次失败的安装就白把一台活机器人
// 打下线了。InstallationService.Upsert 强制了这一点：它先拿这个槽的 advisory
// 锁、读当前主人，只要活主人不是调用方自己的行就直接返回冲突，根本不会去探测
// （见 botSlotConflictErr）。能走到探测这一步的只有：空闲槽、revoked 行、孤儿
// 行、或调用方对自己机器人的重装——绝没有别人正在连着的。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// ErrCredentialsRejected 是企业微信在说「这对凭据不对」：密钥错、机器人已不存
// 在、机器人 API 模式被关。它必须被送到管理员面前，因为这是唯一一件管理员
// 能采取行动的事。
var ErrCredentialsRejected = errors.New("wecom: WeCom rejected this bot id and secret")

// ErrCredentialsUnverifiable 是其他一切情况——拨号失败、握手超时、断网。刻意和
// rejection 区分开：在部署只是够不到企微的时候告诉管理员「你凭据错了」，只会
// 让他们去轮换一把明明没问题的密钥。
var ErrCredentialsUnverifiable = errors.New("wecom: could not reach WeCom to verify this bot")

// rejectionErrCodes 是企微全局错误码（document/path/90313）中明确表达「拒绝这
// 对凭据本身」的那些——只有它们才有资格告诉管理员 Bot ID 或 secret 错了。
//
// 其余一切非零码一律归 ErrCredentialsUnverifiable，这是刻意的。企微只保证 0
// 表示成功；订阅路径还受频率与并发保护约束（45009、45033），平台自己也可能
// 出故障。把任何非零码都读成「密钥错」，会推着管理员去轮换一把没问题、且轮换
// 后不可找回的长连接密钥——这正是本文件要防的确切损害。所以这张表是白名单，
// 默认是「失败即关闭」：拒绝这次安装、不碰已存凭据、说明我们无法核验。
//
// 只按码判断、绝不看 errmsg 文本：那是对人展示的中文散文，企微随时可能改写措辞。
var rejectionErrCodes = map[int]struct{}{
	40001: {}, // 不合法的secret参数 —— secret 与这个机器人对不上
	40013: {}, // 不合法的CorpID —— 凭据对里的身份那一半无效
}

// classifySubscribeAck 把 aibot_subscribe ack 上的非零 errcode 归到上面两个
// 哨兵错误之一。
//
// 两个读这个 ack 的地方都走这里——安装时的这个探测，以及通道每次重连时自己的
// 握手（wecom_channel.go）。做成一个函数而不是同一份查表逻辑的两份拷贝，是为了
// 让二者对同一个码不可能给出不同答案：安装时一个 45009 限流应该是「无法核验，
// 等等再说」，绝不能到了连接路径上变成「去个人把这条安装修一下」。
//
// log 接收我们不认识的码；nil 时退回默认 logger。
func classifySubscribeAck(log *slog.Logger, errCode int, errMsg string) error {
	if _, rejected := rejectionErrCodes[errCode]; rejected {
		// 这是唯一一件管理员能采取行动的事，也是唯一把账算到凭据头上的分支。
		return fmt.Errorf("%w (errcode %d: %s)", ErrCredentialsRejected, errCode, errMsg)
	}
	// 未知的非零码：限流、平台侧故障，或者企微新加的错误码。宁可「失败即关闭」
	// 也不怪罪密钥。原始凭据对在这里记日志，让运维在调用方只透出哨兵错误时也
	// 能看到细节；错误上也带上它，这样调用方自己的日志行同样有。
	if log == nil {
		log = slog.Default()
	}
	log.Warn("wecom: unrecognized subscribe errcode, treating as unverifiable",
		"errcode", errCode, "errmsg", errMsg)
	return fmt.Errorf("%w (subscribe returned errcode %d: %s)", ErrCredentialsUnverifiable, errCode, errMsg)
}

// credentialProbeTimeout 给整个探测设了时间上限。它只需覆盖一次拨号加一次往返、
// 不能再多，而且这段时间是当着管理员的面等一个 spinner，所以要短。尺寸是从
// 连接自身那两个常量推出来的，而不是凭空定的。
const credentialProbeTimeout = handshakeTimeout + subscribeTimeout

// CredentialProbe 证明一对 (bot_id, secret) 是活的。导出是因为 handler 持有
// 它，这样测试不拉 socket 也能走安装路径；生产环境拿到的总是握手探测。
type CredentialProbe interface {
	Probe(ctx context.Context, botID, secret string) error
}

// handshakeProbe 是生产用的探测：一条连接、一次订阅，没有读取循环、没有登记、
// 不发布 sender。进程里其他任何东西都不知道它发生过。
type handshakeProbe struct {
	dialer Dialer
	wsURL  string
}

// NewHandshakeProbe 构造安装服务用的探测。dialer 和 wsURL 是测试接缝；生产
// 传 nil 和 ""。
func NewHandshakeProbe(dialer Dialer, wsURL string) *handshakeProbe {
	if wsURL == "" {
		wsURL = DefaultWSURL
	}
	return &handshakeProbe{dialer: dialer, wsURL: wsURL}
}

func (p *handshakeProbe) Probe(ctx context.Context, botID, secret string) error {
	dialer := p.dialer
	if dialer == nil {
		dialer = defaultDialer
	}

	ctx, cancel := context.WithTimeout(ctx, credentialProbeTimeout)
	defer cancel()

	conn, _, err := dialer.DialContext(ctx, p.wsURL, nil)
	if err != nil {
		return fmt.Errorf("%w: dial: %v", ErrCredentialsUnverifiable, err)
	}
	defer conn.Close()

	// 下面的读不感知 ctx，所以 deadline 既要设到 context 上、也要设到 socket 上。
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}

	reqID := newReqID()
	frame, err := json.Marshal(map[string]any{
		"cmd":     cmdSubscribe,
		"headers": frameHeaders{ReqID: reqID},
		"body":    subscribeBody(botID, secret),
	})
	if err != nil {
		return fmt.Errorf("%w: encode subscribe: %v", ErrCredentialsUnverifiable, err)
	}
	if err := conn.WriteMessage(websocketTextMessage, frame); err != nil {
		return fmt.Errorf("%w: send subscribe: %v", ErrCredentialsUnverifiable, err)
	}

	// 一路读到我们自己的 ack 回来。服务端可能先推别的帧；凡是答这个 req_id 的
	// 之外都不是我们的，一律跳过。
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrCredentialsUnverifiable, err)
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("%w: read subscribe ack: %v", ErrCredentialsUnverifiable, err)
		}
		var env frameEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Headers.ReqID != reqID {
			continue
		}
		if env.ErrCode != 0 {
			return classifySubscribeAck(slog.Default(), env.ErrCode, env.ErrMsg)
		}
		return nil
	}
}

// websocketTextMessage 是 gorilla 的 TextMessage 常量，这里不 import gorilla
// ——探测只写一帧，保持本文件的依赖面只到包已定义的 Dialer 接口为止。
const websocketTextMessage = 1
