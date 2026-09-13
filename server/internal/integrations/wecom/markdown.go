// markdown.go —— 防止「成员写的话」变成「机器人像亲手写的」那种 markdown。
// 收件箱卡片（inbox_message.go）和 /issue 确认（replier.go）共用本文件，两者都
// 会把别人的话拼进一条以机器人名义发出的消息里。
package wecom

import (
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// breakMemberLinks 是每个「把成员文本拼进机器人署名消息」的调用方都要过的一道
// 工序。下面两种打断都需要、各自关掉不同的构造，所以收拢在同一个入口后面：调用
// 点没法只取其一、忘了另一个。
//
// 两者都只是插入一个空格，所以两个顺序结果一致、且这对操作幂等：谁都不会再产
// 生对方要找的那种模式。
func breakMemberLinks(s string) string {
	return breakLinkReferenceDefinitions(breakLinkAdjacency(s))
}

// breakLinkAdjacency 阻止成员文本在机器人署名的消息里拼成一条 markdown 链接。
// 名为 "[click here](http://evil.example)" 的任务，否则就会以「可点击链接」的形
// 态出现在一张接收方完全有理由信任的卡片里：卡片由机器人投递，而里面没有任何
// 东西标出哪些部分是从某个用户引用的。
//
// 它做「拆开」而不是「转义」。链接只在 "]" 与 "(" 相邻时成形——CommonMark 要求
// 链接文字后面必须*立刻*接 "("，而那种用 `\[([^\]]+)\]\(([^)]+)\)` 正则冒充真
// 解析器的改写器同样有这个要求。图片语法 "![x](u)" 需要同样的相邻，所以被同一
// 条规则覆盖。二者之间插入一个普通空格就够，而且这是唯一做的改动：不含 "]("
// 的文本原样返回（逐字节一致），所以常见的 "[Bug] 登录失败" 这类标题不被碰。
//
// 空格效果是在真实渲染器上确认的，不是推断出来的：单独发 "[重置密码]
// (https://evil.example)" 回来是纯黑文本，两个方括号都还在，括号里的 URL 也没
// 被当作目标地址消费掉。
//
// 反斜杠转义是第一个试的方案，在这里没法用。真租户上 "\[Bug\]" 回来变成了无衬
// 线斜体 "Bug"、括号没了——那是数学展示块（display-math）的渲染方式；这个机制
// 是从渲染结果反推出来的，没有任何企微文档描述它——而会话列表预览根本不渲染
// markdown，把反斜杠原样露了出来。所以本函数绝不能吐反斜杠：它要么被看见，要
// 么把成员文本拽进数学块；而标题里一个不成对的 "[" 会开出一个永不闭合的块。
//
// 调用方依赖的两条性质：
//
//   - 插入的空格永远不会成为行首。它前面同一行总有 "]" 顶着，所以不会开启缩进
//     代码块、也不会变成行尾硬换行。
//   - 每出现一次就多一个 rune，所以做长度预算的调用方必须先调它再量长度，不能
//     倒过来。
//
// 它不覆盖的：裸 URL 仍会被客户端自动链接化——实测如此，且不在范围内：裸 URL
// 展示的就是它自己的目的地，而本要堵的攻击是「一个声称去哪、实际不去的标签」。
// 内联 HTML 在这里是惰性的，也实测过：`<a href="https://evil.example">重置密
// 码</a>` 到达时是纯黑文本。唯一不带 "](" 也能变成真链接的构造是 CommonMark 的
// 链接引用定义——而这个渲染器会解析它们——见 breakLinkReferenceDefinitions。
func breakLinkAdjacency(s string) string {
	return channel.BreakMarkdownLinkAdjacency(s)
}

// breakLinkReferenceDefinitions 阻止成员文本去*定义*一个随后由机器人卡片解析的
// 链接。经真租户实测，一段包含
//
//	[重置密码]: https://evil.example
//	[重置密码]
//
// 的评论正文回来时第一行消失了——被当作定义吞掉——第二行渲染成指向
// evil.example 的蓝色下划线链接。里面没有任何相邻的字符，所以 breakLinkAdjacency
// 会把整个攻击原样放行。杀掉定义就同时杀掉了快捷写法 "[label]"、折叠写法
// "[label][]" 和完整写法 "[text][label]"：三者都经同一张表解析，消息里没有定义，
// 它们谁都解析不出来。
//
// # 规则
//
// 以下三条同时成立时，把 "]" 和紧随其后的 ":" 拆开：
//
//  1. 该行 "[" 之前只有块脚手架——缩进、">" 引用标记、列表符号。必要而非装饰：
//     引用块或列表项里的定义照样会填满整篇文档的引用表、并在外面被解析。
//  2. 方括号构成一个 CommonMark 链接标签：由第一个未转义的 "]" 闭合，内部无未转
//     义的 "["，至少一个非空白字符，至多 999。标签可以跨行仍能解析，所以扫描也
//     跟着跨行。
//  3. 冒号后面接的东西看起来像链接*目的地*——见 looksLikeLinkDestination。
//
// 条件 1 与 3 是同一个问题的两半，靠同一个答案保持一致：containerPrefixBefore
// 报告持有标签的块容器，looksLikeLinkDestination 拿到同一个 containerPrefix，好
// 在目的地位于下一行时能回退进那些容器。只教一半关于容器的事、另一半不教，就会
// 让一条定义完整漏网——见 containerPrefix 上的说明。
//
// 条件 3 是整套设计的核心。见 "]" ":" 就拆会更简单，但那是错误的取舍：
// "[Bug]: 登录失败" 是真人会写的格式，而它从这里出去时逐字节原样——因为 登录失败
// 指名不了任何主机：作为目的地它是相对的，只会对着客户端自己所在的 base 解析，
// 没法把读者指向别人的服务器。规则画的正是这条线：会「离开去往另一台主机」的目
// 的地要被拆，去不了的放行。
//
// # 刻意放行的
//
//   - 相对目的地。"[文件]: report.pdf"、"[页面]: /inbox"、"[Owner]: R&D"、
//     "[Regex]: \d+" 和 "[Bug]: 登录失败" 都仍会定义一条引用，标签仍可能以链接
//     出现——但指向的是客户端自己的 base，不是攻击者。为保住人的书写习惯而明知
//     地付出这个代价。
//   - "[foo]:(https://evil.example)"。CommonMark 把括号留在 href 内部，所以没有
//     人会把它们导航过去。无论如何，目的地被检查前会先跳过一个前导 "("，以防本
//     渲染器更宽松——因为跳一个零成本。
//
// # 哪里会过度触发，以及为什么那是安全的一侧
//
// 它只用 ">" 标记的计数来建模块容器，所以会对一条 CommonMark 会折进前一段落、
// 或读作缩进代码的 "[x]: https://…" 行触发，也会对「目的地换行、且引用嵌套比定
// 义行更浅」触发。它也不检查规范里「目的地之后不得跟任何东西」的规则，所以
// "[x]: https://evil.example 请点击" 也会被拆，尽管 CommonMark 会把它当正文留
// 下。上述每种情况付出的都只是「一行本来已带 URL 的行里多一个空格」。而漏拆付
// 出的是一条「机器人署名下的活链接」，所以近似故意朝这个方向偏——更因为这渲染
// 器已被证明并非严格 CommonMark（它把 "\[ … \]" 读成数学展示块的分隔符）。
//
// # 拆在哪里
//
// 在 "]" 与 ":" 之间。CommonMark 要求冒号必须紧贴标签，所以一个空格就终结定义；
// 已在 markdown_test.go 里对照 CommonMark 解析器验证过，包括引用块内、目的地换
// 行的情况。不能拆在标签内部——引用标签按「空白折叠、大小写折叠」后的文本匹配，
// 所以 "[ 重置密码]" 仍会匹配 "[重置密码]"，定义照样活着。不能用反斜杠，原因见
// 上。也不能放在 "[" 之前：没有任何字符能在不显形为内容、或（四个空格后）开启
// 代码块的情况下，把一行推出块位置。
//
// 和 breakLinkAdjacency 一样每次出现多一个 rune，所以做长度预算的调用方必须先调
// 它再量；它的输出不再含有它会再次触发的 "]:"，所以第二遍跑是空操作。
func breakLinkReferenceDefinitions(s string) string {
	if !strings.Contains(s, "]:") {
		return s
	}
	var out strings.Builder
	prev := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		container, ok := containerPrefixBefore(s, i)
		if !ok {
			continue
		}
		end, ok := linkLabelEnd(s, i)
		if !ok {
			continue
		}
		// 标签内不含未转义的 "["，所以它里面不会有另一个能开标签——无论哪种情况
		// 都从它后面继续。
		i = end
		if end+1 >= len(s) || s[end+1] != ':' {
			continue
		}
		if !looksLikeLinkDestination(s[end+2:], container) {
			continue
		}
		out.WriteString(s[prev : end+1])
		out.WriteByte(' ')
		prev = end + 1
	}
	if prev == 0 {
		return s
	}
	out.WriteString(s[prev:])
	return out.String()
}

// containerPrefix 是「一行开头的块脚手架开出的东西」：这一行所处的引用嵌套深度。
// 这是规则拥有的唯一「容器」概念，规则的两半都从它出发——决定标签前可以是什么的
// 那半会产出一个它，扫过冒号的那半会重放它。任何一半都不能知道另一半不知道的容
// 器，这正是它要作为一个「值」而非 bool 存在的全部原因。
//
// 只数 ">" 标记。缩进和列表符号也是脚手架，但它们不需要重放：CommonMark 用普通
// 缩进续行列表项，目的地扫描本来就会把它们当空白跨过。引用块不一样——它持有几
// 行就在每行重复一次标记——所以下一行的目的地到达时是 "> https://…"，一个没料到
// 会有标记的扫描会把标记本身读成目的地。
type containerPrefix struct {
	quotes int
}

// containerPrefixBefore 报告持有 s[i] 处 "[" 的容器，以及「它所在行的行首到 i」
// 是否全是块脚手架：缩进、">" 标记、列表符号。任何别的东西——一个词、一个
// "**"——都说明这个 "[" 在一段正文里，而正文里开不出定义。
//
// 它只往回走到第一个不可能是脚手架的字节为止，所以一段散文中间的 "[" 只需读一
// 个字节就能定案，而不是读完整行——正文里满是 "[" 也保持线性复杂度。
func containerPrefixBefore(s string, i int) (containerPrefix, bool) {
	start := i
	for start > 0 && isBlockScaffoldByte(s[start-1]) {
		start--
	}
	if start > 0 && s[start-1] != '\n' {
		return containerPrefix{}, false
	}
	return parseContainerPrefix(s[start:i])
}

// parseContainerPrefix 把 p 当作一整段块脚手架读，报告它开出的容器。p 里混有任
// 何不是脚手架的东西时 ok 为 false。
func parseContainerPrefix(p string) (prefix containerPrefix, ok bool) {
	for len(p) > 0 {
		switch {
		case p[0] == ' ' || p[0] == '\t':
			p = p[1:]
		case p[0] == '>':
			prefix.quotes++
			p = p[1:]
		case (p[0] == '-' || p[0] == '+' || p[0] == '*') && len(p) > 1 && (p[1] == ' ' || p[1] == '\t'):
			p = p[2:]
		default:
			// 有序列表标记：至多 9 位数字，然后 "." 或 ")"，再一个空格。
			n := 0
			for n < len(p) && n < 9 && p[n] >= '0' && p[n] <= '9' {
				n++
			}
			if n == 0 || n+1 >= len(p) || (p[n] != '.' && p[n] != ')') || (p[n+1] != ' ' && p[n+1] != '\t') {
				return containerPrefix{}, false
			}
			p = p[n+2:]
		}
	}
	return prefix, true
}

// skipContinuationPrefix 在一行续行的行首跳过 prefix 里那些容器的标记，返回这行
// 内容从哪里开始。它是 containerPrefixBefore 的「重放」那一半。
//
// 它最多消费 prefix.quotes 个标记、绝不多吃。允许更少，因为引用块接受惰性续行：
// "> [x]:" 后跟一行不带标记的裸 "https://…"，照样定义这条引用。更多则根本不是
// 这条定义的续行——比所续块更深的一行会开一个新块——所以扫描停住，让剩下的 ">"
// 自己说话，也就是「它不是目的地」。
func skipContinuationPrefix(s string, i int, prefix containerPrefix) int {
	i = skipSpacesTabs(s, i)
	for n := 0; n < prefix.quotes; n++ {
		if i >= len(s) || s[i] != '>' {
			return i
		}
		i = skipSpacesTabs(s, i+1)
	}
	return i
}

// isBlockScaffoldByte 是脚手架前缀能取材的字符集——是 parseContainerPrefix 随后
// 精确解析的标记的超集。它只决定往回走多远；这段东西到底是不是脚手架，由
// parseContainerPrefix 作答。
func isBlockScaffoldByte(c byte) bool {
	switch {
	case c == ' ', c == '\t', c == '>':
		return true
	case c == '-', c == '+', c == '*', c == '.', c == ')':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	return false
}

// linkLabelEnd 返回闭合「开在 s[open] 处的链接标签」那个 "]" 的字节偏移，以及它
// 包住的到底是不是标签。遵循 CommonMark：第一个未转义的 "]" 闭合它；未转义的
// "[" 让它出局；它至少要有 1 个非空白字符、至多 999 个；可以跨行。
func linkLabelEnd(s string, open int) (int, bool) {
	const maxLabelRunes = 999
	runes, content := 0, false
	for i := open + 1; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == ']' {
			return i, content
		}
		if r == '[' {
			return 0, false
		}
		if runes++; runes > maxLabelRunes {
			return 0, false
		}
		if r == '\\' && i+size < len(s) {
			// 反斜杠转义了后面的字符，所以 "\]" 不闭合标签、"\[" 也不让它出局。
			_, n := utf8.DecodeRuneInString(s[i+size:])
			i += size + n
			runes++
			content = true
			continue
		}
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			content = true
		}
		i += size
	}
	return 0, false
}

// looksLikeLinkDestination 报告 rest——"[label]:" 之后的所有内容——是否开启一个
// 可能把读者引到另一台主机的链接目的地。正是这个测试保住了 "[Bug]: 登录失败" 不
// 被拆。
//
// 目的地携带 scheme（"https:"，同样算 "javascript:" 或 "data:"）、或是相对 scheme
// 的（"//host/path"）、或使用了「解码后拼出以上二者之一」的转义机制时，即够格。
//
// prefix 是定义行自身所在的容器；它做成参数而不是在这里重新发现，理由见
// containerPrefix：目的地换到下一行时会顶着同样的标记到达，一个走出冒号却不带这
// 些标记的扫描，会把标记本身读成目的地，从而拆掉一条本可解析的定义。
func looksLikeLinkDestination(rest string, prefix containerPrefix) bool {
	i := skipSpacesTabs(rest, 0)
	// CommonMark 允许冒号与目的地之间至多一个行结束符，所以定义可以跨两行。第二
	// 个行结束符会终结块、定义不复存在——带着换行原样掉下去就处理了这一点，因为
	// 行结束符永远不会是目的地的开头。
	if i < len(rest) && rest[i] == '\r' {
		i++
	}
	if i < len(rest) && rest[i] == '\n' {
		i = skipContinuationPrefix(rest, i+1, prefix)
	}
	if i < len(rest) && (rest[i] == '<' || rest[i] == '(') {
		// "<…>" 是规范里带括号的目的地形式，里面可能带前导空格，客户端会把它们
		// 从 URL 上剥掉。
		i = skipSpacesTabs(rest, i+1)
	}
	dest := rest[i:]
	if j := strings.IndexAny(dest, " \t\r\n"); j >= 0 {
		dest = dest[:j]
	}
	if dest == "" {
		return false
	}
	if strings.HasPrefix(dest, "//") || hasURIScheme(dest) {
		return true
	}
	// 目的地内部认可反斜杠转义和字符引用，而 "https\://evil.example"、
	// "\/\/evil.example" 和 "&#x68;ttps://evil.example" 三者在解码后都会解析成可
	// 用的离站 URL——已对照 CommonMark 解析器全部验证。与其逐个解码，不如把对该
	// 机制的任何使用都当作目的地。
	//
	// 说的是「机制」，不是「字符」。孤零零的 "\" 或 "&" 拼不出任何东西：
	// "R&D"、"\d+"、"docs\setup"、"foo&bar" 全是相对目的地——本函数承诺放行——
	// 而且它们不带任何解析器会执行的转义。
	return hasBackslashEscape(dest) || hasCharacterReference(dest)
}

// hasBackslashEscape 报告 s 是否使用了 CommonMark 反斜杠转义——ASCII 标点前的
// "\"，这是反斜杠唯一有意义的地方。在 "\d+" 和 "docs\setup" 里它保持字面文本。
func hasBackslashEscape(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\\' && isASCIIPunct(s[i+1]) {
			return true
		}
	}
	return false
}

// isASCIIPunct 是 CommonMark 的 ASCII 标点集——所有「既非字母、又非数字、也非
// 空格」的 ASCII 字符。
func isASCIIPunct(c byte) bool {
	switch {
	case c >= '!' && c <= '/', c >= ':' && c <= '@', c >= '[' && c <= '`', c >= '{' && c <= '~':
		return true
	}
	return false
}

// hasCharacterReference 报告 s 是否含字符引用——"&"、一个名字或 "#" 开头的数字
// 主体、然后 ";"。"R&D" 和 "foo&bar" 没有能闭合的 ";"，所以里面没有会解码的东
// 西。
//
// 它不拿名字对 HTML5 的表校验：未知名字解码为空，拆它只花一个空格——本文件其余
// 部分本来就跑在「宁可多拆」这一侧。
func hasCharacterReference(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		for j := i + 1; j < len(s); j++ {
			c := s[j]
			if c == ';' {
				return j > i+1
			}
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '#') {
				break
			}
		}
	}
	return false
}

// hasURIScheme 报告 s 是否以「URI scheme + ":"」开头——一个字母，然后字母、数
// 字、"+ "、"-" 或 "."（RFC 3986）。
func hasURIScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		case c == ':':
			return i > 0
		default:
			return false
		}
	}
	return false
}

func skipSpacesTabs(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}
