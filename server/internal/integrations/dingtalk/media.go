// media.go：入站图片媒体的解析与下载——把机器人消息回调里的 downloadCode 换成
// 真实图片字节，落进对象存储，并把引用挂回归一化后的消息。
//
// 数据流（engine.MediaResolver 的钉钉实现）：媒体属于「重活」，被共享 Router 放
// 到去重/身份/成员校验之后、异步做。对每条消息最多并行下载 2 张图片：先用钉钉的
// messageFiles/download 接口把短命 downloadCode 换成临时下载 URL（URL 是带签的
// 短期凭证，绝不落库、绝不打日志），再限时抓取、校验内容类型、上传对象存储，最后
// 填进 channel.InboundMessage.MediaRefs。
//
// 安全内核：下载 URL 由钉钉签发自外部，属于不可信的出站目标。钉钉可能返回 http
// 明文 URL，必须按原 scheme 抓取；但连到哪台机器必须由我们自己把关——本文件做了
// 一套防 SSRF 的自解析拨号（先解析、验公网、再直连 IP），挡住 DNS rebinding 把
// 连接带进内网。
package dingtalk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 钉钉对入站图片不设公开上限，这里把适配器的内存与远端 I/O 预算刻意压到共享
// Router 45 秒媒体截止时间之下：最多同时缓冲两张 10 MiB 的下载。
const (
	maxImagesPerMessage   = 4
	maxInboundImageBytes  = 10 << 20
	imageFetchTimeout     = 30 * time.Second
	mediaFetchConcurrency = 2
	maxDownloadRedirects  = 3
)

var allowedImageTypes = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"image/bmp":  ".bmp",
}

// mediaResolver 是 engine.MediaResolver 的钉钉实现。client 负责换 downloadCode、
// decrypt 还原安装凭据、store 存图片、ledger 登记媒体意图、fetch 是带了 SSRF
// 防护的下载 HTTP client。
type mediaResolver struct {
	client  *Client
	decrypt Decrypter
	store   storage.Storage
	ledger  engine.MediaIntentLedger
	logger  *slog.Logger
	fetch   *http.Client
}

var _ engine.MediaResolver = (*mediaResolver)(nil)

// NewMediaResolver 构造钉钉媒体解析器。
func NewMediaResolver(client *Client, decrypt Decrypter, store storage.Storage, ledger engine.MediaIntentLedger, logger *slog.Logger) engine.MediaResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &mediaResolver{
		client:  client,
		decrypt: decrypt,
		store:   store,
		ledger:  ledger,
		logger:  logger,
		fetch:   newMediaHTTPClient(),
	}
}

// 钉钉需要鉴权的 messageFiles/download 接口可能返回公开的 HTTP 明文 URL，也可能
// 返回 HTTPS。适配器必须照单全收、不重写 scheme；但这个 URL 始终是不可信的出站
// 输入：这里自己解析主机名、拒绝一切非公网结果、直接拨已验证的 IP——这样 DNS
// rebinding 没法把连接重定向进内网。禁用代理，是因为代理会替我们二次解析目标、
// 绕开这层保证。
func newMediaHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&publicDownloadDialer{
		resolver: net.DefaultResolver,
		dialer:   &net.Dialer{Timeout: imageFetchTimeout},
	}).DialContext
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxDownloadRedirects {
				return errors.New("too many redirects")
			}
			// net/http 默认会拿上一个 URL 当 Referer。下载 URL 的查询串是短命 bearer
			// 凭证，绝不能跟着跳转请求泄漏出去——包括本应放行的同源跳转。
			req.Header.Del("Referer")
			if err := validateDownloadURL(req.URL); err != nil {
				return err
			}
			previous := via[len(via)-1].URL
			if strings.EqualFold(previous.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
				return errors.New("disallowed HTTPS download redirect downgrade")
			}
			if strings.EqualFold(req.URL.Scheme, "http") && !sameDownloadOrigin(previous, req.URL) {
				return errors.New("disallowed cross-origin HTTP download redirect")
			}
			return nil
		},
	}
}

// publicDownloadDialer 拦截每次出站拨号：自己解析主机 → 逐 IP 校验公网 → 只拨
// 已放行的 IP。
type publicDownloadDialer struct {
	resolver *net.Resolver
	dialer   *net.Dialer
}

func (d *publicDownloadDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid download target address")
	}
	addrs, err := d.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("resolve download target failed")
	}
	for _, addr := range addrs {
		if !isPublicDownloadAddress(addr) {
			return nil, errors.New("blocked non-public download target")
		}
	}
	// 全部候选都验过是公网后，逐个试连。验证在应用层解析阶段做，而 Go 的
	// net.Dialer 默认会在系统层再解析一遍主机名——所以这里必须把已验 IP 拼回
	// 地址直连，不能把 host 再交回给 dialer。
	for _, addr := range addrs {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("connect to download target failed")
}

// lookup 把主机名解析成 IP 列表；本身已是 IP 的字面量直接返回（并归一化掉 IPv4
// 映射的 IPv6 形态）。
func (d *publicDownloadDialer) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}, nil
	}
	addrs, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	return addrs, nil
}

// 明确拒收的非公网前缀。大多是文档保留段（TEST-NET、benchmark、文档用）与
// 特殊用途段。
var nonPublicDownloadPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// IPv6 过渡机制可以把一个本应被拦的 IPv4 目标封装在 netip 判为「全局单播」的
	// 地址里。下载客户端用不到这些老的/局部的过渡网段，与其逐个解码各家部署特有的
	// 映射（还有绕进 loopback 或 RFC1918 的风险），不如直接默认拒绝。
	netip.MustParsePrefix("::/96"),          // deprecated IPv4-compatible（已废弃的 IPv4 兼容地址）
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64（局部用途 NAT64）
	netip.MustParsePrefix("100::/64"),       // discard-only（仅丢弃）
	netip.MustParsePrefix("2001::/32"),      // Teredo
	netip.MustParsePrefix("2001:2::/48"),    // benchmarking
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:10::/28"), // deprecated ORCHID
	netip.MustParsePrefix("2001:20::/28"), // ORCHIDv2
	netip.MustParsePrefix("2002::/16"),    // 6to4
	netip.MustParsePrefix("3fff::/20"),    // documentation
}

var wellKnownNAT64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// isPublicDownloadAddress 判断一个 IP 是否可作下载目标（公网且非受限段）。先做
// netip 自带分类，再叠加手工前缀表；标准 NAT64 前缀则递归校验其内嵌的 IPv4。
func isPublicDownloadAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	if wellKnownNAT64Prefix.Contains(addr) {
		// 标准 NAT64 前缀把 IPv4 地址放在低 32 位。合成目标本身可以放行，但内嵌的
		// IPv4 必须再过一遍完整拒绝策略——否则攻击者用一条看起来是全局 IPv6 的
		// AAAA 记录就能把 loopback / RFC1918 偷渡进来。
		raw := addr.As16()
		return isPublicDownloadAddress(netip.AddrFrom4([4]byte(raw[12:16])))
	}
	for _, prefix := range nonPublicDownloadPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// sameDownloadOrigin 判断两个 URL 是否同源（scheme 与 host 忽略大小写相等）。
func sameDownloadOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// HasMedia 纯解码已收到的回调元数据判断消息是否带媒体，不做任何远端调用。真正的
// 远端工作（下载/上传）在去重、身份与成员校验之后的 ResolveMedia 里做。
func (m *mediaResolver) HasMedia(msg channel.InboundMessage) bool {
	raw, err := decodeDingTalkRaw(msg)
	return err == nil && len(raw.Media) > 0
}

// ResolveMedia 是引擎在身份/去重/成员校验通过后回调的媒体解析入口：把消息里每个
// downloadCode 下载成图片并传进对象存储，能成功的以 MediaRef 挂在消息上，失败逐
// 张记 warn、不整体失败。下载是横向并发、受 mediaFetchConcurrency 限制。
func (m *mediaResolver) ResolveMedia(ctx context.Context, inst engine.ResolvedInstallation, _ engine.ResolvedIdentity, _ pgtype.UUID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage {
	raw, err := decodeDingTalkRaw(msg)
	if err != nil || len(raw.Media) == 0 {
		return msg
	}
	if len(raw.Media) > maxImagesPerMessage {
		m.logWarn(msg, fmt.Errorf("%d images exceed the limit of %d", len(raw.Media), maxImagesPerMessage))
		return msg
	}
	if m.client == nil || m.store == nil || m.ledger == nil {
		m.logWarn(msg, errors.New("media dependency missing"))
		return msg
	}
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		m.logWarn(msg, errors.New("installation platform row unavailable"))
		return msg
	}
	creds, err := decodeCredentials(row.Config, m.decrypt)
	if err != nil {
		m.logWarn(msg, fmt.Errorf("decode credentials: %w", err))
		return msg
	}

	refs := make([]channel.MediaRef, len(raw.Media))
	valid := make([]bool, len(raw.Media))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(mediaFetchConcurrency)
	for i, resource := range raw.Media {
		g.Go(func() error {
			key := dingtalkMediaObjectKey(inst, chatMessageID, resource, i)
			link := m.store.ObjectURL(key)
			owned, err := m.ledger.RecordPendingMediaObject(gctx, engine.RecordPendingMediaObjectParams{
				StorageKey:     key,
				WorkspaceID:    inst.WorkspaceID,
				ChatMessageID:  chatMessageID,
				StorageURL:     link,
				InstallationID: inst.ID,
			})
			if err != nil {
				m.logWarn(msg, fmt.Errorf("record media intent: %w", err))
				return nil
			}
			if !owned {
				m.logWarn(msg, errors.New("media key owned by reconciler"))
				return nil
			}
			data, contentType, err := m.fetchResource(gctx, creds, resource)
			if err != nil {
				m.logWarn(msg, err)
				return nil
			}
			ext := allowedImageTypes[contentType]
			filename := fmt.Sprintf("dingtalk-image-%d%s", i+1, ext)
			if _, err := m.store.Upload(gctx, key, data, contentType, filename); err != nil {
				m.logWarn(msg, fmt.Errorf("upload image: %w", err))
				return nil
			}
			refs[i] = channel.MediaRef{
				Type:              channel.MsgTypeImage,
				StorageKey:        key,
				StorageURL:        link,
				Filename:          filename,
				MimeType:          contentType,
				SizeBytes:         int64(len(data)),
				InlinePlaceholder: dingtalkImagePlaceholder,
				InlineIndex:       resource.InlineIndex,
			}
			valid[i] = true
			return nil
		})
	}
	_ = g.Wait()
	for i, ref := range refs {
		if valid[i] {
			msg.MediaRefs = append(msg.MediaRefs, ref)
		}
	}
	return msg
}

// dingtalkMediaObjectKey 为一张图片派生对象存储键：把「消息 id + 资源引用 +
// 序号」做 SHA-256 得到定长十六进制名，再挂到该安装所属工作区/安装的 dingtalk
// 目录下。键与钉钉的 downloadCode 无直观对应（下载码是短命凭证，不落进存储路径）。
func dingtalkMediaObjectKey(inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, resource dingtalkMediaResource, index int) string {
	sum := sha256.Sum256([]byte(util.UUIDToString(chatMessageID) + "\x00" + resource.Ref + "\x00" + fmt.Sprint(index)))
	return path.Join("workspaces", util.UUIDToString(inst.WorkspaceID), "dingtalk", util.UUIDToString(inst.ID), hex.EncodeToString(sum[:]))
}

// fetchResource 抓一个媒体资源：优先用主引用 code；失败且存在不同的备用 code 时
// 落到备用；两条都失败就 errors.Join 合并两段原因返回。
func (m *mediaResolver) fetchResource(ctx context.Context, creds credentials, resource dingtalkMediaResource) ([]byte, string, error) {
	data, contentType, primaryErr := m.fetchByCode(ctx, creds, resource.Ref)
	if primaryErr == nil || resource.Alt == "" || resource.Alt == resource.Ref {
		return data, contentType, primaryErr
	}
	data, contentType, fallbackErr := m.fetchByCode(ctx, creds, resource.Alt)
	if fallbackErr == nil {
		return data, contentType, nil
	}
	return nil, "", errors.Join(
		fmt.Errorf("primary media reference: %w", primaryErr),
		fmt.Errorf("fallback media reference: %w", fallbackErr),
	)
}

// fetchByCode 先用 code 换临时下载 URL，再抓字节。
func (m *mediaResolver) fetchByCode(ctx context.Context, creds credentials, code string) ([]byte, string, error) {
	downloadURL, err := m.client.messageFileDownloadURL(ctx, creds.AppKey, creds.AppSecret, creds.RobotCode, code)
	if err != nil {
		return nil, "", fmt.Errorf("resolve download url: %w", err)
	}
	return m.fetchBytes(ctx, downloadURL)
}

// fetchBytes 带 SSRF 防护地把图片字节抓下来：校验 URL 形态、限制大小与超时、按
// 内容头/内容嗅探判断类型，只放行白名单图片类型。
func (m *mediaResolver) fetchBytes(ctx context.Context, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", errors.New("invalid image download URL")
	}
	if err := validateDownloadURL(parsed); err != nil {
		return nil, "", err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", errors.New("build image download request failed")
	}
	resp, err := m.fetch.Do(req)
	if err != nil {
		// net/http 的 *url.Error 会带上完整的请求 URL。钉钉下载 URL 是短命 bearer
		// 凭证，日志前必须解包、绝不让带签查询串落进日志。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, "", fmt.Errorf("download image request failed: %w", urlErr.Err)
		}
		return nil, "", errors.New("download image request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download image: http %d", resp.StatusCode)
	}
	// 多读 1 字节以探测超限（LimitReader 上限 +1），避免刚好等于上限的字节数被误判。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxInboundImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if len(data) > maxInboundImageBytes {
		return nil, "", fmt.Errorf("image exceeds the %d MB limit", maxInboundImageBytes>>20)
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	// 内容类型信内容嗅探而不是钉钉的声明：声明可被伪造，而真实字节会被用于渲染。
	contentType := http.DetectContentType(sniff)
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
	if _, ok := allowedImageTypes[contentType]; !ok {
		return nil, "", fmt.Errorf("disallowed content type %q", contentType)
	}
	return data, contentType, nil
}

// validateDownloadURL 校验下载 URL 的形态：必须有主机、不能带用户信息或 fragment
// （fragment 不会上 wire，但混进来说明 URL 可疑），scheme 只收 http/https。
func validateDownloadURL(parsed *url.URL) error {
	if parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid image download URL shape")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("invalid image download URL scheme %q", parsed.Scheme)
	}
	return nil
}

// logWarn 是单张图片失败的统一告警出口（不进审计，只记 warn）。
func (m *mediaResolver) logWarn(msg channel.InboundMessage, err error) {
	m.logger.Warn("dingtalk media resolve skipped", "message_id", msg.MessageID, "error", err)
}
