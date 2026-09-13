package channel

import "strings"

// 文件角色：Capability 位掩码的定义与展示。它是本包的纯声明侧——只描述"某个渠道能
// 干什么"，**不含任何降级逻辑**。想降级的调用方（例如渠道缺 CapRichCard 就把富卡片
// 降成纯文本）自己读位掩码做决定，因此新增一个平台永远不需要往核心塞 if/else。

// Capability 是 Channel 用来"声明自己支持什么"的位掩码。纯声明：本包不含降级逻辑。
// 想对输出降级的调用方（渠道没有 CapRichCard 时把富卡片→纯文本）自行读位掩码做决定，
// 这样新增平台永远不用往核心代码里塞分支。Capabilities() 返回 Channel 的固定能力集；
// 零值表示什么都不声明。
type Capability uint64

const (
	// CapText —— 能发送纯文本消息。每个 Channel 都必须至少声明这一项。
	CapText Capability = 1 << iota
	// CapRichCard —— 能渲染富/交互卡片（飞书 interactive card、Slack Block Kit…）。
	CapRichCard
	// CapThreadReply —— 能往线程/话题里回帖。
	CapThreadReply
	// CapQuoteReply —— 能引用回复某条具体消息。
	CapQuoteReply
	// CapAttachment —— 能发送和/或接收媒体附件。
	CapAttachment
	// CapVoice —— 能处理语音/音频消息。
	CapVoice
	// CapTypingIndicator —— 能显示输入中/"思考中"指示器。
	CapTypingIndicator
	// CapMessageEdit —— 能在消息发出后编辑它（飞书 card patch、Slack chat.update…）。
	CapMessageEdit
)

// capabilityNames 把"单一位的能力"映射成稳定的小写名字，供 String() 使用。
// 顺序与上面的位定义一致，保证 String() 从最低位开始读。
var capabilityNames = []struct {
	bit  Capability
	name string
}{
	{CapText, "text"},
	{CapRichCard, "rich_card"},
	{CapThreadReply, "thread_reply"},
	{CapQuoteReply, "quote_reply"},
	{CapAttachment, "attachment"},
	{CapVoice, "voice"},
	{CapTypingIndicator, "typing_indicator"},
	{CapMessageEdit, "message_edit"},
}

// Has 判断 c 是否声明了 want 里的**每一项**。Has(0) 恒为 true（空要求总是满足）。
// 因为 want 可以是多位组合，这是"全部包含"测试，不是"任一命中"。
func (c Capability) Has(want Capability) bool {
	return c&want == want
}

// String 把已置位的位渲染成用 "|" 连接的名单（如 "text|thread_reply"）；零值渲染成
// "none"；任何没登记名字的高位会以十六进制余数追加在末尾——这样将来忘登记的位也绝不会
// 从日志里无声消失。仅供诊断用。
func (c Capability) String() string {
	if c == 0 {
		return "none"
	}
	var (
		parts     []string
		remaining = c
	)
	for _, cn := range capabilityNames {
		if remaining&cn.bit == cn.bit {
			parts = append(parts, cn.name)
			remaining &^= cn.bit
		}
	}
	if remaining != 0 {
		parts = append(parts, "0x"+strings.TrimLeft(hex(uint64(remaining)), "0"))
	}
	return strings.Join(parts, "|")
}

// hex 把 v 格式化成定长的小写十六进制串。不引入 fmt 依赖（让本叶子文件零依赖），
// 只被 String() 用来展示罕见的"未登记位"余数。
func hex(v uint64) string {
	const digits = "0123456789abcdef"
	var buf [16]byte
	for i := 15; i >= 0; i-- {
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf[:])
}
