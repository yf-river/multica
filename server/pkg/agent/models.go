package agent

// 文件职责：本文件是 agent 运行时“模型目录”（model catalog）的发现与归一化层，回答一个贯穿
// 全系统的问题：对某个 agent 提供方（claude / codex / cursor / copilot / pi / kimi / hermes /
// reasonix / kiro / qoder / traecli / codebuddy / grok / dim / antigravity 等），UI 下拉框里
// 应该列出哪些模型、每款模型支持哪些“思考强度/effort”档位、哪款是该运行时自己认定的默认模型。
// 结果统一收敛成 Catalog 结构体交给上层（daemon / 前端）。
//
// 与外部工具/CLI 的交互，数据来源分两类：
//  1. 静态内置表（claudeStaticModels、copilotStaticModels、grokStaticModels 等）：人工维护、
//     长期有效的 ID 清单，通常作为“无需网络/未登录”时的兜底；
//  2. 动态发现：拉起目标 CLI 的子进程（如 `codex debug models`、`pi --list-models`、
//     `cursor-agent --list-models`、`agy models`、`omp models --json`、`kimi provider list`），
//     或对 ACP 系 CLI 跑一遍 initialize→(authenticate)→session/new 握手，再解析响应里的
//     `models.availableModels` / `configOptions` 等结构得到模型与 effort 档位。
//
// 关键约定：动态发现结果做 60s 短 TTL 记忆化（modelCache）；空结果与 Fallback（静态替身）目录
// 一律不进缓存，让“瞬时故障恢复后下一次请求就能立刻重试”；Fallback 目录绝不代表运行时真实能力，
// 尤其禁止写进服务端日级模型目录缓存（MUL-5549）。另一约定：各发现器对超时非常敏感——多数
// 15s 上限，hermes 因其失败路径要 40s（详见各常量注释）。
//
// 内部组织：开头是结果/状态类型（Model、ModelThinking、Catalog 等）与面向调用方的查询函数
// （ListModels、QualifyModelID、ModelSelectionSupported…）；中段按“静态目录 → 各提供方动态
// 发现器/解析器 → ACP 共享握手助手”排布；文件末尾是 CodeBuddy 专属的发现逻辑。
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Model 描述某个 agent 提供方向用户暴露的单个 LLM 模型。
// 当下拉框里的 ID 形如 `provider/model`（例如 opencode 的 "openai/gpt-4o"）时，前端会按
// Provider 分组展示。
// Default 只是*展示*提示：UI 会为运行时自己宣称为默认的那一项打徽标（例如 Claude Code 出厂
// 默认，或 hermes 的 currentModelId）。它在执行期没有任何作用——当 agent.model 为空时 daemon
// 会把 "" 传给后端，由各提供方自己的 CLI 去解析它真正的默认模型，这总是比这里写死的猜测更
// 贴合用户账号/环境的实际能力。
type Model struct {
	ID                                  string             `json:"id"`
	Label                               string             `json:"label"`
	Provider                            string             `json:"provider,omitempty"`
	Default                             bool               `json:"default,omitempty"`
	ServiceTiers                        []ModelServiceTier `json:"service_tiers,omitempty"`
	SupportsExplicitStandardServiceTier bool               `json:"supports_explicit_standard_service_tier,omitempty"`
	// Thinking 上报该运行时为这款模型准备的 reasoning/effort 档位目录。nil 表示运行时/模型没有
	// thinking 档位控制（或 daemon 没发现到），UI 会隐藏对应选择器。目录按模型区分，是因为
	// Codex 的 `codex debug models` 本身就是按模型的，而 Claude 的 `--effort` 档位集存在已知的
	// 按模型缺口（`xhigh` 仅 Opus 可用，`max` 仅会话内可用）。见 MUL-2339。
	Thinking *ModelThinking `json:"thinking,omitempty"`
}

// ModelServiceTier 是某运行时为模型宣传的一种“原生执行档位”（对应前端的速度/speed 选择器）。
// ID 会原样送回提供方协议；Name/Description 是该运行时目录自带的展示文案。
type ModelServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelThinking 承载某 agent 运行时暴露的、按模型区分的思考/effort 档位目录。值都是运行时
// 原生 token：Codex 会发 "none|minimal|low|medium|high|xhigh|max|ultra"；Claude 发
// "low|medium|high|xhigh|max"；Pi 发 "off|minimal|low|medium|high|xhigh|max"。前端原样
// 渲染 SupportedLevels，让用户看到的名字与每个 CLI 自己的界面一致。
type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	// DefaultLevel 是未指定覆盖项时运行时自己会选用的档位。空串表示“由运行时自己选，我们
	// 不知道”——此时 UI 只展示一个笼统的 "Default" 选项。
	DefaultLevel string `json:"default_level,omitempty"`
}

// ThinkingLevel 是 ModelThinking.SupportedLevels 列表里的一项。
// Value 是真正传给 CLI 的字面 token（Claude `--effort <value>` 或 Codex
// `model_reasoning_effort=<value>`）；Label 是展示文字；Description 是可选说明，上游目录里有就
// 摘过来（如 Codex 的 `description` 字段）。
type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Catalog 是一轮模型发现的结果：要展示的模型列表，以及它们是否真的是动态发现到的。
type Catalog struct {
	Models []Model
	// Fallback 表示这次发现并未成功，Models 只是静态替身，不是运行时的真实目录。
	//
	// 背景：若干提供方（codebuddy、copilot、cursor、grok）在发现失败时会退一份内置列表，好让
	// 下拉框仍有内容可展示。作为 UI 兜底说得通，但拿去持久化就是灾难：它不是运行时真正支持的
	// 东西；对 codebuddy 而言静态 ID 与真实 ID 完全无交集，用户每选一次都是一个被 CLI 拒绝的
	// ID。因此调用方绝不能把 fallback 目录当权威——尤其不能让它进入服务端按天粒度的模型目录
	// 缓存，那会把一次瞬时失败固化成 24 小时的答案（MUL-5549）。
	Fallback bool
}

// discovered 把朴素的 `([]Model, error)` 发现函数适配成 Catalog——供那些没有静态兜底的提供方
// 使用。对它们而言，失败本身就已体现为“空列表”或“错误”，下游的守卫逻辑负责处理这两种情况。
func discovered(models []Model, err error) (Catalog, error) {
	return Catalog{Models: models}, err
}

// modelCacheEntry 是 modelCache 里的一条记忆化记录：本次发现到的模型列表 + 过期时间。
// 见 ListModels 文件导读里“短 TTL 缓存”约定。
type modelCacheEntry struct {
	models    []Model
	expiresAt time.Time
}

// modelCacheMu 保护下面的 modelCache 全局 map（ListModels 可能被并发请求同时调用）。
// modelCache 是动态发现的记忆化缓存：provider(+启动路径) → 最近一次发现结果。
var (
	modelCacheMu sync.Mutex
	modelCache   = map[string]modelCacheEntry{}
)

// modelCacheTTL 是动态发现缓存的存活时长。取 60s：UI 打开模型下拉框是高频操作，不能每次都
// 重新拉起一个 CLI 子进程；同时模型列表几个月都不会变，60s 足够“新鲜”。
const modelCacheTTL = 60 * time.Second

// ListModels 返回给定 agent 提供方支持的模型。
// 对有已知静态目录的提供方直接返回内置列表；对有 CLI 发现机制的提供方（codex、opencode、pi、
// forking CLI 等）会带缓存地拉起子进程去查，并在提供方存在安全静态目录时失败兜底。
//
// 对 claude、codex、opencode、pi、kimi，返回的目录还会附上从本地 CLI 发现的、按模型区分的
// thinking 档位。Codex 发现失败会退回一份“模型+thinking 快照”；没有安全兜底的提供方则把
// Thinking 留成 nil，UI 据此隐藏 thinking 选择器。
//
// runtimeCmd 让调用方指向非默认的可执行文件；想用提供方在 PATH 上的默认名字就传零值
// Command。它的启动前缀——自定义运行时 profile 的 fixed_args——会带进每个发现子进程，因此
// 只有经由子命令才能真正触达真实 CLI 的包装器（`ccms start q36`）会按它实际运行的 CLI 被枚举，
// 而不是按包装器本身的名字（GH #7046）。
func ListModels(ctx context.Context, providerType string, runtimeCmd Command) (Catalog, error) {
	// 内置运行时身份（如 "omp"）会在描述符里声明自己的模型发现策略。在按“协议族”的 switch 之前
	// 先统一处理它们，这样下面就不需要为某个运行时写专门分支。描述符没有 ModelDiscovery 策略时
	// 返回空目录（而不是走该协议族默认）：跑一条语义上不兼容的发现命令（例如 omp 拒绝
	// --list-models）比退化成手工录入更糟。
	if desc, ok := BuiltinRuntimeByID(providerType); ok {
		if desc.ModelDiscovery != nil {
			return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
				return discovered(desc.ModelDiscovery(ctx, runtimeCmd))
			})
		}
		return Catalog{Models: []Model{}}, nil
	}
	switch providerType {
	case "claude":
		models := claudeStaticModels()
		annotateClaudeThinking(ctx, models, runtimeCmd)
		// Claude 的目录是“设计上就是静态”，而非“发现失败退回静态”：根本没有发现步骤可回退，
		// 所以这里的结果是权威的。
		return Catalog{Models: models}, nil
	case "codex":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverCodexModels(ctx, runtimeCmd), nil)
		})
	case "antigravity":
		// agy 1.0.6 起新增了 `--model` 参数和 `agy models` 目录命令（MUL-3125）。和其余
		// 动态发现后端一样按需枚举。
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverAntigravityModels(ctx, runtimeCmd))
		})
	case "traecli":
		// 官方 TRAE CLI 原生走 ACP：它的模型目录由 session/new 返回。与其余 ACP 后端一样按需
		// 枚举（要求 traecli 已登录；出错时退回手工录入）。
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverTraecliModels(ctx, runtimeCmd))
		})
	case "cursor":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverCursorModels(ctx, runtimeCmd)
		})
	case "copilot":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverCopilotModels(ctx, runtimeCmd)
		})
	case "hermes":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverHermesModels(ctx, runtimeCmd))
		})
	case "kimi":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverKimiModels(ctx, runtimeCmd))
		})
	case "reasonix":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverReasonixModels(ctx, runtimeCmd))
		})
	case "dsh":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverDshModels(ctx, runtimeCmd))
		})
	case "kiro":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverKiroModels(ctx, runtimeCmd))
		})
	case "qoder", "qoderclicn":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverQoderModels(ctx, runtimeCmd, qoderDefaultBinary(providerType)))
		})
	case "opencode":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverOpenCodeModels(ctx, runtimeCmd))
		})
	case "codearts":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverCodeArtsModels(ctx, runtimeCmd))
		})
	case "deveco":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverDevecoModels(ctx, runtimeCmd))
		})
	case "pi":
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discovered(discoverPiModels(ctx, runtimeCmd))
		})
	case "codebuddy":
		// thinking 标注也在 discoverCodebuddyModels 内部一并完成，同一次 ACP 握手就能同时
		// 喂饱模型目录与 effort 目录；若拆到这里单独标注，会再跑一遍命令（MUL-5549）。
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverCodebuddyModels(ctx, runtimeCmd)
		})
	case "qwen":
		// Qwen Code 没有一个“与账号无关、可无头运行”的模型目录。返回空列表既能保住运行时默认
		// 与手工录入，又不会把某个 Token-Plan 专属的模型推荐给其他账号。
		return Catalog{Models: []Model{}}, nil
	case "qwenpaw":
		// QwenPaw 不支持模型选择（session/set_model 持久化到的是 agent 作用域而不是会话作用域），
		// 所以就算发现了目录也没有消费方。返回空列表，避免起一个白费功夫的 ACP 子进程。如果上游
		// 把模型选择改成会话作用域，再仿照 discoverTraecliModels 在这里恢复一个发现助手。
		return Catalog{Models: []Model{}}, nil
	case "mcode":
		// MCode 的 ACP server 既不暴露会话作用域的模型选择，也没有模型目录。模型由已配置的
		// MCode 运行时自己决定。
		return Catalog{Models: []Model{}}, nil
	case "grok":
		// xAI Grok Build 原生走 ACP（`grok agent stdio`）；模型目录来自 session/new。失败时退回
		// 一份很小的静态列表，保证离线/未登录时 UI 选择器仍可用。
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverGrokModels(ctx, runtimeCmd)
		})
	case "dim":
		// Dim (dimcode) 原生走 ACP（`dim acp`）；其模型目录由 session/new 在
		// models.availableModels 里宣传。枚举要求 dim 已登录（OAuth）；任何失败都退回空目录，
		// 让 UI 保留手工录入入口。
		return cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return discoverDimModels(ctx, runtimeCmd)
		})
	case "zeroclaw":
		// ZeroClaw 的 ACP server 不宣传任何目录：session/new 只精确回答 {sessionId,
		// workspaceDir}（已对 0.8.4 验证），而且它也没有会话作用域的模型选择可消费——见
		// ModelSelectionSupported。返回空列表而不是起一个注定空手而归的 ACP 子进程。
		return Catalog{Models: []Model{}}, nil
	default:
		return Catalog{}, fmt.Errorf("unknown agent type: %q", providerType)
	}
}

// ModelSelectorMustBeProviderQualified 报告某运行时的 CLI 是否拒绝“不携带 `<provider>/` 前缀”
// 的裸模型 ID。
//
// 这是执行契约，与目录长什么样无关。它只决定一件事：daemon 在启动一个钉死了模型的 task 之前，
// 是否必须先读一遍运行时目录。那次读取要起一个 CLI 子进程、耗时上限 15–30s；而 cachedDiscovery
// 对空结果或 fallback 结果刻意不做记忆化（#3729、MUL-5549），所以一个未登录的运行时每次读取都
// 要付这个上限——只有“没有它启动真的会失败”时才值得花（MUL-6471 review）。
//
// opencode 的 `run --model` 及其 DevEco 分支只认 `provider/model`，其余一律拒绝——已对 opencode
// 1.18.14 验证：裸 ID 会在任何 provider 调用之前被 `UnknownError` 打回。pi 刻意缺席：它的解析器
// 既接受规范 selector，也接受裸 ID 和含 `/` 的 ID（见 buildPiArgs），所以 pi task 不需要 daemon
// 预先补前缀也能正常启动。
//
// 内置运行时身份按自己的协议族解析，因此某个 fork 会从描述符条目继承契约，而不是吃这个函数名
// 里的硬编码清单。
func ModelSelectorMustBeProviderQualified(providerType string) bool {
	if desc, ok := BuiltinRuntimeByID(providerType); ok {
		providerType = desc.ProtocolFamily
	}
	switch providerType {
	case "opencode", "deveco":
		return true
	default:
		return false
	}
}

// QualifyModelID 把持久化下来的模型字符串解析成运行时自己目录宣传的那个规范 ID，并报告是否
// 改写过。catalog 是运行时的发现结果；调用方手上通常已经持有它（daemon 本来就要为 thinking 档位
// 和服务档位检查读它），所以这里不额外花成本。
//
// 把自己的目录按命名空间划分的运行时（opencode 的 `provider/model`、pi 的 `provider/id`
// selector）要的是带前缀形式，但 `agent.model` 里存的是当初随便持久化的值——而 gateway 类提供方
// 的裸模型 ID 本身就是斜杠形状的（provider `multica-anthropic` 下的
// `claude/claude-opus-5`）。因此斜杠不是 provider 边界，不能靠猜：只有目录知道某个 ID 归属哪个
// provider。当恰好只有一个 provider 声称拥有该值时，调用方拿到限定后的 ID；其他情况原样返回。
//
// 所有不确定的情形都“刻意原样返回”——目录是静态兜底而非运行时真实列表、值已与某目录 ID 完全
// 相等、或两个 provider 暴露了同一个裸 ID，都是如此。到处都支持手工录入模型，所以这个函数没
// 把握定位的值必须逐字到达 CLI；CLI 自己的解析器比这里的猜测判断得更准。
func QualifyModelID(catalog Catalog, model string) (string, bool) {
	model = strings.TrimSpace(model)
	// fallback 目录是静态替身，不是运行时真正支持的列表（见 Catalog.Fallback）——拿它做限定
	// 会把一个能正常工作的 ID 改写成 CLI 从未宣传过的值。
	if model == "" || catalog.Fallback {
		return model, false
	}
	for _, m := range catalog.Models {
		if m.ID == model {
			return model, false
		}
	}
	qualified := ""
	for _, m := range catalog.Models {
		if m.Provider == "" || m.ID != m.Provider+"/"+model {
			continue
		}
		if qualified != "" && qualified != m.ID {
			// 两个 provider 暴露了同一个裸 ID。二选一等于抛硬币，会悄悄把 task 路由到错误的
			// gateway，所以原样返回、不做改写。
			return model, false
		}
		qualified = m.ID
	}
	if qualified == "" {
		return model, false
	}
	return qualified, true
}

// ModelSelectionSupported 报告对给定 provider 设置 `agent.model` 是否真的起作用。现在每个
// 内置 provider 都会端到端遵守 `opts.Model`——Hermes 在每次 prompt 前通过 ACP
// `session/set_model` RPC 路由它；Claude / Codex / Cursor / Gemini / Copilot / Kimi / Reasonix /
// Kiro / OpenCode / forking CLI / Pi / Antigravity 则通过 flag 或会话配置传递（Antigravity 在
// agy 1.0.6 才支持 `--model`——MUL-3125）。
//
// 这里保留成一个钩子（而不是在调用点直接内联 `true`），是为了让“没有模型概念的运行时”能在
// 一处统一退出，从而让 UI 渲染一个禁用的 "Managed by runtime" 选择器，而不是一个空下拉框外加
// 一个被悄悄忽略的手工录入框。
func ModelSelectionSupported(providerType string) bool {
	switch providerType {
	case "qwenpaw", "mcode", "zeroclaw":
		// QwenPaw 的 `session/set_model` 持久化到的是 agent.json 的 agent 作用域，不是会话作用域。
		// 调用它等于改动用户共享且持久的 agent 配置，因此不支持模型覆盖——运行时用 agent profile
		// 里配好的模型。如果上游把 QwenPaw 的模型选择改成会话作用域，这里可以改回 `true`。
		// MCode 同样不通过 ACP 暴露任何模型选项，其运行时配置才是唯一真源。ZeroClaw 更彻底：
		// 它的 ACP 分发表里根本没有 `session/set_model`（0.8.4 会回 -32601），也没有任何 handler
		// 读 model 参数，所以模型来自 ZeroClaw agent profile（`agents.<alias>.model_provider`），
		// Multica 发什么过去都改不了。
		return false
	default:
		return true
	}
}

// ModelKnownIncompatibleWithProvider 报告某个已保存的模型是否与目标运行时 provider 存在“已知”
// 不匹配。对拥有维护中静态目录的第一方 provider 来说，兼容性是精确判断：模型必须是该运行时
// 宣传的 ID 之一。未知/自定义模型字符串一律返回 false——UI 和 CLI 都允许手工录入，服务端不应
// 抹掉它无法放心分类的值。
func ModelKnownIncompatibleWithProvider(providerType, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}

	accepted, ok := acceptedModelIDsForProvider(providerType)
	if !ok {
		return false
	}
	if accepted[modelIDForCapabilityLookup(providerType, model)] {
		return false
	}
	return isRuntimeSpecificModelID(model)
}

// claudeContextWindowTagRe 识别 Claude Code 尾部那种“上下文窗口”模型修饰符（例如
// claude-opus-5[1m]）。刻意比通用的“方括号后缀”匹配得更窄：只有当修饰符语法上是一个上下文
// 大小时，能力查询才允许继承基础模型（base model）的 effort 目录。
var claudeContextWindowTagRe = regexp.MustCompile(`\[[1-9][0-9]*[km]\]$`)

// modelIDForCapabilityLookup 返回“运行时原生模型字符串”对应的目录身份。它绝不改动持久化在
// agent 上或传给 provider CLI 的值。Claude 的上下文窗口变体继承其基础模型的能力；其他所有
// provider 以及格式错误/未知的修饰符都保持“精确匹配”行为。
func modelIDForCapabilityLookup(providerType, model string) string {
	if providerType != "claude" {
		return model
	}
	return claudeContextWindowTagRe.ReplaceAllString(model, "")
}

// acceptedModelIDsForProvider 返回该 provider“静态目录里可接受的模型 ID”集合；第二个返回值
// 表示该 provider 是否在“已知兼容性精确判断”的名单里（目前只有 claude/codex 有维护中的静态
// 目录，其余 provider 一律 ok=false）。
func acceptedModelIDsForProvider(providerType string) (map[string]bool, bool) {
	switch {
	case providerType == "claude":
		return modelIDSet(claudeStaticModels()), true
	case providerType == "codex":
		return modelIDSet(codexStaticModels()), true
	default:
		return nil, false
	}
}

// modelIDSet 把模型列表压成一个以 ID 为键的集合，方便 O(1) 查成员。
func modelIDSet(models []Model) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, m := range models {
		out[m.ID] = true
	}
	return out
}

// isRuntimeSpecificModelID 判断一个模型 ID 是否明显属于“某个运行时专属 ID”而不是用户自定义
// 自由文本：带 `/`（provider 限定）、命中已知前缀（claude-/gpt-/gemini-…）或正好落在
// claude/codex 静态目录里都算。
func isRuntimeSpecificModelID(model string) bool {
	if strings.Contains(model, "/") {
		return true
	}
	return modelHasKnownPrefix(model) ||
		modelIDSet(claudeStaticModels())[model] ||
		modelIDSet(codexStaticModels())[model]
}

// modelHasKnownPrefix 判断 ID 是否命中已知的模型厂商前缀。命中即认为“眼熟”，用于区分用户随手
// 敲的自定义字符串与来自真实目录的 ID。
func modelHasKnownPrefix(model string) bool {
	return strings.HasPrefix(model, "claude-") ||
		strings.HasPrefix(model, "gpt-") ||
		strings.HasPrefix(model, "gemini-") ||
		strings.HasPrefix(model, "auto-gemini-") ||
		isOpenAIReasoningSeriesID(model)
}

// cachedDiscovery 调用 fn 并把结果缓存 modelCacheTTL 时长。调用方用 discoveryCacheKey 拼 key，
// 保证同一协议族下两台枚举不同可执行文件的运行时永远不会共享一条记忆条目。
func cachedDiscovery(key string, fn func() (Catalog, error)) (Catalog, error) {
	modelCacheMu.Lock()
	if entry, ok := modelCache[key]; ok && time.Now().Before(entry.expiresAt) {
		out := entry.models
		modelCacheMu.Unlock()
		return Catalog{Models: out}, nil
	}
	modelCacheMu.Unlock()

	catalog, err := fn()
	if err != nil {
		return Catalog{}, err
	}

	// 空结果不缓存。零模型几乎总是瞬时故障（发现 CLI 超时、未登录、网络抖动），而不是运行时
	// 真的没有模型；缓存了它，即使故障原因已消失，下拉框也会在整个 TTL 期间保持空白。跳过缓存
	// 让下一次请求能立刻重试。见 #3729。
	//
	// fallback 目录是同一情形披了件马甲：非空，但只是因为发现失败、provider 用静态列表顶上。
	// 缓存它会把替身钉住整个 TTL，并挡住下一次请求的重试——那正是我们想保留的恢复路径
	// （MUL-5549）。
	if len(catalog.Models) == 0 || catalog.Fallback {
		return catalog, nil
	}

	modelCacheMu.Lock()
	modelCache[key] = modelCacheEntry{models: catalog.Models, expiresAt: time.Now().Add(modelCacheTTL)}
	modelCacheMu.Unlock()
	return catalog, nil
}

// discoveryCacheKey 把一条发现记忆记录限定到“被枚举的那个可执行文件”。自定义运行时 profile
// （MUL-3284）在内置协议族下跑的是另一个可执行文件，所以如果只按 provider 拼 key，一台机器上
// 的内置运行时与同族 profile 运行时就会在 TTL 内互相投喂目录——这等于把 daemon 路径解析修好的
// 问题在缓存层重新引入（MUL-5789）。路径为空时保留裸 provider key——这正是 PATH 上的内置运行
// 时解析到的样子。
//
// key 还覆盖启动前缀而不只是路径：两个 profile 可能用不同 fixed_args 包同一个二进制——
// `ccms start q36` 和 `ccms start opus`——从中枚举出的目录确实不同。
func discoveryCacheKey(providerType string, runtimeCmd Command) string {
	if runtimeCmd.Path == "" && len(runtimeCmd.Prefix) == 0 {
		return providerType
	}
	return providerType + ":" + runtimeCmd.cacheKey()
}

// ── 静态目录 ──

// claudeStaticModels 反映 Claude Code CLI 实际接受的 --model 取值。保持这份清单又短又新；过期
// 条目误导用户远大于帮助。默认选 Sonnet，因为它是日常主力（Opus 留给 advisor 类流程）。
func claudeStaticModels() []Model {
	return []Model{
		{ID: "claude-sonnet-5", Label: "Claude Sonnet 5", Provider: "anthropic"},
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Default: true},
		{ID: "claude-fable-5", Label: "Claude Fable 5", Provider: "anthropic"},
		{ID: "claude-opus-5", Label: "Claude Opus 5", Provider: "anthropic"},
		{ID: "claude-opus-4-8", Label: "Claude Opus 4.8", Provider: "anthropic"},
		{ID: "claude-opus-4-7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Provider: "anthropic"},
		{ID: "claude-opus-4-6", Label: "Claude Opus 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5", Provider: "anthropic"},
	}
}

// codexStaticModels 是两处场景的兜底：Codex 版本旧于 0.122.0，或 `codex debug models
// --bundled` 调用失败/输出畸形。它要与“本机最新验证过的 bundled 目录里的可见条目”保持一致，
// 另外收进一些旧 Codex 版本仍常见的模型。每个条目自带 reasoning 目录，让旧/离线 CLI 也能维持
// 与动态发现一致的“模型 + thinking 选择器”契约。service tier（速度档位）刻意不在这里猜：它们
// 对运行时/版本/账号敏感，所以发现失败时应隐藏速度选择器、把覆盖请求按关闭处理。
func codexStaticModels() []Model {
	// 这里的 `Default` 不是用户可见的“默认模型”徽标——那个徽标已经不再渲染（模型未设置时
	// Multica 跟随 CLI 配置）。它只用来标记当前旗舰，供“默认必须跟随最新发布”的目录守卫测试
	// （TestCodexStaticModelsMatchVerifiedFallbackCatalog、multica#2009）使用。它也刻意不被
	// 用来校验“空模型（跟随 CLI 配置）”的 effort：那份配置可能解析到任何模型，所以
	// ValidateThinkingLevel 对空的 codex 模型按“关闭”处理，而不是借用这条目录（只有它会宣传
	// `ultra`）——见 ValidateThinkingLevel 与 MUL-4347。保证恰好只有一条目带这个标记。
	standardThinking := func(defaultLevel string, includeMax, includeUltra bool) *ModelThinking {
		levels := []ThinkingLevel{
			{Value: "low", Label: "Low", Description: "Fast responses with lighter reasoning"},
			{Value: "medium", Label: "Medium", Description: "Balances speed and reasoning depth for everyday tasks"},
			{Value: "high", Label: "High", Description: "Greater reasoning depth for complex problems"},
			{Value: "xhigh", Label: "Extra high", Description: "Extra high reasoning depth for complex problems"},
		}
		if includeMax {
			levels = append(levels, ThinkingLevel{Value: "max", Label: "Max", Description: "Maximum reasoning depth for the hardest problems"})
		}
		if includeUltra {
			levels = append(levels, ThinkingLevel{Value: "ultra", Label: "Ultra", Description: "Maximum reasoning with automatic task delegation"})
		}
		return &ModelThinking{DefaultLevel: defaultLevel, SupportedLevels: levels}
	}
	gpt52Thinking := func() *ModelThinking {
		return &ModelThinking{
			DefaultLevel: "medium",
			SupportedLevels: []ThinkingLevel{
				{Value: "low", Label: "Low", Description: "Balances speed with some reasoning; useful for straightforward queries and short explanations"},
				{Value: "medium", Label: "Medium", Description: "Provides a solid balance of reasoning depth and latency for general-purpose tasks"},
				{Value: "high", Label: "High", Description: "Maximizes reasoning depth for complex or ambiguous problems"},
				{Value: "xhigh", Label: "Extra high", Description: "Extra high reasoning for complex problems"},
			},
		}
	}
	return []Model{
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Provider: "openai", Default: true, Thinking: standardThinking("low", true, true)},
		{ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Provider: "openai", Thinking: standardThinking("medium", true, true)},
		{ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Provider: "openai", Thinking: standardThinking("medium", true, false)},
		{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.4", Label: "GPT-5.4", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.4-mini", Label: "GPT-5.4-Mini", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex", Provider: "openai", Thinking: standardThinking("medium", false, false)},
		{ID: "gpt-5.2", Label: "GPT-5.2", Provider: "openai", Thinking: gpt52Thinking()},
	}
}

// discoverTraecliModels 起一个一次性 `traecli acp serve --yolo` 进程，解析 traecli 在
// session/new 里返回的模型目录（形状与 Kiro/Qoder 相同）。官方 TRAE CLI 必须已登录目录才非空；
// 任何失败都由调用方退回手工录入框。
func discoverTraecliModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	return discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "traecli",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-traecli-discovery-",
		acpArgs:      []string{"acp", "serve", "--yolo"},
	})
}

// cursorStaticModels 是 `cursor-agent --list-models` 不可用（二进制缺失、离线等）时的最小兜底。
// 真实目录走动态抓取，因为 Cursor 的模型 ID 变得很快（例如 `composer-2-fast`、
// `claude-4.6-sonnet-medium`、`gemini-3.1-pro`），我们内置的任何静态清单都会迅速过时。
func cursorStaticModels() []Model {
	return []Model{
		{ID: "auto", Label: "Auto", Provider: "cursor", Default: true},
	}
}

// copilotStaticModels——GitHub Copilot CLI 不在 PATH 上或用户未登录时的兜底。正常运行走
// discoverCopilotModels()：对 CLI 讲 ACP，拿实时目录（包括用户账号真正能访问哪些 ID）。这份
// 列表只是安全网，保证实时查询失败时 UI 下拉框仍有合理选项。
//
// 来源：https://docs.github.com/en/copilot/reference/ai-models/supported-models
// ID 使用 `copilot --model <id>` 真正接受的加点形式。
func copilotStaticModels() []Model {
	return []Model{
		// OpenAI
		{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai"},
		{ID: "gpt-5.4", Label: "GPT-5.4", Provider: "openai"},
		{ID: "gpt-5.4-mini", Label: "GPT-5.4 mini", Provider: "openai"},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex", Provider: "openai"},
		{ID: "gpt-5.2-codex", Label: "GPT-5.2-Codex", Provider: "openai"},
		{ID: "gpt-5.2", Label: "GPT-5.2", Provider: "openai"},
		{ID: "gpt-5-mini", Label: "GPT-5 mini", Provider: "openai"},
		{ID: "gpt-4.1", Label: "GPT-4.1", Provider: "openai"},
		// Anthropic
		{ID: "claude-opus-4.7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-sonnet-4.6", Label: "Claude Sonnet 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4.5", Label: "Claude Sonnet 4.5", Provider: "anthropic"},
		{ID: "claude-haiku-4.5", Label: "Claude Haiku 4.5", Provider: "anthropic"},
	}
}

// inferCopilotProvider 给 Copilot 模型 ID 打上厂商名，方便 UI 分组。Copilot CLI 的 ACP
// `availableModels` 载荷只暴露 `modelId`/`name`，厂商隐含在前缀里。返回 "" 则条目不分组——
// 与其他 ACP 发现路径（hermes/kimi）对无前缀 ID 的做法一致。
//
// OpenAI 推理系列（`o1`、`o3`、`o3-mini`、`o4-mini`、未来的 `o5`/`o6`/…）用通用的
// `o<数字>…` 模式匹配，这样就不用每次追新发布的代际。
func inferCopilotProvider(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "gpt-") || isOpenAIReasoningSeriesID(modelID):
		return "openai"
	case strings.HasPrefix(modelID, "claude-"):
		return "anthropic"
	case strings.HasPrefix(modelID, "gemini-"):
		return "google"
	case strings.HasPrefix(modelID, "grok-"):
		return "xai"
	default:
		return ""
	}
}

// isOpenAIReasoningSeriesID 匹配 OpenAI `o` 前缀推理家族：小写 `o` 后跟至少一位数字，之后要么
// 到字符串结尾、要么是 `-` 分隔符（例如 `o3`、`o3-mini`、`o4-mini-high`）。它避免了像
// `opus-…` 或恰好以 `o` 开头的随机 ID 这样的误报。
func isOpenAIReasoningSeriesID(id string) bool {
	if len(id) < 2 || id[0] != 'o' {
		return false
	}
	i := 1
	for i < len(id) && id[i] >= '0' && id[i] <= '9' {
		i++
	}
	if i == 1 {
		return false
	}
	return i == len(id) || id[i] == '-'
}

// ── 动态发现 ──

// discoverOpenCodeModels 运行 `opencode models --verbose` 并解析其输出。CLI 先打印
// `provider/model` 行，verbose 模式下每行后跟一段 JSON 元数据；ID 我们原样透出，保证用户看到
// 的与 `--model` 实际接受的完全一致。模型 JSON 里的 `variants` 会映射进 thinking 档位选择器，
// 因为 OpenCode 的 `run --variant` flag 就是它“按 provider 暴露 reasoning effort”的界面。
// 任何失败（CLI 缺失、解析错误、超时）都退回空列表，保证创建 UI 仍可用。
func discoverOpenCodeModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "opencode"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}
	// 新版 opencode（1.15+）执行 `opencode models` 时会在网络上同步它托管的免费模型目录，可能
	// 要 ~6s；此前 5s 的上限会超时并返回空列表，于是运行时显示在线、模型下拉框却是空的。见
	// multica-ai/multica#3627。
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models", "--verbose")
	hideAgentWindow(cmd)
	// 即使退出码非零也照常解析 verbose 输出——一条过期的配置项可能让 `opencode models` 非零
	// 退出、却仍列出可解析的目录（与 pi 路径一致；见 #3729/#3627）。
	out, _ := outputOwned(cmd, runtimeCmd.logger)
	models := parseOpenCodeModels(string(out))
	if len(models) == 0 {
		// verbose 没给出可用内容（flag 不支持、错误文本或空列表）。退回裸命令再试一次——
		// 它省掉逐模型的 JSON，但仍会打印 ID。
		cmd = runtimeCmd.exec(runCtx, "models")
		hideAgentWindow(cmd)
		out, _ = outputOwned(cmd, runtimeCmd.logger)
		models = parseOpenCodeModels(string(out))
	}
	if len(models) == 0 {
		return []Model{}, nil
	}
	return models, nil
}

// parseOpenCodeModels 接受 `opencode models` 的文本输出并抽取 ID。非 verbose 输出是每行一个
// `provider/model`。verbose 输出在每个 ID 后追加一段美化打印的 JSON；当该 JSON 含 `variants`
// 时，每个启用的 variant 会变成一个 thinking 档位，后端稍后用 `opencode run --variant` 传递。
func parseOpenCodeModels(output string) []Model {
	lines := strings.Split(output, "\n")
	var models []Model
	indexByID := map[string]int{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		id := parseOpenCodeModelIDLine(line)
		if id == "" {
			continue
		}
		idx, seen := indexByID[id]
		if !seen {
			provider := ""
			if slash := strings.Index(id, "/"); slash > 0 {
				provider = id[:slash]
			}
			idx = len(models)
			indexByID[id] = idx
			models = append(models, Model{ID: id, Label: id, Provider: provider})
		}

		next := i + 1
		for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[next]), "{") {
			continue
		}
		raw, resumeAt := collectOpenCodeModelJSON(lines, next)
		if json.Valid(raw) {
			annotateOpenCodeModelMetadata(&models[idx], raw)
		}
		i = resumeAt - 1
	}
	return models
}

// parseOpenCodeModelIDLine 从一行输出里抽出候选模型 ID：取第一个空白分隔的字段，要求它形如
// `provider/model` 且不是 JSON 片段、不是全大写的表头行（如 PROVIDER/MODEL）。
func parseOpenCodeModelIDLine(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	id := fields[0]
	if strings.HasPrefix(id, `"`) || strings.HasPrefix(id, "{") || strings.HasPrefix(id, "[") {
		return ""
	}
	if !strings.Contains(id, "/") {
		return ""
	}
	// Skip header rows such as PROVIDER/MODEL.
	if id == strings.ToUpper(id) {
		return ""
	}
	return id
}

// collectOpenCodeModelJSON 从第 start 行开始把多行 JSON（美化打印的对象）攒成一个 []byte；
// 一旦拼出的内容构成合法 JSON 或遇到下一个模型行就停。返回 (JSON 字节, 消费到的下一行下标)。
func collectOpenCodeModelJSON(lines []string, start int) ([]byte, int) {
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if i > start && parseOpenCodeModelIDLine(line) != "" {
			return []byte(b.String()), i
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
		if json.Valid([]byte(b.String())) {
			return []byte(b.String()), i + 1
		}
	}
	return []byte(b.String()), len(lines)
}

// opencodeModelMetadata 对应 verbose 输出里挂在每个模型名后面的 JSON 元数据：它是否支持
// reasoning，以及有哪些 reasoning variant（每个 variant 是一个 effort 档位）。
type opencodeModelMetadata struct {
	Reasoning bool                            `json:"reasoning"`
	Variants  map[string]opencodeModelVariant `json:"variants"`
}

// opencodeModelVariant 描述 opencode 模型的一个 reasoning variant：disabled 表示该档位被上游
// 关掉；ReasoningEffort 是新版 CLI 上报档位强度的字段；Thinking 保留原样，暂未使用。
type opencodeModelVariant struct {
	Disabled        bool            `json:"disabled"`
	ReasoningEffort string          `json:"reasoningEffort"`
	Thinking        json.RawMessage `json:"thinking"`
}

// opencodeVariantLabel 把 opencode 上报的 effort token 映射成 UI 展示文案，与 opencode 自己
// 界面里的措辞保持一致。
var opencodeVariantLabel = map[string]string{
	"none":    "None",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

// opencodeVariantOrder 给出各档位的排序权重，让档位列表按强度升序展示而不是按 map 随机序。
var opencodeVariantOrder = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// annotateOpenCodeModelMetadata 把 opencode verbose 元数据里能用的部分写回 Model.Thinking。
// 只有当模型明确支持 reasoning（或 variants 里有像 reasoning 的档位）才标注，避免给不支持的
// 模型画出无用的选择器。
func annotateOpenCodeModelMetadata(model *Model, raw []byte) {
	var meta opencodeModelMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return
	}
	if !meta.Reasoning && !openCodeVariantsLookReasoning(meta.Variants) {
		return
	}
	levels := openCodeThinkingLevelsFromVariants(meta.Variants)
	if len(levels) == 0 {
		return
	}
	model.Thinking = &ModelThinking{SupportedLevels: levels}
}

// openCodeVariantsLookReasoning 粗略判断 variants 集合是否表现出“与 reasoning 相关”：档位名
// 命中已知表、或任一 variant 带 reasoning 字段都算。用于过滤掉那些名字碰巧叫 variant 但并非
// effort 档位的情况。
func openCodeVariantsLookReasoning(variants map[string]opencodeModelVariant) bool {
	for name, variant := range variants {
		if _, known := opencodeVariantOrder[name]; known {
			return true
		}
		if variant.ReasoningEffort != "" || len(variant.Thinking) > 0 {
			return true
		}
	}
	return false
}

// openCodeThinkingLevelsFromVariants 把启用的 variant 排成 ThinkingLevel 列表：去掉空名与被
// disabled 的档位，按强度升序（opencodeVariantOrder），未知档位名放在已知档位之后再按字典序。
func openCodeThinkingLevelsFromVariants(variants map[string]opencodeModelVariant) []ThinkingLevel {
	if len(variants) == 0 {
		return nil
	}
	values := make([]string, 0, len(variants))
	for value, variant := range variants {
		if value == "" || variant.Disabled {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		left, leftKnown := opencodeVariantOrder[values[i]]
		right, rightKnown := opencodeVariantOrder[values[j]]
		if leftKnown && rightKnown {
			return left < right
		}
		if leftKnown != rightKnown {
			return leftKnown
		}
		return values[i] < values[j]
	})
	levels := make([]ThinkingLevel, 0, len(values))
	for _, value := range values {
		label, ok := opencodeVariantLabel[value]
		if !ok {
			label = strings.Title(strings.ReplaceAll(value, "-", " ")) //nolint:staticcheck
		}
		levels = append(levels, ThinkingLevel{Value: value, Label: label})
	}
	return levels
}

// piThinkingLevelLabels 把 Pi 上报的 thinking token 映射成 UI 文案。
var piThinkingLevelLabels = map[string]string{
	"off":     "Off",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

// piThinkingLevelOrder 是 Pi 支持档位的官方顺序，保证列表按强度升序展示。
var piThinkingLevelOrder = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Pi 发现预算要控在既有的 15 秒窗口内，同时给“RPC 挂起”情况下的兼容性兜底留出真正机会。
const (
	piRPCDiscoveryTimeout   = 7 * time.Second
	piTableDiscoveryTimeout = 8 * time.Second
)

// piRPCModel 是 Pi RPC get_available_models 返回的单个模型记录；ThinkingLevelMap 把档位名
// 映射到 *string（值为 null 表示该档位对该模型显式禁用）。
type piRPCModel struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Provider         string             `json:"provider"`
	Reasoning        bool               `json:"reasoning"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
}

// piRPCState 是 Pi RPC get_state 返回的当前会话状态；Multica 用它识别“不传 --model 时 Pi 会
// 选哪个模型、该模型的 thinking 档位默认值是什么”。
type piRPCState struct {
	Model         *piRPCModel `json:"model"`
	ThinkingLevel string      `json:"thinkingLevel"`
}

// piRPCResponse 是 Pi RPC 对单条请求的响应外壳：Data 里才是各方法真正的载荷。
type piRPCResponse struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// discoverPiModels 通过 Pi 的 RPC API 拿它机器可读的模型目录。RPC 模型对象携带逐模型精确的
// reasoning 元数据，而人类可读的 `--list-models` 表格只给一列 yes/no。旧版 Pi 与 pi 家族 fork
// 可能没实现 RPC，所以发现会退回旧的表格解析器，且不虚报猜出来的 thinking 目录。
func discoverPiModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	return discoverPiModelsWithin(ctx, runtimeCmd, piRPCDiscoveryTimeout, piTableDiscoveryTimeout)
}

// discoverPiModelsWithin 把 15 秒发现预算拆成两段：先试 RPC，再退到表格。两个超时参数化是
// 为了测试能注入不同取值。
func discoverPiModelsWithin(ctx context.Context, runtimeCmd Command, rpcTimeout, tableTimeout time.Duration) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "pi"
	}
	lookedUp, err := exec.LookPath(runtimeCmd.Path)
	if err != nil {
		return []Model{}, nil
	}
	// 把 15 秒发现预算拆开：即使 RPC 接口“接受模式但永不回话”，也不会饿死后面的表格兜底。
	// 两段上下文都继承调用方的取消信号。
	rpcCtx, rpcCancel := context.WithTimeout(ctx, rpcTimeout)
	models, ok := discoverPiModelsRPC(rpcCtx, runtimeCmd, lookedUp)
	rpcCancel()
	if ok {
		return models, nil
	}

	tableCtx, tableCancel := context.WithTimeout(ctx, tableTimeout)
	defer tableCancel()
	return discoverPiModelsTable(tableCtx, runtimeCmd)
}

// discoverPiModelsRPC 起一个短命 Pi RPC 会话，同时请求可用模型与当前状态。状态能指出“Multica
// 不传 --model 时 Pi 会选哪个模型”；它的 thinking 档位就是该运行时对该模型的生效默认值。
func discoverPiModelsRPC(ctx context.Context, runtimeCmd Command, lookedUp string) ([]Model, bool) {
	args := []string{
		"--mode", "rpc",
		"--no-session",
		// 扩展保持启用，因为 Pi 扩展可以注册 provider 和模型；关掉会让 RPC 发现与真实任务执行
		// 所用的目录不一致。
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
	}
	cmd, _, _ := runtimeCmd.execVia(ctx, choosePiInvocation, lookedUp, args, slog.Default())
	hideAgentWindow(cmd)
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, false
	}
	if err := startOwnedProcessTree(cmd, runtimeCmd.logger); err != nil {
		_ = stdin.Close()
		return nil, false
	}
	defer releaseProcessGroup(cmd)

	encoder := json.NewEncoder(stdin)
	requests := []map[string]string{
		{"id": "multica-state", "type": "get_state"},
		{"id": "multica-models", "type": "get_available_models"},
	}
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return nil, false
		}
	}

	var (
		rawModels  []piRPCModel
		state      piRPCState
		modelsDone bool
		stateDone  bool
	)
	scanner := newAgentStreamScanner(stdout)
	for scanner.Scan() {
		var response piRPCResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.Type != "response" {
			continue
		}
		switch {
		case response.ID == "multica-state" || response.Command == "get_state":
			stateDone = true
			if response.Success {
				_ = json.Unmarshal(response.Data, &state)
			}
		case response.ID == "multica-models" || response.Command == "get_available_models":
			modelsDone = true
			if response.Success {
				var payload struct {
					Models []piRPCModel `json:"models"`
				}
				if err := json.Unmarshal(response.Data, &payload); err == nil {
					rawModels = payload.Models
				}
			}
		}
		if modelsDone && stateDone {
			break
		}
	}

	// Pi 的 RPC 循环在 stdin 仍开着时会继续等更多命令。EOF 能干净地结束这次一次性会话；context
	// 仍是安全网——用于对付那些忽略 EOF 或始终不完成两条响应的旧版/fork CLI。
	_ = stdin.Close()
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return nil, false
	}
	if !modelsDone || len(rawModels) == 0 {
		return nil, false
	}
	return piModelsFromRPC(rawModels, state), true
}

// piModelsFromRPC 把 RPC 返回的原始模型记录组装成 Model 列表。Pi 的模型 ID 用
// `provider/id` 形式；当前会话模型（state.Model）被打成 Default=true，且如果它的 thinking
// 档位集合确实支持会话当前档位，就把该档位作为默认档位写回去。
func piModelsFromRPC(rawModels []piRPCModel, state piRPCState) []Model {
	defaultID := ""
	if state.Model != nil && state.Model.Provider != "" && state.Model.ID != "" {
		defaultID = state.Model.Provider + "/" + state.Model.ID
	}

	models := make([]Model, 0, len(rawModels))
	seen := make(map[string]bool, len(rawModels))
	for _, raw := range rawModels {
		provider := strings.TrimSpace(raw.Provider)
		modelID := strings.TrimSpace(raw.ID)
		if provider == "" || modelID == "" {
			continue
		}
		id := provider + "/" + modelID
		if seen[id] {
			continue
		}
		seen[id] = true

		model := Model{
			ID:       id,
			Label:    id,
			Provider: provider,
			Default:  id == defaultID,
			Thinking: piThinkingFromRPCModel(raw),
		}
		if model.Default && model.Thinking != nil && piThinkingSupports(model.Thinking, state.ThinkingLevel) {
			model.Thinking.DefaultLevel = state.ThinkingLevel
		}
		models = append(models, model)
	}
	return models
}

// piThinkingFromRPCModel 复刻 Pi 的 getSupportedThinkingLevels(model) 对支持 reasoning 的
// 模型的行为：off..high 默认可用，除非被显式映射成 null；xhigh/max 必须有非空显式映射才出现。
// Pi 对 reasoning=false 只报 "off"，Multica 刻意不展示那个无意义的开关。
func piThinkingFromRPCModel(model piRPCModel) *ModelThinking {
	if !model.Reasoning {
		return nil
	}
	levels := make([]ThinkingLevel, 0, len(piThinkingLevelOrder))
	for _, value := range piThinkingLevelOrder {
		mapped, present := model.ThinkingLevelMap[value]
		if present && mapped == nil {
			continue
		}
		if (value == "xhigh" || value == "max") && !present {
			continue
		}
		levels = append(levels, ThinkingLevel{Value: value, Label: piThinkingLevelLabels[value]})
	}
	if len(levels) == 0 {
		return nil
	}
	return &ModelThinking{SupportedLevels: levels}
}

// piThinkingSupports 判断给定 thinking 目录里是否包含 value 这个档位。
func piThinkingSupports(thinking *ModelThinking, value string) bool {
	if thinking == nil || value == "" {
		return false
	}
	for _, level := range thinking.SupportedLevels {
		if level.Value == value {
			return true
		}
	}
	return false
}

// discoverPiModelsTable 跑旧的人类可读目录命令。旧版 pi 把列表打到 stderr，新版本用 stdout；
// 这里两个都抓，解析非空的那边。该兜底刻意把 Thinking 留空，因为表格只暴露一个 yes/no 能力位。
func discoverPiModelsTable(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	// 新版 pi 会通过网络从每个已配置 provider 抓目录，所以发现耗时随 provider 数量增长——多
	// provider 配置实测 ~4.6–4.8s，正好卡在旧 5s 上限。抖动一旦推过线，daemon 会在 CLI 打印任何
	// 内容前就把它杀掉，模型下拉框空着而运行时却在线。15s 与 opencode 发现上限一致（见 #3729，
	// 与 #3627 同类）。
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "--list-models")
	hideAgentWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil && len(stdout) == 0 && stderr.Len() == 0 {
		return []Model{}, nil
	}
	text := string(stdout)
	if strings.TrimSpace(text) == "" {
		text = stderr.String()
	}
	return parsePiModels(text), nil
}

// parsePiModels 接受 `pi --list-models` 的输出。Pi 历史上每行输出 `provider:model`，现在改
// 成多列表格（`provider  model  context …`）；两种形状都被归一化成 `provider/model`，与
// opencode/UI 的惯例对齐。第 0 列是大小写不敏感的 `provider` 时视为表头行并跳过。
func parsePiModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// agent 配置引用了过期 pattern 时，pi 会把人类可读的诊断信息与目录行混打，例如
		//   Warning: No models match pattern "opencode-go/mimo-v2-omni"
		// 要先跳过它们再做字段切分；否则散文句子会被切出 `No/models`、`Warning/` 这种假模型。
		// 见 #3729。
		if isPiDiscoveryNoise(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		first := fields[0]
		if strings.EqualFold(first, "provider") {
			continue
		}
		var id string
		if strings.ContainsAny(first, ":/") {
			// 旧 `provider:model` 格式——把冒号归一化成斜杠。只在这个分支里改，免得把表格格式
			// 第 1 列里恰好含 `:` 的模型名也悄悄改掉。
			id = strings.Replace(first, ":", "/", 1)
		} else if len(fields) >= 2 {
			id = first + "/" + fields[1]
		} else {
			continue
		}
		// 真正的 ID 在斜杠两侧各有非空的 provider 和 model。丢掉不满足的（例如游离的
		// `something:` token）——这是在上述诊断过滤器之外再加的一层廉价结构兜底。
		if slash := strings.Index(id, "/"); slash <= 0 || slash == len(id)-1 {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		provider := ""
		if i := strings.Index(id, "/"); i > 0 {
			provider = id[:i]
		}
		models = append(models, Model{ID: id, Label: id, Provider: provider})
	}
	return models
}

// isPiDiscoveryNoise 判断 `pi --list-models` 的一行是诊断消息而不是目录行。当 agent 配置引用
// 过期的 provider/model pattern 时，pi 会把这些消息打印在表格旁边，例如：
//
//	Warning: No models match pattern "opencode-go/mimo-v2-omni"
//
// `Warning:` 前缀并非所有版本都保证存在，所以“没有 pattern 命中”这条消息本身也要单独匹配。
// 这些是散文，不是 `provider model` 行；不跳过的话字段切分会造出 `No/models` 这类假模型。见
// #3729。
//
// pi 家族的自定义运行时 profile（MUL-3284）可能指向一个压根没有 `--list-models` 的 fork。
// 它们非零退出并打印用法文本——
//
//	Error: unknown flag: --list-models
//	Run `omp --help` for available flags.
//
// ——第二行没有任何诊断前缀，过去会被切成一个 `Run/`omp` 假模型。用法提示按其自身特征匹配
// （反引号引用的命令、`--help`、`usage:`、unknown flag/command），让选择器空手而归、UI 退回
// 手工录入，而不是给出垃圾 ID。这里刻意保持狭窄：目录行是 `provider/model` 或
// `provider model …` 的 token，都不会携带这些特征。“非零退出也照常解析”的 #3729 行为不受
// 影响——旧版 pi 确实会非零退出、同时把目录打到 stderr。见 #4482。
func isPiDiscoveryNoise(line string) bool {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "no models match pattern") {
		return true
	}
	if strings.HasPrefix(lower, "warning:") ||
		strings.HasPrefix(lower, "error:") ||
		strings.HasPrefix(lower, "info:") {
		return true
	}
	return strings.Contains(line, "`") ||
		strings.Contains(lower, "--help") ||
		strings.Contains(lower, "usage:") ||
		strings.Contains(lower, "unknown flag") ||
		strings.Contains(lower, "unknown command")
}

// discoverOmpModels 运行 `omp models --json` 并解析 JSON 目录。omp（oh-my-pi）拒绝
// `--list-models`——那是它从未采纳的 pi flag，遇到会非零退出——所以它的原生发现命令是
// `omp models --json`，打印一个 `{"models":[...]}` 对象；条目形状见 parseOmpModels。
// 空目录（未配置任何 provider 凭据的 omp 会打印 `{"models":[]}`）或非零退出（二进制缺失、
// omp 太旧）都退回空列表，让 UI 退化成手工录入而不是报错。
func discoverOmpModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "omp"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models", "--json")
	hideAgentWindow(cmd)
	stdout, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil || len(stdout) == 0 {
		return []Model{}, nil
	}
	return parseOmpModels(stdout)
}

// parseOmpModels 解析 `omp models --json` 的 JSON 输出。omp 输出的是带 `models` 数组的对象
// 包裹——不是裸的顶层数组：
//
//	{"models":[{"provider":"anthropic","id":"claude-sonnet-5","selector":"anthropic/claude-sonnet-5","name":"Claude Sonnet 5",...}]}
//
// 可持久化的 Model.ID 取 selector（provider/id），与 parsePiModels 的惯例一致：buildPiArgs 会把
// Model.ID 整个交给 --model。若用裸 id，omp 内部的 provider 优先级排序可能为同一个模型 id 选到
// 另一个后端。Provider 留给 UI 分组。去重键优先用 selector，缺省再退回 provider/id。
func parseOmpModels(data []byte) ([]Model, error) {
	var wrapper struct {
		Models []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Selector string `json:"selector"`
			Name     string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return []Model{}, nil
	}
	var models []Model
	seen := map[string]bool{}
	for _, e := range wrapper.Models {
		bareID := strings.TrimSpace(e.ID)
		if bareID == "" {
			continue
		}
		provider := strings.TrimSpace(e.Provider)
		// Model.ID 取限定后的 selector（provider/id），这样 buildPiArgs 交给 omp 的是一个它能
		// 无歧义解析的 selector。selector 缺省时退回 provider/id；provider 也缺省时只能用裸 id。
		selector := strings.TrimSpace(e.Selector)
		if selector == "" {
			if provider != "" {
				selector = provider + "/" + bareID
			} else {
				selector = bareID
			}
		}
		if seen[selector] {
			continue
		}
		seen[selector] = true
		label := strings.TrimSpace(e.Name)
		if label == "" {
			label = selector
		}
		models = append(models, Model{ID: selector, Label: label, Provider: provider})
	}
	return models, nil
}

// discoverHermesModels 起一个一次性 `hermes acp` 进程，只驱动协议中“收 session/new 响应里
// 宣传的模型列表”所需的部分，然后关掉它。列表和 `current` 标记都来自 hermes 自己的
// `_build_model_state`，所以 ~/.hermes/config.yaml 在运行时解析出什么，UI 就显示什么。
//
// 失败会原样向上抛。Hermes 没有可退的静态目录：其他老牌 provider 那种“失败返回空列表”的写法
// 在这里会变成“一次成功的发现、结果是啥也没有”——选择器会把空下拉框渲染成权威结果，既无错误
// 也无提示，而这正是 packages/core/runtimes/models.ts 明确拒绝产出的唯一结局（MUL-6606）。
// 改抛错误则让选择器进入“发现失败”状态，仍然提供可创建的手工录入框，并带上 Hermes 给出的
// “为何没发现任何东西”的原因。
//
// 对照 grok（strictErrors + 静态兜底）和 codebuddy：它们用内置清单顶上，只 slog.Debug 记原因，
// 因为在那边“有替身目录”好过没有。这里根本没有替身。
func discoverHermesModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "hermes",
		clientName:   "multica-model-discovery",
		extraEnv:     []string{"HERMES_YOLO_MODE=1"},
		tmpdirPrefix: "multica-hermes-discovery-",
		strictErrors: true,
		timeout:      hermesDiscoveryTimeout,
		// 同一次握手在 jcode 上会带 effort selector，在 Hermes Agent 上则不带，所以 annotate
		// 正好用来区分二者——Hermes Agent 的模型返回 nil Thinking、不显示选择器。只有会话的
		// 当前模型会被标注；见 annotateACPThinkingForSessionModel。
		annotate: annotateACPThinkingForSessionModel,
	})
	if err != nil {
		return nil, annotateHermesDiscoveryUnconfigured(err)
	}
	return models, nil
}

// hermesDiscoveryUnconfiguredHint 解释由 MODEL DISCOVERY 抛出的“未配置任何 LLM provider”失败
// ——它和任务场景抛同样文案的故事完全不同。
//
// Hermes 自己的解决办法（“运行 `hermes model`”）假设用户站在自己的 shell 里。但发现并不在
// shell 里跑：它是 daemon 的 `hermes acp` 子进程，而 daemon 常常由 GUI 拉起，此时它的环境从未
// 见过用户 shell 的 rc 文件。agents_probe.go 的 login-shell 兜底也补不上这个缺口——它只解析
// 了二进制的 PATH、别的什么都不管——所以凭据放在某个导出变量或 gcloud/ADC 状态（GH: Vertex
// AI）里的 provider，在用户侧能解析、在 daemon 侧却不行。
//
// 刻意不用任务路径的提示（daemon 里的 annotateHermesProviderUnconfigured）：那条讲的是按任务
// 隔离的 HERMES_HOME overlay，而发现不构造任何 overlay。在这里叫用户去 agent 的 custom_env 里
// 设 HERMES_HOME 反而有害——发现是按运行时来的、从不读任何 agent 的 custom_env，所以那个改动
// 无法让这个下拉框多出任何一个模型。
//
// 固定文案、无插值，理由与任务提示相同：这是错误文案，任何用户可控的内容都不该进到别的代码
// 可能拿去匹配的字符串里。
const hermesDiscoveryUnconfiguredHint = " [multica] this is what hermes reported to the daemon, " +
	"which runs `hermes acp` with its OWN environment — not your login shell. " +
	"Credentials exported only from a shell rc file are invisible to it. " +
	"Reproduce with `env -i HOME=\"$HOME\" PATH=\"$PATH\" hermes model`: if that fails while a plain " +
	"`hermes model` succeeds, restart the daemon from a shell that already has those variables. " +
	"Setting HERMES_HOME or custom_env on an agent will not populate this picker — " +
	"model discovery is per-runtime and reads neither."

// annotateHermesDiscoveryUnconfigured 仅当“发现失败的原因是 hermes 一个 provider 都解析不到”
// 时，把上面那段提示接到错误后面。其余失败（二进制缺失、握手超时、凭据被拒）原样放行——那种
// 场景讲环境故事会误导人，而且“凭据被拒”恰恰说明配置是被找到的。
func annotateHermesDiscoveryUnconfigured(err error) error {
	if err == nil || !taskfailure.ProviderUnconfigured(err.Error()) {
		return err
	}
	return fmt.Errorf("%w%s", err, hermesDiscoveryUnconfiguredHint)
}

// discoverKimiModels 把 Kimi 的 ACP 模型目录与 `kimi provider list --json` 的结构化逐模型
// effort 数据合并起来。provider 目录是逐模型 supportEfforts/defaultEffort 唯一权威来源，因为
// session/new 的响应永远只描述“创建该会话用的那一个模型”。
//
// 从会话里读 effort 目录行不通，尽管 Kimi（0.33.0）在 session/set_model 后确实会刷新 thinking
// 选项。刷新的选项以 `session/update` 通知到达（`sessionUpdate: "config_option_update"`），而本
// 文件共用的 requestACP 助手按 id 匹配响应、丢弃通知。就算消费那条通知也不够：Kimi 切到
// low/high/max 模型后发的选项列表仍会带上一款模型的 currentValue 作为尾项（`[low, high, max,
// on]`），拿它当目录会渲染出一个幽灵档位。supportEfforts 没有这种残留。
//
// ACP 侧不变：Kimi ≤0.28 返回 `models` 块（`availableModels`/`currentModelId`），0.29 把同一
// 目录挪进了 `configOptions`（MUL-5239）；共享解析器两种都认，所以发现路径保持不变。见
// parseACPConfigOptionModels。
//
// effort 选择还要看 CLI 版本，因为光凭 provider-list 无法判断运行时能不能真正执行它宣传的
// 档位。用真实二进制验证过：0.28.1 对 K3 报的 supportEfforts [low high max] 和 0.33.0 完全
// 一样，但它的 ACP 只实现 on/off 开关——set_config_option("max") 返回成功、实际却停留在
// "on"。用会话的 `thinking` 配置 id 做门控也无法区分二者——0.28.1 也宣传那个 id。版本号是
// 唯一诚实的信号，而 initialize 响应本来就带着它，因此不用额外起进程。
//
// 再往上，支持与否只按模型判定，别的不看：provider-list 没列出的模型、或列了但没有 efforts 的
// 模型，其 Thinking 保持 nil，只对该模型隐藏控件。
//
// 失败模式（kimi 缺失、未登录、配置错误）返回空列表，让 UI 退回手工录入。
func discoverKimiModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	var acpVersion string
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "kimi",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-kimi-discovery-",
		inspectInit: func(initResult json.RawMessage) {
			acpVersion = acpAgentInfoVersion(initResult)
		},
	})
	if err != nil || len(models) == 0 {
		return models, err
	}
	if !kimiSupportsThinkingEfforts(acpVersion) {
		// 不是错误、也不值得 Warn：旧版 CLI 照样能列模型、跑任务，只是没法告诉它用哪档 effort。
		// 模型发现每次缓存未命中都会重跑，所以在这里 Warn 会永远重复下去。
		slog.Debug("kimi CLI predates ACP effort selection; hiding thinking controls",
			"detected_version", acpVersion,
			"required_version", kimiMinThinkingEffortVersion,
		)
		return models, nil
	}

	perModel, err := discoverKimiProviderThinking(ctx, runtimeCmd)
	if err != nil {
		// 不要把 err 或命令输出带进来：provider JSON 可能含凭据。用固定原因解释为什么隐藏
		// thinking 控件，不泄漏主机或账号数据。用 Debug 而非 Warn：模型发现每次缓存未命中都会
		// 重跑，旧版 CLI 会因此没完没了地记日志。
		slog.Debug("kimi per-model thinking discovery unavailable; hiding thinking controls",
			"reason", "provider_list_unavailable",
		)
		return models, nil
	}
	for i := range models {
		if thinking, ok := perModel[models[i].ID]; ok {
			models[i].Thinking = thinking
		}
	}
	return models, nil
}

// kimiMinThinkingEffortVersion 是第一个让 ACP 真正应用 effort 档位（而非简单 on/off 开关）的
// Kimi Code CLI 版本（上游 0.29.0，2026-07-22 发布）。低于它时，`session/set_config_option`
// 会无报错地接受 "low"/"high"/"max"，却把会话留在 "on"——宣传这些档位等于承诺运行时根本
// 做不到的事。
const kimiMinThinkingEffortVersion = "0.29.0"

// kimiSupportsThinkingEfforts 报告 ACP 上报的 Kimi 版本能否真正执行 effort 档位。空或无法解析
// 的版本一律回答 no：认不出身份的构建（fork、包装器、未来格式）不值得我们为它承诺 effort
// 选择——隐藏选择器最多让那个构建少一个它可能本来就不遵守的控件。无论哪种情况任务照常能跑。
func kimiSupportsThinkingEfforts(version string) bool {
	detected, err := parseSemver(strings.TrimSpace(version))
	if err != nil {
		return false
	}
	minimum, err := parseSemver(kimiMinThinkingEffortVersion)
	if err != nil {
		return false
	}
	return !detected.lessThan(minimum)
}

// acpAgentInfoVersion 从 ACP initialize 结果里取 `agentInfo.version`。ACP agent 在这里上报自己
// 的构建号（Kimi 发 {"name":"Kimi Code CLI","version":"0.33.0"}）；漏报的 agent 得到 ""，所有
// 调用方都必须把 "" 当“未知”处理，绝不能当“够新”。
func acpAgentInfoVersion(raw json.RawMessage) string {
	var response struct {
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
		AgentInfoSnake struct {
			Version string `json:"version"`
		} `json:"agent_info"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ""
	}
	if version := strings.TrimSpace(response.AgentInfo.Version); version != "" {
		return version
	}
	return strings.TrimSpace(response.AgentInfoSnake.Version)
}

// discoverKimiProviderThinking 跑 `kimi provider list --json`，取每个模型的 effort 档位目录。
// 这是独立于 ACP 握手的一次子进程调用，失败时由调用方决定如何处理。
func discoverKimiProviderThinking(ctx context.Context, runtimeCmd Command) (map[string]*ModelThinking, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "kimi"
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := runtimeCmd.exec(runCtx, "provider", "list", "--json")
	hideAgentWindow(cmd)
	cmd.Stderr = io.Discard
	raw, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil {
		return nil, fmt.Errorf("kimi provider list: %w", err)
	}
	return parseKimiProviderThinking(raw)
}

// kimiProviderModel 是 `kimi provider list --json` 里单个模型记录的 effort 部分。JSON 字段
// 同时兼容驼峰与下划线两种拼写（不同版本 CLI 输出不同）。
type kimiProviderModel struct {
	SupportEfforts      []string `json:"supportEfforts"`
	SupportEffortsSnake []string `json:"support_efforts"`
	DefaultEffort       string   `json:"defaultEffort"`
	DefaultEffortSnake  string   `json:"default_effort"`
}

// parseKimiProviderThinking 解析 provider 目录：把每款模型的支持档位转成 ThinkingLevel 列表，
// 只保留合法的 effort 值并去重；默认档位必须是列表成员，否则留空（表示“CLI 自己选”）。
func parseKimiProviderThinking(raw []byte) (map[string]*ModelThinking, error) {
	var response struct {
		Models map[string]kimiProviderModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("parse kimi provider catalog: %w", err)
	}
	if len(response.Models) == 0 {
		return nil, fmt.Errorf("kimi provider catalog contained no models")
	}

	result := make(map[string]*ModelThinking, len(response.Models))
	for modelID, model := range response.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		efforts := model.SupportEfforts
		if len(efforts) == 0 {
			efforts = model.SupportEffortsSnake
		}
		seen := make(map[string]bool, len(efforts))
		levels := make([]ThinkingLevel, 0, len(efforts))
		for _, rawEffort := range efforts {
			effort := strings.TrimSpace(rawEffort)
			if effort == "" || seen[effort] || !isValidDynamicThinkingValue(effort) {
				continue
			}
			seen[effort] = true
			levels = append(levels, ThinkingLevel{
				Value: effort,
				Label: kimiThinkingLabel(effort),
			})
		}
		if len(levels) == 0 {
			continue
		}

		defaultEffort := strings.TrimSpace(model.DefaultEffort)
		if defaultEffort == "" {
			defaultEffort = strings.TrimSpace(model.DefaultEffortSnake)
		}
		if !seen[defaultEffort] {
			defaultEffort = ""
		}
		result[modelID] = &ModelThinking{
			SupportedLevels: levels,
			DefaultLevel:    defaultEffort,
		}
	}
	return result, nil
}

// kimiThinkingLabel 把 kimi provider 目录里的档位值翻译成 UI 文案；未知值退回 title-case 拼写。
func kimiThinkingLabel(value string) string {
	switch value {
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "max":
		return "Max"
	default:
		return strings.Title(value) //nolint:staticcheck
	}
}

// acpConfigOptionState 是 ACP configOptions 列表里“单条配置项”的状态快照：ID 是该配置的协议
// 标识，CurrentValue 是当前取值。
type acpConfigOptionState struct {
	ID                string `json:"id"`
	CurrentValue      string `json:"currentValue"`
	CurrentValueSnake string `json:"current_value"`
}

// findACPConfigOption 在 ACP 响应里找一个精确命中 config-id 的配置项。配置 id 是协议标识，不是
// 展示标签：只按分类（category）或大小写不敏感去匹配，会宣传出一个执行层根本设不了的控件。
// 驼峰与下划线两种字段拼写都接受，因为现实中的 ACP agent 两种都发。
func findACPConfigOption(raw json.RawMessage, configID string) (acpConfigOptionState, bool) {
	var response struct {
		ConfigOptions      []acpConfigOptionState `json:"configOptions"`
		ConfigOptionsSnake []acpConfigOptionState `json:"config_options"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return acpConfigOptionState{}, false
	}
	options := append(response.ConfigOptions, response.ConfigOptionsSnake...)
	for _, option := range options {
		if option.ID == configID {
			return option, true
		}
	}
	return acpConfigOptionState{}, false
}

// acpConfigOptionCurrentValue 取某配置项的当前值；字段缺失或值为空都返回 not-ok。
func acpConfigOptionCurrentValue(raw json.RawMessage, configID string) (string, bool) {
	option, ok := findACPConfigOption(raw, configID)
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(option.CurrentValue)
	if value == "" {
		value = strings.TrimSpace(option.CurrentValueSnake)
	}
	return value, value != ""
}

// discoverReasonixModels 驱动一次短命 Reasonix ACP 会话，解析 session/new 宣传的模型
// configOptions。认证与 provider 配置仍由 `reasonix setup` 负责；因此发现失败与其余 ACP
// 运行时一样退回手工录入模型。
//
// 同一次握手也会带 effort selector，所以 annotate 顺手就把它收下——不用第二个进程，也不需要
// 任何 reasonix 专属解析器。
//
// 只有会话的当前模型能拿到目录：reasonix 的 effort 词表是从当前模型所属 provider 条目推导的，
// 宣传出来的列表只描述那一款模型。其余模型 Thinking 保持 nil、不显示选择器，直到有逐模型探测
// 为止。见 annotateACPThinkingForSessionModel。
func discoverReasonixModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	return discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:       "reasonix",
		clientName:       "multica-model-discovery",
		acpArgs:          reasonixACPLaunchArgs(),
		tmpdirPrefix:     "multica-reasonix-discovery-",
		isolatedStateEnv: "REASONIX_STATE_HOME",
		annotate:         annotateACPThinkingForSessionModel,
	})
}

// discoverKiroModels 起一个一次性 `kiro-cli acp` 进程，解析 Kiro 在 session/new 返回的 models
// 块。
func discoverKiroModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	return discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "kiro-cli",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-kiro-discovery-",
	})
}

// discoverCopilotModels 起 `copilot --acp` 并从 session/new 读 `availableModels` 块。目录是按
// 用户的 GitHub 账号归类的，所以这是唯一能知道用户实际能访问哪些 ID（Pro vs Pro+ vs
// Enterprise vs 评估模型）的办法。
//
// 二进制缺失或 ACP 握手失败（认证缺失、网络断、…）时退回 copilotStaticModels()，保证 UI
// 下拉框总有内容可展示。
//
// 还会给每条目在 Provider 字段打上厂商——Copilot ACP 载荷不带这个字段，但 UI 按 Provider
// 分组，所以从 ID 前缀推导出来能把 OpenAI / Anthropic / Gemini 分区区分开。
//
// 不需要额外的 env 或权限 flag：发现只驱动 `initialize` + `session/new`，两者都不会触发工具
// 权限弹窗——模型目录本身就是 session/new 响应的一部分。
func discoverCopilotModels(ctx context.Context, runtimeCmd Command) (Catalog, error) {
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "copilot",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-copilot-discovery-",
		acpArgs:      []string{"--acp"},
	})
	if err != nil || len(models) == 0 {
		return Catalog{Models: copilotStaticModels(), Fallback: true}, nil
	}
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = inferCopilotProvider(models[i].ID)
		}
	}
	return Catalog{Models: models}, nil
}

// discoverQoderModels 以 `--yolo --acp` 起一个 Qoder CLI 二进制，从 session/new 解析模型。
// defaultBin 参数让 qoder 与 qoderclicn 两个 provider 名指向各自的可执行文件。
func discoverQoderModels(ctx context.Context, runtimeCmd Command, defaultBin string) ([]Model, error) {
	return discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   defaultBin,
		clientName:   "multica-model-discovery",
		acpArgs:      []string{"--yolo", "--acp"},
		tmpdirPrefix: "multica-qoder-discovery-",
	})
}

// acpDiscoveryDefaultTimeout 是 ACP 发现握手的总预算上限，除非 provider 单独覆盖。发现是一次
// 前台 UI 请求，所以上限按“用户愿意为一个下拉框等多久”来定，而不是按 CLI 最终能磨出什么来定。
const acpDiscoveryDefaultTimeout = 15 * time.Second

// hermesDiscoveryTimeout 是按 hermes 的*失败*路径而非成功路径定的。见
// acpDiscoveryProvider.timeout：配置好的 hermes ~2s 就能返回目录，但解析不到 provider 的那种
// （真正需要上报的情形）要花 ~25s 才走完。用默认 15s 的话，用户看到的是 "context deadline
// exceeded"，而不是 hermes 想让他跑的那条精确命令。
//
// 刻意远低于服务端 60s 的 modelListRunningTimeout，让发现请求在关闭前留出报告投递与重试退避
// 的时间。
const hermesDiscoveryTimeout = 40 * time.Second

// acpDiscoveryProvider 配置 discoverACPModels 如何启动一个讲 ACP 的 agent CLI。共享助手对所有
// CLI 用同一条路驱动（initialize → 可选 authenticate → session/new → 解析 models 块）；各
// provider 的差异只在：spawn 哪个二进制、哪些 env 变量用来压住 init 期间交互式弹窗、什么 argv
// 能让二进制进入 ACP server 模式（多数用 `acp`，Copilot 用 `--acp`）、以及临时工作目录用什么
// 前缀命名以便日志里好认。
type acpDiscoveryProvider struct {
	defaultBin       string
	clientName       string
	extraEnv         []string
	tmpdirPrefix     string
	isolatedStateEnv string
	// acpArgs 是让二进制进入 ACP server 模式的 argv。为 nil/空时默认 []string{"acp"}。
	acpArgs []string
	// selectAuthMethod 在 initialize 成功之后检查初始化响应与子进程环境。非空选择器必须返回
	// 一个被宣传的方法 id；它返回的错误会在任何会话操作之前中止发现。
	selectAuthMethod func(json.RawMessage, []string) (string, error)
	// strictErrors 把各阶段握手错误原样抛给调用方。老牌发现 provider 保留“返回空列表”的行为；
	// Grok 打开它，以便能记下真正的兜底原因。
	strictErrors bool
	// annotate 收到“解析出的目录 + 原始 session/new 结果”，供 provider 从共享解析器忽略的
	// 响应部分里进一步丰富模型。CodeBuddy 用它从同一次握手读出 effort 目录，这也是它完全不再
	// 需要单独一次发现调用的原因。
	annotate func([]Model, json.RawMessage)
	// timeout 约束整个握手——spawn、initialize、session/new。零值表示
	// acpDiscoveryDefaultTimeout。
	//
	// 它按 provider 单独设，是因为要装下的正是 CLI 的“不快乐路径”，而那条路径没有通用形状：
	// hermes 对一个健康的 session/new ~2s 就回答，但要花 ~25s 才能断定自己解析不到 provider
	//（放弃前它会反复探测；对照 hermes 0.20.0 实测——MUL-6606）。按快乐路径配的预算会把这类
	// 诊断全部变成 "context deadline exceeded"，而那恰恰是对用户零信息量的失败文案。
	timeout time.Duration
	// inspectInit 在任何会话创建前收到原始 initialize 结果。它用于读取握手本来就会带的能力
	// 事实——Kimi 读 `agentInfo.version` 以按 CLI 构建做功能门控——这样 provider 不必再花第二
	// 个进程问二进制“你是谁”。它不能使发现失败：provider 在这里没学到有用信息时只能降级，
	// 不能中止。
	inspectInit func(json.RawMessage)
}

// discoverACPModels 对任何实现了标准 `initialize` + `session/new` 流程、并在响应里以
// `models.availableModels` / `models.currentModelId`（或更新的 `configOptions` 列表）宣传模型
// 目录的 agent CLI 跑 ACP 握手。provider 专属的启动参数负责选择 ACP 模式（例如 `acp` vs
// `--acp`）。
func discoverACPModels(ctx context.Context, runtimeCmd Command, p acpDiscoveryProvider) ([]Model, error) {
	fail := func(stage string, err error) ([]Model, error) {
		if p.strictErrors {
			return nil, fmt.Errorf("ACP model discovery %s failed: %w", stage, err)
		}
		return []Model{}, nil
	}
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = p.defaultBin
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return fail("executable lookup", err)
	}
	timeout := p.timeout
	if timeout <= 0 {
		timeout = acpDiscoveryDefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var isolatedStateDir string
	if p.isolatedStateEnv != "" {
		var err error
		isolatedStateDir, err = os.MkdirTemp("", p.tmpdirPrefix+"state-")
		if err != nil {
			return fail("temporary state", err)
		}
		defer os.RemoveAll(isolatedStateDir)
	}

	cmdArgs := p.acpArgs
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"acp"}
	}
	cmd := runtimeCmd.exec(runCtx, cmdArgs...)
	hideAgentWindow(cmd)
	childEnv := append(os.Environ(), p.extraEnv...)
	if isolatedStateDir != "" {
		// 把可能指向用户真实配置目录的状态 env 重定向到一次性临时目录，避免发现进程读到/改到
		// 用户的持久化 agent 状态。
		childEnv = replaceEnvValue(childEnv, p.isolatedStateEnv, isolatedStateDir)
	}
	cmd.Env = childEnv
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail("stdin setup", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fail("stdout setup", err)
	}
	// 丢弃 stderr；这里的噪声日志帮不上忙，也不该每 60s 就漏进 daemon 日志。
	cmd.Stderr = io.Discard
	if err := startOwnedProcessTree(cmd, runtimeCmd.logger); err != nil {
		return fail("process start", err)
	}
	// 保证子进程及它派生的一切都被回收。这个探测按发现调度跑，泄漏一个 ACP server 会累积，
	// 而不是只出现一次。
	defer func() {
		_ = stdin.Close()
		signalProcessGroup(cmd, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		releaseProcessGroup(cmd)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	nextID := 1
	// requestACP 向子进程发出一帧 JSON-RPC 请求，然后阻塞读 stdout，直到收到与自己 id 匹配的
	// 响应帧；通知（无 id 或 id 不匹配的帧）一律跳过。
	requestACP := func(method string, params map[string]any) (json.RawMessage, error) {
		id := nextID
		nextID++
		msg := map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		data = append(data, '\n')
		if _, err = stdin.Write(data); err != nil {
			return nil, err
		}

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var env struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal([]byte(line), &env); err != nil || string(env.ID) != fmt.Sprint(id) {
				continue
			}
			if len(env.Error) > 0 && string(env.Error) != "null" {
				var rpcErr struct {
					Code    int             `json:"code"`
					Message string          `json:"message"`
					Data    json.RawMessage `json:"data"`
				}
				_ = json.Unmarshal(env.Error, &rpcErr)
				detail := ""
				if len(rpcErr.Data) > 0 && string(rpcErr.Data) != "null" {
					if err := json.Unmarshal(rpcErr.Data, &detail); err != nil {
						detail = strings.TrimSpace(string(rpcErr.Data))
					}
				}
				return nil, &acpRPCError{Method: method, Code: rpcErr.Code, Message: rpcErr.Message, Data: detail}
			}
			if len(env.Result) == 0 {
				return nil, fmt.Errorf("response contained neither result nor error")
			}
			return env.Result, nil
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if err := runCtx.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}

	// 每个阶段都由响应驱动。特别是：不要猜 Grok 的认证方法，也不要在 initialize/authenticate
	// 成功前就排队 session/new。
	initResult, err := requestACP("initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": p.clientName, "version": "0.1.0"},
		"clientCapabilities": map[string]any{},
	})
	if err != nil {
		return fail("initialize", err)
	}
	if p.inspectInit != nil {
		p.inspectInit(initResult)
	}

	// session/new 要求一个合法的 cwd——用事后清理的临时目录，而不是 daemon 的工作目录（那里
	// 可能正处在另一个 task 的 worktree 中）。
	tmp, err := os.MkdirTemp("", p.tmpdirPrefix)
	if err != nil {
		return fail("temporary cwd", err)
	}
	defer os.RemoveAll(tmp)

	if p.selectAuthMethod != nil {
		methodID, err := p.selectAuthMethod(initResult, childEnv)
		if err != nil {
			return fail("auth method selection", err)
		}
		if _, err := requestACP("authenticate", map[string]any{
			"methodId": methodID,
			"_meta":    map[string]any{"headless": true},
		}); err != nil {
			return fail(fmt.Sprintf("authenticate (%s)", methodID), err)
		}
	}

	sessionResult, err := requestACP("session/new", map[string]any{
		"cwd":        tmp,
		"mcpServers": []any{},
	})
	if err != nil {
		return fail("session/new", err)
	}
	models := parseACPSessionNewModels(sessionResult)
	if len(models) == 0 {
		// session/new 成功了却没带任何我们认识的目录。站在这里看，这就是上游 schema 漂移的样子
		// （MUL-5239：kimi 0.29 把目录从 `models` 挪到了 `configOptions`）；没有这行日志，它和
		// “CLI 真没有模型”就无从区分。只记顶层键——绝不记响应体，那里面是会话 id 和账号形态的
		// 数据。
		slog.Debug("ACP model discovery found no models in session/new response",
			"binary", runtimeCmd.Path,
			"result_keys", strings.Join(acpResultTopLevelKeys(sessionResult), ","),
		)
	}
	if models == nil {
		return fail("session/new model parsing", fmt.Errorf("response contained no model catalog"))
	}
	if err := runCtx.Err(); err != nil {
		return fail("completion", err)
	}
	if p.annotate != nil {
		p.annotate(models, sessionResult)
	}
	return models, nil
}

// replaceEnvValue 返回一份 env 切片，其中 key 对应的条目被替换成新值（大小写不敏感匹配）。
// key 不存在时直接追加；空 key 的校验由调用方保证。
func replaceEnvValue(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, key) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}

// parseACPSessionNewModels 从 ACP `session/new` 响应里抽取模型目录。Hermes 与旧版 Kimi
// （以及任何遵循该 schema 的 ACP agent）返回：
//
//	{
//	  "sessionId": "...",
//	  "models": {
//	    "availableModels": [
//	      {"modelId": "...", "name": "...", "description": "..."}
//	    ],
//	    "currentModelId": "..."
//	  }
//	}
//
// 新版 agent 改走 ACP `configOptions` 列表宣传同一目录——见 parseACPConfigOptionModels。两种
// 形状都接受：`models` 块存在时优先，只有它一无所获时才去查 `configOptions`，这样现有 provider
// 的行为都不变。
//
// 载荷缺失时返回 nil（而不是空切片），让调用方能够区分“解析出零模型”（合法空目录）与
// “压根找不到该结构”。
func parseACPSessionNewModels(raw json.RawMessage) []Model {
	type acpModelInfo struct {
		ModelID      string `json:"modelId"`
		ModelIDSnake string `json:"model_id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
	}
	var resp struct {
		Models struct {
			AvailableModels      []acpModelInfo `json:"availableModels"`
			AvailableModelsSnake []acpModelInfo `json:"available_models"`
			CurrentModelID       string         `json:"currentModelId"`
			CurrentModelIDSnake  string         `json:"current_model_id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	// 驼峰与下划线字段兼容：先认驼峰，为空再退回下划线。
	availableModels := resp.Models.AvailableModels
	if len(availableModels) == 0 && resp.Models.AvailableModelsSnake != nil {
		availableModels = resp.Models.AvailableModelsSnake
	}
	currentModelID := strings.TrimSpace(resp.Models.CurrentModelID)
	if currentModelID == "" {
		currentModelID = strings.TrimSpace(resp.Models.CurrentModelIDSnake)
	}
	models := make([]Model, 0, len(availableModels))
	seen := map[string]bool{}
	for _, m := range availableModels {
		modelID := strings.TrimSpace(m.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(m.ModelIDSnake)
		}
		if modelID == "" || seen[modelID] {
			continue
		}
		seen[modelID] = true
		models = append(models, acpModelEntry(modelID, m.Name, currentModelID))
	}
	if len(models) > 0 {
		return models
	}
	// `models` 块没有任何成果，退回从 configOptions 解析（kimi ≥0.29 等新版 agent 的形状）。
	if fromConfig := parseACPConfigOptionModels(raw); len(fromConfig) > 0 {
		return fromConfig
	}
	return models
}

// parseACPConfigOptionModels 从 `session/new` 返回的 ACP `configOptions` 列表里抽取模型目录。
// Kimi Code 0.29 弃用了顶层 `models` 块、改用这种形状（MUL-5239）：
//
//	{
//	  "sessionId": "...",
//	  "configOptions": [
//	    {
//	      "type": "select", "id": "model", "name": "Model", "category": "model",
//	      "currentValue": "kimi-code/k3",
//	      "options": [
//	        {"value": "kimi-code/k3", "name": "K3"},
//	        {"value": "kimi-code/kimi-for-coding", "name": "K2.7 Coding"}
//	      ]
//	    },
//	    {"id": "thinking", "category": "thought_level", ...}
//	  ]
//	}
//
// 当某条配置项的 `id` 或 `category` 是 "model"（大小写不敏感）时，它才算模型选择器。其余选项——
// 尤其是 thinking 档位——被刻意忽略：那是独立的产品面，把它们映射进模型下拉框会给出
// `session/set_model` 无法兑现的值。`currentValue` 标记默认模型。
//
// 没有 model 选项时返回 nil，让调用方保留 `models` 块产出的结果。
func parseACPConfigOptionModels(raw json.RawMessage) []Model {
	type acpConfigChoice struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	}
	type acpConfigOption struct {
		ID                string            `json:"id"`
		Category          string            `json:"category"`
		CurrentValue      string            `json:"currentValue"`
		CurrentValueSnake string            `json:"current_value"`
		Options           []acpConfigChoice `json:"options"`
	}
	var resp struct {
		ConfigOptions      []acpConfigOption `json:"configOptions"`
		ConfigOptionsSnake []acpConfigOption `json:"config_options"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	configOptions := resp.ConfigOptions
	if len(configOptions) == 0 {
		configOptions = resp.ConfigOptionsSnake
	}
	for _, opt := range configOptions {
		if !strings.EqualFold(strings.TrimSpace(opt.ID), "model") &&
			!strings.EqualFold(strings.TrimSpace(opt.Category), "model") {
			continue
		}
		currentValue := strings.TrimSpace(opt.CurrentValue)
		if currentValue == "" {
			currentValue = strings.TrimSpace(opt.CurrentValueSnake)
		}
		models := make([]Model, 0, len(opt.Options))
		seen := map[string]bool{}
		for _, choice := range opt.Options {
			modelID := strings.TrimSpace(choice.Value)
			if modelID == "" || seen[modelID] {
				continue
			}
			seen[modelID] = true
			models = append(models, acpModelEntry(modelID, choice.Name, currentValue))
		}
		if len(models) > 0 {
			return models
		}
	}
	return nil
}

// acpModelEntry 用 ACP 宣传的模型 id 与其展示名构造一条下拉框条目。Provider 只从
// `provider:model` 形式推导——像 kimi 的 `kimi-code/k3` 这种不带冒号的 id 保持不分组，与 UI
// 渲染扁平目录时的做法一致。
func acpModelEntry(modelID, name, currentModelID string) Model {
	provider := ""
	if idx := strings.Index(modelID, ":"); idx > 0 {
		provider = modelID[:idx]
	}
	return Model{
		ID:       modelID,
		Label:    acpModelLabel(name, modelID),
		Provider: provider,
		Default:  modelID == currentModelID,
	}
}

// acpResultTopLevelKeys 返回 ACP 结果的顶层对象键（已排序）。只取键、绝不取值：足够在
// daemon.log 里把 `configOptions` 漂移与“目录真的为空”区分开，又不会记下 session id 或上游
// 响应里的任何其他内容。
func acpResultTopLevelKeys(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// acpModelLabel 挑一个能展示的模型名：name 为空或等于 "unknown"（上游常用来占位）时退回 id。
func acpModelLabel(name, modelID string) string {
	label := strings.TrimSpace(name)
	if label == "" || strings.EqualFold(label, "unknown") {
		return modelID
	}
	return label
}

// discoverAntigravityModels 运行 `agy models`，返回已安装 Antigravity CLI 宣传的目录（每行一条
// 模型记录）。
//
// 与 cursor / pi / opencode 不同，这里刻意没有静态兜底。agy 的 `--model` 只接受已安装 CLI
// 宣传的精确标识符，对任何它不认识的取值会静默 no-op——空输出、退出码 0——所以一份猜出来的
// 静态清单有可能推荐一个已装 CLI 根本兑现不了的模型，把一次手误变成一次“成功”的空跑。任何
// 发现失败都返回空目录；agent.model 保持未设置，由 agy 自行解析默认。cachedDiscovery 从不缓存
// 空结果，所以原因清除后下一次请求就会重试。
func discoverAntigravityModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "agy"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return nil, nil
	}
	// `agy models` 是本地枚举（无网络往返），短上限足够；但也留到能吸收冷启动的余量。
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models")
	hideAgentWindow(cmd)
	out, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil && len(out) == 0 {
		return nil, nil
	}
	return parseAntigravityModels(string(out)), nil
}

// parseAntigravityModels 把 `agy models` 输出变成 Model 条目。agy 1.1.11 输出 "id<TAB>显示
// 标签"；旧版本每行只输出一个值。对旧格式，该值同时充当 ID 与 Label。空行与重复 ID 跳过。
func parseAntigravityModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		raw := scanner.Text()
		id := strings.TrimSpace(raw)
		label := id
		if idField, remaining, ok := strings.Cut(raw, "\t"); ok {
			id = strings.TrimSpace(idField)
			labelField, _, _ := strings.Cut(remaining, "\t")
			label = strings.TrimSpace(labelField)
			if label == "" {
				label = id
			}
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{
			ID:       id,
			Label:    label,
			Provider: "antigravity",
		})
	}
	return models
}

// discoverGrokModels 起 `grok agent --always-approve stdio` 并从 session/new 解析模型目录
// （与 Kiro/Qoder/Trae 同形）。要求 Grok CLI 已认证；任何失败退回 grokStaticModels。
func discoverGrokModels(ctx context.Context, runtimeCmd Command) (Catalog, error) {
	// 与 daemon 启动运行时一致：加 `--no-auto-update`（全局 flag），免得后台更新检查卡住发现。
	// 认证方法只有在 initialize 返回了“这个已装 CLI 实际提供哪些方法”之后才会被选中。
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "grok",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-grok-discovery-",
		acpArgs:      []string{"--no-auto-update", "agent", "--always-approve", "stdio"},
		annotate:     annotateGrokThinkingFromACP,
		selectAuthMethod: func(initResult json.RawMessage, childEnv []string) (string, error) {
			return selectGrokAuthMethod(extractACPAuthMethods(initResult), envHasNonEmpty(childEnv, "XAI_API_KEY"))
		},
		strictErrors: true,
	})
	if err != nil || len(models) == 0 {
		if err != nil {
			slog.Debug("grok model discovery fell back to static catalog", "error", err)
		}
		return Catalog{Models: grokStaticModels(), Fallback: true}, nil
	}
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = "xai"
		}
	}
	return Catalog{Models: models}, nil
}

// grokStaticModels 是 Grok Build CLI 的离线兜底目录。ID 与一个典型的已登录 `session/new` /
// `grok models` 列表一致。Grok 4.6 是当前 Grok Build 默认（xAI，2026-08-12）。
func grokStaticModels() []Model {
	models := []Model{
		{ID: "grok-4.6", Label: "Grok 4.6", Provider: "xai", Default: true},
		{ID: "grok-4.5", Label: "Grok 4.5", Provider: "xai"},
		{ID: "grok-composer-2.5-fast", Label: "Grok Composer 2.5 Fast", Provider: "xai"},
	}
	annotateGrokThinking(models)
	return models
}

// annotateGrokThinking 只挂载 xAI 逐模型 reasoning 文档与 Grok Build `--effort` flag 都确认过
// 的能力。未知模型与 composer 模型刻意保持 Thinking nil，而不是暴露可能运行失败的值。成功的
// 发现会用已装 CLI 宣传的值替换这些兜底目录。
//
// grok-4.6 有文档且接受 `xhigh`（docs.x.ai/developers/grok-4-6、grok 1.0.5 `--effort`）。
// grok-4.5 不接受；服务端的动态字面量门控会让 token 通过，而 ValidateThinkingLevel 仍会用
// 逐模型目录把 4.5 的 xhigh 按关闭处理。
func annotateGrokThinking(models []Model) {
	for i := range models {
		switch models[i].ID {
		case "grok-4.6":
			models[i].Thinking = grokThinkingCatalog(true)
		case "grok-4.5":
			models[i].Thinking = grokThinkingCatalog(false)
		}
	}
}

// grokThinkingCatalog 构造 Grok 的 thinking 目录；includeXHigh 只对支持它的模型打开 `xhigh`。
func grokThinkingCatalog(includeXHigh bool) *ModelThinking {
	levels := []ThinkingLevel{
		{Value: "low", Label: "Low"},
		{Value: "medium", Label: "Medium"},
		{Value: "high", Label: "High"},
	}
	if includeXHigh {
		levels = append(levels, ThinkingLevel{Value: "xhigh", Label: "Extra high"})
	}
	return &ModelThinking{SupportedLevels: levels}
}

// annotateGrokThinkingFromACP 从 xAI 厂商在 `session/new` 条目的 `_meta` 块里填每个模型的
// effort 目录：
//
//	{"modelId": "grok-4.6", "_meta": {"supportsReasoningEffort": true,
//	  "reasoningEfforts": [{"value": "high", "label": "High Effort", "default": true}, ...]}}
//
// 该扩展在核心 ACP schema 之外，所以解析保持窄：条目不宣传它的模型 Thinking 保持 nil——隐藏
// 选择器，而不是给出 CLI 可能拒绝的档位。
func annotateGrokThinkingFromACP(models []Model, sessionResult json.RawMessage) {
	var resp struct {
		Models struct {
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Meta    struct {
					SupportsReasoningEffort bool `json:"supportsReasoningEffort"`
					ReasoningEfforts        []struct {
						Value   string `json:"value"`
						Label   string `json:"label"`
						Default bool   `json:"default"`
					} `json:"reasoningEfforts"`
				} `json:"_meta"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(sessionResult, &resp); err != nil {
		return
	}
	thinkingByModel := map[string]*ModelThinking{}
	for _, entry := range resp.Models.AvailableModels {
		if !entry.Meta.SupportsReasoningEffort {
			continue
		}
		thinking := &ModelThinking{}
		seen := map[string]bool{}
		for _, effort := range entry.Meta.ReasoningEfforts {
			value := strings.TrimSpace(effort.Value)
			if value == "" || seen[value] || !isValidDynamicThinkingValue(value) {
				continue
			}
			seen[value] = true
			label := strings.TrimSpace(effort.Label)
			if label == "" {
				label = value
			}
			thinking.SupportedLevels = append(thinking.SupportedLevels, ThinkingLevel{Value: value, Label: label})
			if effort.Default {
				thinking.DefaultLevel = value
			}
		}
		if len(thinking.SupportedLevels) > 0 {
			thinkingByModel[strings.TrimSpace(entry.ModelID)] = thinking
		}
	}
	for i := range models {
		models[i].Thinking = thinkingByModel[models[i].ID]
	}
}

// discoverCursorModels 运行 `cursor-agent --list-models` 并解析 `id - Label` 行。Cursor 的目录
// 变得很频繁，同一基础模型带很多变体（thinking / fast / max 后缀）——静态内置几周就会过时。
// 任何失败都退回最小静态目录，保证 daemon 宿主机上没装 cursor-agent 时 UI 仍可用。
func discoverCursorModels(ctx context.Context, runtimeCmd Command) (Catalog, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "cursor-agent"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return Catalog{Models: cursorStaticModels(), Fallback: true}, nil
	}
	// 15s 与其他网络型发现路径对齐（pi/opencode/ACP）；cursor-agent 要抓取它频繁变化的目录，
	// 所以紧上限会超时并退到最小静态清单。见 #3729。
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "--list-models")
	hideAgentWindow(cmd)
	out, err := outputOwned(cmd, runtimeCmd.logger)
	if err != nil && len(out) == 0 {
		return Catalog{Models: cursorStaticModels(), Fallback: true}, nil
	}
	models := parseCursorModels(string(out))
	if len(models) == 0 {
		return Catalog{Models: cursorStaticModels(), Fallback: true}, nil
	}
	return Catalog{Models: models}, nil
}

// parseCursorModels 从 `cursor-agent --list-models` 抽取模型 ID。输出格式（截至 cursor-agent
// 2026.04）：
//
//	Available models
//	<blank>
//	auto - Auto
//	composer-2-fast - Composer 2 Fast (current, default)
//	composer-2 - Composer 2
//	…
//
// 带 `(default)` 标记的模型被置为 Default=true，让 UI 徽标指向 cursor 自己的推荐而不是我们目录
// 里的硬编码猜测。
func parseCursorModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// 行格式："<id> - <label>"。跳过 "Available models" 表头。
		idx := strings.Index(line, " - ")
		if idx <= 0 {
			continue
		}
		id := strings.TrimSpace(line[:idx])
		label := strings.TrimSpace(line[idx+3:])
		if !isModelIdentifier(id) {
			// Cursor ID 用紧凑的标识符字符集；畸形行直接忽略。
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		isDefault := strings.Contains(label, "default")
		// 把 "(current, default)" 后缀从展示标签里剥掉——这个信息已经通过 Default 标记表达了。
		if paren := strings.Index(label, "("); paren > 0 {
			label = strings.TrimSpace(label[:paren])
		}
		if label == "" {
			label = id
		}
		models = append(models, Model{
			ID:       id,
			Label:    label,
			Provider: "cursor",
			Default:  isDefault,
		})
	}
	return models
}

// isModelIdentifier 判断 s 看起来像个合法模型 ID token（字母开头，只含字母/数字/`-_./`，
// 不以冒号结尾）。
func isModelIdentifier(s string) bool {
	if s == "" || strings.HasSuffix(s, ":") {
		return false
	}
	first := s[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '/':
		default:
			return false
		}
	}
	return true
}

// ── CodeBuddy 模型发现 ──

// discoverCodebuddyModels 通过 ACP 向 CodeBuddy 要目录（`codebuddy --acp`），与 Copilot / Kimi
// / Reasonix / Kiro / Qoder / Grok / TRAE 共用同一条握手。`session/new` 在
// `models.availableModels` 下带结构化目录、外加一个 `currentModelId`，共享解析器
// parseACPSessionNewModels 读的就是它。
//
// 这取代了过去从 `codebuddy --help` 里抠 `--model` 行的做法（MUL-5549）。help 文本只带 ID、
// 别无其他，于是标签只能从 ID 猜，猜出 CodeBuddy 根本不用的名字（把 CLI 叫 Kimi-K3 的写成
// "Kimi K3 1"、DeepSeek-V3.2 写成 "Deepseek V3 2 Volc"）；默认模型靠“首个条目胜出”的猜测而非
// 宣传的 currentModelId；effort 目录还得在同一段输出上再做一次正则。对照 CodeBuddy 2.130.0
// 实测，握手也比 --help 明显更快——这点很关键，因为那条命令慢到曾是 #6180 超时的主要嫌疑。
//
// 握手失败时退回静态目录（标 Fallback，永远不能被当作权威缓存），包括未登录的情况——那时
// session/new 可能合理地拒绝。
func discoverCodebuddyModels(ctx context.Context, runtimeCmd Command) (Catalog, error) {
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "codebuddy",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-codebuddy-discovery-",
		acpArgs:      []string{"--acp"},
		strictErrors: true,
		annotate:     annotateCodebuddyThinkingFromACP,
	})
	if err != nil || len(models) == 0 {
		if err != nil {
			slog.Debug("codebuddy model discovery fell back to static catalog", "error", err)
		}
		return codebuddyFallbackCatalog(), nil
	}
	// 与 Copilot 相同的后处理：ACP 载荷不带厂商，acpModelEntry 也只能从 `vendor:model` id 里
	// 恢复一个。CodeBuddy 的 id 是裸的（`glm-5.2`、`kimi-k3-1`），所以不做这步的话每个模型都会
	// 落进同一个无标签分组，而不是选择器按 Provider 渲染出的 Zhipu / Kimi / DeepSeek 分区。
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = codebuddyModelProvider(models[i].ID)
		}
	}
	return Catalog{Models: models}, nil
}

// codebuddyModelProvider 从 CodeBuddy 模型 ID 前缀推断厂商。CodeBuddy 在自己账号下聚合多家
// 厂商，而 ACP 目录和静态兜底都不带厂商字段，所以 ID 前缀是给选择器分组的唯一可用信号。
func codebuddyModelProvider(id string) string {
	switch {
	case strings.HasPrefix(id, "claude-"):
		return "anthropic"
	case strings.HasPrefix(id, "gemini-"):
		return "google"
	case strings.HasPrefix(id, "gpt-"):
		return "openai"
	case strings.HasPrefix(id, "glm-"):
		return "zhipu"
	case strings.HasPrefix(id, "minimax-"):
		return "minimax"
	case strings.HasPrefix(id, "kimi-"):
		return "kimi"
	case len(id) >= 3 && id[0] == 'h' && id[1] == 'y' && id[2] >= '0' && id[2] <= '9':
		return "hunyuan"
	case strings.HasPrefix(id, "deepseek-"):
		return "deepseek"
	default:
		return ""
	}
}

// codebuddyFallbackCatalog 是发现失败时的静态替身。它就地套用静态 effort 档位而不是再去探测
// 一遍：能打断 ACP 握手的原因（二进制缺失、CLI 未登录、超时）同样会打断第二次尝试。
func codebuddyFallbackCatalog() Catalog {
	models := codebuddyStaticModels()
	applyCodebuddyStaticThinking(models)
	return Catalog{Models: models, Fallback: true}
}

// codebuddyStaticModels 是 ACP 发现失败（二进制缺失、CLI 未登录、握手超时）时的兜底目录。
//
// 这些 ID 与 CodeBuddy 的真实目录完全无交集，所以这份清单只是“让选择器还能用”的最后一招，
// 永远不是答案。它总是以标着 Fallback 的形式返回，因此不可能被当作权威缓存（MUL-5549）。
func codebuddyStaticModels() []Model {
	return []Model{
		{ID: "claude-sonnet-4.6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Default: true},
		{ID: "claude-opus-4.7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "gemini-3.1-pro", Label: "Gemini 3.1 Pro", Provider: "google"},
		{ID: "gpt-5.5", Label: "GPT 5.5", Provider: "openai"},
		{ID: "deepseek-v3-2-volc-ioa", Label: "Deepseek V3 2 Volc IOA", Provider: "deepseek"},
	}
}

// discoverDimModels 从 Dim 的 ACP session/new 握手枚举模型目录。Dim (dimcode) 宣传
// models.availableModels；枚举要求已登录的 dim（OAuth）。任何失败都由调用方退回手工录入框。
func discoverDimModels(ctx context.Context, runtimeCmd Command) (Catalog, error) {
	models, err := discoverACPModels(ctx, runtimeCmd, acpDiscoveryProvider{
		defaultBin:   "dim",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-dim-discovery-",
		acpArgs:      []string{"acp"},
	})
	if err != nil || len(models) == 0 {
		if err != nil {
			slog.Debug("dim model discovery failed; falling back to manual entry", "error", err)
		}
		return Catalog{Models: []Model{}, Fallback: true}, nil
	}
	return Catalog{Models: models}, nil
}
