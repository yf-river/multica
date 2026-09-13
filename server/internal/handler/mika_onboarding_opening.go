// 本文件：Mika 开场白文案与渲染。Mika 的第一句回复由产品手写，不再由 Agent 运行生成
// （MUL-5827）。文案本身已内含设计说明，见 mikaOnboardingOpening。
package handler

import (
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/service"
)

// mikaOnboardingOpening 是 Mika 的第一句回复，由产品手写而非 Agent 运行生成（MUL-5827）。
//
// 它读起来像 Agent 的话，却是全产品里最不依赖 Agent 的输出：不查任何工作区状态、不调工具、
// 不做任何决策。multica-onboarding skill 已经把它钉死在长度预算内的四个节拍上，所以以前跑
// 一次 Agent 要付 runtime 冷启动加两次模型往返，只为渲染一件早已决定好的东西——成员却看着
// 转圈，而一次失败运行还会用一个报错气泡跟他打招呼。
//
// 四个节拍按顺序承重，编辑时务必保留：
//
//  1. 用一句话说 Multica 是什么，
//  2. 说 Mika 对这个成员是谁，
//  3. 说接下来会发生什么，
//  4. 桥接到下面渲染的 starter 卡片。
//
// 刻意不按角色或使用场景变化。七个用例 × 四种语言是一张谁都维护不下去的文案矩阵，而问卷
// 照样会到达模型——它搭 kickoff 进入成员第一条真实消息，那才是个性化真正值钱的地方。也不
// 随成员在别的工作区做过什么而变化：每个工作区都从零开场。
//
// %[1]s 是工作区名，%[2]s 是 Mika 的显示名。两者都可被 owner 编辑，所以都是参数、绝不写进
// 格式串本体。
const mikaOnboardingOpening = `你好，欢迎来到 %[1]s。Multica 是一个人和 AI 智能体通过任务一起把事情做完的工作区。

我是 %[2]s，这里的 Chief of Staff。我负责把事情理清楚、找到合适的智能体接手，也是你随时可以开口的第一站。

接下来是这样：你说一个目标，我把它变成一个任务，交给合适的智能体开始跑，你能看着它推进。

从下面选一个开始，或者直接告诉我你现在想做成什么。`

// buildMikaOnboardingOpening 渲染这段中文开场白。owner 若把名字清空了就回退到产品默认名，
// 因为这句话讲的是「谁在说话」，没有名字读起来是碎的。
func buildMikaOnboardingOpening(agentName, workspaceName string) string {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = service.MikaDefaultName
	}
	return fmt.Sprintf(
		mikaOnboardingOpening,
		escapeMarkdownInline(strings.TrimSpace(workspaceName)),
		escapeMarkdownInline(name),
	)
}

// escapeMarkdownInline 在把成员写的名字插进要渲染的文案前，中和掉其中的行内 markdown 元
// 字符。聊天把助手内容当 markdown 渲染，否则一个叫 `**Ops**` 的工作区会把 Mika 第一句的
// 一部分加粗，含反引号的则会开启一段吞掉整行剩余部分的代码 span。
//
// 反斜杠第一个处理，这样它永远不会给本函数新增的转义再做一次转义。只覆盖行内结构：名字
// 是插在句中、从不在行首，所以 `#`、`-` 这类块级标记是惰性的。
var markdownInlineEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	`[`, `\[`,
	`]`, `\]`,
	`<`, `\<`,
)

func escapeMarkdownInline(s string) string {
	return markdownInlineEscaper.Replace(s)
}
