package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// acp_effort.go 承载 ACP 推理投入度（reasoning effort）支持里与 provider 无关的
// 一半：从 backend 在 `session/new` 响应里宣告的选项中定位"投入度选择器"，把它变成
// 模型目录（Thinking 字段），并把持久化的档位推回一条进行中的会话。
//
// ACP 给了两个通用原语——session/new 结果里的 `configOptions` 与
// `session/set_config_option`——所以"发现"和"应用"对每个 backend 形态一致。
// 协议没有标准化、仍要靠这里约定的是：
//
//   - 语义：协议里没有"这个选项就是推理投入度"的标记，所以选择器要靠 id/category
//     与 acpEffortOptionIDs 匹配来认。
//   - 词表：reasonix 宣告 auto|disabled|low|high|max，Codex 是 none|minimal|…|ultra。
//     我们把这些 token 原样透传（MUL-2339），而不是压扁到一套共享枚举上。
//   - "设了到底有没有用"：协议说客户端可以设一个选项，但没说 agent 必须照做。
//     Kimi ≤0.28.1 与 Hermes v0.18.2 都会收下档位却让会话纹丝不动（见
//     discoverKimiModels 与 ThinkingControlSupported）。这就是为什么
//     applyACPEffortOption 要把值读回来确认，而不是默认成功；也只有当某运行时的
//     Execute 主动 opt-in 之后才走到这条路径。

// acpEffortOptionIDs 是标识"推理投入度选择器"的选项 id / category token。
// reasonix 以 id `effort`、category `thought_level` 宣告；CodeBuddy 与 jcode
// 宣告 `thought_level`。两个字段都匹配，就不需要按运行时维护一张表。
var acpEffortOptionIDs = map[string]bool{
	"effort":        true,
	"thought_level": true,
}

// acpEffortOption 是某个 backend 宣告的投入度选择器。
type acpEffortOption struct {
	// ConfigID 是 session/set_config_option 必须拿到的选项 id。它不能跨 backend
	// 通用——reasonix 用 `effort`、CodeBuddy 用 `thought_level`、Kimi 用 `thinking`
	// ——所以从会话里读出来，而不是按运行时写死。
	ConfigID string
	// CurrentValue 是会话当前所处的档位；backend 不上报时为 ""。
	CurrentValue string
	// Choices 是按宣告顺序排列的可选档位。label 取自 backend 自己的 `name` 字段，
	// 让选择器的观感与该 CLI 自家 UI 一致。
	Choices []ThinkingLevel
}

// supports 报告 value 是否为已宣告的可选档位之一。
func (o acpEffortOption) supports(value string) bool {
	for _, choice := range o.Choices {
		if choice.Value == value {
			return true
		}
	}
	return false
}

// values 返回已宣告的档位 token，保持原顺序。
func (o acpEffortOption) values() []string {
	out := make([]string, 0, len(o.Choices))
	for _, choice := range o.Choices {
		out = append(out, choice.Value)
	}
	return out
}

// parseACPEffortOption 从 ACP session/new（或 session/resume）结果里提取投入度
// 选择器。响应里没有可辨识的选项时返回 false——所有调用方都把它当作"这个运行时
// 没有投入度旋钮"，而不是错误。
//
// 档位值原样取用：词表归 CLI 所有，这里如果做白名单，会静默丢掉 backend 真实
// 接受的档位。需要收窄集合的调用方——例如 CodeBuddy，它的 `--effort` 旗标会拒绝
// ACP 表面仍宣告的仅会话级选项——在它们自己那一层叠加过滤。
func parseACPEffortOption(raw json.RawMessage) (acpEffortOption, bool) {
	type acpChoice struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	}
	type acpOption struct {
		ID                string      `json:"id"`
		Category          string      `json:"category"`
		CurrentValue      string      `json:"currentValue"`
		CurrentValueSnake string      `json:"current_value"`
		Options           []acpChoice `json:"options"`
	}
	var resp struct {
		ConfigOptions      []acpOption `json:"configOptions"`
		ConfigOptionsSnake []acpOption `json:"config_options"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return acpEffortOption{}, false
	}
	// 驼峰与下划线两种命名都认，优先驼峰。
	options := resp.ConfigOptions
	if len(options) == 0 {
		options = resp.ConfigOptionsSnake
	}

	for _, opt := range options {
		id := strings.TrimSpace(opt.ID)
		if !acpEffortOptionIDs[strings.ToLower(id)] &&
			!acpEffortOptionIDs[strings.ToLower(strings.TrimSpace(opt.Category))] {
			continue
		}
		// 读得到却寻址不了的选项毫无用处：没有 id 就没有东西可回发给
		// session/set_config_option。
		if id == "" {
			continue
		}
		result := acpEffortOption{ConfigID: id}
		seen := map[string]bool{}
		for _, choice := range opt.Options {
			value := strings.TrimSpace(choice.Value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			label := strings.TrimSpace(choice.Name)
			if label == "" {
				label = strings.Title(value) //nolint:staticcheck
			}
			result.Choices = append(result.Choices, ThinkingLevel{Value: value, Label: label})
		}
		current := strings.TrimSpace(opt.CurrentValue)
		if current == "" {
			current = strings.TrimSpace(opt.CurrentValueSnake)
		}
		// 只回显"我们同时也在广告"的默认值。currentValue 若落在选项列表之外
		// （例如 CodeBuddy 的 `enabled`），就变成空 DefaultLevel——UI 会渲染成
		// 泛化的 "Default"，而不是一个选择器给不出的档位。
		if result.supports(current) {
			result.CurrentValue = current
		}
		return result, true
	}
	return acpEffortOption{}, false
}

// annotateACPThinkingForSessionModel 用与模型同一次 session/new 响应里的
// configOptions，为"会话当前所在的那个模型"填上投入度目录——不额外起进程。
//
// 只标注那一个模型，这是本函数最要紧的地方。一个会话的 configOptions 描述的是
// 会话*当前*的配置，而 ACP session-config 的 RFD 允许选项互相依赖：一换模型，
// 投入度选项可能换词表、甚至消失。reasonix v1.21.5 正是如此——它用
// SessionConfigState 解析当前模型对应的 provider 条目，再用
// EffortCapabilityForEntry 推导目录，于是 `deepseek-v4-flash` 宣告 `low` 而
// `deepseek-v4-pro` 不宣告，GLM / OpenAI / MiniMax / Ollama Cloud 各自带各的
// 词表，解析不出协议模型的模型干脆没有投入度选项。
//
// 因此把某个模型的目录抄到它的兄弟模型上，等于发明一些运行时根本会拒绝的档位：
// 选择器会在 pro 上给出 `low`，ValidateThinkingLevel 会放行，任务则以默认档位
// 静默运行——这正是整条路径要杜绝的"假开关"。
//
// 当前模型以外的模型保持 Thinking 为 nil，即对它们隐藏选择器。要读真正按模型
// 区分的目录，得对每个模型驱动会话自己的 `model` 配置选项、消费每次刷新的响应；
// 那是后续工作，不该在这里造假。
func annotateACPThinkingForSessionModel(models []Model, sessionResult json.RawMessage) {
	option, ok := parseACPEffortOption(sessionResult)
	if !ok || len(option.Choices) == 0 {
		return
	}
	thinking := &ModelThinking{
		SupportedLevels: option.Choices,
		DefaultLevel:    option.CurrentValue,
	}
	for i := range models {
		// Default 由会话宣告的 currentModelId 置位，所以它标记的正是这份目录真正
		// 描述的那个模型。要是没标任何模型，就一个都不标，而不是去猜。
		if models[i].Default {
			models[i].Thinking = thinking
		}
	}
}

// acpRequestFn 是每个 backend 客户端早已暴露的 ACP JSON-RPC 调用。把它收成函数
// 参数，让本辅助逻辑与"具体是哪个客户端结构体"解耦。
type acpRequestFn func(ctx context.Context, method string, params any) (json.RawMessage, error)

// applyACPEffortOption 用会话宣告的选择器，把一个已持久化的思考档位推到一条
// 进行中的 ACP 会话上。
//
// 配置失败从不阻塞任务：prompt 无论成败都会照发，告警日志是"UI 里显示的档位可能
// 并不是实际生效的档位"的唯一记录。实际生效档位取决于怎么失败的——请求出错时
// 会话保持原样（全新会话上是 CLI 自己的设置，续跑会话上是上一回合的档位）；回复
// 未被确认时，停留在 backend 上报的那个值。
//
// 读回确认值得做，但并不是"档位已生效"的证据。它只确认一件事：会话自己的
// configOptions 现在报出了我们要的值。它能抓住那种"收下调用却原样不动"的运行时
// ——Kimi ≤0.28.1 在被设成 "max" 之后仍确认 "on"——但运行时也可能回显新值、
// 却不把它真正带进 provider 请求。这条线上没有任何可观测的东西能区分这两种情况。
// 要知道某运行时真的落实设置，得查它的源码或真实跑一次——acpCatalogThinkingProviders
// 的准入标准就是这个；读回只是在它之上加一层运行时确认与诊断。
//
// stateIsCurrent 表示 sessionResult 此刻是否仍描述着会话现状。换过模型之后它就不是
// 了：ACP 选项可以互相依赖，会话建在哪个模型上时广告的投入度词表，可能与会话在
// session/set_model 之后落在的模型不同——而且 reasonix 的 set_model 返回空结果，
// 刷新的选项只通过 config_option_update 通知发布，而共享客户端会丢掉它。当状态
// 过期时，我们跳过本地词表校验（不拿档位去对错误模型的清单），直接发请求，让
// 运行时自己的回答来定。
func applyACPEffortOption(
	ctx context.Context,
	request acpRequestFn,
	backend string,
	logger *slog.Logger,
	sessionID string,
	sessionResult json.RawMessage,
	level string,
	stateIsCurrent bool,
) {
	if level == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	option, ok := parseACPEffortOption(sessionResult)
	if !ok || len(option.Choices) == 0 {
		// agent 带着一个"服务器此前对已发现目录接受过"的档位，但这个会话没有
		// 宣告任何投入度选项。CLI 降级、换了账号、或模型没有旋钮，都会落到这里。
		// 没有选项 id 就无处寻址，即便试探性地发也没请求可发。
		logger.Warn("session advertises no reasoning-effort option; sending the prompt without it",
			"backend", backend,
			"requested_level", level,
		)
		return
	}
	if stateIsCurrent && !option.supports(level) {
		// 给一个没宣告过的 token 发请求，极可能在"我们故意吞掉失败"的调用上吃到
		// 硬错误。跳过则让会话停留在一个它真能理解的档位上。这个判断只有在"宣告
		// 列表仍描述实时会话"时可信——见 stateIsCurrent。
		logger.Warn("session does not advertise the requested reasoning effort; sending the prompt without it",
			"backend", backend,
			"config_id", option.ConfigID,
			"requested_level", level,
			"advertised_levels", strings.Join(option.values(), ","),
		)
		return
	}

	result, err := request(ctx, "session/set_config_option", map[string]any{
		"sessionId": sessionID,
		"configId":  option.ConfigID,
		"value":     level,
	})
	if err != nil {
		logger.Warn("runtime rejected the reasoning effort request; sending the prompt anyway",
			"backend", backend,
			"config_id", option.ConfigID,
			"requested_level", level,
			"effective_level", "unchanged",
			"error", err,
		)
		return
	}

	effectiveLevel, confirmed := acpConfigOptionCurrentValue(result, option.ConfigID)
	if !confirmed || effectiveLevel != level {
		if !confirmed {
			effectiveLevel = "unknown"
		}
		logger.Warn("runtime did not confirm the requested reasoning effort; sending the prompt anyway",
			"backend", backend,
			"config_id", option.ConfigID,
			"requested_level", level,
			"effective_level", effectiveLevel,
		)
		return
	}
	logger.Info("session reasoning effort confirmed",
		"backend", backend,
		"config_id", option.ConfigID,
		"level", effectiveLevel,
	)
}
