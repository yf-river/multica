package agent

// 文件职责：本文件为 claude、codex、opencode、pi、kimi 等后端发现“按模型区分的
// reasoning/effort 档位目录”（即每个模型支持哪些思考强度），好让 daemon 把真实能力宣传给 UI，
// 而不是硬编码（并常常猜错）本机装了什么的档位表。
//
// MUL-2339：刻意不把 Claude 的 `low|medium|high|xhigh|max` 和 Codex 的
// `none|minimal|low|medium|high|xhigh|max|ultra` 压到一套共享枚举上。OpenCode 通过
// `opencode run --variant` 暴露 provider 专属的模型变体，且本地 opencode.json 配置还能扩展
// 这些名字。用户选的档位必须能在各 CLI 自己的值词汇表里精确往返。
//
// 数据来源：claude 解析 `claude --help` 的 `--effort` 帮助行（再用手维护的按模型表收敛）；
// codex 走结构化命令 `codex debug models --bundled`（0.122.0+）；CodeBuddy 复用 ACP
// session/new 响应里同一个 `thought_level` 配置项，不额外起进程；结果都放进带 TTL 的缓存。
// 文件后半部分是服务端/daemon 共用的校验入口（ValidateThinkingLevel/IsKnownThinkingValue 等）
// 以及“哪些 provider 用动态目录 / 固定枚举”的分类表。
//
// 内部组织：按 provider 分节（Cache → Claude → Codex → CodeBuddy → 共享校验），每节有各自的
// 说明头。
import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── 缓存 ────────────────────────────────────────────────────────────
//
// 发现结果以 (provider, command, cliVersion) 为键缓存。本地 CLI 升级会让引用“旧版本 help/
// `debug models` 输出”的条目失效——这正是 Anthropic / OpenAI 增删档位时我们踩过的失败模式
// （Elon 的 review 意见）。
//
// command 用 Command.cacheKey() 而不是裸路径：两个自定义运行时 profile 可能用不同启动前缀包
// 同一个二进制，并从中得到不同答案。

// thinkingCacheKey 是 thinking 目录缓存的键：provider + 启动命令缓存键 + CLI 版本。
type thinkingCacheKey struct {
	provider   string
	command    string
	cliVersion string
}

// thinkingCacheEntry 是缓存里的一条：按模型 ID 索引的档位目录 + 过期时间。
type thinkingCacheEntry struct {
	value     map[string]*ModelThinking // 键为模型 ID
	expiresAt time.Time
}

// thinkingDiscoveryTTL 是 thinking 目录发现的缓存时长。取 10 分钟：档位表随 CLI 版本走，
// 同一版本内几乎不变，10 分钟足够新鲜又不至于反复起子进程。
const thinkingDiscoveryTTL = 10 * time.Minute

var (
	thinkingCacheMu sync.Mutex
	thinkingCache   = map[thinkingCacheKey]thinkingCacheEntry{}
)

// thinkingCacheGet 读缓存；未命中或已过期返回 not-ok。
func thinkingCacheGet(key thinkingCacheKey) (map[string]*ModelThinking, bool) {
	thinkingCacheMu.Lock()
	defer thinkingCacheMu.Unlock()
	entry, ok := thinkingCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// thinkingCachePut 写入一条缓存记录（带 TTL）。
func thinkingCachePut(key thinkingCacheKey, value map[string]*ModelThinking) {
	thinkingCacheMu.Lock()
	defer thinkingCacheMu.Unlock()
	thinkingCache[key] = thinkingCacheEntry{value: value, expiresAt: time.Now().Add(thinkingDiscoveryTTL)}
}

// resetThinkingCacheForTests 仅供测试清空缓存；生产代码只能依赖 TTL 或进程重启来失效。
func resetThinkingCacheForTests() {
	thinkingCacheMu.Lock()
	thinkingCache = map[thinkingCacheKey]thinkingCacheEntry{}
	thinkingCacheMu.Unlock()
}

// ── Claude ───────────────────────────────────────────────────────────
//
// `claude --help` 会宣传 `--effort <level>`，括号里带完整的档位超集；我们解析那一行来得知
// “这台主机上的这个 CLI 版本接受哪些档位”。按模型的缺口（仅 Opus 可用的 `xhigh`、仅会话级
// 的 `max`）来自一张手维护的表，因为 CLI 不暴露“模型→effort”的编程式映射。

// claudeEffortRe 匹配 `claude --help` 输出的帮助行：
//
//	--effort <level>   Effort level for the current session (low, medium, high, xhigh, max)
//
// 锚定在 `--effort` 上、对空白宽松，这样 flag 名的格式变化（`--effort=…`、缩进帮助块）不会弄坏
// 解析。
var claudeEffortRe = regexp.MustCompile(`--effort\s*(?:<[^>]+>)?\s*(?:Effort level[^(]*)?\(([^)]+)\)`)

// claudeEffortLabel 把 Claude 的原始档位 token 映射成 UI 渲染用的展示文案。标题式大小写与
// Anthropic 自己的 slash UI 一致。
var claudeEffortLabel = map[string]string{
	"low":    "Low",
	"medium": "Medium",
	"high":   "High",
	"xhigh":  "Extra high",
	"max":    "Max",
}

// claudeModelEffortAllow 在上游文档说“只有部分档位有效”的模型上收窄档位集合。模型缺失/不在
// 表里 → 直接用解析出的超集（当前 Claude Code 默认行为）。Anthropic 发布不支持 `xhigh`/`max`
// 的新模型时要更新这张表。
var claudeModelEffortAllow = map[string]map[string]bool{
	// Opus 是唯一公开支持 xhigh 的模型；帮助列表里 Sonnet / Haiku 仍带它，所以在这里过滤。
	"claude-opus-5":             {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	"claude-opus-4-8":           {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	"claude-opus-4-7":           {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	"claude-opus-4-6":           {"low": true, "medium": true, "high": true, "xhigh": true, "max": true},
	"claude-sonnet-4-6":         {"low": true, "medium": true, "high": true, "max": true},
	"claude-sonnet-4-5":         {"low": true, "medium": true, "high": true, "max": true},
	"claude-haiku-4-5-20251001": {"low": true, "medium": true, "high": true},
}

// claudeStaticEffortFallback 是“解析 `--effort` 帮助行失败（二进制缺失、输出漂移等）”时使用的
// 保守子集。从近期各 Claude Code 发布版本的“最小公分母”里挑出来。
var claudeStaticEffortFallback = []string{"low", "medium", "high"}

// claudeStaticEffortFullSuperset 是 `claude --help` 在 2.1.121 上列出的档位全集。当模型不在
// 按模型白名单里时当目录超集用——宁可多提供让 CLI 去拒绝，也不人为挡住合法组合。
var claudeStaticEffortFullSuperset = []string{"low", "medium", "high", "xhigh", "max"}

// annotateClaudeThinking 通过跑一次 `claude --help` 并把解析出的超集投影过
// claudeModelEffortAllow，来填充每条目录记录的 Thinking 字段。错误被静默吸收，这样缺失的 CLI
// 不会弄坏模型列表——UI 只是隐藏该模型的档位选择器。
func annotateClaudeThinking(ctx context.Context, models []Model, cmd Command) {
	mapping := loadClaudeThinkingByModel(ctx, cmd)
	for i := range models {
		if t, ok := mapping[models[i].ID]; ok && t != nil {
			models[i].Thinking = t
		}
	}
}

// loadClaudeThinkingByModel 为 Claude 静态目录里的每个模型算出档位目录并缓存。默认档位写死
// medium——与 Claude 自己对“未指定 effort”的处理一致。
func loadClaudeThinkingByModel(ctx context.Context, cmd Command) map[string]*ModelThinking {
	if cmd.Path == "" {
		cmd.Path = "claude"
	}
	version, _ := DetectVersion(ctx, cmd)
	key := thinkingCacheKey{provider: "claude", command: cmd.cacheKey(), cliVersion: version}
	if cached, ok := thinkingCacheGet(key); ok {
		return cached
	}

	superset := claudeEffortSuperset(ctx, cmd)
	result := map[string]*ModelThinking{}
	for _, m := range claudeStaticModels() {
		allow := claudeModelEffortAllow[m.ID]
		levels := projectClaudeLevels(superset, allow)
		if len(levels) == 0 {
			continue
		}
		result[m.ID] = &ModelThinking{
			SupportedLevels: levels,
			DefaultLevel:    "medium",
		}
	}
	thinkingCachePut(key, result)
	return result
}

// claudeEffortSuperset 返回解析出的 `--effort` 取值列表。help 输出完全抓不到时返回静态兜底而
// 不是空，这样调用方仍能渲染一个可用的选择器。
func claudeEffortSuperset(ctx context.Context, runtimeCmd Command) []string {
	cmd := runtimeCmd.exec(ctx, "--help")
	hideAgentWindow(cmd)
	out, err := combinedOutputOwned(cmd, runtimeCmd.logger)
	if err != nil {
		return append([]string(nil), claudeStaticEffortFallback...)
	}
	return claudeEffortLevelsFromHelp(string(out))
}

// claudeEffortLevelsFromHelp 从一份成功抓到的 `claude --help` 里决定档位超集。三种情况：
//   - 值列表解析成功 → 原样使用；
//   - 有 `--effort` 但值列表没解析出来 → help 格式漂移了；退回“最后一个已知正确超集”，这样在
//     我们手工改兜底之前，更新的档位仍会被提供；
//   - 完全没有 `--effort` → 安装的 CLI 比这个 flag 老。返回空档位：提供任何档位都会让 daemon
//     通过 ValidateThinkingLevel 并注入 --effort，而这样的二进制会用
//     `error: unknown option '--effort'` 拒绝——对持久化了 thinking_level 的 agent，这会硬性
//     搞挂每一个任务，而不是降级成一次普通运行。
func claudeEffortLevelsFromHelp(helpText string) []string {
	parsed := parseClaudeEffortHelp(helpText)
	if len(parsed) > 0 {
		return parsed
	}
	if strings.Contains(helpText, "--effort") {
		return append([]string(nil), claudeStaticEffortFullSuperset...)
	}
	return nil
}

// parseClaudeEffortHelp 从一条 `--effort` 帮助行里抽逗号分隔的值列表。行缺失或捕获组为空时
// 返回 nil，让调用方可以走兜底路径。
func parseClaudeEffortHelp(helpText string) []string {
	match := claudeEffortRe.FindStringSubmatch(helpText)
	if len(match) < 2 {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(match[1], ",") {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		out = append(out, token)
	}
	return out
}

// projectClaudeLevels 把档位超集投影成 ThinkingLevel 列表，并过滤掉 allow 白名单里不允许的
// 值（allow 为 nil 表示不限制）。
func projectClaudeLevels(superset []string, allow map[string]bool) []ThinkingLevel {
	out := make([]ThinkingLevel, 0, len(superset))
	for _, value := range superset {
		if allow != nil && !allow[value] {
			continue
		}
		label, ok := claudeEffortLabel[value]
		if !ok {
			// daemon 还没见过的新值——原样浮出来，让高级用户仍能选中。
			label = strings.Title(value) //nolint:staticcheck
		}
		out = append(out, ThinkingLevel{Value: value, Label: label})
	}
	return out
}

// ── Codex ────────────────────────────────────────────────────────────
//
// `codex debug models --bundled` 是可看见的模型目录、每个模型的 reasoning 目录以及 service
// tier 的结构化发现入口。OpenAI 在 Codex 0.122.0 一并加入了这个命令与 `--bundled` flag
// （openai/codex #18625）。旧版本、调用失败、以及畸形/空载荷都退回 codexStaticModels，保证
// 选择器仍可用。
//
// 采用它而不是旧的“制造配置错误去探测”把戏，因为：
//   1. 它给出逐模型子集，不需要手维护的表。
//   2. schema 是结构化的，且自 0.122.0 面世以来一直稳定。
//   3. 它不会用一次故意的配置错误去污染 stderr。
//
// 子命令默认把 JSON 打到 stdout——没有 `--output json` flag（本代码早一版传过这个 flag，在
// 0.131.0 上静默失败）。加 `--bundled` 是为了跳过网络刷新：发现每次 daemon 轮询都会跑，这里
// 多一次网络往返会把选择器卡在用户网络连接后面。bundled 目录决定的是本地二进制真正接受哪些
// `model_reasoning_effort` token——这正是校验唯一需要的东西。
//
// 静态兜底刻意镜像一份“近期验证过的 bundled 模型/thinking 目录”。它不猜 service-tier 可用性。

// codexEffortLabel 是每个 Codex effort 值的人类可读文案，与 Codex 自己的 TUI 一致
// （`Extra high`、`Minimal`、…），让用户在我们的选择器和 `codex /model` 里看到相同的标签。
var codexEffortLabel = map[string]string{
	"none":    "None",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
	"ultra":   "Ultra",
}

const (
	minCodexDebugModelsVersion = "0.122.0"
	// Codex 0.133.0 是第一个包含 openai/codex#23537 加入的、仅请求时才有的 `default` 哨兵值
	// 的稳定版。
	minCodexExplicitStandardServiceTierVersion = "0.133.0"
)

// codexDebugModelsResponse 镜像 `codex debug models --bundled`（Codex 0.122.0+）输出的 JSON
// 形状。只类型化我们消费的字段；未知键被忽略。
type codexDebugModelsResponse struct {
	Models []codexDebugModel `json:"models"`
}

// codexDebugModel 是 bundled 目录里单个模型记录：slug/显示名/可见性、默认与支持的 reasoning
// 档位、以及可用的 service tier。
type codexDebugModel struct {
	Slug                    string                     `json:"slug"`
	DisplayName             string                     `json:"display_name"`
	Visibility              string                     `json:"visibility"`
	DefaultReasoningLevel   string                     `json:"default_reasoning_level"`
	SupportedReasoningLevel []codexDebugReasoningLevel `json:"supported_reasoning_levels"`
	ServiceTiers            []codexDebugServiceTier    `json:"service_tiers"`
}

// codexDebugReasoningLevel 是一个 reasoning 档位条目：effort token + 说明文案。
type codexDebugReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexDebugServiceTier 是一个 service tier 条目。
type codexDebugServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// discoverCodexModels 返回已装 Codex 二进制 bundled 的可见目录（含 reasoning 元数据）。
// 版本检测放在 debug 命令之前，避免旧二进制每次缓存刷新都记一条可预期的 "unknown command"
// 失败。
func discoverCodexModels(ctx context.Context, cmd Command) []Model {
	if cmd.Path == "" {
		cmd.Path = "codex"
	}
	version, err := DetectVersion(ctx, cmd)
	if err != nil {
		return codexStaticModels()
	}
	supportsExplicitStandard := codexSupportsExplicitStandardServiceTier(version)
	if !codexSupportsDebugModels(version) {
		return annotateCodexExplicitStandardServiceTier(codexStaticModels(), supportsExplicitStandard)
	}

	raw, err := runCodexDebugModels(ctx, cmd)
	if err != nil {
		return annotateCodexExplicitStandardServiceTier(codexStaticModels(), supportsExplicitStandard)
	}
	models, err := parseCodexModelCatalog(raw)
	if err != nil || len(models) == 0 {
		return annotateCodexExplicitStandardServiceTier(codexStaticModels(), supportsExplicitStandard)
	}
	return annotateCodexExplicitStandardServiceTier(models, supportsExplicitStandard)
}

// codexSupportsDebugModels 判断该版本是否支持 `codex debug models`（≥ 0.122.0）。
func codexSupportsDebugModels(version string) bool {
	return codexVersionAtLeast(version, minCodexDebugModelsVersion)
}

// codexSupportsExplicitStandardServiceTier 判断该版本是否支持显式的 standard 服务档位哨兵
// （≥ 0.133.0）。
func codexSupportsExplicitStandardServiceTier(version string) bool {
	return codexVersionAtLeast(version, minCodexExplicitStandardServiceTierVersion)
}

// codexVersionAtLeast 用语义化版本比较判断 version ≥ minimumVersion；任一侧解析失败都回答否。
func codexVersionAtLeast(version, minimumVersion string) bool {
	parsed, err := parseSemver(version)
	if err != nil {
		return false
	}
	minimum, err := parseSemver(minimumVersion)
	if err != nil {
		return false
	}
	return !parsed.lessThan(minimum)
}

// annotateCodexExplicitStandardServiceTier 在受支持的版本上给目录里每个模型打上“可显式
// standard 档位”标记；不支持的版本原样返回。
func annotateCodexExplicitStandardServiceTier(models []Model, supported bool) []Model {
	if !supported {
		return models
	}
	for i := range models {
		models[i].SupportsExplicitStandardServiceTier = true
	}
	return models
}

// codexDebugModelsArgs 是我们发现本地 Codex 目录时传的 argv。保持为包级变量（而不是调用点的
// 字面量），是为了让测试能断言真实 `codex` 调用收到的确切形态，而不只是解析器对一条 fixture
// 字符串的行为。argv 形态正是 PR1 review 时被弄坏的契约；钉住它的测试在 thinking_test.go。
var codexDebugModelsArgs = []string{"debug", "models", "--bundled"}

// runCodexDebugModels 执行 `codex debug models --bundled` 并返回 stdout 字节。
func runCodexDebugModels(ctx context.Context, runtimeCmd Command) ([]byte, error) {
	cmd := runtimeCmd.exec(ctx, codexDebugModelsArgs...)
	hideAgentWindow(cmd)
	return outputOwned(cmd, runtimeCmd.logger)
}

// parseCodexModelCatalog 把 CLI 的原始目录投影成 daemon 的线上模型结构。隐藏条目被刻意排除，
// 以对齐 Codex 自己的模型选择器；第一条可见条目就是 bundled 目录偏好的默认模型。
func parseCodexModelCatalog(raw []byte) ([]Model, error) {
	var resp codexDebugModelsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		label = normalizeCodexModelLabel(m.Slug, label)
		models = append(models, Model{
			ID:           m.Slug,
			Label:        label,
			Provider:     "openai",
			Thinking:     codexThinkingFromDebugModel(m),
			ServiceTiers: codexServiceTiersFromDebugModel(m),
		})
	}
	if len(models) > 0 {
		models[0].Default = true
	}
	return models, nil
}

// normalizeCodexModelLabel 对 bundled 目录里显示名不理想的几个 id 覆写为规范展示名。
func normalizeCodexModelLabel(id, label string) string {
	switch id {
	case "gpt-5.6-sol":
		return "GPT-5.6 Sol"
	case "gpt-5.6-terra":
		return "GPT-5.6 Terra"
	case "gpt-5.6-luna":
		return "GPT-5.6 Luna"
	default:
		return label
	}
}

// codexServiceTiersFromDebugModel 把记录里的 service tier 转成对外结构（ID 为空的行丢弃）。
func codexServiceTiersFromDebugModel(m codexDebugModel) []ModelServiceTier {
	tiers := make([]ModelServiceTier, 0, len(m.ServiceTiers))
	for _, tier := range m.ServiceTiers {
		if tier.ID == "" {
			continue
		}
		name := tier.Name
		if name == "" {
			name = tier.ID
		}
		tiers = append(tiers, ModelServiceTier{
			ID:          tier.ID,
			Name:        name,
			Description: tier.Description,
		})
	}
	return tiers
}

// codexThinkingFromDebugModel 从 debug 模型的 supported reasoning 档位构造 ModelThinking；
// 默认档位取记录的 default_reasoning_level。没有档位则返回 nil。
func codexThinkingFromDebugModel(m codexDebugModel) *ModelThinking {
	levels := make([]ThinkingLevel, 0, len(m.SupportedReasoningLevel))
	for _, lvl := range m.SupportedReasoningLevel {
		if lvl.Effort == "" {
			continue
		}
		label, ok := codexEffortLabel[lvl.Effort]
		if !ok {
			// Codex 的 effort token 由目录拥有。新出现的“安全 token”立刻浮出：服务端接受它们
			// 的语法，daemon 也正用这份逐模型目录做兼容性校验。
			label = strings.Title(lvl.Effort) //nolint:staticcheck
		}
		levels = append(levels, ThinkingLevel{
			Value:       lvl.Effort,
			Label:       label,
			Description: lvl.Description,
		})
	}
	if len(levels) == 0 {
		return nil
	}
	return &ModelThinking{
		SupportedLevels: levels,
		DefaultLevel:    m.DefaultReasoningLevel,
	}
}

// ── CodeBuddy ────────────────────────────────────────────────────────
//
// CodeBuddy 和 Claude 一样用 `--effort <level>` flag。档位集从 ACP session/new 响应里的
// `thought_level` 配置项发现——和产出模型目录的是同一次握手——所以不用额外起进程。所有模型共享
// 一份 effort 目录，因为 CodeBuddy 是按会话（而非按模型）宣传它的。

// codebuddyEffortLabel 把 CodeBuddy 的 effort token 映射成 UI 展示文案。
var codebuddyEffortLabel = map[string]string{
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

// codebuddyStaticEffortFallback 是发现够不到 CLI 时用的兜底。它列出 `--effort` 接受的每一个
// 档位（对照 CodeBuddy 2.130.0 确认过：宣传 minimal/low/medium/high/xhigh/max）——旧值漏了
// `minimal` 和 `max`，导致发现一旦降级，即使安装是好的也会丢掉两个真实档位。
var codebuddyStaticEffortFallback = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// codebuddyThinkingByModel 把每个模型都映射到由 levels 构造的共享 effort 目录上。CodeBuddy
// 对整个 CLI 只宣传一份 `--effort` 档位集、不按模型区分，所以每条记录都拿到同一个
// ModelThinking 指针。
func codebuddyThinkingByModel(models []Model, levels []string) map[string]*ModelThinking {
	thinkingLevels := make([]ThinkingLevel, 0, len(levels))
	for _, value := range levels {
		label, ok := codebuddyEffortLabel[value]
		if !ok {
			label = strings.Title(value) //nolint:staticcheck
		}
		thinkingLevels = append(thinkingLevels, ThinkingLevel{Value: value, Label: label})
	}

	result := map[string]*ModelThinking{}
	if len(thinkingLevels) > 0 {
		thinking := &ModelThinking{
			SupportedLevels: thinkingLevels,
			DefaultLevel:    "medium",
		}
		for _, m := range models {
			result[m.ID] = thinking
		}
	}
	return result
}

// applyCodebuddyStaticThinking 用静态 effort 兜底给模型标注档位。用于“发现够不到 CLI”或
// “够到了但没拿到可识别的 thought_level 选项”时。
func applyCodebuddyStaticThinking(models []Model) {
	result := codebuddyThinkingByModel(models, codebuddyStaticEffortFallback)
	for i := range models {
		if t, ok := result[models[i].ID]; ok && t != nil {
			models[i].Thinking = t
		}
	}
}

// codebuddyFlagEffortValues 是 `codebuddy --effort <level>` 实际接受的 token。
//
// ACP 的 `thought_level` 选项会额外宣传一个 `enabled`（"On (default)"）选择——那是会话级开关，
// 不是 flag 参数。daemon 会把选中的档位直通给 `--effort`（codebuddy.go），所以在选择器里暴露
// `enabled` 等于让用户拼出一条 CodeBuddy 会拒绝的命令行。因此要对照这份集合过滤，而不是
// 全盘相信宣传的列表。
var codebuddyFlagEffortValues = map[string]bool{
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

// annotateCodebuddyThinkingFromACP 从承载模型的那份 `session/new` 响应里的 `thought_level`
// 配置项填每个模型的 effort 目录——所以这份 effort 目录一个额外进程都不用花。它取代了对
// `codebuddy --help` 的第二次正则扫描（MUL-5549）。
//
// CodeBuddy 对整个 CLI 宣传一份 effort 档位集而不是按模型，所以每条记录共享它。会被 `--effort`
// flag 拒绝的档位被丢掉；而一个落在 flag 档位集之外的 currentValue（默认的 `enabled`）会变成
// 空 DefaultLevel——UI 把它渲染成笼统的 "Default"，而不是发明一个我们传不过去的档位。
func annotateCodebuddyThinkingFromACP(models []Model, sessionResult json.RawMessage) {
	levels, defaultLevel := parseACPCodebuddyEffort(sessionResult)
	if len(levels) == 0 {
		applyCodebuddyStaticThinking(models)
		return
	}
	result := codebuddyThinkingByModel(models, levels)
	for _, thinking := range result {
		thinking.DefaultLevel = defaultLevel
	}
	for i := range models {
		if t, ok := result[models[i].ID]; ok && t != nil {
			models[i].Thinking = t
		}
	}
}

// parseACPCodebuddyEffort 从 ACP session/new 结果里抽 effort 档位与宣传的默认值。响应没有可
// 识别的 effort 选项时返回零档位，让调用方退回静态档位集，而不是整个隐藏 thinking 选择器。
//
// 它是共享解析器（parseACPEffortOption）加上 CodeBuddy 的 flag 覆盖层。覆盖层刻意保持
// CodeBuddy 专属：它存在是因为该后端通过 `--effort` 而非 ACP 施加档位，所以它的可用词汇比
// 会话宣传的窄。其余运行时都原样采用宣传的列表。
func parseACPCodebuddyEffort(raw json.RawMessage) (levels []string, defaultLevel string) {
	option, ok := parseACPEffortOption(raw)
	if !ok {
		return nil, ""
	}
	for _, choice := range option.Choices {
		if !codebuddyFlagEffortValues[choice.Value] {
			continue
		}
		levels = append(levels, choice.Value)
	}
	// 只回显一个我们确实能传给 --effort 的默认值。
	if codebuddyFlagEffortValues[option.CurrentValue] {
		defaultLevel = option.CurrentValue
	}
	return levels, defaultLevel
}

// ── 共享校验 ────────────────────────────────────────────────────────

// catalogLoader 把现场的 ListModels 调用适配成 Validate*With 函数要的惰性加载器，让基于 ctx
// 的入口保持一行。
func catalogLoader(ctx context.Context, providerType string, cmd Command) func() (Catalog, error) {
	return func() (Catalog, error) { return ListModels(ctx, providerType, cmd) }
}

// ValidateThinkingLevel 报告 `value` 是否在给定 (provider, model) 的支持目录里。空值永远合法
// ——它表示“用运行时默认”。
//
// 空 model 表示“跟随运行时自己的默认”，在任务执行时解析。针对它校验 effort 能有多安全，
// 因 provider 而异：
//
//   - codex：生效模型来自用户本地 config.toml，可以是*任何*已装模型，不一定是目录里打 Default
//     标记的那个。借用 Default 条目（gpt-5.6-sol，唯一宣传 `ultra` 的）会放行实际配置模型可能
//     不支持的档位——Luna 最高到 `max`，gpt-5.5/5.4 最高到 `xhigh`——而且 Codex 自己不拒绝
//     这种不匹配。不解析任务 cwd 里的 config.toml 就无法知道生效模型（为什么避免那样做见本
//     文件 Codex 节头部），所以空 codex 模型按“关闭”处理：daemon 丢弃该档位，而不是注入一个
//     可能不合适的。需要特定 effort 的用户必须显式选模型。（MUL-4347 review。）
//   - 其他 provider：空 model 解析到目录的 Default 条目，这样“默认模型的 task 带上合法
//     thinking_level”不会被误判成“未知模型 → 拒绝”（早先 review 指出的误判）。opencode 没有
//     单一默认，所以它接受“任何已宣传模型支持的档位”。
//
// 查找走 ListModels，所以看到的是*当前* CLI 目录（包括 codex 的动态发现），而不只是一张静态
// map。该函数刻意与 HTTP 无关，让 daemon 的执行前守卫和服务端的 UpdateAgent 门控能共享同一
// 个真源。
func ValidateThinkingLevel(ctx context.Context, providerType string, cmd Command, model, value string) (bool, error) {
	return ValidateThinkingLevelWith(catalogLoader(ctx, providerType, cmd), providerType, model, value)
}

// ValidateThinkingLevelWith 是 ValidateThinkingLevel 的“目录加载器由调用方提供”版本。
// loadCatalog 至多被调用一次，而且只在答案确实依赖目录时才调用——下面的守卫不需要它就能
// 处理自己的情况。
//
// daemon 传的是“对整个任务记忆化”的加载器，让模型限定与两个能力检查共享一轮发现。这点很
// 重要，因为发现是一个上限 15–30s 的 CLI 子进程，而 cachedDiscovery 对空/兜底结果刻意不记忆化
// （#3729、MUL-5549）——因此在一个未登录或超时的运行时上，每次读取都要再付一次上限
// （MUL-6471 review）。
func ValidateThinkingLevelWith(loadCatalog func() (Catalog, error), providerType, model, value string) (bool, error) {
	if value == "" {
		return true, nil
	}
	// codex 空模型按关闭处理（见 doc 注释）。在加载目录之前检查，让结果即使发现会报错也确定
	// ——报错的查找会让 daemon 放行该档位，而这恰恰是我们对一个解析不了的 codex 模型绝不能做
	// 的事。
	if model == "" && providerType == "codex" {
		return false, nil
	}
	catalog, err := loadCatalog()
	if err != nil {
		return false, err
	}
	models := catalog.Models
	target := modelIDForCapabilityLookup(providerType, model)
	if target == "" {
		// 默认模型 = 目录里标 Default 的条目。没有条目被标记就落到下面的“无匹配返回”；这与
		// 既有的“未知模型按关闭失败、而不是猜”语义一致。
		for _, m := range models {
			if m.Default {
				target = m.ID
				break
			}
		}
		if target == "" {
			if providerType == "opencode" {
				return anyModelSupportsThinkingValue(models, value), nil
			}
			return false, nil
		}
	}
	for _, m := range models {
		if m.ID != target {
			continue
		}
		if m.Thinking == nil {
			return false, nil
		}
		for _, lvl := range m.Thinking.SupportedLevels {
			if lvl.Value == value {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// ValidateServiceTier 报告 value 是否被“当前 Codex 目录对显式模型”宣传。空值永远合法，表示
// “继承运行时配置”。Codex 的 "default" 哨兵只有当 daemon 装的 CLI 上报支持“显式 standard
// 路由”时才合法。空 codex 模型其余情况按关闭失败，因为它的生效模型来自 config.toml、可能不
// 支持所请求的档位。
func ValidateServiceTier(ctx context.Context, providerType string, cmd Command, model, value string) (bool, error) {
	return ValidateServiceTierWith(catalogLoader(ctx, providerType, cmd), providerType, model, value)
}

// ValidateServiceTierWith 是 ValidateServiceTier 的“目录加载器由调用方提供”版本。为什么 daemon
// 需要这种形式见 ValidateThinkingLevelWith。
func ValidateServiceTierWith(loadCatalog func() (Catalog, error), providerType, model, value string) (bool, error) {
	if value == "" {
		return true, nil
	}
	if providerType != "codex" {
		return false, nil
	}
	if value != codexStandardServiceTier && model == "" {
		return false, nil
	}
	catalog, err := loadCatalog()
	if err != nil {
		return false, err
	}
	if value == codexStandardServiceTier {
		for _, candidate := range catalog.Models {
			if candidate.SupportsExplicitStandardServiceTier {
				return true, nil
			}
		}
		return false, nil
	}
	for _, m := range catalog.Models {
		if m.ID != model {
			continue
		}
		for _, tier := range m.ServiceTiers {
			if tier.ID == value {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// anyModelSupportsThinkingValue 判断目录里是否存在任一模型支持 value 这个 thinking 档位。
func anyModelSupportsThinkingValue(models []Model, value string) bool {
	for _, m := range models {
		if m.Thinking == nil {
			continue
		}
		for _, lvl := range m.Thinking.SupportedLevels {
			if lvl.Value == value {
				return true
			}
		}
	}
	return false
}

// providerThinkingEnums 是“reasoning-effort 词汇表固定”的服务端接受名单。Codex 和 OpenCode
// 刻意缺席，因为它们的值来自 daemon 本地的模型目录，可以在不发 Multica 版本的情况下获得新
// token。
//
// 服务端没有本地 CLI 二进制，无法像 daemon 那样做逐模型发现。固定目录 provider 用这份枚举；
// 动态 provider 走下面 IsKnownThinkingValue 的“安全 token”路径。按模型的缺口由 daemon 的
// 执行前守卫处理——它记录并跳过注入，而不去改动持久化的 agent 状态。
//
// 固定 provider 的列表保持宽松：这是“provider 全集”检查，不是“对这款模型合不合适”的检查。
var providerThinkingEnums = map[string]map[string]bool{
	"claude": {
		"low":    true,
		"medium": true,
		"high":   true,
		"xhigh":  true,
		"max":    true,
	},
	// 对照 CodeBuddy 2.130.0 宣传的 thought_level 目录确认过。这里过去漏了 `minimal` 和
	// `max`，导致服务端拒绝了 CLI 真正接受的两个档位。
	"codebuddy": {
		"minimal": true,
		"low":     true,
		"medium":  true,
		"high":    true,
		"xhigh":   true,
		"max":     true,
	},
	// Pi 拥有固定 CLI 词汇；RPC 发现在执行前把这个全集收窄到各模型精确支持的子集。
	"pi": {
		"off":     true,
		"minimal": true,
		"low":     true,
		"medium":  true,
		"high":    true,
		"xhigh":   true,
		"max":     true,
	},
}

// thinkingDynamicCatalogProviders 是那些“effort 词汇由 daemon 本地模型目录拥有、而非上面的
// 固定枚举”的运行时。服务端对它们接受任何格式良好的 token，把逐模型检查留给 daemon 在
// 执行前决定。
var thinkingDynamicCatalogProviders = map[string]bool{
	"codex": true,
	"dsh":   true,
	// Grok 通过 session/new 宣传每个模型的 effort 目录，所以服务端不为它维护 provider 级固定
	// 枚举。daemon 用 `--effort` 施加所选档位，而不是 session/set_config_option。
	"grok":     true,
	"opencode": true,
	"kimi":     true,
}

// acpCatalogThinkingProviders 是“从 `session/new` 发现 effort 目录、并用
// `session/set_config_option` 施加它”的 ACP 运行时。它们表现得像上面的动态目录 provider——
// 服务端接受格式良好的 token，daemon 拿它对照发现的目录检查——但单独列出，因为加入这里意味
// 着更强的承诺：该运行时的 Execute 必须真的调用 applyACPEffortOption。
//
// 不要因为某个运行时讲 ACP 就把它加进来。必须同时成立两件事，而协议都不保证：
//
//   - 它的 Execute 接好了 applyACPEffortOption。Copilot 是反例——它的发现在 ACP 上跑，却经
//     自己的 CLI 面执行（`--acp` 在 copilot.go 里被拦），所以在这里加目录只会渲染出一个背后
//     什么都没有的选择器。
//   - 有人已经从源码或真实运行确认过该运行时确实把设置传进了 provider 请求。宣传不是证据——
//     Hermes 接受 set_config_option 却忽略它，Kimi ≤0.28.1 被设成 "max" 后回执仍是 "on"——
//     applyACPEffortOption 里的回读也不是证据，那只能证明会话报告了新值。这份名单就是记录
//     这种离线验证的地方；回读只是它之上的运行时诊断。
var acpCatalogThinkingProviders = map[string]bool{
	// reasonix v1.21.5：session/new 宣传 option id `effort`（category `thought_level`），
	// set_config_option 返回刷新后的选项，effort 能到达会话控制器而不只是停在配置面。其目录按
	// 模型区分——见 annotateACPThinkingForSessionModel。
	"reasonix": true,
	// hermes 覆盖两个互不相关的二进制，这里加入之所以安全，纯粹因为目录会按会话决定是哪一个
	// 回答的：
	//
	//   - jcode 宣传 option id `reasoning_effort`（category `thought_level`）且真的应用它——
	//     set_config_option 会等一个 `effort_changed` ack，provider 请求里带着
	//     `reasoning.effort` 上行。对照 jcode v0.71.1 与 v0.73.0 确认（GitHub #6720）。它的目录
	//     也按模型：切换模型时 jcode 会用新模型的宣传列表重新校验 effort。
	//   - Hermes Agent 完全不宣传 configOptions，所以它拿到空目录、无选择器、也无
	//     set_config_option 调用。2026-08-11 对照 v0.20.0 复验：session/new 仍然只返回
	//     `_meta`、`models`、`modes`、`sessionId`——与 MUL-5770 里 v0.18.2 的发现一致。
	//
	// 正因为这个分裂，该功能才由目录驱动而不是用版本字符串门控：一个 provider、两个二进制，
	// 而会话直接回答了能力问题。
	"hermes": true,
	// dim（dimcode 0.3.10+）：session/new 宣传 thought_level。
	"dim": true,
}

// usesDynamicThinkingCatalog 报告某 provider 的 effort 词汇是否由 daemon 本地目录（而非固定
// 服务端枚举）拥有。
func usesDynamicThinkingCatalog(providerType string) bool {
	return thinkingDynamicCatalogProviders[providerType] || acpCatalogThinkingProviders[providerType]
}

// UsesACPCatalogThinking 报告某 provider 的 effort 支持是否是“由它的 ACP 握手按会话宣传”决定，
// 而不是光凭 provider 名字。
//
// 能够触达已发现目录的调用方应该用它来回答某个具体运行时的能力问题：`hermes` 同时覆盖 jcode
// （宣传并施加 effort）和 Hermes Agent（什么都不宣传），所以 provider 名对任何一边都不是充分
// 答案。见 acpCatalogThinkingProviders。
func UsesACPCatalogThinking(providerType string) bool {
	return acpCatalogThinkingProviders[providerType]
}

// ThinkingControlSupported 报告 Multica 能否向该运行时投递“按 agent 的 reasoning effort”。
// false 意味着对任何 thinking_level 的回答都是“不行”，与 token 无关：运行时在 daemon 与它对话
// 的那条面上不暴露 effort 旋钮，所以没有可注入的东西，换个拼写也修不好。
//
// Copilot 是说明性案例。它对模型发现讲 ACP，但经自己的 CLI 面执行（`--acp` 在 copilot.go 里
// 被拦），所以不存在一条活着的 ACP 会话能把 effort 带上去——不管发现宣传了什么，那里的选择
// 器都是死的。
//
// 只在 provider 粒度返回 true。`hermes` provider 覆盖两个答案互不相同的二进制——jcode 应用
// 宣传的 effort，Hermes Agent 在 ACP 上完全没有 effort 面——所以它在这里报 true，由按会话的
// 目录决定选择器到底出不出来。双方证据见 acpCatalogThinkingProviders。
func ThinkingControlSupported(providerType string) bool {
	if usesDynamicThinkingCatalog(providerType) {
		return true
	}
	_, ok := providerThinkingEnums[providerType]
	return ok
}

// IsKnownThinkingValue 报告 `value` 对给定 provider 是不是可识别的 effort token。空字符串
// 永远接受（表示“用运行时默认”）。没有 reasoning 控制的 provider 只接受空；Codex、OpenCode、
// Kimi 与 ACP 目录运行时在这里接受格式良好的 token，因为它们的 daemon 本地目录会在执行前做
// 精确的逐模型检查。
//
// 这是服务端在 CreateAgent / UpdateAgent 上用的廉价同步门控。与 ValidateThinkingLevel 不同，
// 它不查实时目录或逐模型子集。要把一次拒绝展示给用户的调用方，应先问 ThinkingControlSupported，
// 好让文案说“该运行时没有 reasoning 控制”，而不是暗示 token 写错了。
func IsKnownThinkingValue(providerType, value string) bool {
	if value == "" {
		return true
	}
	if usesDynamicThinkingCatalog(providerType) {
		return isValidDynamicThinkingValue(value)
	}
	enum, ok := providerThinkingEnums[providerType]
	if !ok {
		return false
	}
	return enum[value]
}

// IsKnownServiceTier 是服务端的字面量门控。精确的逐模型目录在 daemon 宿主机上，所以 Codex
// 在这里接受“安全的未来目录 ID”，由 ValidateServiceTier 在执行期做兼容性检查。其他 provider
// 当前不暴露 service tier。
func IsKnownServiceTier(providerType, value string) bool {
	if value == "" {
		return true
	}
	return providerType == "codex" && isValidDynamicThinkingValue(value)
}

// isValidDynamicThinkingValue 判断 value 是否“格式良好”的动态 thinking token：只含字母数字与
// `-_ .`，长度 ≤64，且不以连字符/下划线/点开头。
func isValidDynamicThinkingValue(value string) bool {
	if len(value) > 64 {
		return false
	}
	for i, r := range value {
		valid := r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.'
		if !valid {
			return false
		}
		if i == 0 && (r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
