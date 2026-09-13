// Package llm 是官方 OpenAI Go SDK（github.com/openai/openai-go）外面一层很薄的
// 可复用包装。它存在的意义，是让 server 其它部分对「就是想调一次 LLM」这类
// 不需要完整 agent 运行时的需求有一个单一、类型良好的入口——目前是聊天自动
// 起标题与聊天的后续问题（MUL-4238）。
//
// # 边界：辅助层，不是产品里的每一次模型调用
//
// 本包覆盖「API 进程替自己发起的 LLM 调用」。跑 agent 是另一条完全不同的数据
// 路径：daemon 把某个 AI 编码工具当子进程执行，用的是那个工具自己的凭证，
// 不会把本层的 MULTICA_LLM_* 设置转发给它。（它确实会注入 agent 自己需要的
// 任务级 Multica 连接变量——见 pkg/agent 的 mergeEnv，它先丢弃 daemon 继承的
// MULTICA_*，再附上为该任务拼装的值。）这里管不到那条路径，面向运维的文案
// 也不得暗示能管到——一个把「留空 = 什么都不发」理解成覆盖整个产品的管理员，
// 已被误导了自己的聊天内容会流向哪里。
//
// # 单一入口规则
//
// server 进程替自己发起的每一次 LLM 调用都走这个包，包外没有任何东西直接
// import OpenAI SDK。这是一条要守住的约束、不是巧合：正是它让「这个部署的
// 辅助层向第三方发了什么」读一个包就能回答，而无需审计整棵代码树。新功能想
// 用模型就调这个包。两条测试各自守住该规则的一半：
// TestOpenAISDKIsImportedOnlyByThisPackage（别处摸不到 SDK）和
// TestDocumentedConsumersAreTheOnlyCallers（别处摸不到这个 client）。
//
// # 消费者，以及它们向上游发什么
//
// 增删消费者或改它们发送的内容时，请保持这份清单是最新的——面向运维的文案
// （.env.example 与 apps/docs/content/docs/environment-variables*.mdx）就是照它
// 写的，而 TestDocumentedConsumersAreTheOnlyCallers 会在新调用点没同步到这里
// 之前一直失败。
//
//   - 聊天自动起标题——server/internal/handler/chat_title.go。发送新聊天会话
//     的第一条用户消息，原样且不设上限。附件从不包含。
//   - 聊天后续问题，也叫 quick actions——
//     server/internal/service/chat_quick_actions_generate.go。
//     发送对话的尾巴：最多 6 条消息，被回复的那条截到 3000 个 rune
//     （前 2000 + 后 1000），更早的每条 800。
//
// 两个消费者发送的都是私密聊天内容，所以「未配置的部署零上游请求」是一条
// 契约而非副作用：没有 API key 也没有 base URL 时，New 产出一个禁用 client，
// 它的每次调用在构造 HTTP 请求之前就以 ErrNotConfigured 失败；两个消费者也
// 都在动手干活前先查 Enabled()
// （TestUnconfiguredClientMakesZeroUpstreamRequests）。不允许本层发送聊天内容
// 的运维把 MULTICA_LLM_API_KEY 与 MULTICA_LLM_BASE_URL 留空即可；产品照常
// 可用（客户端推断标题、没有后续问题按钮）。
//
// 包装刻意做得很小：
//
//   - 它独占 SDK client 的构造（base URL + API key + 重试/超时默认值），
//     调用方永远不用直接碰 option.RequestOption。
//   - 它同时暴露原始 Chat Completions 面（Chat / ChatStream）和一个便捷的
//     GenerateText 助手，供 server 内部调用方做简单的一次性补全
//     （如聊天标题生成）。
//   - 默认模型可配置；请求没给 model 时回落默认值，默认值也空时再回落一个
//     靠谱的内置默认，让配错的部署得到清晰的上游错误，而不是本层自造的 400。
//
// Base URL 与 API key 都可配，让同一层既能打 OpenAI、也能打 OpenAI 兼容网关
// 或自托管模型服务。
package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"
)

// FallbackModel 是请求与配置的默认模型都没给时用的最后手段。刻意选一个小而
// 便宜的模型，因为本层支撑的是轻量工具调用。
const FallbackModel = "gpt-5.6-luna"

// defaultTimeout 在调用方自己的 context 没有截止时间时，约束整个请求生命周期
// （含 SDK 重试）。流式请求不受它约束，因为连接生命周期由 handler 自己拥有。
const defaultRequestTimeout = 60 * time.Second

// ErrNotConfigured 由 Chat/ChatStream/GenerateText 在 client 没有任何凭证或
// base URL 时返回。内部调用方应把它当作「LLM 已禁用」信号并优雅回落（例如
// 聊天起标题就保留原标题），这样配错的自托管部署绝不会无 key 去拨 OpenAI。
var ErrNotConfigured = errors.New("llm: no API key or base URL configured")

// Config 装 LLM 层的所有可调项。所有字段可选；空 Config 产出禁用 client
// （见 Client.Enabled）。
type Config struct {
	// APIKey 对上游做鉴权。对应 MULTICA_LLM_API_KEY。
	APIKey string
	// BaseURL 指向 OpenAI 或任意 OpenAI 兼容网关。留空时用 SDK 默认
	// （https://api.openai.com/v1）。对应 MULTICA_LLM_BASE_URL。
	BaseURL string
	// DefaultModel 在请求没给 model 时使用。对应 MULTICA_LLM_DEFAULT_MODEL。
	// 留空时用 FallbackModel。
	DefaultModel string
	// MaxRetries 是应用到本 client 每个请求上的传输层重试预算。对应
	// MULTICA_LLM_MAX_RETRIES。用 Retries 构造；nil 表示未设置，按
	// DefaultMaxRetries。
	//
	//   - nil            — 未设置；按 DefaultMaxRetries。
	//   - Retries(0)     — 关闭重试；恰好一次上游请求。
	//   - Retries(N)     — 至多重试 N 次，即至多 N+1 次上游请求。
	//
	// 用「指向已验证类型的指针」而不是裸 int，有两个原因（MUL-6364）。裸 int
	// 让 0 与零值无法区分，于是「要求不重试」会静默变成 SDK 默认；它还允许
	// 负数——option.WithMaxRetries 遇负数会 panic——直达本层，让这里只有
	// 「panic 或悄悄换预算」两条路。Retries 是唯一构造器并拒绝负数，
	// 于是两种坏状态在这条边界上根本无法被表示出来。
	//
	// 这个预算具体重试什么由 SDK 决定：连接级失败（完全没有响应）、HTTP 408、
	// 409、429 与任何 5xx，外加任何带 `x-should-retry: true` 的响应。其余 4xx
	// ——400、401、403、404——不做重试直接返回调用方。退避从 0.5s 起、翻倍到
	// 8s 封顶，并减掉至多 25% 的抖动，除非响应带 Retry-After 头——它优先。
	//
	// 它 NOT 是 GenerateJSON 里那套参数兼容重试（那套独立且单独设上限），也不
	// 覆盖 ChatStream 已经返回之后才断掉的流。取值时要对着调用方自己的截止时间：
	// 光退避一项，2 次重试约 1.5s、6 次约 21s——预算比调用方超时还大，只会把
	// 「可恢复的失败」变成「截止时间超了」。
	MaxRetries *RetryOverride
	// HTTPClient 设置后替换 SDK 的默认传输。主要用作测试缝。
	HTTPClient option.HTTPClient
}

// DefaultMaxRetries 是 Config.MaxRetries 未设置时使用的重试预算。
// 它镜像了我们锁定的 openai-go 版本默认值，但 New 总是显式传它，因此有效
// 策略归我们报告，且不会随 SDK 升级悄悄漂移。
const DefaultMaxRetries = 2

// RetryOverride 是 Config.MaxRetries 用的「已验证」重试预算。它唯一的字段
// 未导出、Retries 是唯一构造器，所以非法预算在这条边界上无法被表示——
// New 因此没有需要处理的纠偏分支，也不需要。issue #7154 要求非法值走校验
// 失败而不是被悄悄矫正；让它们「构造不出来」就是能做到的最强形态。
type RetryOverride struct{ n int }

// Retries 返回「每次调用至多重试 n 次」的覆盖。它拒绝负 n 而不是矫正它：
// option.WithMaxRetries 遇负数会 panic，而这里也没有诚实的矫正可做——
// 「少于零次重试」是个错误，不是「关闭重试」的意思。Retries(0) 才是关掉。
func Retries(n int) (*RetryOverride, error) {
	if n < 0 {
		return nil, fmt.Errorf("llm: max retries must not be negative, got %d (use 0 to disable retries)", n)
	}
	return &RetryOverride{n: n}, nil
}

// Value 报告已配置的预算。RetryOverride 的零值报 0，这保持一致：一个从未经
// Retries 构造的覆盖，同样不带任何重试。
func (r *RetryOverride) Value() int {
	if r == nil {
		return 0
	}
	return r.n
}

// 重试策略来源，由 RetryBudget.Source 报告。
const (
	// RetrySourceDefault 表示 Config.MaxRetries 未设置。
	RetrySourceDefault = "default"
	// RetrySourceConfig 表示 Config.MaxRetries 被显式设置过，含显式 0。
	RetrySourceConfig = "config"
)

// RetryBudget 描述一个 Client 的「有效传输层重试策略」。它存在，是为了让部署
// 能报告自己实际会做什么，而不是从原始配置重新推导——「未设置 vs 0」的歧义
// 当初正是栽在重新推导上。每个字段都是标量或固定枚举，所以整结构体打进日志
// 永远不会泄出 API key 或网关 URL。
type RetryBudget struct {
	// MaxRetries 是一次请求失败后的重试上限，所以 N 意味着每次调用至多 N+1
	// 次上游请求。只有可重试的失败会消耗它；成功或调用方截止时间可提前
	// 结束调用。
	MaxRetries int
	// Source 是 RetrySourceDefault 或 RetrySourceConfig。
	Source string
	// RequestTimeout 在调用方 context 自己没有更早截止时间时，约束整条非流式
	// 调用链（含重试与退避）。目前每个内部调用方都设了更紧的。
	RequestTimeout time.Duration
}

// Client 是一个已配置、可复用的 LLM 调用方。并发安全；底层 SDK client
// 不持有任何按请求的状态。
type Client struct {
	sdk          openai.Client
	defaultModel string
	enabled      bool
	retry        RetryBudget
}

// New 由 cfg 构造一个 Client。它永不返回错误：未配置的 Config 产出禁用
// client，其调用返回 ErrNotConfigured——这让 main/router 里的装配保持简单
// （某个部署只是没搭 LLM 层，也不至于启动即失败）。
func New(cfg Config) *Client {
	opts := make([]option.RequestOption, 0, 4)
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if base := strings.TrimSpace(cfg.BaseURL); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	retry := RetryBudget{
		MaxRetries:     DefaultMaxRetries,
		Source:         RetrySourceDefault,
		RequestTimeout: defaultRequestTimeout,
	}
	if cfg.MaxRetries != nil {
		// 这里刻意不做校验也不做钳制：RetryOverride 只可能装着 Retries 已经
		// 接受过的值，留给本层「静默纠偏」的非法预算根本不存在。
		retry.MaxRetries = cfg.MaxRetries.Value()
		retry.Source = RetrySourceConfig
	}
	// 即使是用默认也总是显式传，让 SDK 强制执行的预算与 RetryBudget 上报的
	// 是同一个数。
	opts = append(opts, option.WithMaxRetries(retry.MaxRetries))
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}

	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel == "" {
		defaultModel = FallbackModel
	}

	return &Client{
		sdk:          openai.NewClient(opts...),
		defaultModel: defaultModel,
		// 一个部署只要给了 key 或 base URL 任一，就算「已配置」。
		// 只有 base URL 没有 key 对免 key 的本地网关是合法的。
		enabled: strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.BaseURL) != "",
		retry:   retry,
	}
}

// RetryBudget 返回生效的传输层重试策略，供启动诊断用。整结构体可安全打日志：
// 它不带任何凭证或 URL。
func (c *Client) RetryBudget() RetryBudget {
	if c == nil {
		return RetryBudget{}
	}
	return c.retry
}

// Enabled 报告这个 client 是否被给过任何凭证或 base URL。
// handler 用它先短路返回 503，而不做任何实际工作。
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// DefaultModel 返回生效的默认模型（永不空）。
func (c *Client) DefaultModel() string { return c.defaultModel }

// applyDefaultModel 在调用方留空 model 时填上默认模型。
func (c *Client) applyDefaultModel(params *openai.ChatCompletionNewParams) {
	if strings.TrimSpace(string(params.Model)) == "" {
		params.Model = shared.ChatModel(c.defaultModel)
	}
}

// Chat 做一次非流式聊天补全。params 原样透传给 SDK（所以 tools、
// response_format、temperature 等都照常生效）；只套一层默认模型。返回的
// *ChatCompletion 对 OpenAI 兼容响应暴露 RawJSON() 取字节级原文。
func (c *Client) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	c.applyDefaultModel(&params)

	// 调用方没给 deadline 时给请求一个有界生命周期，免得挂死的上游无限期
	// 钉住一个 goroutine。
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	return c.sdk.Chat.Completions.New(ctx, params)
}

// ChatStream 做一次流式聊天补全，返回 SDK 流，让调用方转发各个 chunk
// （每个 chunk 暴露 RawJSON() 取字节级 OpenAI 兼容 SSE）。调用方必须 Close
// 返回的流。
//
// 与 Chat 不同，这里不施加默认超时：流的生命周期归调用方所有（通常是绑着
// 客户端连接的 HTTP handler）。
//
// Config.MaxRetries 只覆盖「开启流的那次 POST」。本函数返回后，流在应答中途
// 断掉就归调用方处理了：SDK 无法重放已经送出的 chunk。
func (c *Client) ChatStream(ctx context.Context, params openai.ChatCompletionNewParams) (*ssestream.Stream[openai.ChatCompletionChunk], error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	c.applyDefaultModel(&params)
	return c.sdk.Chat.Completions.NewStreaming(ctx, params), nil
}

// GenerateText 是给简单内部一次性补全（聊天标题、quick-create 草稿……）用的
// 便捷入口。它发送一段可选的系统提示加一段用户提示，返回 assistant 的文本
// 内容。model 传空 = 用配置的默认。
func (c *Client) GenerateText(ctx context.Context, model, systemPrompt, userPrompt string) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, openai.SystemMessage(systemPrompt))
	}
	messages = append(messages, openai.UserMessage(userPrompt))

	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    shared.ChatModel(strings.TrimSpace(model)),
	}

	completion, err := c.Chat(ctx, params)
	if err != nil {
		return "", err
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("llm: upstream returned no choices")
	}
	return completion.Choices[0].Message.Content, nil
}

// GenerateJSON 是 GenerateText 的结构化兄弟，服务那些回复必须可机器读取的
// 内部调用方（quick-action 建议……）。它请求 response_format=json_object，
// 返回 assistant 的原始文本（不做解析）。
//
// JSON-object 模式只保证回复在语法上是合法 JSON，绝不保证它的形状符合提示的
// 要求——解析与校验仍是调用方的责任。有一条调用方必须遵守的上游约束：提示
// 里某处必须出现 "JSON" 这个词，否则 OpenAI 兼容端点会直接拒绝请求。
//
// 这个助手面向小体量、延迟敏感的活。对 GPT-5.6 家族，它显式关掉推理、把采样
// 控件留在模型默认。这保住 maxCompletionTokens 给可见的 JSON 用、不被推理吃
// 掉，也避开那些模型可能拒绝的采样参数。其它模型保留调用方的 temperature，
// 让可配置的部署不改变行为。temperature 与 maxCompletionTokens 只在为正时
// 生效；为零则保留上游对应默认。model 传空 = 用配置的默认。
func (c *Client) GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, openai.SystemMessage(systemPrompt))
	}
	messages = append(messages, openai.UserMessage(userPrompt))

	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    shared.ChatModel(strings.TrimSpace(model)),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		},
	}
	effectiveModel := strings.TrimSpace(model)
	if effectiveModel == "" {
		effectiveModel = c.defaultModel
	}
	if isGPT56Family(effectiveModel) {
		// GPT-5.6 默认开 medium 推理。这条路径要在严格的墙钟预算下生成一个
		// 很小的 JSON 对象，推理只会加延迟、吃补全 token 上限，却改不了契约。
		params.ReasoningEffort = shared.ReasoningEffortNone
	} else if temperature > 0 {
		params.Temperature = openai.Float(temperature)
	}
	if maxCompletionTokens > 0 {
		// max_tokens 已废弃、且会被当前推理模型（含 GPT-5.6 家族）拒绝。
		// 对所有上游优先用替代字段；下面那段窄兼容重试兜住还没实现的
		// 老网关。
		params.MaxCompletionTokens = openai.Int(maxCompletionTokens)
	}

	// 首选请求与其可选的兼容重试共用同一个 deadline，老网关没法把调用方的
	// 时间预算翻倍。
	ctx, cancel := withDefaultTimeout(ctx)
	defer cancel()

	// 一些较老的 OpenAI 兼容网关还没实现这两个现代字段中的一个或两个。
	// 只在上游明确点名「不支持的参数」时协商：校验发生在生成之前，而每个
	// 字段在共享 deadline 下至多被移除/替换一次。
	//
	// 这个循环是参数兼容协商，NOT 错误重试，且刻意独立于 Config.MaxRetries：
	// 它只在 SDK 从不重试的 400 上触发，上限恒为 2、与传输预算无关。两者确实
	// 会叠加——下面每次尝试各自带着自己的传输预算，所以一次调用可能花到两次
	// 协商请求加最后一次尝试的 MaxRetries+1。
	var completion *openai.ChatCompletion
	for compatibilityRetries := 0; ; compatibilityRetries++ {
		var err error
		completion, err = c.Chat(ctx, params)
		if err == nil {
			break
		}
		if compatibilityRetries >= 2 {
			return "", err
		}

		switch {
		case params.MaxCompletionTokens.Valid() && isUnsupportedParameter(err, "max_completion_tokens"):
			params.MaxCompletionTokens = param.Opt[int64]{}
			params.MaxTokens = openai.Int(maxCompletionTokens)
		case params.ReasoningEffort != "" && isUnsupportedParameter(err, "reasoning_effort"):
			params.ReasoningEffort = ""
		default:
			return "", err
		}
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("llm: upstream returned no choices")
	}
	choice := completion.Choices[0]
	if choice.FinishReason == "length" {
		return "", errors.New("llm: upstream reached the max completion token limit before producing complete JSON")
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", errors.New("llm: upstream returned empty JSON content")
	}
	return choice.Message.Content, nil
}

// isUnsupportedParameter 判断某错误是不是「上游明确说该参数不受支持」的 400。
func isUnsupportedParameter(err error, parameter string) bool {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) ||
		apiErr.StatusCode != http.StatusBadRequest ||
		apiErr.Param != parameter {
		return false
	}
	return apiErr.Code == "unsupported_parameter" ||
		(apiErr.Code == "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(apiErr.Message)), "unsupported parameter"))
}

// isGPT56Family 判断模型是否属于 GPT-5.6 家族（"gpt-5.6" 或 "gpt-5.6-" 前缀）。
func isGPT56Family(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "gpt-5.6" || strings.HasPrefix(model, "gpt-5.6-")
}

// withDefaultTimeout 在 ctx 已有 deadline 时原样返回（配一个空操作的 cancel），
// 否则返回一个以 defaultRequestTimeout 为界的子 context。
func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultRequestTimeout)
}

// 编译期断言 *http.Client 满足 option.HTTPClient，让调用方可以把普通
// *http.Client 当测试缝传进来。
var _ option.HTTPClient = (*http.Client)(nil)
