// 本文件维护「模型别名 → 单价」的静态价目表：把 agent 运行时上报的自由格式模型名
// （可能带厂商前缀、上下文窗口后缀、大小写变体）匹配到已知型号，从而把 token 用量换算
// 成估算成本。价目表写死于此、按需更新，前端另有一份镜像表
// （packages/views/runtimes/utils.ts），两表需保持同步。
package metrics

import (
	"regexp"
	"strings"
)

// CostUSDTicksPerUSD 是厂商上报成本的计量刻度：xAI 上报时以 1e-10 美元为整体刻度。
// 常量写在此处而非从 pkg/agent 导入（该包负责线格式解析），是为了让本包不依赖 agent
// 运行时就能表达物理单位；两边数值必须保持一致。
const CostUSDTicksPerUSD = 10_000_000_000

// ModelPrice 一个型号的单价表，均为「每百万 token 的美元数」。
// Provider/Model 是归一化后的标签值；四个价格字段分别是输入、缓存读、缓存写、输出单价。
type ModelPrice struct {
	Provider       string
	Model          string
	InputPerM      float64
	CacheReadPerM  float64
	CacheWritePerM float64
	OutputPerM     float64
}

// modelPrices 价目表，键为规范别名（provider:model）。金额与以下注释中的口径随时可能
// 随厂商调价而变，改价必须先核对厂商官方公告。
var modelPrices = map[string]ModelPrice{
	// GPT-5.6 系列（Codex `codex` provider）。官方价来自 OpenAI 的 GPT-5.6 发布公告
	// （openai.com/index/previewing-gpt-5-6-sol）。从 5.6 起，缓存读按 0.1 倍输入价计
	// （相当于打了 9 折），缓存写按未缓存输入价的 1.25 倍计——与更早的 OpenAI SKU 不同，
	// 那些型号的缓存写不单独计费。注意：Codex 的 app-server 用量流（0.144.1）目前还不会
	// 单独上报 cache-write token，所以这些 token 暂时落在普通 input 里按 1 倍计费；
	// 下面 CacheWrite 的单价是对的，只是对 Codex 尚未被真正触发。
	"openai:gpt-5.6-sol":   {Provider: "openai", Model: "gpt-5.6-sol", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 30.00},
	"openai:gpt-5.6-terra": {Provider: "openai", Model: "gpt-5.6-terra", InputPerM: 2.50, CacheReadPerM: 0.25, CacheWritePerM: 3.125, OutputPerM: 15.00},
	"openai:gpt-5.6-luna":  {Provider: "openai", Model: "gpt-5.6-luna", InputPerM: 1.00, CacheReadPerM: 0.10, CacheWritePerM: 1.25, OutputPerM: 6.00},
	"openai:gpt-5.5":       {Provider: "openai", Model: "gpt-5.5", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 0.50, OutputPerM: 30.00},
	"openai:gpt-5.4":       {Provider: "openai", Model: "gpt-5.4", InputPerM: 2.50, CacheReadPerM: 0.25, CacheWritePerM: 0.25, OutputPerM: 15.00},
	"openai:gpt-5.4-mini":  {Provider: "openai", Model: "gpt-5.4-mini", InputPerM: 0.75, CacheReadPerM: 0.075, CacheWritePerM: 0.075, OutputPerM: 4.50},
	"openai:gpt-5.3-codex": {Provider: "openai", Model: "gpt-5.3-codex", InputPerM: 1.75, CacheReadPerM: 0.175, CacheWritePerM: 0.175, OutputPerM: 14.00},
	"openai:gpt-5.2-codex": {Provider: "openai", Model: "gpt-5.2-codex", InputPerM: 1.75, CacheReadPerM: 0.175, CacheWritePerM: 0.175, OutputPerM: 14.00},
	// Anthropic Sonnet 5 的发布价是 $2/$10，有效期到 2026-08-31。这张静态表还无法
	// 调度「发布期结束后涨到 $3/$15」的定时变化，所以先保留发布价，等目录（catalog）
	// 支持再更新这一行。
	"anthropic:claude-sonnet-5":   {Provider: "anthropic", Model: "claude-sonnet-5", InputPerM: 2.00, CacheReadPerM: 0.20, CacheWritePerM: 2.50, OutputPerM: 10.00},
	"anthropic:claude-fable-5":    {Provider: "anthropic", Model: "claude-fable-5", InputPerM: 10.00, CacheReadPerM: 1.00, CacheWritePerM: 12.50, OutputPerM: 50.00},
	"anthropic:claude-opus-5":     {Provider: "anthropic", Model: "claude-opus-5", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.8":   {Provider: "anthropic", Model: "claude-opus-4.8", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.7":   {Provider: "anthropic", Model: "claude-opus-4.7", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.6":   {Provider: "anthropic", Model: "claude-opus-4.6", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-opus-4.5":   {Provider: "anthropic", Model: "claude-opus-4.5", InputPerM: 5.00, CacheReadPerM: 0.50, CacheWritePerM: 6.25, OutputPerM: 25.00},
	"anthropic:claude-sonnet-4.6": {Provider: "anthropic", Model: "claude-sonnet-4.6", InputPerM: 3.00, CacheReadPerM: 0.30, CacheWritePerM: 3.75, OutputPerM: 15.00},
	"anthropic:claude-sonnet-4.5": {Provider: "anthropic", Model: "claude-sonnet-4.5", InputPerM: 3.00, CacheReadPerM: 0.30, CacheWritePerM: 3.75, OutputPerM: 15.00},
	"anthropic:claude-haiku-4.5":  {Provider: "anthropic", Model: "claude-haiku-4.5", InputPerM: 1.00, CacheReadPerM: 0.10, CacheWritePerM: 1.25, OutputPerM: 5.00},
	"deepseek:v4-pro":             {Provider: "deepseek", Model: "v4-pro", InputPerM: 1.74, CacheReadPerM: 0.0145, CacheWritePerM: 1.74, OutputPerM: 3.48},
	"deepseek:v4-flash":           {Provider: "deepseek", Model: "v4-flash", InputPerM: 0.56, CacheReadPerM: 0.0112, CacheWritePerM: 0.56, OutputPerM: 1.12},
	"minimax:m2.7":                {Provider: "minimax", Model: "m2.7", InputPerM: 0.30, CacheReadPerM: 0.06, CacheWritePerM: 0.375, OutputPerM: 1.20},
	"minimax:m2.7-highspeed":      {Provider: "minimax", Model: "m2.7-highspeed", InputPerM: 0.60, CacheReadPerM: 0.06, CacheWritePerM: 0.375, OutputPerM: 2.40},
	"google:gemini-3-flash":       {Provider: "google", Model: "gemini-3-flash", InputPerM: 0.50, CacheReadPerM: 0.05, CacheWritePerM: 0.50, OutputPerM: 3.00},
	"google:gemini-3.1-pro":       {Provider: "google", Model: "gemini-3.1-pro", InputPerM: 2.00, CacheReadPerM: 0.20, CacheWritePerM: 2.00, OutputPerM: 12.00},
	"google:gemini-2.5-pro":       {Provider: "google", Model: "gemini-2.5-pro", InputPerM: 1.25, CacheReadPerM: 0.31, CacheWritePerM: 1.25, OutputPerM: 10.00},
	"google:gemini-2.5-flash":     {Provider: "google", Model: "gemini-2.5-flash", InputPerM: 0.30, CacheReadPerM: 0.03, CacheWritePerM: 0.30, OutputPerM: 2.50},
	// xAI Grok（docs.x.ai/developers/pricing）。按短上下文档计价：xAI 在 prompt 到达
	// 20 万 token 时会把整笔请求按 2 倍计费，但一条用量记录聚合的是整轮里的所有模型调用，
	// 无从知道单次请求落在哪个档——于是统一按标准档计价，对长上下文轮次最多低估 50%，
	// 而不是对短上下文轮次高估 100%。xAI 不公布单独的缓存写价（写入按普通输入计费），
	// 所以 CacheWrite 与 Input 相同。这些行与 packages/views/runtimes/utils.ts 互为镜像，
	// 两表必须保持同步。`grok-composer-*` 虽在 Grok Build 目录里，但价格表上没有它，
	// 所以保持不映射，而不去猜一个费率挂上去。
	"xai:grok-4.6":                     {Provider: "xai", Model: "grok-4.6", InputPerM: 2.00, CacheReadPerM: 0.50, CacheWritePerM: 2.00, OutputPerM: 6.00},
	"xai:grok-4.5":                     {Provider: "xai", Model: "grok-4.5", InputPerM: 2.00, CacheReadPerM: 0.30, CacheWritePerM: 2.00, OutputPerM: 6.00},
	"xai:grok-4.3":                     {Provider: "xai", Model: "grok-4.3", InputPerM: 1.25, CacheReadPerM: 0.20, CacheWritePerM: 1.25, OutputPerM: 2.50},
	"xai:grok-build-0.1":               {Provider: "xai", Model: "grok-build-0.1", InputPerM: 1.00, CacheReadPerM: 0.20, CacheWritePerM: 1.00, OutputPerM: 2.00},
	"xai:grok-4.20-multi-agent-0309":   {Provider: "xai", Model: "grok-4.20-multi-agent-0309", InputPerM: 1.25, CacheReadPerM: 0.20, CacheWritePerM: 1.25, OutputPerM: 2.50},
	"xai:grok-4.20-0309-reasoning":     {Provider: "xai", Model: "grok-4.20-0309-reasoning", InputPerM: 1.25, CacheReadPerM: 0.20, CacheWritePerM: 1.25, OutputPerM: 2.50},
	"xai:grok-4.20-0309-non-reasoning": {Provider: "xai", Model: "grok-4.20-0309-non-reasoning", InputPerM: 1.25, CacheReadPerM: 0.20, CacheWritePerM: 1.25, OutputPerM: 2.50},
	// 阿里 Qwen（models.dev 的 providers/alibaba，2026-08-13 查取；数据来源为
	// alibabacloud.com 的 model-pricing——国际 ≤256K 输入档——以及 qwencloud.com 的
	// 模型页）。qwen3.7-plus 与 qwen3.6-flash 取已公布的「国际 ≤256K」费率；
	// CacheWritePerM 是「显式建缓存」费率（输入价的 1.25 倍），CacheReadPerM 是
	// 「显式读缓存」费率（输入价的 0.1 倍）。qwen3.8-max 按其 qwencloud.com/models/
	// qwen3.8-max 页面（即 models.dev 引用的来源）公布的按量付费费率定价；
	// qwen3.8-max-preview 走阿里 Token Plan 订阅，不按 token 计费，故价格为 0
	// （与免费 GLM flash 档的约定一致）。与 packages/views/runtimes/utils.ts 互为镜像。
	"alibaba:qwen3.7-plus":        {Provider: "alibaba", Model: "qwen3.7-plus", InputPerM: 0.40, CacheReadPerM: 0.04, CacheWritePerM: 0.50, OutputPerM: 1.60},
	"alibaba:qwen3.6-flash":       {Provider: "alibaba", Model: "qwen3.6-flash", InputPerM: 0.25, CacheReadPerM: 0.025, CacheWritePerM: 0.3125, OutputPerM: 1.50},
	"alibaba:qwen3.8-max":         {Provider: "alibaba", Model: "qwen3.8-max", InputPerM: 2.00, CacheReadPerM: 0.17, CacheWritePerM: 2.50, OutputPerM: 6.00},
	"alibaba:qwen3.8-max-preview": {Provider: "alibaba", Model: "qwen3.8-max-preview", InputPerM: 0, CacheReadPerM: 0, CacheWritePerM: 0, OutputPerM: 0},
	// 月之暗面 Kimi K3（platform.kimi.ai/docs/pricing/chat-k3，经 models.dev 的
	// providers/moonshotai/models/kimi-k3.toml）。Moonshot 不单独收缓存写费用，
	// 所以 CacheWritePerM 与 Input 相同。
	"moonshotai:kimi-k3": {Provider: "moonshotai", Model: "kimi-k3", InputPerM: 3.0, CacheReadPerM: 0.30, CacheWritePerM: 3.0, OutputPerM: 15.0},
	// 火山方舟 Ark（ark.cn-beijing.volces.com）。`ark-code-latest` 是可滚动切换的
	// 别名：目标模型可在火山控制台跨系列切换，因此不是稳定的模型身份；daemon 上报的
	// 就是别名本身，从不给解析后的模型。该别名没有公布费率，所以保持不映射，不去猜一个
	// 费率挂上去（与 xAI 的 `grok-composer-*` 同一约定）。
}

// modelAliasRules 把「自由格式的模型字符串」逐个正则试配，命中即映射到某条价目。
// 顺序敏感：排在前面的规则优先。每条规则都要小心处理前缀/后缀变体与点横写法差异，
// 匹配规则与 packages/views/runtimes/utils.ts 的前端解析逻辑保持一致。
var modelAliasRules = []struct {
	re       *regexp.Regexp
	priceKey string
}{
	// 锚定精确匹配：effort 放在独立字段里，模型 id 就是裸 slug。锚定到 `$` 是为了把
	// 未知变体（`gpt-5.6-luna-pro`、`gpt-5.6-luna/x`）挡在门外。注意这里的 `.` 是
	// LITERAL 点号，不是老规则里的 `[.-]` 字符类——真实 Codex slug 永远是点分形式
	// （`gpt-5.6-luna`），而前端解析器（utils.ts）不做连字符归一化，所以连字符的
	// `gpt-5-6-luna` 必须在两侧都表现为未映射，而不是在这里悄悄借用某档位。
	{regexp.MustCompile(`(^|/|:)gpt-5\.6-sol$`), "openai:gpt-5.6-sol"},
	{regexp.MustCompile(`(^|/|:)gpt-5\.6-terra$`), "openai:gpt-5.6-terra"},
	{regexp.MustCompile(`(^|/|:)gpt-5\.6-luna$`), "openai:gpt-5.6-luna"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]5$|^gpt-5-5$`), "openai:gpt-5.5"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]4($|-2026-03-05|-xhigh)`), "openai:gpt-5.4"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]4-mini($|[^a-z0-9])`), "openai:gpt-5.4-mini"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]3-codex$`), "openai:gpt-5.3-codex"},
	{regexp.MustCompile(`(^|/|:)gpt-5[.-]2-codex$`), "openai:gpt-5.2-codex"},
	{regexp.MustCompile(`claude-sonnet-5|claude-5-sonnet`), "anthropic:claude-sonnet-5"},
	{regexp.MustCompile(`claude-fable-5`), "anthropic:claude-fable-5"},
	{regexp.MustCompile(`claude-opus-5`), "anthropic:claude-opus-5"},
	{regexp.MustCompile(`claude-opus-4[-.]8`), "anthropic:claude-opus-4.8"},
	{regexp.MustCompile(`claude-opus-4[-.]7`), "anthropic:claude-opus-4.7"},
	{regexp.MustCompile(`claude-opus-4[-.]6`), "anthropic:claude-opus-4.6"},
	{regexp.MustCompile(`claude-opus-4[-.]5`), "anthropic:claude-opus-4.5"},
	{regexp.MustCompile(`claude-sonnet-4[-.]6|claude-4[-.]6-sonnet`), "anthropic:claude-sonnet-4.6"},
	{regexp.MustCompile(`claude-sonnet-4[-.]5|claude-4[-.]5-sonnet`), "anthropic:claude-sonnet-4.5"},
	{regexp.MustCompile(`claude-haiku-4[-.]5`), "anthropic:claude-haiku-4.5"},
	{regexp.MustCompile(`deepseek-v4-pro`), "deepseek:v4-pro"},
	{regexp.MustCompile(`deepseek-v4-flash|^deepseek-chat$|^deepseek-reasoner$`), "deepseek:v4-flash"},
	{regexp.MustCompile(`minimax-m2[.]7.*highspeed|highspeed.*minimax-m2[.]7`), "minimax:m2.7-highspeed"},
	{regexp.MustCompile(`minimax-m2[.]7`), "minimax:m2.7"},
	{regexp.MustCompile(`gemini-3-flash`), "google:gemini-3-flash"},
	{regexp.MustCompile(`gemini-3[.]1-pro`), "google:gemini-3.1-pro"},
	{regexp.MustCompile(`gemini-2[.]5-pro`), "google:gemini-2.5-pro"},
	{regexp.MustCompile(`gemini-2[.]5-flash`), "google:gemini-2.5-flash"},
	// 锚定精确匹配、只认点分写法——规则同上面的 gpt-5.6 各条。前端解析器不做非
	// Anthropic id 的连字符归一化，所以连字符的 `grok-4-5` 必须在两侧都表现为未映射，
	// 而不是在这里悄悄借用某档位。
	{regexp.MustCompile(`(^|/|:)grok-4\.6$`), "xai:grok-4.6"},
	{regexp.MustCompile(`(^|/|:)grok-4\.5$`), "xai:grok-4.5"},
	{regexp.MustCompile(`(^|/|:)grok-4\.3$`), "xai:grok-4.3"},
	{regexp.MustCompile(`(^|/|:)grok-build-0\.1$`), "xai:grok-build-0.1"},
	{regexp.MustCompile(`(^|/|:)grok-4\.20-multi-agent-0309$`), "xai:grok-4.20-multi-agent-0309"},
	{regexp.MustCompile(`(^|/|:)grok-4\.20-0309-reasoning$`), "xai:grok-4.20-0309-reasoning"},
	{regexp.MustCompile(`(^|/|:)grok-4\.20-0309-non-reasoning$`), "xai:grok-4.20-0309-non-reasoning"},
	// 阿里 Qwen。所有规则都锚定，让未知后缀变体（`qwen3.7-plus-extra`、
	// `qwen3.8-max-preview-extra`）保持未映射；允许末尾跟一个「至少含一个字符的完整
	// 方括号上下文标签」，与前端剥离上下文标签的行为一致（utils.ts 里的
	// `\[[^\]]+\]$`），空标签如 `qwen3.7-plus[]` 因而在两侧都保持未映射。
	// qwen3.8-max 保持锚定，保证 `qwen3.8-max-preview`（及其 `[1m]` 变体）绝不会
	// 借用到 GA 档的价格。
	{regexp.MustCompile(`(^|/|:)qwen3[.-]7-plus(\[[^\]]+\])?$`), "alibaba:qwen3.7-plus"},
	{regexp.MustCompile(`(^|/|:)qwen3[.-]6-flash(\[[^\]]+\])?$`), "alibaba:qwen3.6-flash"},
	{regexp.MustCompile(`(^|/|:)qwen3[.-]8-max(\[[^\]]+\])?$`), "alibaba:qwen3.8-max"},
	{regexp.MustCompile(`(^|/|:)qwen3[.-]8-max-preview(\[[^\]]+\])?$`), "alibaba:qwen3.8-max-preview"},
	// Kimi K3。锚定以保证另一个 CodeBuddy SKU `kimi-k3-1` 保持未映射；
	// `kimi-code/k3`（Kimi Code CLI）经 `/k3$` 形式解析。
	{regexp.MustCompile(`(^|/|:)kimi-k3$`), "moonshotai:kimi-k3"},
	{regexp.MustCompile(`(^|/|:)k3$`), "moonshotai:kimi-k3"},
	// 火山方舟 Ark 的 `ark-code-latest` 刻意缺席：它是可在控制台跨模型系列切换的滚动
	// 别名，不是稳定的模型身份，所以保持未映射。
}

// contextTagRe 匹配模型 id 末尾的「上下文窗口变体」标签，例如 Claude Code 追加的
// `[1m]`。要求是完整的方括号标签、内部至少一个字符、锚定在结尾——与前端 stripContextTag
// 剥掉的样子一致（utils.ts 中的 `\[[^\]]+\]$`）；空标签（`model[]`）与残缺的结尾
// 方括号（`model[`）因此在两侧都保持未映射。
var contextTagRe = regexp.MustCompile(`\[[^\]]+\]$`)

// matchModelAlias 逐个试配规则，第一个命中的规则决定落到哪条价目；全部落空返回
// (零值, false)。规则表顺序即优先级。
func matchModelAlias(model string) (ModelPrice, bool) {
	for _, rule := range modelAliasRules {
		if rule.re.MatchString(model) {
			price, ok := modelPrices[rule.priceKey]
			return price, ok
		}
	}
	return ModelPrice{}, false
}

// PriceForModelAlias 给定一个模型字符串，返回它对应的单价与是否命中。先对原始 id
// 做规则匹配；没中时再剥掉尾部上下文标签（如 `kimi-k3[1m]`、`grok-4.5[1m]`）重试——
// 带标签的型号与裸型号是同 SKU 同档位。锚定的 Codex/Grok/Kimi 规则都止于 `$`，
// 没有这一步，带方括号的变体就会在 RecordLLMUsage 里走「未定价」分支。
// 该步骤只会把 miss 变成 hit——原始形态先试，所以显式带方括号的规则仍然优先。
func PriceForModelAlias(model string) (ModelPrice, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if price, ok := matchModelAlias(model); ok {
		return price, true
	}
	// 原始 id 没解析出来：可能是 harness 追加的上下文窗口标签。剥掉再试一次。
	if stripped := contextTagRe.ReplaceAllString(model, ""); stripped != model {
		return matchModelAlias(stripped)
	}
	return ModelPrice{}, false
}

// tokenCostUSD 把 token 数按「每百万单价」换算成美元。tokens 或单价非正时按 0 处理，
// 避免免费档/0 单价产生垃圾样本或负成本。
func tokenCostUSD(tokens int64, pricePerM float64) float64 {
	if tokens <= 0 || pricePerM <= 0 {
		return 0
	}
	return float64(tokens) * pricePerM / 1_000_000
}
