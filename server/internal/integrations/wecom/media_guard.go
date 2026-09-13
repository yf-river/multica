// media_guard.go —— 媒体抓取器指向的地址是「别人给的」。
//
// 回调会携带一个预签名 COS URL，我们去 GET 它。这个 URL 是从 socket 上收到的
// 字符串：企业微信确实把它放进了回调，但适配器无法证明这一点；而抓取是从部署
// 内网发出的，内网能触达什么它就能触达什么。单看这台机器，能触达的范围就包括
// Tailscale 的 tailnet（100.64.0.0/10）、代理的 fake-IP 段（198.18.0.0/15）、
// Docker 的各种 bridge、以及后端自己管理端点在监听的 loopback。
//
// 所以闸门不设在 URL 上，设在「连接」上。光检查主机名毫无价值：URL 会重定向
// （一条 302 到 http://169.254.169.254/ 不过是攻击者控制的一行响应而已），而且
// 一个刚通过检查的主机名转眼就能解析成别的东西。一个自己解析目的地、拒绝非公网
// 地址的 DialContext 会跑在每一次重定向的每一跳上，对着「实际正要连的那个地址」
// 做判断——这是两个问题能被同时覆盖的唯一位点。
//
// 设计原型出自 @leroy-chen 在另一个企微媒体 PR（#6524，
// newSecureMediaClient / isAllowedMediaIP）——那个 PR 最先提出这个问题，它的
// 修法就是正解。这里加了两样东西：下面这份保留段清单（靠 netip 自带谓词搭出的
// 列表会漏掉 CGNAT 邻近和 IETF 保留空间，而 Tailscale tailnet 恰好住在 CGNAT
// 里），以及一个可注入的解析器，让 DNS 重绑这种情形可以实测而不是靠嘴辩。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrMediaAddrBlocked 在媒体主机的每个解析地址都是「部署不应被指去的地方」时
// 返回，取代一次真实的连接。它刻意区别于拨号失败：调用方对两者记不同的日志，
// 而且只有这一个错误意味着「有人给了我们一条不该给的 URL」。
var ErrMediaAddrBlocked = errors.New("wecom: media host resolves to a non-public address")

// mediaDialTimeout 约束一次 TCP 连接。它嵌在约束整个抓取的
// mediaDownloadTimeout 内部。
const mediaDialTimeout = 10 * time.Second

// 保留段分两组，差别不是分类学——是「运维是否被允许放开它」。
//
// 两张表都来自 IANA IPv4 与 IPv6 Special-Purpose Address Registries，扣掉
// netip 自带谓词已覆盖的（IsLoopback / IsPrivate / IsLinkLocal* / IsMulticast /
// IsUnspecified），再扣掉少数「虽是特殊用途、却是普通全球单播」的段（AS112
// 委派的 192.31.196.0/24、192.175.48.0/24 和 2620:4f:8000::/48，以及 AMT 的
// 192.52.193.0/24）——它们「特殊」在归谁运营，不在指向哪里，拒掉它们一无所获。
//
// reservedMediaPrefixes 是「不是公网」的空间：媒体 URL 解析到这里，要么是错，
// 要么是图谋，两条路走到头都没有对象。100.64.0.0/10 是最要紧的一段——RFC 6598
// 共享地址空间，标准库没有任何谓词会把它报成私有，而 Tailscale 发的正是它。
// tailnet 里的一台对端，就是信任边界内一台「无凭据、按 IP 可达」的机器。
//
// 下面这组是 mediaAllowedPrefixes 能重新放开的，因为有一种真实部署形态住在里
// 面：fake-IP 代理对每个公网主机名都发 198.18.0.0/15 的地址——企微自己的 COS
// 主机也不例外。知道自家代理池子的运维可以说一声。
var reservedMediaPrefixes = []netip.Prefix{
	// ---- IPv4 ----
	netip.MustParsePrefix("0.0.0.0/8"),       // "本网络"
	netip.MustParsePrefix("100.64.0.0/10"),   // RFC 6598 CGNAT —— Tailscale 住这里
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF 协议分配
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // 基准测试用 —— 也是代理的 fake-IP 段
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // 保留，含 255.255.255.255
	netip.MustParsePrefix("192.88.99.0/24"),  // 已废弃的 6to4 中继 anycast —— 2002::/16 的 v4 端

	// ---- IPv6 ----
	netip.MustParsePrefix("100::/64"),       // 只丢弃
	netip.MustParsePrefix("100:0:0:1::/64"), // dummy 前缀，同样只丢弃
	// 整个 IETF 协议分配块，而不是它下面十几条子目。它装着 Teredo（2001::/32）、
	// 基准测试（2001:2::/48——上面 198.18.0.0/15 的 v6 孪生）、AMT、AS112-v6、
	// ORCHID/ORCHIDv2、DET，以及 PCP / TURN / DNS-SD 的 anycast 地址。里面没有
	// 任何一处是 COS 对象住的地方，而维护一条前缀比维护八条更容易不出错。v4 侧
	// 用 192.0.0.0/24 已经是同一个取舍。文档段是 2001:db8::/32，它在 /23 之外，
	// 单列在下。
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), // 文档段
	netip.MustParsePrefix("3fff::/20"),     // 文档段，RFC 9637
	netip.MustParsePrefix("5f00::/16"),     // SRv6 SID，RFC 9602 —— 路由标签，不是主机
	// 站点本地（site-local）。RFC 3879 已废弃它、IANA 也已除名，所以没有任何
	// 谓词或注册表条目覆盖它——但 2004 年前按它编号的网络至今仍在内部路由它，
	// 而它只占一行。
	netip.MustParsePrefix("fec0::/10"),
}

// translationMediaPrefixes 是「翻译」空间，任何配置都无法重新放开它。这些地址
// 不是目的地，而是「穿着 IPv6 外衣的 IPv4 目的地」：64:ff9b:1::a9fe:a9fe 一碰到
// NAT64 翻译器就是 169.254.169.254，2002:7f00:1::1 经 6to4 中继就是 127.0.0.1。
//
// 正是这件外衣造就了这组独立清单。前面的 IsLoopback / IsPrivate /
// IsLinkLocalUnicast 检查对它们永远不触发——此刻它们是 IPv6 地址，而一个只会按
// IPv4 写法认 IPv4 地址的闸门，对它们每一个都形同虚设。这张表是闸门和「藏在里
// 面的地址」之间的唯一屏障。若让 mediaAllowedPrefixes 覆盖它，就等于让
// MULTICA_WECOM_MEDIA_ALLOW_CIDRS=::/0——或者退一步，=2002::/16——以一种运维
// 从没意识到自己在放开的拼写，直通 loopback 和本闸门要拒的那个元数据端点。
//
// 所以它是硬阻断，而且零成本：COS 对象、代理池、任何部署都不住在翻译空间里，
// 这里没有 fake-IP 代理那种需要迁就的形态。
var translationMediaPrefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64，well-known 前缀
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64，本地使用前缀（RFC 8215）
	netip.MustParsePrefix("2002::/16"),      // 6to4 —— 末 112 位开出一个 IPv4 地址
}

// addrPolicy 回答一个已解析的地址是否允许被拨号。生产用 publicAddrOnly；测试
// 换成允许「测试自身服务器所在 loopback」的策略，这样被测的是闸门的决策，而不
// 是测试脚手架自己的地址。
type addrPolicy func(netip.Addr) bool

// mediaAllowedPrefixes 是运维声明「尽管看起来像保留段、但对媒体抓取是安全的」
// 的那些网段。它只为一种真实部署形态存在：一台挂在 fake-IP 代理后面的机器，其
// 解析器对每个公网主机名都答一个 198.18.0.0/15 里的地址、由代理把流量继续转出
// 去。在这种机器上，企微自己的 COS 主机和 link-local 元数据端点单看地址无法区
// 分，所以闸门会拒绝每个附件、入站媒体整个停摆。
//
// 默认空。放宽这里是有代价的决定：列进来的网段，都可能被一条「别人控制的 URL」
// 触达——这正是本闸门要防的东西。它按部署逐个 opt-in，只有当运维确知该网段属
// 于自家代理时才值得设。
//
// 无论开多宽都打不开的：loopback、私有段和 link-local——它们在本表被查阅的那层
// 循环之前就被拒了；以及 translationMediaPrefixes——在下一步用同样的理由被拒。
// 这些正是「开得过宽的 CIDR」会白送给 URL 提供者的网段。
var mediaAllowedPrefixes []netip.Prefix

// SetMediaAllowedPrefixes 声明媒体闸门可以拨号的网段。启动时由
// MULTICA_WECOM_MEDIA_ALLOW_CIDRS 调用。解析不了的条目会被报告并跳过，而不是
// 悄悄放宽或悄悄收紧闸门。
func SetMediaAllowedPrefixes(cidrs []string) []error {
	var (
		out  []netip.Prefix
		errs []error
	)
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("wecom: media allow cidr %q: %w", raw, err))
			continue
		}
		out = append(out, p)
	}
	mediaAllowedPrefixes = out
	return errs
}

// publicAddrOnly 是生产策略：凡不是「可路由的公网」一律拒绝。
func publicAddrOnly(a netip.Addr) bool {
	// IPv4-mapped 的 IPv6 地址（::ffff:127.0.0.1）在拆映射之前，任何 IPv4 谓词
	// 都不会命中——这就是它的小把戏所在。
	a = a.Unmap()
	if !a.IsValid() {
		return false
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return false
	}
	// 翻译空间排最前、而且完全不 consult 允许列表。藏在其中一个地址，本来就是
	// 上面那些检查一眼就会拒的 IPv4 地址；它们没触发，仅仅是因为拼写不同。运维
	// 配什么都不放它回来。
	for _, p := range translationMediaPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	for _, p := range reservedMediaPrefixes {
		if p.Contains(a) {
			// 运维可能已声明这段归他——fake-IP 代理的池子就是为它存在的。
			// 只在闸门本要拒绝的地址上才查允许列表，所以空 allow 列表让闸门
			// 和从前一样严格、一分不多。
			for _, allowed := range mediaAllowedPrefixes {
				if allowed.Contains(a) {
					return true
				}
			}
			return false
		}
	}
	return true
}

// hostResolver 是 net.DefaultResolver 上我们唯一需要的那一个方法，这样测试能
// 给「它想给的任何名字」回「它想给的任何地址」。
type hostResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// mediaGuard 是媒体客户端穿行其上的拨号器。
type mediaGuard struct {
	allow    addrPolicy
	resolve  hostResolver
	onRefuse func(host string, addr netip.Addr) // 测试钩子；生产为 nil
}

// dial 自己解析目的地，并连到一个「已检查过的地址」——绝不连主机名，因为解析
// 两次就是 DNS 重绑的窗口。
//
// 每个地址在拨号之前先全部检查一遍，连接只发给那个通过了字面量。一个主机既回
// 公网又回内网地址时，公网的那几个会被尝试、内网的被拒，而不是因为一个良答案
// 就把整个名字放行。
func (g mediaGuard) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("wecom: media dial: %w", err)
	}
	addrs, err := g.resolver().LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("wecom: media dial: resolve %s: %w", host, err)
	}
	allow := g.allow
	if allow == nil {
		allow = publicAddrOnly
	}

	d := &net.Dialer{Timeout: mediaDialTimeout}
	lastErr := error(ErrMediaAddrBlocked)
	for _, a := range addrs {
		if !allow(a) {
			if g.onRefuse != nil {
				g.onRefuse(host, a)
			}
			continue
		}
		conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func (g mediaGuard) resolver() hostResolver {
	if g.resolve != nil {
		return g.resolve
	}
	return net.DefaultResolver
}

// newMediaHTTPClient 构造每个媒体下载都要经过的那个客户端。
//
// 两道闸，管的是不同的东西。拨号器拒的是「目的地」；CheckRedirect 拒的是
// 「协议」，因为跳转到 file:// 或 gopher:// 根本到不了拨号器——传输层会高高兴
// 兴把它交给对应的协议处理器。
func newMediaHTTPClient(g mediaGuard) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = g.dial
	// 拿 COS 对象没有任何需要代理的地方，而在这里理会 HTTP_PROXY 会把抓取送
	// 到一个拨号闸门从未见过的地址上。
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("wecom: media download: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("wecom: media redirect to scheme %q refused", req.URL.Scheme)
			}
			return nil
		},
	}
}
