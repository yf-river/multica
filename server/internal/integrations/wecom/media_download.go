// media_download.go —— 抓取回调里指向的那段字节。
//
// URL 是一个预签名的腾讯 COS 地址：不带 access_token、不带 header、五分钟有效。
// 这让抓取本身变得平凡，把全部的讲究放到了别处——正文以密文到达、它的大小在
// 回调里没有任何地方声明、而唯一描述「这个文件是什么」的信息来自响应 header。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

// maxMediaBytes 是单次下载正文的上限。企微把智能机器人文件和视频都限在 100 MB，
// 图片则没有文档化上限，所以这个数就是一切的顶：超过它的东西，本来就不该是回
// 调塞给我们的。
//
// 在本路径上正文是整体缓冲的——CBC 解密要先见尾才能信头——所以这个上限同时也
// 是单次下载的内存边界，再乘上路由器的媒体并发数。本路径是兜底：实现了
// UploadStream 的存储后端（两个随附后端都有）改走 media_stream.go，那里同一个
// 上限约束的是磁盘。
const maxMediaBytes = 100 << 20

// mediaDownloadTimeout 约束单次抓取。路由器本来就在「45 秒、由消息上所有附件与
// 队列前面的东西共享」的预算下跑媒体解析，所以一个慢对象绝不能把预算全吃掉。
const mediaDownloadTimeout = 30 * time.Second

// errMediaTooLarge 在正文超过 maxMediaBytes 时返回——无论是 Content-Length 声
// 明的，还是读到一半才发现的。调用方拿它做匹配，向用户说明是「文件太大」而不
// 是「哪里出错了」。
var errMediaTooLarge = fmt.Errorf("wecom: media exceeds the %d byte limit", maxMediaBytes)

// mediaHeaders 是响应关于这个文件说了什么。两条抓取路径都返回它，这样缓冲式和
// 流式入站学到的是同样几件事。
type mediaHeaders struct {
	// Filename 是从 Content-Disposition 解析出的展示名，可能为空。回调 body 自
	// 身不携带任何名字、大小或 MIME 类型，所以这个 header 是原始名字存在的唯一
	// 地方。
	Filename string
	// Disposition 是 Content-Disposition 原样到达的值，只为 trace 保留、不为
	// 别的。COS 往这个 header 里放的东西，是本包无法在本地核验的一件事——它来
	// 自的 URL 五分钟就过期，所以一个看起来不对的名字无法重新抓来比对，只能靠
	// 当时记录。
	Disposition string
}

// downloadedMedia 是一份抓取到的正文、外加响应对它说了什么。
type downloadedMedia struct {
	// Body 是原始响应——仍是密文。decryptMedia 把它变成文件。
	Body []byte
	mediaHeaders
}

// downloadMedia GET 一个媒体 URL。错误会带上原因（链接过期、正文超限、服务器
// 卡死），因为调用方要把它们变成人能读的话。
func downloadMedia(ctx context.Context, hc *http.Client, rawURL string) (downloadedMedia, error) {
	if err := checkMediaURL(rawURL); err != nil {
		return downloadedMedia{}, err
	}
	// 缺 client 是接线 bug，而那个诱人的补救——http.DefaultClient——正是
	// media_guard.go 要防的无防护抓取。把它拒掉，等于把那个 bug 变成一次失败的
	// 下载，而不是一次 SSRF。
	if hc == nil {
		return downloadedMedia{}, errors.New("wecom: media download: no guarded http client configured")
	}
	ctx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return downloadedMedia{}, fmt.Errorf("wecom: media request: %w", stripURL(err))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return downloadedMedia{}, fmt.Errorf("wecom: media download: %w", stripURL(err))
	}
	defer resp.Body.Close()

	if resp.ContentLength > maxMediaBytes {
		return downloadedMedia{}, errMediaTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 五分钟 URL 已过期是这里的家常便饭，而 COS 会用一个短 XML 正文解释自
		// 己。截一段带上，好让日志说出是哪一种拒绝。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return downloadedMedia{}, fmt.Errorf("wecom: media download: http %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(snippet)))
	}

	// LimitReader 多放一个字节的余量：精确读到上限时，「文件恰好到顶」和「后面
	// 还有」无法靠读本身区分。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return downloadedMedia{}, fmt.Errorf("wecom: media download: read body: %w", err)
	}
	if len(body) > maxMediaBytes {
		return downloadedMedia{}, errMediaTooLarge
	}
	return downloadedMedia{
		Body:         body,
		mediaHeaders: mediaHeadersOf(resp),
	}, nil
}

// mediaHeadersOf 读响应里唯一描述文件的那一个 header。
func mediaHeadersOf(resp *http.Response) mediaHeaders {
	raw := resp.Header.Get("Content-Disposition")
	return mediaHeaders{Filename: mediaFilenameFromDisposition(raw), Disposition: raw}
}

// stripURL 在传输层错误被写进日志之前，把里面的请求 URL 摘掉。
//
// net/http 会把每个失败都包成 *url.Error，其 Error() 会打印它正在抓取的 URL
// ——而这里的 URL 是预签名 COS 链接：一条同事私有附件的五分钟 bearer 凭证，谁
// 拿到都能用。一次 DNS 抖动、一次 TCP reset、一个 TLS 错误或下载超时，都会把它
// 写进应用日志、再从那里进入任何转发日志的管道。被包裹的 cause 带着同样的诊断、
// 却没有那份凭证，所以我们只留它。
func stripURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// checkMediaURL 拒绝任何不该让传输层指向的东西。URL 是经鉴权的 socket 送来的，
// 所以这是护栏而非防线，但它毕竟是一条指名了「接下来要 GET 的主机」的外部字符
// 串。
func checkMediaURL(rawURL string) error {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return errors.New("wecom: media url is empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		// url.Parse 的错误文本会引用整个输入，而这里的输入是一条活体预签名链接。
		// 说出错在哪，别说它是什么。
		return errors.New("wecom: media url is unparseable")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("wecom: media url scheme %q is not fetchable", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("wecom: media url has no host")
	}
	return nil
}

// mediaFilenameFromDisposition 从 Content-Disposition 里读出展示名，优先 RFC
// 5987 的扩展 `filename*` 形式、其次旁边的普通 `filename=`。两个都发的服务器，
// 会把真实（非 ASCII）名字放在扩展形式里、把乱码的 ASCII 近似放在普通形式里，
// 所以取普通形式等于把所有中文附件重命名成一串下划线。mime.ParseMediaType 无论
// 二者以什么顺序出现，都已经替我们排好这个优先级；测试把它钉死，因为这是「我
// 们依赖、但并非我们实现」的性质。
//
// COS 发来的普通 `filename=` 是 form-urlencoded 形态，而 ParseMediaType 不会解
// 它（它只对扩展形式做百分号解码），所以这个值要再走 decodeFormEncodedFilename
// 才配展示给人看。扩展形式则在那里被跳过：既然服务器发了 `filename*`，就说明
// 它声明了自己的编码，而 ParseMediaType 已应用过它——再解一次等于在声明之上叠
// 猜。
//
// 解码要跑在「缩到基名」之前而不是之后：转义过的分隔符（`..%2F..%2Fetc%2Fpasswd`）
// 只有在解码后才成其为路径，先做清理的代码会把路径穿越原样放行。
//
// 结果再过 cleanMediaFilename：它把名字缩成基名、剥掉控制字符——header 是远端
// 输入，上面那些转义一旦被解开，它就能带上原始 header 带不进来的分隔符或 NUL。
func mediaFilenameFromDisposition(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	name := params["filename"]
	if !hasExtendedFilename(raw) {
		name = decodeFormEncodedFilename(name)
	}
	return cleanMediaFilename(name)
}

// hasExtendedFilename 报告 header 是否提供了 RFC 5987 的 `filename*`。
// mime.ParseMediaType 会把扩展参数折进 params["filename"]，却不说是哪种形式来
// 的，所以原始 header 是唯一还能问的地方。
//
// 一个普通 filename 的「值」里恰好含文本 "filename*" 时，这里会误判成正向——
// 那没有代价：唯一后果是这个值被原样保留。
func hasExtendedFilename(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "filename*")
}

// decodeFormEncodedFilename 对一个「确凿带 form-urlencoding」的文件名做逆解
// 码，其余名字则逐字节原样保留。
//
// 为什么需要它：一个真实租户发过 "PC D&T Strategy 2026.docx"，存下来却成了
// "PC+D%26T+Strategy+2026.docx"——空格成了 '+', '&' 成了 %26，正是原名的
// url.QueryEscape。COS 会对普通 filename 参数做 form 编码，因为按规范该参数只
// 允许 ASCII；中文名字也走这条路：非 ASCII 名字合法地放不进去，所以到达时是一
// 长串百分号转义，不做这步用户看到的就是转义本身。
//
// 为什么要有条件：url.QueryUnescape 本身会把 '+' 读成空格，于是它会把
// "C++ notes.docx" 改写成 "C   notes.docx"。裸 '+' 的两种读法都合法，而任何
// header 字段都无法区分它们。能区分它们的是自洽性——真正的 form 编码经一次
// 「编回来」能幸存，偶然的通常不能："C++ notes.docx" 重编码成 "C+++notes.docx"
// （字面空格本来也会编码成 '+'），因此被放行。所以规则是：仅当把结果重编码后
// 能还原出 header 原值时才解码。
//
// 它不处理什么、以及为什么不：
//
//   - 任何「唯一被编码器动过的字符是 '+'」的名字，本身就是同名带空格版的标准
//     编码，往返测试排除不了它，于是这里照解、把加号弄丢。这比一个孤例宽得
//     多："C++.docx" → "C  .docx"，"React+Redux.md"、"Q1+Q2.xlsx"、
//     "C++11.pdf"、"10+1.pdf" 全都如此。单凭 header 无法消除这种歧义，而这
//     是有意选定的这一侧——带空格的名字远比「带加号且无空格」常见，两者皆有的
//     名字已由上面的往返保住。
//   - 按 RFC 3986 风格百分号编码、用 %20 而非 '+' 表示空格的名字
//     （"PC%20D&T.docx"）重编码成 "PC+D%26T.docx"，通不过校验，保留转义。
//   - 来自「unreserved 集合不是 Go 那套」的服务器的名字。往返用 url.QueryEscape
//     重编码，它保留 A-Za-z0-9-_.~，所以校验在静默假设发送方认同同一集合。
//     Java 的 URLEncoder 还多放过 '*', PHP 的 urlencode 会把 '~' 转成 %7E，所以
//     两者来的 "backup~.docx" 到达时是 "backup%7E.docx"，重编码回 "backup~.docx"
//     后长度对不上。失败是全有或全无而非局部的：只要有一个字符在 Go 的集合之
//     外，整个名字就保留转义——所以 "Q1 report~.docx" 会显示成
//     "Q1+report%7E.docx"，正是本解码要消除的那种不可读状态，且 '+' 还在里面。
//     把比较放宽到容忍两侧任意的转义，等于要去裁决「到底是哪家的编码器写的这
//     个 header」，而支持这个裁决的证据目前不存在。
//
// 这些缺口之所以留着，是因为唯一的证据就是存储中的那一个文件名，而产出它的
// URL 早已过期。traceMediaHeaders 记录 header 到达时的原样，下次真实运行就能用
// 字节、而不是又一次「从一个样本里推」来定案 COS 到底发了什么。
func decodeFormEncodedFilename(name string) string {
	// 编码器产出的东西长不成这样，所以没有可逆的、也没有值得跑的往返。
	if !strings.ContainsAny(name, "+%") {
		return name
	}
	decoded, err := url.QueryUnescape(name)
	if err != nil {
		// 一个开不出任何合法转义的孤立 '%'（"100%.docx"）说明这根本不是编码，
		// 没什么可逆的。
		return name
	}
	if decoded == name || !sameFormEncoding(url.QueryEscape(decoded), name) {
		return name
	}
	return decoded
}

// sameFormEncoding 报告同一值的两次 form 编码是否一致，只容忍百分号转义里十六
// 进制数字的大小写差异。
//
// 这份容忍不是装饰。url.QueryEscape 写大写十六进制，而不少服务器写小写，所以按
// 字节比较会拒掉 "%e5%ad%a3%e6%8a%a5.png"——一个中文文件名，正是这个解码最该
// 管的场景（非 ASCII 名除了转义什么都不是）——然后悄无声息地什么都不做。
//
// 大小写是唯一的容忍，这也正是往返测试在假设「发送方的 unreserved 集合等于
// url.QueryEscape 的集合」的原因。某个被 Go 放行、却被服务器转义的字符会让整个
// 比较直接失败；代价见 decodeFormEncodedFilename 上列出的第三个缺口。
func sameFormEncoding(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); {
		if a[i] == '%' && b[i] == '%' && i+2 < len(a) {
			if !strings.EqualFold(a[i+1:i+3], b[i+1:i+3]) {
				return false
			}
			i += 3
			continue
		}
		if a[i] != b[i] {
			return false
		}
		i++
	}
	return true
}

// cleanMediaFilename 把来自任何地方的名字收成一个不含控制字符的单一路径段；
// 实在没剩什么可用时返回空。
//
// 控制字符剥离放在这里，是因为 decodeFormEncodedFilename 拓宽了能到达本函数的
// 东西。过去几乎没有字符能走到这一步：对照 net/http，裸 TAB 会原样穿过 header
// 值，U+0085（NEL——对 unicode.IsControl 是控制字符、对传输层只是两个普通字
// 节）也一样，而 NUL、CR、LF、ESC、DEL 会让传输层把整个响应拒掉。由于
// ParseMediaType 从不对普通 filename= 形式做百分号解码，`filename="a%00b.docx"`
// 一直是它看起来那个样子（可打印字符串）。解开转义后，它们全都变成自己指代的
// 字节：%00 变成真 NUL，%0D%0A 变成真 CRLF。
//
// NUL 是唯一会造成真损失的那个。这个名字要写进 attachments.filename——Postgres
// TEXT——而 Postgres 根本存不了 NUL：插入失败、整个附件丢失，哪怕它的字节早已
// 完美下载并解密。CR 与 LF 是 header 注入的形状；internal/storage 的
// ContentDisposition 在构造下载 header 时已经替换它们，那一端有覆盖，但
// attachments 行在那之前就写了，没有理由让它们带着走。
//
// 走到这一步需要恶意或有 bug 的 CDN——用户没法往文件名里敲 NUL。这个标准本来就
// 对：此 header 是远端输入，而本函数是两条抓取路径抵达它时共用的唯一咽喉。
func cleanMediaFilename(name string) string {
	name = strings.TrimSpace(stripControlRunes(name))
	if name == "" {
		return ""
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}

// stripControlRunes 丢弃每个控制字符——是「丢」而不是「换成占位符」：占位符会
// 给名字里塞一个发送者没敲过的字符，而把真名 "ab.docx" 存成 "a_b.docx" 是它自己
// 的一种小谎言。
//
// 解码后成了非法 UTF-8 的字节是另一回事，而且不被丢弃：strings.Map 会把其中每
// 一个替换成 U+FFFD——这正是它对它们做 range 循环时给出的产物。这是有用的答案
// 而非意外——Postgres TEXT 同样不收非法 UTF-8 序列，所以保留原始字节的名字会像
// NUL 一样让插入失败，而 U+FFFD 是「这里有过一个字符、它没能存活」的标准说法。
func stripControlRunes(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// openMedia 是去掉了缓冲的 downloadMedia：它把正文当作流返回，让调用方能边到边
// 解密，外加缓冲式路径会读的那份 mediaHeaders。同样的闸门、同样的状态处理、同
// 样的上限——上限由调用方经由其读取的 LimitReader 强制，所以一个谎报长度的正文
// 也无法拖垮进程。
//
// 调用方负责关闭 reader。它拥有底下的响应，提前 return 而不 Close 会漏掉连接。
func openMedia(ctx context.Context, hc *http.Client, rawURL string) (io.ReadCloser, mediaHeaders, error) {
	if err := checkMediaURL(rawURL); err != nil {
		return nil, mediaHeaders{}, err
	}
	if hc == nil {
		return nil, mediaHeaders{}, errors.New("wecom: media download: no guarded http client configured")
	}

	// 这里不像 downloadMedia 那样设 context 超时：调用方要在整个解密期间读这个
	// 正文，一个盖住抓取期的 deadline 会把读拦腰截断。整个操作由路由器自己的媒
	// 体预算约束。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, mediaHeaders{}, fmt.Errorf("wecom: media request: %w", stripURL(err))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, mediaHeaders{}, fmt.Errorf("wecom: media download: %w", stripURL(err))
	}
	if resp.ContentLength > maxMediaBytes {
		resp.Body.Close()
		return nil, mediaHeaders{}, errMediaTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, mediaHeaders{}, fmt.Errorf("wecom: media download: http %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(snippet)))
	}
	return &cappedBody{
		ReadCloser: resp.Body,
		remaining:  maxMediaBytes + 1, // 多一个字节余量，和 downloadMedia 一致
	}, mediaHeadersOf(resp), nil
}

// cappedBody 拦住超过上限还一直往外吐的响应，这样一段没声明长度的正文没法像从
// 前填爆堆那样去填爆磁盘。
type cappedBody struct {
	io.ReadCloser
	remaining int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errMediaTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.ReadCloser.Read(p)
	c.remaining -= int64(n)
	if c.remaining <= 0 && err == nil {
		return n, errMediaTooLarge
	}
	return n, err
}
