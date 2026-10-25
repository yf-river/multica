// ============================================================================
// runtimes（运行时间）用量展示层——纯工具函数集
//
// 角色定位：本文件是运行时间视图层共用的「纯函数工具箱」：用量金额估算、
// token/费用/设备信息格式化、按日/按周聚合、日期窗口切片、日历运算与按
// 不同维度（agent/模型）分组的成本聚合。不包含任何 React 组件，也不发起
// 网络请求——输入是后端下发的用量行（usage row），输出是展示组件可以直接
// 拿来渲染的数字、字符串和图表数据结构。
//
// 数据流：@multica/core/types 的 RuntimeUsage / RuntimeUsageByAgent /
// DashboardUsageDaily 等用量行进入本文件 → 经过定价估算、格式化、聚合 →
// 交给 usage-section、runtime-list、runtime-detail 与各图表组件展示。
//
// 贯穿全文件的核心概念（后面不再重复展开）：
//  - usage 行：一条含 model、输入/输出/缓存读写 token 计数的记录，可选带
//    provider（厂商名）与 cost_usd_ticks（厂商实际收取的费用）。
//  - cost_usd_ticks：厂商以 1e-10 美元为单位的整数上报成本，保证亚美分
//    级别的费用在传输与运算中不丢精度（见 COST_USD_TICKS_PER_USD）。
//  - 定价（MODEL_PRICING 表）：每百万 token 的价格。原则是「厂商自己报过
//    价就以厂商数字为权威」，价格表只负责估算没被计费的那部分 token。
//  - cache_read / cache_write：命中缓存的读取/写入 token，价格远低于
//    cacheRead 对应的原始输入价，用它们能算出「缓存帮你省了多少钱」。
//  - 日/周聚合统一在「运行环境的 IANA 时区」下以 YYYY-MM-DD 字符串做日历
//    运算（见文末 Calendar helpers），与后端按运行时本地自然日分桶一致，
//    避免浏览器与运行时跨时区导致统计边界漂移一天。
//
// 内部组织：格式化小工具（token/设备/版本号等）→ 成本估算核心（定价表 +
// resolvePricing + estimateCost 系列）→ 单次运行（run）的用量汇总 →
// 按日/按周聚合与窗口切片 → 日历助手 → 按维度的成本聚合。
// ============================================================================
import type {
  AgentRuntime,
  RuntimeUsage,
  RuntimeUsageByAgent,
} from "@multica/core/types";
import { getCustomPricing } from "@multica/core/runtimes/custom-pricing-store";

/**
 * 四种 token 求和得到一行用量的总 token 数。
 * 字段可能缺失（旧数据），缺失按 0 处理。
 */
export function usageTokenTotal(usage: {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}): number {
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) +
    (usage.cache_read_tokens ?? 0) + (usage.cache_write_tokens ?? 0);
}

// 本机（local）运行时间只要还在线，就有自愈机制：服务端一侧删除后几秒内
// daemon 会把自己重新注册回来（daemon 自愈，#2404）。因此对「在线本机运行
// 时间」从 UI 上删除是无效操作——删了也会马上复活。详情页和列表行的删除
// 菜单都用同一个判定来禁用删除按钮，避免误导用户。
export function isSelfHealingRuntime(runtime: AgentRuntime): boolean {
  return runtime.runtime_mode === "local" && runtime.status === "online";
}

// ---------------------------------------------------------------------------
// 格式化小工具
// ---------------------------------------------------------------------------

// 生成复合单位的相对时间文案（如 "2m 14s ago"、"1d 4h ago"、"6d 19h ago"）。
// 让用户一眼能区分「刚掉线」还是「已经失联很久」，不用鼠标悬停去看完整时间戳。
export function formatLastSeen(lastSeenAt: string | null): string {
  if (!lastSeenAt) return "Never";
  const diffMs = Date.now() - new Date(lastSeenAt).getTime();
  if (diffMs < 5_000) return "Just now";

  const seconds = Math.floor(diffMs / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (minutes < 1) return `${seconds}s ago`;
  if (hours < 1) {
    const s = seconds % 60;
    return s > 0 ? `${minutes}m ${s}s ago` : `${minutes}m ago`;
  }
  if (days < 1) {
    const m = minutes % 60;
    return m > 0 ? `${hours}h ${m}m ago` : `${hours}h ago`;
  }
  const h = hours % 24;
  return h > 0 ? `${days}d ${h}h ago` : `${days}d ago`;
}

// 把后端下发的 `device_info` 字符串（形如 "MacBook-Pro · darwin-amd64"、
// "some-host · linux-amd64"）改写成人类友好的样子。线上数据目前不含硬件
// 型号或地理位置信息，所以只能把 GOOS/GOARCH 后缀翻译成通俗名称（如
// "darwin-amd64" → "macOS (x86_64)"），主机名保持不变。
export function formatDeviceInfo(raw: string | null): string | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;
  return trimmed
    .split(" · ")
    .map((part) => prettifyOsArch(part))
    .join(" · ");
}

// prettifyOsArch 把一个「系统-架构」片段（如 darwin-amd64）翻译成带括号的
// 可读文案（如 macOS (x86_64)）；不认识的就原样返回。
function prettifyOsArch(part: string): string {
  const lower = part.toLowerCase();
  // 期望形态是 <系统>-<架构>，例如 darwin-amd64、linux-arm64、windows-amd64。
  const match = lower.match(/^(darwin|linux|windows|freebsd|openbsd|netbsd)-(amd64|arm64|386|arm)$/);
  if (!match) return part;
  const os = match[1] ?? "";
  const arch = match[2] ?? "";
  const osLabel = OS_LABEL[os] ?? os;
  const archLabel = ARCH_LABEL[arch] ?? arch;
  return `${osLabel} (${archLabel})`;
}

// OS_LABEL：GOOS 标识 → 用户可读的系统名。
const OS_LABEL: Record<string, string> = {
  darwin: "macOS",
  linux: "Linux",
  windows: "Windows",
  freebsd: "FreeBSD",
  openbsd: "OpenBSD",
  netbsd: "NetBSD",
};

// ARCH_LABEL：GOARCH 标识 → 用户可读的 CPU 架构名。
const ARCH_LABEL: Record<string, string> = {
  amd64: "x86_64",
  arm64: "arm64",
  "386": "x86",
  arm: "arm",
};

// 去掉版本号开头的 "v"——GitHub 发布版本是 `v0.2.17`，daemon 上报的是
// `0.2.15`；统一格式后才能对两者做大小比较。
function stripVersionPrefix(v: string): string {
  return v.replace(/^v/, "");
}

// 当 `latest` 比 `current` 严格更新时返回 true，比较按点分数字逐段进行。
// 非数字或缺失的段按 0 参与比较（因此 "0.2" < "0.2.1"）。runtime-list 的
// CLI 列用它决定是否显示「有新版 ↑」标记；update-section.tsx 里暂时还
// 内联了一份相同逻辑。
export function isVersionNewer(latest: string, current: string): boolean {
  const l = stripVersionPrefix(latest).split(".").map(Number);
  const c = stripVersionPrefix(current).split(".").map(Number);
  for (let i = 0; i < Math.max(l.length, c.length); i++) {
    const lv = l[i] ?? 0;
    const cv = c[i] ?? 0;
    if (lv > cv) return true;
    if (lv < cv) return false;
  }
  return false;
}

// 数量单位档位表：除数和后缀，从个位到万亿（1K=1000），供 formatTokens 做
// 紧凑缩写。
const TOKEN_UNITS = [
  { divisor: 1, suffix: "" },
  { divisor: 1_000, suffix: "K" },
  { divisor: 1_000_000, suffix: "M" },
  { divisor: 1_000_000_000, suffix: "B" },
  { divisor: 1_000_000_000_000, suffix: "T" },
] as const;

/**
 * 把 token 数格式化成紧凑可读形式：少于 1000 保留整数（带千分位），否则
 * 缩写为带 K/M/B/T 后缀的一位小数（如 999 → "999"，1500000 → "1.5M"）。
 */
export function formatTokens(n: number): string {
  const magnitude = Math.abs(n);
  let unitIndex = TOKEN_UNITS.findLastIndex(
    ({ divisor }) => magnitude >= divisor,
  );
  unitIndex = Math.max(unitIndex, 0);

  if (unitIndex === 0) return n.toLocaleString();

  let unit = TOKEN_UNITS[unitIndex]!;
  let scaled = n / unit.divisor;

  // 值四舍五入后如果跨过了档位边界就向上进位（999,999 → 1M），避免紧凑标签
  // 渲染成 1000K / 1000M 这种看起来还在增长的尴尬形式。
  if (
    Math.abs(Number(scaled.toFixed(1))) >= 1_000 &&
    unitIndex < TOKEN_UNITS.length - 1
  ) {
    unit = TOKEN_UNITS[unitIndex + 1]!;
    scaled = n / unit.divisor;
  }

  return `${Number(scaled.toFixed(1))}${unit.suffix}`;
}

/**
 * Whole-number prompt-cache hit rate across mutually exclusive input buckets.
 * Callers must sum each bucket before calling so the result is a ratio of sums,
 * never an average of per-row percentages. Output tokens are not input-side
 * traffic and therefore do not belong in the denominator.
 *
 * Returns null when no input-side usage was reported. Round ordinary values to
 * the nearest percent, but reserve 100% for a genuinely complete cache hit.
 */
export function cacheHitRatePercent(
  inputTokens: number,
  cacheReadTokens: number,
  cacheWriteTokens: number,
): number | null {
  const inputSideTokens = inputTokens + cacheReadTokens + cacheWriteTokens;
  if (inputSideTokens <= 0) return null;

  const percent = (cacheReadTokens / inputSideTokens) * 100;
  return percent === 100 ? 100 : Math.min(Math.round(percent), 99);
}

// Cents below $100, whole dollars above — two decimals on a four-figure spend
// is noise, and dropping them below $100 would round most single runs to $0.
export function formatUsd(n: number): string {
  if (n >= 100) return `$${n.toFixed(0)}`;
  return `$${n.toFixed(2)}`;
}

// ---------------------------------------------------------------------------
// 成本估算
// ---------------------------------------------------------------------------

// 每百万 token 的价格表（美元）。各厂商条目下方的注释标注了对应的官方
// 定价来源——厂商发新版模型或调价时请保持同步：
//
//   Anthropic: https://platform.claude.com/docs/en/about-claude/pricing
//   OpenAI:    https://openai.com/api/pricing
//   DeepSeek:  https://api-docs.deepseek.com/quick_start/pricing
//   Moonshot:  https://www.kimi.com/resources/kimi-k2-6-pricing
//   Zhipu:     https://docs.z.ai/guides/overview/pricing
//   xAI:       https://docs.x.ai/developers/pricing
//
// cacheWrite（缓存写入价）的取值约定：Anthropic 按 5 分钟缓存 TTL 计费
// （输入价的 1.25 倍）；daemon 上报 cache_creation_input_tokens 时不带 TTL
// 元数据，所以 5 分钟是最稳妥/最省钱的假设（也是 API 默认值）。DeepSeek、
// Moonshot、Zhipu、xAI 不对缓存写入单独计费（命中缓存的输入在后续读取时
// 直接打折），因此这些厂商 cacheWrite 与 input 相同。OpenAI 历史上也如此，
// 但 GPT-5.6 起开始单独计费缓存写入（输入价的 1.25 倍，缓存读取仍享受
// 缓存输入 9 折优惠），所以带上了独立的 cacheWrite。Codex 用量目前还不
// 上报缓存写入 token，这条费率今天暂时用不到。
//
// 查表规则：解析时先去掉末尾的日期快照再精确匹配（见 resolvePricing）。
// 刻意不做 startsWith 前缀回退：目录里的每个 SKU 都必须有自己的条目，这样
// 不认识的变体（`gpt-5.5-mini`、假设的 `gpt-5.4-foo`）才不会静默继承某个
// 名字相近型号的价格，而是会出现在「无法定价」的诊断清单里。新增型号时，
// 记得同步维护 `server/pkg/agent/models.go`，保证目录与价格表一致。
//
// 带厂商前缀的键：如果模型 id 本身不带厂商前缀（`claude-*`、`gpt-*`、
// `o3*`/`o4*`、`glm-*`、`deepseek-*`、`kimi-*`、`grok-*`），且又不是厂商
// 名本身，那么不同厂商上报同一个通用 id（如 `auto`）时就会互相撞键。这类
// 通用 id 必须用 `${provider}/${model}` 作为键（例如 `cursor/auto`）。
// resolvePricing 会先试 `${provider}/…` 形态、再试裸 id，所以自带厂商前缀
// 的 SKU 可以不加前缀也能正确命中。
const MODEL_PRICING: Record<
  string,
  { input: number; output: number; cacheRead: number; cacheWrite: number }
> = {
  // -- Anthropic 当前一代：Sonnet 5 用的是官方发布期优惠价（$2 / $10，
  //    截至 2026-08-31）。这张静态表还不支持未来的自动调价，所以优惠期
  //    结束后的 $3 / $15 正式价生效时，要记得手工更新这一行。Fable 5 属于
  //    Mythos 级别 SKU（10/50）；Opus 4.5~Opus 5 都维持 Opus 更低的 5/25 档。 --
  "claude-sonnet-5":     { input: 2,    output: 10,   cacheRead: 0.20, cacheWrite: 2.50 },
  "claude-fable-5":     { input: 10,   output: 50,   cacheRead: 1.00, cacheWrite: 12.50 },
  "claude-opus-5":      { input: 5,    output: 25,   cacheRead: 0.50, cacheWrite: 6.25 },
  "claude-haiku-4-5":   { input: 1,    output: 5,    cacheRead: 0.10, cacheWrite: 1.25 },
  "claude-sonnet-4-5":  { input: 3,    output: 15,   cacheRead: 0.30, cacheWrite: 3.75 },
  "claude-sonnet-4-6":  { input: 3,    output: 15,   cacheRead: 0.30, cacheWrite: 3.75 },
  "claude-opus-4-5":    { input: 5,    output: 25,   cacheRead: 0.50, cacheWrite: 6.25 },
  "claude-opus-4-6":    { input: 5,    output: 25,   cacheRead: 0.50, cacheWrite: 6.25 },
  "claude-opus-4-7":    { input: 5,    output: 25,   cacheRead: 0.50, cacheWrite: 6.25 },
  "claude-opus-4-8":    { input: 5,    output: 25,   cacheRead: 0.50, cacheWrite: 6.25 },

  // -- Anthropic：4.5 之前的 Opus（老型号，仍按当时的价格档服务） --
  "claude-opus-4-1":    { input: 15,   output: 75,   cacheRead: 1.50, cacheWrite: 18.75 },
  "claude-opus-4":      { input: 15,   output: 75,   cacheRead: 1.50, cacheWrite: 18.75 },

  // -- Anthropic：Sonnet 4.0（已废弃；价格与整个 4.x 家族一致） --
  "claude-sonnet-4":    { input: 3,    output: 15,   cacheRead: 0.30, cacheWrite: 3.75 },

  // -- Anthropic：更早的 Haiku 档（防御性条目，防极少数仍跑在老型号上的 runtime） --
  "claude-haiku-3-5":   { input: 0.80, output: 4,    cacheRead: 0.08, cacheWrite: 1.00 },

  // -- OpenAI：以点分小版本命名的 Codex 目录 SKU。每代模型独立定价，不做
  //    回退到 `gpt-5` 的处理；条目与 `server/pkg/agent/models.go`（Codex
  //    厂商清单）保持一致。gpt-5.6（sol/terra/luna）用 OpenAI 官方发布价。
  //    5.6 起是 OpenAI 第一代单独计费缓存写入的版本：cacheRead = 输入价 0.1x
  //    （缓存输入 9 折），cacheWrite = 输入价 1.25x（见上方总注释）。Codex
  //    用量目前还不上报缓存写入 token，这条费率暂时用不上，但仍按正确值
  //    维护，等支持那天直接生效。
  "gpt-5.6-sol":        { input: 5,    output: 30,   cacheRead: 0.50,  cacheWrite: 6.25 },
  "gpt-5.6-terra":      { input: 2.50, output: 15,   cacheRead: 0.25,  cacheWrite: 3.125 },
  "gpt-5.6-luna":       { input: 1,    output: 6,    cacheRead: 0.10,  cacheWrite: 1.25 },
  "gpt-5.5":            { input: 5,    output: 30,   cacheRead: 0.50,  cacheWrite: 5 },
  "gpt-5.4-mini":       { input: 0.75, output: 4.50, cacheRead: 0.075, cacheWrite: 0.75 },
  "gpt-5.4":            { input: 2.50, output: 15,   cacheRead: 0.25,  cacheWrite: 2.50 },
  "gpt-5.3-codex":      { input: 1.75, output: 14,   cacheRead: 0.175, cacheWrite: 1.75 },

  // -- OpenAI：GPT-5 家族（Codex CLI 默认走 gpt-5-codex；-codex/-mini/-nano
  //    各变体按 OpenAI 各档价格计） --
  "gpt-5-codex":        { input: 1.25, output: 10,   cacheRead: 0.125, cacheWrite: 1.25 },
  "gpt-5-mini":         { input: 0.25, output: 2,    cacheRead: 0.025, cacheWrite: 0.25 },
  "gpt-5-nano":         { input: 0.05, output: 0.40, cacheRead: 0.005, cacheWrite: 0.05 },
  "gpt-5":              { input: 1.25, output: 10,   cacheRead: 0.125, cacheWrite: 1.25 },

  // -- OpenAI：o 系列推理模型 --
  "o3-mini":            { input: 1.10, output: 4.40, cacheRead: 0.55,  cacheWrite: 1.10 },
  "o3":                 { input: 2,    output: 8,    cacheRead: 0.50,  cacheWrite: 2 },
  "o4-mini":            { input: 1.10, output: 4.40, cacheRead: 0.275, cacheWrite: 1.10 },

  // -- OpenAI：GPT-4o 家族（老型号，保留给仍配置了它们的 runtime） --
  "gpt-4o-mini":        { input: 0.15, output: 0.60, cacheRead: 0.075, cacheWrite: 0.15 },
  "gpt-4o":             { input: 2.50, output: 10,   cacheRead: 1.25,  cacheWrite: 2.50 },

  // -- DeepSeek（api-docs.deepseek.com/quick_start/pricing）。
  //    官方目录只有两个在售 SKU；`deepseek-chat` 与 `deepseek-reasoner`
  //    是别名，分别路由到 `deepseek-v4-flash`（非思考模式 / 思考模式），
  //    同页可查。`deepseek-v4-pro` 目前有 75% 折扣促销，截止 2026-05-31
  //    15:59 UTC；我们按促销结束后的标准价（$1.74/$3.48）记账，避免 6 月 1
  //    日仪表盘金额一夜跳涨 4 倍——宁可促销期间短暂高估，也不要促销结束后
  //    的断崖式变化。 --
  "deepseek-v4-flash":  { input: 0.14, output: 0.28, cacheRead: 0.0028, cacheWrite: 0.14 },
  "deepseek-v4-pro":    { input: 1.74, output: 3.48, cacheRead: 0.0145, cacheWrite: 1.74 },
  "deepseek-chat":      { input: 0.14, output: 0.28, cacheRead: 0.0028, cacheWrite: 0.14 },
  "deepseek-reasoner":  { input: 0.14, output: 0.28, cacheRead: 0.0028, cacheWrite: 0.14 },

  // -- Moonshot Kimi（kimi.com/resources/kimi-k2-6-pricing）。
  //    目前官方价目表只有 K2.6；更早的 K2 变体在 Moonshot 公布价格前刻意
  //    不收录。 --
  "kimi-k2.6":          { input: 0.95, output: 4.00, cacheRead: 0.16,   cacheWrite: 0.95 },
  // Kimi K3（platform.kimi.ai/docs/pricing/chat-k3，转自 models.dev 的
  // providers/moonshotai/models/kimi-k3.toml）。Moonshot 不单独计费缓存
  // 写入，因此 cacheWrite 与 input 相同（与 kimi-k2.6 约定一致）。
  "kimi-k3":            { input: 3.0,  output: 15.0,  cacheRead: 0.30,   cacheWrite: 3.0 },
  // Kimi Code CLI 上报的模型名是 `kimi-code/k3`；因为 `k3` 是通用 id，所以
  // 加厂商前缀（见上方「带厂商前缀的键」说明）。
  "kimi/k3":            { input: 3.0,  output: 15.0,  cacheRead: 0.30,   cacheWrite: 3.0 },

  // -- Zhipu z.ai（docs.z.ai/guides/overview/pricing）。免费的 flash 档定价为
  //    0，使它们能正常解析成功、而不是掉进「无法定价」诊断清单。 --
  "glm-5.1":            { input: 1.4,  output: 4.4,  cacheRead: 0.26,   cacheWrite: 1.4 },
  "glm-5":              { input: 1.0,  output: 3.2,  cacheRead: 0.2,    cacheWrite: 1.0 },
  "glm-5-turbo":        { input: 1.2,  output: 4.0,  cacheRead: 0.24,   cacheWrite: 1.2 },
  "glm-4.7":            { input: 0.6,  output: 2.2,  cacheRead: 0.11,   cacheWrite: 0.6 },
  "glm-4.7-flashx":     { input: 0.07, output: 0.4,  cacheRead: 0.01,   cacheWrite: 0.07 },
  "glm-4.7-flash":      { input: 0,    output: 0,    cacheRead: 0,      cacheWrite: 0 },
  "glm-4.6":            { input: 0.6,  output: 2.2,  cacheRead: 0.11,   cacheWrite: 0.6 },
  "glm-4.5":            { input: 0.6,  output: 2.2,  cacheRead: 0.11,   cacheWrite: 0.6 },
  "glm-4.5-x":          { input: 2.2,  output: 8.9,  cacheRead: 0.45,   cacheWrite: 2.2 },
  "glm-4.5-air":        { input: 0.2,  output: 1.1,  cacheRead: 0.03,   cacheWrite: 0.2 },
  "glm-4.5-airx":       { input: 1.1,  output: 4.5,  cacheRead: 0.22,   cacheWrite: 1.1 },
  "glm-4.5-flash":      { input: 0,    output: 0,    cacheRead: 0,      cacheWrite: 0 },

  // -- 阿里云 Qwen（国际版 ≤256K 档；官方来源：alibabacloud.com/help 的
  //    Model Studio 定价表与 qwencloud.com/models/<model> 页面，访问于
  //    2026-08-12）。qwen3.7-plus：输入 $0.40 / 输出 $1.60；qwen3.6-flash：
  //    输入 $0.25 / 输出 $1.50。缓存价格：显式缓存创建 = 输入价 1.25×
  //    （qwen3.7-plus $0.50、qwen3.6-flash $0.3125）；显式缓存读取 = 输入价
  //    10%（qwen3.7-plus $0.04、qwen3.6-flash $0.025）。
  // qwen3.8-max 按官方公布的按量付费价计（qwencloud.com/models/qwen3.8-max：
  // 输入 $2、输出 $6、隐式缓存 $0.25、创建 $2.5、显式缓存读取 $0.17；也列于
  // alibabacloud.com/help 的模型定价页），这样仪表盘展示的是绝对成本，即使
  // runtime 实际是通过阿里云 Token/编程套餐订阅走通的。qwen3.8-max-preview
  // 保持 0：它只能经订阅访问（token-plan.cn-beijing.maas.aliyuncs.com），
  // 订阅不按 token 计费——定价 0 可干净解析、不触发「无法定价」诊断（与
  // 下方免费的 GLM flash 档同约定）。 --
  "qwen3.7-plus":       { input: 0.40,  output: 1.60,  cacheRead: 0.04,   cacheWrite: 0.50 },
  "qwen3.6-flash":      { input: 0.25,  output: 1.50,  cacheRead: 0.025,  cacheWrite: 0.3125 },
  "qwen3.8-max":        { input: 2.00,  output: 6.00,  cacheRead: 0.17,   cacheWrite: 2.5 },
  "qwen3.8-max-preview":{ input: 0,      output: 0,     cacheRead: 0,      cacheWrite: 0 },

  // -- 火山引擎方舟（ark.cn-beijing.volces.com）。`ark-code-latest` 是滚动
  //    别名，目标可在火山控制台的不同模型家族间切换，并非稳定的模型身份。
  //    daemon 上报的是别名本身而不是解析后的模型，因此没有可靠费率可绑——
  //    故意保持不映射（与下方 xAI 的 `grok-composer-*` 同理），让它出现在
  //    「无法定价」诊断里，而不是继承一个猜出来的费率。 --

  // -- xAI Grok（docs.x.ai/developers/pricing）。下面都是短上下文档的费率，
  //    而且对 Grok 而言现在只是兜底：xAI 会上报自己的按轮（per turn）价格，
  //    estimateCost 优先用厂商数字。这点很重要——xAI 对提示词达到 200K token
  //    的请求按 2 倍计费，而一条用量行聚合了一轮里的每次模型调用，token 数
  //    根本分不清命中了哪一档，xAI 自己的数字则已经把翻倍算进去了。这些费率
  //    行仍适用于老到不上报成本的 daemon 记录的 Grok 用量（与 Anthropic `[1m]`
  //    上下文标签在 resolvePricing 里的取舍相同）。
  //    cacheRead 用 xAI 公布的 "Cached" 输入价；价目表上没有单独的缓存写入
  //    费率（缓存写入按普通输入计费），因此 cacheWrite 与 input 相同（同上方
  //    总注释约定）。Grok 的 id 自带厂商前缀，键保持不带前缀——也只有这样才
  //    能命中，因为 daemon 给这些行打的厂商标签是 `grok` 而不是 `xai`。
  //    `grok-composer-*` 系列在 Grok Build 目录（server/pkg/agent/models.go）
  //    里有，但官方价目表上没有；刻意保持不映射，而不是继承一个猜的费率。 --
  "grok-4.6":                     { input: 2,    output: 6,    cacheRead: 0.50, cacheWrite: 2 },
  "grok-4.5":                     { input: 2,    output: 6,    cacheRead: 0.30, cacheWrite: 2 },
  "grok-4.3":                     { input: 1.25, output: 2.50, cacheRead: 0.20, cacheWrite: 1.25 },
  "grok-build-0.1":               { input: 1,    output: 2,    cacheRead: 0.20, cacheWrite: 1 },
  "grok-4.20-multi-agent-0309":   { input: 1.25, output: 2.50, cacheRead: 0.20, cacheWrite: 1.25 },
  "grok-4.20-0309-reasoning":     { input: 1.25, output: 2.50, cacheRead: 0.20, cacheWrite: 1.25 },
  "grok-4.20-0309-non-reasoning": { input: 1.25, output: 2.50, cacheRead: 0.20, cacheWrite: 1.25 },

  // -- Cursor Composer / Auto（cursor.com/docs/models-and-pricing、
  //    cursor.com/docs/models/cursor-composer-2、
  //    cursor.com/docs/models/cursor-composer-2-5）。
  //    Cursor 的模型 id 全是不带前缀的通用名（`auto`、`composer-*`），会与
  //    其它厂商撞名（另一家厂商也可能上报 `auto`），所以全部挂在 `cursor/`
  //    厂商前缀下。见上方「带厂商前缀的键」说明。Cursor 的结果事件经常缺
  //    `model` 字段，daemon 会回退到 runtime 配置的模型或遗留键 `cursor`。
  //    Cursor 对这些行不公布缓存写入费率；保持为 0，避免把上报的
  //    cache_write_tokens 按输入价算出虚构的开销。
  "cursor/auto":              { input: 1.25, output: 6,    cacheRead: 0.25,   cacheWrite: 0 },
  "cursor/composer-2.5-fast": { input: 3,    output: 15,   cacheRead: 0.5,    cacheWrite: 0 },
  "cursor/composer-2.5":      { input: 0.5,  output: 2.5,  cacheRead: 0.2,    cacheWrite: 0 },
  "cursor/composer-2-fast":   { input: 1.5,  output: 7.5,  cacheRead: 0.35,   cacheWrite: 0 },
  "cursor/composer-2":        { input: 0.5,  output: 2.5,  cacheRead: 0.2,    cacheWrite: 0 },
  "cursor/composer-1.5":      { input: 3.5,  output: 17.5, cacheRead: 0.35,   cacheWrite: 0 },
  "cursor/composer-1":        { input: 1.25, output: 10,   cacheRead: 0.125,  cacheWrite: 0 },
  // 兜底条目：结果事件里和 runtime 配置里都查不到模型时，daemon 就上报裸的
  // `cursor`。这个键恰好等于厂商名本身，跨厂商不可能撞键，因此不带前缀。
  // 价格按当前 Composer 2.5 Fast 默认档计。
  "cursor":                   { input: 3,    output: 15,   cacheRead: 0.5,    cacheWrite: 0 },
};

// 把模型字符串解析成对应的定价档。精确匹配，按顺序容忍四种写法差异：
//
//  1. 带厂商前缀的 id（某些 runtime / opencode 上报的 `anthropic/claude-
//     opus-4.7`）——`<provider>/` 段只是路由信息、不是 SKU 的一部分，查表
//     前先剥掉。
//  2. Anthropic 的点↔横线归一化——Claude Code 上报 `claude-opus-4-7`，
//     GitHub Copilot 上报 `claude-opus-4.7`。同一 SKU、两种渠道写法。把
//     `claude-*` id 统一成 Anthropic 官方发布的横线形式。只对 `claude-*`
//     生效，因为 OpenAI 的分隔符是有语义的（`gpt-5.4` ≠ `gpt-5-4`）。
//  3. 尾部日期快照（`claude-sonnet-4-5-20250929`、`gpt-5-2025-08-07`）——
//     定价按「家族」算，日期是会变的，所以去掉尾部的日期 / "latest" 标签。
//  4. 尾部上下文窗口标签（`claude-opus-4-7[1m]`）——Anthropic 的 1M 上下
//     文 beta 仍是同一 SKU：提示词 ≤200K 输入 token 按标准价、超出部分 2 倍
//     加价。聚合后的用量行不携带每次请求的提示词大小，因此带方括号的变体也
//     按标准档计价。轻微低估好过之前直接把整行丢弃的行为。
//
// 仍解析不到的模型回退到用户自定义定价存储（custom pricing store）。
// 不做 startsWith 前缀回退：`gpt-5.5-mini` 这类变体必须有自己的条目才能被
// 正确计价（否则会继承 `gpt-5.5` 的价格）。
//
// `provider` 用于区分不带前缀的通用 id（见上方总注释）：每个候选键都先试
// `${provider}/…` 限定形式、再试裸 id——因此 Cursor 的一行会命中 `cursor/auto`，
// 而无 provider 的裸 `auto` 保持「无法定价」，不会静默借用 Cursor 的价格。
function resolvePricing(model: string, provider?: string) {
  if (!model) return undefined;

  const candidates = pricingCandidates(model, provider);
  for (const candidate of candidates) {
    const hit = MODEL_PRICING[candidate];
    if (hit) return hit;
  }
  for (const candidate of candidates) {
    const hit = getCustomPricing(candidate);
    if (hit) return hit;
  }
  return undefined;
}

// 规范化厂商标记：去掉首尾空白并转小写，让查询键、存储键与分组标签都容忍
// 存储值里的大小写差异。厂商未知时返回空串。
function normalizeProvider(provider?: string): string {
  return provider?.trim().toLowerCase() ?? "";
}

// 给键加上厂商前缀；若键本身已经带该厂商前缀（上游已经限定好的
// `cursor/auto` 不能再变成 `cursor/cursor/auto`）则跳过。`provider` 必须
// 已经过 normalizeProvider 规范化。
function qualify(provider: string, key: string): string {
  return key.startsWith(`${provider}/`) ? key : `${provider}/${key}`;
}

// 为 (model, provider) 组合生成查表候选键：知道厂商时每个规范候选都先生成
// `${provider}/` 限定形式、再生成裸形式。限定形式排前面，意味着厂商范围
// 内的条目/覆盖总是优先于不带厂商的条目。
function pricingCandidates(model: string, provider?: string): string[] {
  const base = canonicalCandidates(model);
  const p = normalizeProvider(provider);
  if (!p) return base;
  return [...base.map((c) => qualify(p, c)), ...base];
}

// (model, provider) 组合的规范存储/诊断键：知道厂商时用限定形式，否则用裸
// 模型名。`collectUnmappedModels` 返回的就是这些键，自定义定价对话框也按它们
// 存覆盖价——因此用户给 `cursor/auto` 填的价只对 Cursor 的行生效，不会影响
// 另一个也上报 `auto` 的厂商。厂商名已转小写，查找时容忍存储值的大小写差异。
export function pricingKey(model: string, provider?: string): string {
  const p = normalizeProvider(provider);
  return p ? qualify(p, model) : model;
}

// 用量行模型的展示/分组键。能自己解析定价的 id（自带厂商前缀的 SKU 如
// `claude-opus-4-7`，以及键恰好等于厂商名的遗留 `cursor` 兜底键）保持裸
// 形式；只有在厂商前缀下才有价格的通用 id（`auto`、`composer-*`）则带上
// 厂商前缀，避免两个厂商上报同一个裸 id 被合并成一条错误标注的行，也让标签
// 与 `collectUnmappedModels` / 定价对话框展示的内容一致。
export function modelGroupingKey(model: string, provider?: string): string {
  if (!model) return normalizeProvider(provider) || "unknown";
  return isSelfResolvingId(model) ? model : pricingKey(model, provider);
}

// 一个模型 id 是否不带厂商前缀就能自己定价（自带厂商前缀的 SKU，或遗留的
// `cursor` 兜底键）。这类 id 保留裸分组键；通用 id（`auto`、`composer-*`）
// 继续保持厂商前缀限定。
//
// 刻意探测「裸」模型而非转发 provider：若把 provider 传进来，一条已限定的
// 行就会把自己误判为「可自解析」而缩回裸键，重新引入本方案想避免的跨厂商
// 合并。因此这个函数的参数保持不带 provider，这条保证才能成立。
function isSelfResolvingId(model: string): boolean {
  return isModelPriced(model);
}

// 按优先级顺序为一个模型串生成候选键：先是原始字符串（保留用户/目录的明确
// 写法），再是各种规范化后的形态。内部已去重，避免重复查表。
//
// 只依赖 `model` 一个入参，而聚合循环对每行会调用它 3~4 次，所以结果做了
// 缓存——模型串集合又小又有界。调用方只读返回数组（pricingCandidates 通过
// map/spread 复制出新数组），因此共享缓存的引用是安全的。
// 缓存故意持续到进程结束、从不淘汰（键集合有界，见上）。
const canonicalCandidatesCache = new Map<string, string[]>();
function canonicalCandidates(model: string): string[] {
  const cached = canonicalCandidatesCache.get(model);
  if (cached) return cached;
  const seen = new Set<string>();
  const out: string[] = [];
  const push = (s: string) => {
    if (!s || seen.has(s)) return;
    seen.add(s);
    out.push(s);
  };
  const stripDate = (s: string) =>
    s.replace(/-(20\d{2}-\d{2}-\d{2}|20\d{6}|latest)$/, "");
  const stripProvider = (s: string) => {
    // 路由前缀有两种形态：`vendor/model`（opencode 风格）和
    // `provider:model`（Hermes 自定义厂商），而且可以嵌套
    // （`custom:anthropic/claude-opus-4.7` 就是一个厂商前缀 id，其模型段
    // 本身又带厂商前缀）。从最早的 `/` 或 `:` 分隔处迭代剥除——只要前面那
    // 一段长得像路由层（`^[a-z][a-z0-9_-]*$`）就继续，直到剥不动为止。原始
    // 串永远是第一个候选（见下方 `push(raw)`），所以 `cursor/composer-2.5`
    // 这类已限定厂商的表键在剥皮之前就已经能命中——迭代剥皮只会补上之前
    // 会漏掉的嵌套形态。
    let out = s;
    for (;;) {
      const i = out.indexOf("/");
      const j = out.indexOf(":");
      const sep = i === -1 ? j : j === -1 ? i : Math.min(i, j);
      if (sep <= 0 || !/^[a-z][a-z0-9_-]*$/i.test(out.slice(0, sep))) break;
      out = out.slice(sep + 1);
    }
    return out;
  };
  // 只有 Anthropic 的 id 点↔横线等价。OpenAI 的分隔符有语义，所以 `gpt-5.4`
  // 之类保持原样。
  const canonAnthropic = (s: string) =>
    s.startsWith("claude-") ? s.replace(/\./g, "-") : s;
  // 去掉尾部的上下文窗口标签（`claude-opus-4-7[1m]`）。同一家族、同一价格
  // 档——1M 上下文的定价取舍见上方解析器的注释。
  const stripContextTag = (s: string) => s.replace(/\[[^\]]+\]$/, "");

  const raw = model;
  const noProvider = stripProvider(raw);
  const dashed = canonAnthropic(noProvider);
  const noTag = stripContextTag(dashed);

  push(raw);
  push(noProvider);
  push(dashed);
  push(noTag);
  push(stripDate(raw));
  push(stripDate(noProvider));
  push(stripDate(dashed));
  push(stripDate(noTag));
  canonicalCandidatesCache.set(model, out);
  return out;
}

// 「无法定价」空状态诊断用的廉价谓词：判断一批用量里哪些模型串定价失败。
// 用户盯着「$0.00 / 2M tokens」想弄明白为什么时，就用它。
export function isModelPriced(model: string, provider?: string): boolean {
  return resolvePricing(model, provider) !== undefined;
}

// 返回 `rows` 中「解析不到价格」的定价键的去重、有序列表。行带厂商时键会带
// 前缀（`cursor/auto`），这样两个厂商上报的同一个裸模型 id 会显示成两条可
// 分别定价的独立条目。全部可定价或无行时返回空数组。
// 已被厂商完整计费的行不需要费率表条目，因此绝不能触发「我们无法给这个模型
// 定价」的告警——它的成本已经精确，再让用户给它填费率，等于要用户用猜测去
// 覆盖一张真实账单。
export function collectUnmappedModels(rows: readonly Priceable[]): string[] {
  const set = new Set<string>();
  for (const r of rows) {
    if (!r.model || isModelPriced(r.model, r.provider)) continue;
    const uncosted = uncostedTokens(r);
    const needsEstimate =
      uncosted.input > 0 ||
      uncosted.output > 0 ||
      uncosted.cacheRead > 0 ||
      uncosted.cacheWrite > 0;
    if (!needsEstimate && (r.cost_usd_ticks ?? 0) > 0) continue;
    set.add(pricingKey(r.model, r.provider));
  }
  return Array.from(set).toSorted();
}

// 只要带着按模型的 token 汇总就可以定价——RuntimeUsage、RuntimeUsageByAgent、
// RuntimeUsageByHour 都有意共用这个形状（后端特意保留模型维度，好让客户端能
// 对任意聚合轴跑这套计算）。
// `provider` 可选：没有 provider 的行（以及既有测试夹具）也能通过类型检查；
// 有 provider 时用来消解通用模型 id 的歧义。RuntimeUsage / RuntimeUsageByAgent
// / DashboardUsageDaily / DashboardUsageByAgent 线上都带这个字段。
export type Priceable = Pick<
  RuntimeUsage,
  | "model"
  | "input_tokens"
  | "output_tokens"
  | "cache_read_tokens"
  | "cache_write_tokens"
> & {
  provider?: string;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
};

// 厂商按 1e-10 美元为单位的 tick 上报成本（xAI 的单位），这样亚美分的单轮
// 成本能作为整数从 agent 一路无损传到前端。
const COST_USD_TICKS_PER_USD = 10_000_000_000;

// 一行里仍需用上方定价表估算的 token，即厂商没有替我们计价的部分。
//
// 早于「成本拆分」版本的后端完全不上报 `uncosted_*` 字段。若把缺失当作
// 「剩 0 个 token 需要估算」，每一行都会显示 $0——所以 `undefined` 回退到
// 完整 token 数，正好是拆分前的行为。唯一例外是：一行没有拆分字段、却带
// 权威成本时，再叠加一份全量 token 估算会重复计费，因此让权威数字单独成立。
// 当前后端总是两者都发，这里的防御只针对版本漂移。
function uncostedTokens(usage: Priceable): {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
} {
  if (usage.uncosted_input_tokens === undefined) {
    if ((usage.cost_usd_ticks ?? 0) > 0) {
      return { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
    }
    return {
      input: usage.input_tokens,
      output: usage.output_tokens,
      cacheRead: usage.cache_read_tokens,
      cacheWrite: usage.cache_write_tokens,
    };
  }
  return {
    input: usage.uncosted_input_tokens,
    output: usage.uncosted_output_tokens ?? 0,
    cacheRead: usage.uncosted_cache_read_tokens ?? 0,
    cacheWrite: usage.uncosted_cache_write_tokens ?? 0,
  };
}

// 一行的成本：厂商实际收取的费用 + 对厂商未计费部分用费率表补的估算。
//
// 费率表表达不了请求级定价规则——xAI 的 Grok 请求提示词达到 200K token 就
// 按 2 倍计费，而这些行把一轮里的每次模型调用都聚合在一起，token 数确实无法
// 判断某次请求命中哪一档。凡是厂商告诉了我们它自己的价格，那个数字就是账单，
// 任何估算都不如它准。
//
// 两半是相加而不是取其一，因为单行可能聚合两种来源（一个桶里有两个厂商，或
// 一次 CLI 升级前后的 Grok）。自定义定价覆盖依然生效——但只作用于「估算」的
// 那一半，因为覆盖价是用户对费率的猜测，而权威数字不是猜测。
// hasKnownCost 报告这批用量里是否有任何一行"能给出价格"：要么厂商自己报了成本，
// 要么本地费率表认识这个模型。
//
// 它存在的唯一目的是把「$0」和「不知道多少钱」分开。estimateCost 对没有价格的
// 模型返回 0，直接渲染就成了"这次运行免费"——一个我们并没有依据的结论。调用方
// 在整批都为 false 时应该显示未知，而不是 $0.00。
export function hasKnownCost(rows: Priceable[]): boolean {
  for (const row of rows) {
    if ((row.cost_usd_ticks ?? 0) > 0) return true;
    if (resolvePricing(row.model, row.provider)) return true;
  }
  return false;
}

// 缓存节省需要每笔缓存读取对应的输入价与缓存价；厂商总费用无法推导这项差额。
// 无缓存读取的行贡献真实的 0；有读取却缺费率时，合计也不能用部分已知金额代替。
export function hasKnownCacheSavings(rows: Priceable[]): boolean {
  return (
    rows.length > 0 &&
    rows.every(
      (row) => row.cache_read_tokens === 0 || !!resolvePricing(row.model, row.provider),
    )
  );
}

// hasUnknownCostRemainder 报告这一行里是否还有"已经计过 token、但没有价格能覆盖"的部分。
//
// 一行可以同时携带厂商报价（cost_usd_ticks>0）和未计价的 token（补报、换模型、混合
// 网关都会这样）。此时 hasKnownCost 为 true——确实有可用的价格——但这一行的金额仍然
// 只覆盖了一部分。判断"部分已知"必须看剩下的 token 能不能被费率表或自定义价覆盖，
// 而不是看这一行有没有价格。
export function hasUnknownCostRemainder(usage: Priceable): boolean {
  const uncosted = uncostedTokens(usage);
  const remaining =
    uncosted.input + uncosted.output + uncosted.cacheRead + uncosted.cacheWrite;
  if (remaining === 0) return false;
  return !resolvePricing(usage.model, usage.provider);
}

export function estimateCost(usage: Priceable): number {
  const authoritative = (usage.cost_usd_ticks ?? 0) / COST_USD_TICKS_PER_USD;
  const pricing = resolvePricing(usage.model, usage.provider);
  if (!pricing) return authoritative;
  const uncosted = uncostedTokens(usage);
  return (
    authoritative +
    (uncosted.input * pricing.input +
      uncosted.output * pricing.output +
      uncosted.cacheRead * pricing.cacheRead +
      uncosted.cacheWrite * pricing.cacheWrite) /
      1_000_000
  );
}

// estimateCost 结果按 token 类型（输入/输出/缓存读写）的拆分。
//
// 背景：估算的那一半按价格自然拆分；权威的那一半每行只有「一个总数」，所以
// 按费率表本会收取的比例摊到四个桶里。只有总数是权威的——拆分只是为了展示，
// 这样堆积图各段相加恒等于标题数字，而不是让每条 Grok 行悄悄少画一段。
export interface CostBreakdown {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export function estimateCostBreakdown(usage: Priceable): CostBreakdown {
  const pricing = resolvePricing(usage.model, usage.provider);
  if (!pricing) {
    // 没有费率可拆，但厂商可能已经自己报过整轮的成本。这里若返回全零，会让
    // 堆积图在「成本恰好精确」的那几行上跟标题的 estimateCost 对不上，所以把
    // 整笔费用放进 input 桶，保证总额不丢。
    return {
      input: (usage.cost_usd_ticks ?? 0) / COST_USD_TICKS_PER_USD,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    };
  }
  const uncosted = uncostedTokens(usage);
  const breakdown: CostBreakdown = {
    input: (uncosted.input * pricing.input) / 1_000_000,
    output: (uncosted.output * pricing.output) / 1_000_000,
    cacheRead: (uncosted.cacheRead * pricing.cacheRead) / 1_000_000,
    cacheWrite: (uncosted.cacheWrite * pricing.cacheWrite) / 1_000_000,
  };

  const authoritative = (usage.cost_usd_ticks ?? 0) / COST_USD_TICKS_PER_USD;
  if (authoritative <= 0) return breakdown;

  // 把权威费用塑形成「费率表本会为它所覆盖的 token 收多少钱」的形状——用该行
  // 总 token 减去已估算的 token 作为覆盖面。
  const shape = {
    input: ((usage.input_tokens - uncosted.input) * pricing.input) / 1_000_000,
    output: ((usage.output_tokens - uncosted.output) * pricing.output) / 1_000_000,
    cacheRead:
      ((usage.cache_read_tokens - uncosted.cacheRead) * pricing.cacheRead) / 1_000_000,
    cacheWrite:
      ((usage.cache_write_tokens - uncosted.cacheWrite) * pricing.cacheWrite) / 1_000_000,
  };
  const shapeTotal = shape.input + shape.output + shape.cacheRead + shape.cacheWrite;
  if (shapeTotal <= 0) {
    // 没有可用来塑形的 token（无定价的 token，或一行带成本却无 token）。
    // 把钱保留在总额里，而不是把它丢掉。
    return { ...breakdown, input: breakdown.input + authoritative };
  }
  const scale = authoritative / shapeTotal;
  return {
    input: breakdown.input + shape.input * scale,
    output: breakdown.output + shape.output * scale,
    cacheRead: breakdown.cacheRead + shape.cacheRead * scale,
    cacheWrite: breakdown.cacheWrite + shape.cacheWrite * scale,
  };
}

// ---------------------------------------------------------------------------
// 单次运行（run）的用量
// ---------------------------------------------------------------------------

/** 单个 agent 运行、或一组运行的用量汇总。 */
export interface TaskUsageSummary {
  /** 四种 token 之和（input + output + cacheRead + cacheWrite），与用量页标题数字口径一致。 */
  tokens: number;
  /** 估算总成本（美元）。 */
  cost: number;
  /** 缓存命中省下的钱（见 estimateCacheSavings）。 */
  cacheSavings: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  /** 本次运行用到的不同模型，按首次出现的顺序。通常只有一个。 */
  models: string[];
}

/**
 * 把一次运行按模型拆开的用量分片折叠成一条汇总。
 *
 * 成本按分片逐个累加、而不是先加总 token 再按总价算，因为每个分片可能按
 * 不同费率计价——一次从 Sonnet 溢出到 Opus 的运行有两行，无论拿哪个费率去
 * 乘总数都是错的。这也是线上格式要保留模型维度的同一个原因。
 *
 * `undefined` 和 `[]` 都返回 `null`：二者都不是「这次运行免费」，而是「我们
 * 没有这笔数字」，UI 必须渲染成破折号。若调用方自己加总成 0，就会静默声称
 * 一次运行分文未花。
 */
export function summarizeTaskUsage(
  usage: readonly Priceable[] | undefined,
): TaskUsageSummary | null {
  if (!usage || usage.length === 0) return null;

  const models: string[] = [];
  const summary: TaskUsageSummary = {
    tokens: 0, cost: 0, cacheSavings: 0,
    input: 0, output: 0, cacheRead: 0, cacheWrite: 0,
    models,
  };

  for (const slice of usage) {
    summary.input += slice.input_tokens;
    summary.output += slice.output_tokens;
    summary.cacheRead += slice.cache_read_tokens;
    summary.cacheWrite += slice.cache_write_tokens;
    summary.cost += estimateCost(slice);
    summary.cacheSavings += estimateCacheSavings(slice);
    if (slice.model && !models.includes(slice.model)) models.push(slice.model);
  }
  summary.tokens =
    summary.input + summary.output + summary.cacheRead + summary.cacheWrite;

  return summary;
}

/**
 * 把多次运行的用量汇总成一个数字——执行日志头部展示的 issue 级总额。没有
 * 记录用量（undefined 或空数组）的运行不贡献任何值，也不会把总额拖成 null；
 * 只有没有任何运行有用量时才返回 null，即确实没有可展示的东西。
 */
export function summarizeTaskUsageAcross(
  runs: readonly (readonly Priceable[] | undefined)[],
): TaskUsageSummary | null {
  return summarizeTaskUsage(runs.flatMap((u) => u ?? []));
}

// 缓存节省额：缓存「读取」若按原始输入价计费要花多少钱，减去按打折的缓存
// 命中价实际花的钱。这是对「缓存帮你省下的钱」的重构，不是真实的花费。
export function estimateCacheSavings(usage: Priceable): number {
  const pricing = resolvePricing(usage.model, usage.provider);
  if (!pricing) return 0;
  const wouldHaveCost = (usage.cache_read_tokens * pricing.input) / 1_000_000;
  const actualCost = (usage.cache_read_tokens * pricing.cacheRead) / 1_000_000;
  return wouldHaveCost - actualCost;
}

// ---------------------------------------------------------------------------
// 数据聚合
// ---------------------------------------------------------------------------

// 某自然日四种 token 的计数（用于 token 堆叠图）。
export interface DailyTokenData {
  /** ISO 日期（YYYY-MM-DD）。 */
  date: string;
  /** X 轴刻度文案，如 "5/12"。 */
  label: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

// 某自然日的估算总成本（美元）。
export interface DailyCostData {
  date: string;
  label: string;
  cost: number;
}

// 堆叠变体——把每日的 $ 数字拆成驱动计费的四个组成部分。estimateCost 计费
// 的每个组成部分都必须在这里：`total` 是悬浮提示与空状态检查读取的值，所以
// 堆叠里少一个组成部分，就等于用户成本数字里少了一笔钱。
//
// 缓存读取曾一度被排除，理由是它的费率太小看不见。其实并非如此：在当前费率
// 表下缓存输入大约比非缓存便宜 10 倍、而不是 100 倍，而且 agent 会话读取的
// 缓存 token 通常是非缓存的几十倍——所以缓存读取往往是「最大」的一段，丢弃
// 它会让某些桶低估超过 50%（MUL-6334）。
//
// 缓存「节省」——对折扣避免了多少钱的重构——是另一个独立的 KPI，刻意不放进
// 这个堆叠；节省不是花费。
export interface DailyCostStackData {
  date: string;
  label: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  /** 四舍五入后各组成部分之和，即悬浮提示显示的当日总成本。 */
  total: number;
}

// 按模型聚合的用量分布：某模型在窗口内的总 token 与总成本。
export interface ModelDistribution {
  /** 模型展示名（可能带厂商前缀，见 modelGroupingKey）。 */
  model: string;
  tokens: number;
  cost: number;
}

// 某自然周的 token 数据（周图每个柱代表一周）。
export interface WeeklyTokenData {
  /** 周一日期（YYYY-MM-DD）。 */
  weekStart: string;
  /** 周日日期（YYYY-MM-DD）。 */
  weekEnd: string;
  // X 轴刻度——所在周的周一，如 "5/12"。
  label: string;
  // 悬浮提示标题——闭区间范围，如 "May 12 – May 18"。
  rangeLabel: string;
  // 当 `weekEnd` 还在未来时（今天在一周中间）为 true。图表据此把这根柱画成
  // 半透明/斜纹，示意「别把这一周当成已经完整结束的一周」。
  partial: boolean;
  /** 该周已实际过去的自然日数（完整周为 7）。 */
  daysCovered: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

// 某自然周的成本堆叠数据（字段含义同 DailyCostStackData，只是按周聚合）。
export interface WeeklyCostStackData {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  total: number;
}

/**
 * 把按 (日期, 模型) 拆开的日粒度用量行聚合为图表所需的各种日级数据：
 * 每日 token 堆叠、每日总成本、每日成本堆叠（四个计费组件分列）、按模型的
 * 用量分布。输入已是日粒度，这里只做按日期/模型的累加，并用日期排序。
 */
export function aggregateByDate(usage: RuntimeUsage[]): {
  dailyTokens: DailyTokenData[];
  dailyCost: DailyCostData[];
  dailyCostStack: DailyCostStackData[];
  modelDist: ModelDistribution[];
} {
  const dateMap = new Map<string, Omit<DailyTokenData, "label">>();
  const costMap = new Map<string, number>();
  const stackMap = new Map<
    string,
    { input: number; output: number; cacheRead: number; cacheWrite: number }
  >();
  const modelMap = new Map<string, { tokens: number; cost: number }>();

  for (const u of usage) {
    const existing = dateMap.get(u.date) ?? {
      date: u.date,
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    };
    existing.input += u.input_tokens;
    existing.output += u.output_tokens;
    existing.cacheRead += u.cache_read_tokens;
    existing.cacheWrite += u.cache_write_tokens;
    dateMap.set(u.date, existing);

    const dayCost = (costMap.get(u.date) ?? 0) + estimateCost(u);
    costMap.set(u.date, dayCost);

    const breakdown = estimateCostBreakdown(u);
    const stack = stackMap.get(u.date) ?? {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    };
    stack.input += breakdown.input;
    stack.output += breakdown.output;
    stack.cacheRead += breakdown.cacheRead;
    stack.cacheWrite += breakdown.cacheWrite;
    stackMap.set(u.date, stack);

    const modelName = modelGroupingKey(u.model, u.provider);
    const m = modelMap.get(modelName) ?? { tokens: 0, cost: 0 };
    m.tokens +=
      u.input_tokens + u.output_tokens + u.cache_read_tokens + u.cache_write_tokens;
    m.cost += estimateCost(u);
    modelMap.set(modelName, m);
  }

  const formatLabel = (d: string) => {
    const date = new Date(d + "T00:00:00");
    return `${date.getMonth() + 1}/${date.getDate()}`;
  };

  const dailyTokens = Array.from(dateMap.values())
    .toSorted((a, b) => a.date.localeCompare(b.date))
    .map((d) => ({ ...d, label: formatLabel(d.date) }));

  const dailyCost = Array.from(costMap.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([date, cost]) => ({
      date,
      label: formatLabel(date),
      cost: Math.round(cost * 100) / 100,
    }));

  const dailyCostStack = Array.from(stackMap.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([date, s]) => {
      const round = (n: number) => Math.round(n * 100) / 100;
      const input = round(s.input);
      const output = round(s.output);
      const cacheRead = round(s.cacheRead);
      const cacheWrite = round(s.cacheWrite);
      return {
        date,
        label: formatLabel(date),
        input,
        output,
        cacheRead,
        cacheWrite,
        // 各段各自四舍五入后再相加，而不是对原始和做一次 round(sum)——
        // 悬浮提示里的 Total 就是各柱段的绘制值之和，先舍入各段再求和才能让
        // 底部总额与它下方的柱条完全一致。
        total: round(input + output + cacheRead + cacheWrite),
      };
    });

  const modelDist = [...modelMap.entries()]
    .map(([model, data]) => ({ model, ...data }))
    .sort((a, b) => b.tokens - a.tokens);

  return { dailyTokens, dailyCost, dailyCostStack, modelDist };
}

// 把日粒度行折叠进 ISO 自然周（周一至周日）。复用日级聚合读取的同一条
// 180 天缓存，不额外发请求。当「今天」（按运行时所在时区）还没到周日时，
// 最新一周会被标记为 `partial`，图表就能把进行中的这根柱画成半透明，避免
// 用户把「本周」误读成费用骤降。
//
// `weekCount` 把输出严格限制为「截至今天（按 `tz`）所在周」往前数这么多
// 个自然周。桶预先填零，稀疏数据——包括完全没用量的周——会渲染成零柱而
// 不是消失。落在窗口外的行被丢弃；没有这道防线，对稀疏的 180 天聚合数据
// 做 `.slice(-weekCount)` 会把「早期有数据的老周」顶出来，而不是用户想要
// 的「窗口内的空桶」（MUL-2382 周窗口范围问题）。
// 接受任何携带 `date` + token 计数 + 定价所需 model 的行。`RuntimeUsage`
// （runtime 详情）与 `DashboardUsageDaily`（工作区仪表盘）都匹配这个形状——
// 没有行为差异，只是周围字段略有不同，两个聚合器都不关心那些字段。
type WeeklyAggregable = Pick<
  RuntimeUsage,
  | "date"
  | "model"
  | "input_tokens"
  | "output_tokens"
  | "cache_read_tokens"
  | "cache_write_tokens"
> & { provider?: string };

/**
 * 把日粒度用量行聚合进 ISO 自然周，输出每周的 token 与成本堆叠数据
 * （详见 WeeklyTokenData / WeeklyCostStackData）。
 */
export function aggregateByWeek(
  usage: readonly WeeklyAggregable[],
  tz: string,
  weekCount: number,
): {
  weeklyTokens: WeeklyTokenData[];
  weeklyCostStack: WeeklyCostStackData[];
} {
  const count = Math.max(1, Math.floor(weekCount));
  const today = todayIso(tz);
  const currentWeekStart = weekStartIso(today);
  const firstWeekStart = addDaysIso(currentWeekStart, -(count - 1) * 7);

  type TokenAgg = Omit<WeeklyTokenData, "label" | "rangeLabel" | "partial" | "daysCovered" | "weekEnd">;
  const tokenMap = new Map<string, TokenAgg>();
  const stackMap = new Map<
    string,
    { input: number; output: number; cacheRead: number; cacheWrite: number }
  >();

  // 预先为窗口内的每个自然周填零，保证稀疏/空周也渲染成零柱、而不是被丢弃。
  for (let i = 0; i < count; i++) {
    const wkStart = addDaysIso(firstWeekStart, i * 7);
    tokenMap.set(wkStart, {
      weekStart: wkStart,
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    });
    stackMap.set(wkStart, { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 });
  }

  for (const u of usage) {
    const wkStart = weekStartIso(u.date);
    if (wkStart < firstWeekStart || wkStart > currentWeekStart) continue;
    const tokens = tokenMap.get(wkStart);
    if (!tokens) continue;
    tokens.input += u.input_tokens;
    tokens.output += u.output_tokens;
    tokens.cacheRead += u.cache_read_tokens;
    tokens.cacheWrite += u.cache_write_tokens;

    const breakdown = estimateCostBreakdown(u);
    const stack = stackMap.get(wkStart);
    if (!stack) continue;
    stack.input += breakdown.input;
    stack.output += breakdown.output;
    stack.cacheRead += breakdown.cacheRead;
    stack.cacheWrite += breakdown.cacheWrite;
  }

  const decorate = (weekStart: string) => {
    const weekEnd = addDaysIso(weekStart, 6);
    const partial = today < weekEnd;
    // 本周实际已过去的天数（含今天）。已结束的周为 7，本周取 1..6。
    const elapsedDays = Math.min(
      7,
      Math.max(
        1,
        // `today` 在 [weekStart, weekEnd] 区间内的日序号 + 1（越界时收敛到区间端点）。
        diffDaysIso(weekStart, today < weekStart ? weekStart : today < weekEnd ? today : weekEnd) + 1,
      ),
    );
    return {
      weekStart,
      weekEnd,
      label: formatShortDate(weekStart),
      rangeLabel: `${formatShortDate(weekStart)} – ${formatShortDate(weekEnd)}`,
      partial,
      daysCovered: partial ? elapsedDays : 7,
    };
  };

  const weeklyTokens: WeeklyTokenData[] = Array.from(tokenMap.values())
    .toSorted((a, b) => a.weekStart.localeCompare(b.weekStart))
    .map((t) => ({ ...t, ...decorate(t.weekStart) }));

  const weeklyCostStack: WeeklyCostStackData[] = Array.from(stackMap.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([weekStart, s]) => {
      const round = (n: number) => Math.round(n * 100) / 100;
      const input = round(s.input);
      const output = round(s.output);
      const cacheRead = round(s.cacheRead);
      const cacheWrite = round(s.cacheWrite);
      return {
        ...decorate(weekStart),
        input,
        output,
        cacheRead,
        cacheWrite,
        total: round(input + output + cacheRead + cacheWrite),
      };
    });

  return { weeklyTokens, weeklyCostStack };
}

// 把日粒度用量序列切成「用户选中的窗口」和「紧邻其前、等长的对比窗口」两段。
// 「今天」按运行时所在时区读取，让截断点落在后端分桶时用的同一个自然日边界
// 上——否则浏览器与运行时的时区差会让边界上的日期漂移一天（#MUL-2382
// sliceWindow 时区 bug）。
export function sliceWindow(
  usage: readonly RuntimeUsage[],
  days: number,
  tz: string,
): { filtered: RuntimeUsage[]; prevFiltered: RuntimeUsage[] } {
  const today = todayIso(tz);
  const isoCurrent = addDaysIso(today, -days);
  const isoPrev = addDaysIso(today, -days * 2);
  return {
    filtered: usage.filter((u) => u.date >= isoCurrent),
    prevFiltered: usage.filter(
      (u) => u.date >= isoPrev && u.date < isoCurrent,
    ),
  };
}

// diffDaysIso 计算两个 YYYY-MM-DD 字符串之间相差的天数（to − from）。改用
// Date.UTC 做纯算术，杜绝了本地时区/夏令时带来的 ±1 天误差。
function diffDaysIso(from: string, to: string): number {
  const [y1, m1, d1] = from.split("-").map(Number);
  const [y2, m2, d2] = to.split("-").map(Number);
  const a = Date.UTC(y1 ?? 1970, (m1 ?? 1) - 1, d1 ?? 1);
  const b = Date.UTC(y2 ?? 1970, (m2 ?? 1) - 1, d2 ?? 1);
  return Math.round((b - a) / 86_400_000);
}

// ---------------------------------------------------------------------------
// 日历助手——所有日期运算都在「运行时的 IANA 时区」下以 YYYY-MM-DD 字符串
// 进行。后端已经按「运行时时区的当日零点」把每日用量分桶，前端日/周聚合
// 保持同一时间轴，避免浏览器与运行时跨时区时统计漂移一天。
// ---------------------------------------------------------------------------

// 给定 IANA 时区下的今日日期（YYYY-MM-DD）。用 `en-CA` 区域格式，因为它输出
// 的就是 ISO 形态，省得我们手工拼 Intl 各部分。
export function todayIso(tz: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date());
}

// 对 YYYY-MM-DD 字符串做纯日期加减。底层走 UTC，夏令时切换绝不会让结果
// 偏一小时、进而进位到相邻的一天。
export function addDaysIso(iso: string, days: number): string {
  const [y, m, d] = iso.split("-").map(Number);
  const dt = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, d ?? 1));
  dt.setUTCDate(dt.getUTCDate() + days);
  return dt.toISOString().slice(0, 10);
}

// 返回所在周的周一（YYYY-MM-DD）。ISO 8601 规定周一为一周之始，与热力图及
// 团队日常「本周」的心智模型一致。纯字符串运算——不读取 `new Date()` 的本地
// 时区——在任何宿主机时区下都稳定。
export function weekStartIso(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  const dt = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, d ?? 1));
  const day = dt.getUTCDay(); // 0=周日，1=周一，…，6=周六
  const offset = (day + 6) % 7; // 往回退到周一的天数
  dt.setUTCDate(dt.getUTCDate() - offset);
  return dt.toISOString().slice(0, 10);
}

// 把 YYYY-MM-DD 格式化成简短的月/日（如 "May 12"），本地化语言为英文。
// 用 UTC 解析，确保显示的日期不随浏览器时区变化。
export function formatShortDate(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  const dt = new Date(Date.UTC(y ?? 1970, (m ?? 1) - 1, d ?? 1));
  return dt.toLocaleString("en", {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

// ---------------------------------------------------------------------------
// 按维度分组（Cost-by-X）的成本聚合
//
// 三个「按…统计」页签共用同一形状：一行一个维度键（agent 名、模型名或某个
// 小时），带总量 token、总成本与任务数。图表/列表组件不需要知道自己渲染的是
// 哪个轴——它们只看到 {key, tokens, cost}。
// ---------------------------------------------------------------------------

// 按某维度聚合出来的一行：维度键、token 总量、估算总成本与任务数。
export interface CostByKey {
  key: string;
  tokens: number;
  cost: number;
  taskCount: number;
}

// 把按 (agent, 模型) 拆的行聚合成每个 agent 的总量。成本把该 agent 的全部
// 模型累加，再按成本降序排列，让花钱最多的 agent 排最前。
export function aggregateCostByAgent(rows: RuntimeUsageByAgent[]): CostByKey[] {
  const map = new Map<string, CostByKey>();
  for (const r of rows) {
    const entry = map.get(r.agent_id) ?? {
      key: r.agent_id,
      tokens: 0,
      cost: 0,
      taskCount: 0,
    };
    entry.tokens +=
      r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_write_tokens;
    entry.cost += estimateCost(r);
    entry.taskCount += r.task_count;
    map.set(r.agent_id, entry);
  }
  return Array.from(map.values()).toSorted((a, b) => b.cost - a.cost);
}

// 把按 (日期, 模型) 拆的行聚合成每个模型的总量（「按模型」页签复用日级
// 数据缓存，无需额外请求）。成本降序排列。
export function aggregateCostByModel(rows: RuntimeUsage[]): CostByKey[] {
  const map = new Map<string, CostByKey>();
  for (const r of rows) {
    const key = modelGroupingKey(r.model, r.provider);
    const entry = map.get(key) ?? { key, tokens: 0, cost: 0, taskCount: 0 };
    entry.tokens +=
      r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_write_tokens;
    entry.cost += estimateCost(r);
    map.set(key, entry);
  }
  return Array.from(map.values()).toSorted((a, b) => b.cost - a.cost);
}

// 估算「尾部窗口」内的总成本，窗口为
//   [今天 − offsetDays − daysBack, 今天 − offsetDays)。
// `offsetDays = 0, daysBack = 7` → 最近 7 天。
// `offsetDays = 7, daysBack = 7` → 最近 7 天再往前的那 7 天（runtime 列表
// 里 ↑/↓ 涨跌对比所用的「上一周期」窗口）。
//
// 「今天」按 `tz`（查看者时区）读取，让截断点与后端分桶用的自然日边界对齐
// ——这些行本来就是按查看者时区分桶到达的，用 JS 引擎本地时区去切会让窗口
// 在边界处漂移一天。
//
// 与 aggregateByDate 遍历同一条日粒度 `RuntimeUsage` 行，因此 runtime 列表的
// 成本与 runtime 详情的 KPI 口径一致（关键是命中同一条 TanStack Query 缓存键）。
export function computeCostInWindow(
  rows: readonly RuntimeUsage[],
  daysBack: number,
  tz: string,
  offsetDays: number = 0,
): number {
  const today = todayIso(tz);
  const isoEnd = addDaysIso(today, -offsetDays);
  const isoStart = addDaysIso(today, -offsetDays - daysBack);
  let total = 0;
  for (const r of rows) {
    if (r.date >= isoStart && r.date < isoEnd) total += estimateCost(r);
  }
  return total;
}

/**
 * 计算 (当前 − 上期) / 上期的百分比变化，四舍五入到整数。
 * 上期 ≤ 0 时无法有意义地算涨跌幅（除零/无对比基准），返回 null，调用方
 * 通常显示成「—」而不是误导性的百分比。
 */
export function pctChange(current: number, previous: number): number | null {
  if (previous <= 0) return null;
  return Math.round(((current - previous) / previous) * 100);
}
