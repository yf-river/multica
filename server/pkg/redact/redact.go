// Package redact 提供在 agent 输出进入数据库或 WebSocket 广播前，检测并打码
// 其中机密（密钥/token 等）的能力。
package redact

import (
	"regexp"
)

// secretPattern 把编译好的正则与其替换文本配对。
type secretPattern struct {
	re          *regexp.Regexp
	replacement string
}

// patterns 依次检查；同一位置按先命中者为准。
var patterns = []secretPattern{
	// AWS access key ID（总是以 AKIA 开头）
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED AWS KEY]"},

	// AWS secret access key（40 字符类 base64，前有常见分隔符）
	{regexp.MustCompile(`(?i)(?:aws_secret_access_key|secret_?access_?key)\s*[=:]\s*[A-Za-z0-9/+=]{40}`), "[REDACTED AWS SECRET]"},

	// PEM 私钥（多行）
	{regexp.MustCompile(`(?s)-----BEGIN[A-Z\s]*PRIVATE KEY-----.*?-----END[A-Z\s]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},

	// GitHub token（经典 PAT、OAuth、user-to-server、server-to-server、refresh）
	{regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,255}\b`), "[REDACTED GITHUB TOKEN]"},

	// GitHub fine-grained PAT 用 github_pat_ 前缀，上面的 ghp_/gho_/... 规则不覆盖。
	// 没有这一条，agent 输出里的 fine-grained PAT 会未打码地泄漏到数据库与
	// WebSocket 广播。
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,255}\b`), "[REDACTED GITHUB TOKEN]"},

	// OpenAI / Anthropic API key
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), "[REDACTED API KEY]"},

	// Slack bot/user/legacy token。字符类含 'e'，让较新的 xoxe- 配置/刷新 token
	// 与 xoxb/p/o/r/a/s 一起被覆盖。
	{regexp.MustCompile(`\bxox[bporase]-[A-Za-z0-9\-]{10,}\b`), "[REDACTED SLACK TOKEN]"},

	// Slack app 级 token 用 xapp- 前缀，上面的 xox*- 规则不匹配。没有这一条，
	// agent 输出回显的 app 级 token 会未打码地泄漏到 DB / WebSocket 广播。
	{regexp.MustCompile(`\bxapp-[A-Za-z0-9-]{10,}\b`), "[REDACTED SLACK TOKEN]"},

	// GitLab personal access token
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`), "[REDACTED GITLAB TOKEN]"},

	// Google API key 总以 AIza 前缀开头且共 39 字符（AIza + 35）。捕获并还原
	// 尾随分隔符，让以 '-' 等非单词字符结尾的 key 也能被打码。
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}([^0-9A-Za-z_-]|$)`), "[REDACTED GOOGLE API KEY]$1"},

	// Stripe secret / restricted live key（sk_live_ / rk_live_）。上面的 sk- 规则
	// 只匹配 OpenAI/Anthropic 用的连字符形式；Stripe 用下划线，live key 没有这条
	// 就不被覆盖。Publishable key（pk_live_）刻意排除——它们不是机密。
	{regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{16,}\b`), "[REDACTED STRIPE KEY]"},

	// JWT（三段 base64url）
	{regexp.MustCompile(`\bey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "[REDACTED JWT]"},

	// 输出里的通用 "Bearer <token>"
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*\b`), "Bearer [REDACTED]"},

	// 内嵌密码的连接串
	{regexp.MustCompile(`(?i)(?:postgres|mysql|mongodb|redis|amqp)(?:ql)?://[^:\s]+:[^@\s]+@`), "[REDACTED CONNECTION STRING]@"},

	// 常见机密环境变量名的通用 key=value 形式
	{regexp.MustCompile(`(?i)(?:API_KEY|API_SECRET|SECRET_KEY|SECRET|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|DATABASE_URL|DB_PASSWORD|DB_URL|REDIS_URL|PASSWORD|TOKEN)\s*[=:]\s*\S+`), "[REDACTED CREDENTIAL]"},
}

// maxRedactDepth 限制 redactValue 的遍历深度。工具输入来自 daemon 提供的 JSON，
// 嵌套深度受攻击者影响；没有上限的话，病态嵌套载荷会递归到栈爆并拖垮进程。
// 真实工具输入最多嵌套几层，因此它只会在被滥用时触发。
const maxRedactDepth = 32

// depthLimitPlaceholder 顶替所有低于 maxRedactDepth 的内容。在那里返回原始值等于
// 交回一个未清洗的字符串——那正是本包存在要防止的——所以保险方向是丢弃它。
const depthLimitPlaceholder = "[REDACTED DEPTH LIMIT]"

// InputMap 返回 m 的副本，其中每个字符串值都过一遍 Text，包括 map 与 slice 里
// 嵌套的字符串。
//
// 递归遍历是承重功能而非防御性修饰：provider 记录结构化工具输入，Codex 把一次
// 文件编辑记录成 changes[]{path, diff, content}。只做顶层清洗的话，patch 正文里
// 的凭据——或被删 .env 的完整内容——会在去往数据库与 WebSocket 广播的路上原样
// 留下。
func InputMap(m map[string]any) map[string]any {
	return redactMap(m, 0)
}

// redactMap 清洗一个 JSON 对象：逐键清洗其值。
func redactMap(m map[string]any, depth int) map[string]any {
	if m == nil {
		return nil
	}
	if depth >= maxRedactDepth {
		return map[string]any{"_": depthLimitPlaceholder}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = redactValue(v, depth+1)
	}
	return out
}

// redactValue 清洗单个解码后的 JSON 值，递归处理 json.Unmarshal 产生的复合形态，
// 外加 provider 用于 argv 风格输入的 []string。
//
// 复合结构是拷贝清洗而非原地清洗：调用方仍持有原 map 并在打码后继续使用（daemon
// handler 会打日志并再读它），经由共享引用就地改写会是一种"隔空惊吓"。
func redactValue(v any, depth int) any {
	if depth >= maxRedactDepth {
		return depthLimitPlaceholder
	}
	switch t := v.(type) {
	case string:
		return Text(t)
	case map[string]any:
		return redactMap(t, depth)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(e, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = Text(e)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(t))
		for k, e := range t {
			out[k] = Text(e)
		}
		return out
	default:
		return v
	}
}

// Text 扫描输入字符串中的已知机密模式，把匹配替换为安全占位符。
//
// 刻意不打码本地 home 目录。这种打码以前在这里，但它从来不是真正的边界：能读转写
// 记录的人本就能看到仓库路径、文件内容、命令与 diff，藏一个路径段什么也保护不了，
// 却让路径没法复制粘贴与调试。它对"隐藏谁的 home"也自相矛盾——Content/Output 是在
// server 的摄入 handler 里打码的，托管部署会用 *server* 的 home 去匹配，而不是跑
// agent 的那台机器。转写可见性是授权问题，应在那一层处理，而不是靠这里的字符串
// 替换。
func Text(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.replacement)
	}
	return s
}
