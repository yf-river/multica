package engine

import "strings"

// 角色定位：共享会话控制命令（/clear 与 /new）的统一解析器。这两个指令的入站语法刻意
// 完全相同，区别只在 Router 规范化之后执行的产品动作。解析逻辑放在共享引擎里，所有
// 渠道的适配器就不用各自实现一遍"从用户消息里剥掉指令前缀"。

const freshSessionCommandPrefix = "/clear"
const newChatCommandPrefix = "/new"

// ControlCommandKind 标识"入站渠道消息首行"上的共享会话控制指令。适配器可以规范化
// 传输细节（例如平台 bot 提及或富媒体排版），但**只有** Router 应用这些产品语义。
type ControlCommandKind uint8

const (
	ControlCommandFreshSession ControlCommandKind = iota + 1
	ControlCommandNewChat
)

// ControlCommand 是一条解析出来的渠道会话控制指令。
type ControlCommand struct {
	Kind ControlCommandKind
	Body string // 剥掉指令后的剩余正文
}

// ParseControlCommand 用同一个解析器分类 /clear 与 /new。二者入站语法刻意完全一致；
// 唯一区别是规范化后 Router 执行的产品动作。
func ParseControlCommand(body string) (ControlCommand, bool) {
	if parsed, ok := parseLeadingCommand(body, newChatCommandPrefix); ok {
		return ControlCommand{Kind: ControlCommandNewChat, Body: parsed}, true
	}
	if parsed, ok := parseLeadingCommand(body, freshSessionCommandPrefix); ok {
		return ControlCommand{Kind: ControlCommandFreshSession, Body: parsed}, true
	}
	return ControlCommand{}, false
}

// ParseFreshSessionCommand 从渠道消息里抽出一条首行 /clear 指令。返回去掉指令后的用户
// 提示。这是共享产品行为：任何走到 Router 的渠道都拿到同一套"开全新会话"能力，适配器
// 无需各自重写解析。
//
// 匹配规则与 /issue 命令一致：大小写敏感、按 token 边界匹配、只有第一个非空行可以是
// 命令——这意味着 /clear 与 /issue 在同一首行上是互斥的。
func ParseFreshSessionCommand(body string) (string, bool) {
	command, ok := ParseControlCommand(body)
	if !ok || command.Kind != ControlCommandFreshSession {
		return "", false
	}
	return command.Body, true
}

// ParseNewChatCommand 抽出共享的 /new 命令。解析刻意与 /clear 保持一致，适配器只需
// 规范化传输细节；路由轮换永远由共享引擎负责。
func ParseNewChatCommand(body string) (string, bool) {
	command, ok := ParseControlCommand(body)
	if !ok || command.Kind != ControlCommandNewChat {
		return "", false
	}
	return command.Body, true
}

// parseLeadingCommand 若正文的**第一个非空行**以 prefix 开头（且紧跟空白或行尾，保证
// 不会把 "/clearx" 误判成 "/clear"），则返回剥掉 prefix 后的正文。
func parseLeadingCommand(body, prefix string) (string, bool) {
	lines := strings.Split(body, "\n")

	// 找到第一个非空行；跳过文件开头可能出现的空行/纯空白行。
	firstIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			firstIdx = i
			break
		}
	}
	if firstIdx == -1 {
		return "", false
	}

	trimmed := strings.TrimLeft(lines[firstIdx], " \t")
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}

	// prefix 后面要么是行尾，要么必须以空白接正文——"/clearfoo" 不算 /clear 命令。
	rest := trimmed[len(prefix):]
	if rest != "" {
		if r0 := rest[0]; r0 != ' ' && r0 != '\t' {
			return "", false
		}
	}

	bodyParts := make([]string, 0, 2)
	if firstLineBody := strings.TrimSpace(rest); firstLineBody != "" {
		bodyParts = append(bodyParts, firstLineBody)
	}
	if firstIdx+1 < len(lines) {
		bodyParts = append(bodyParts, strings.Join(lines[firstIdx+1:], "\n"))
	}
	return strings.TrimRight(strings.Join(bodyParts, "\n"), " \t\n"), true
}
