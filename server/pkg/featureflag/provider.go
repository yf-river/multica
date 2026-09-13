package featureflag

// 本文件职责：定义功能开关的两块地基——「求值结果」Decision（含理由 Reason）
// 与「配置后端抽象」Provider 接口。业务代码只认识 Decision；不同的配置来源
// （静态 YAML、环境变量、链式组合）都通过实现 Provider 接入。

import "context"

// Reason 说明一次 Decision 为什么给出这个结果。取值是可直接观测的字符串，
// 方便暴露到元数据接口和结构化日志里。
type Reason string

const (
	// ReasonStatic 表示 provider 无条件返回了一个值
	// （命中 Rule.Default、Allow 命中、Deny 命中或 Variant 查表）。
	ReasonStatic Reason = "static"

	// ReasonPercent 表示结果来自确定性的百分比发布分桶。
	// 同一对 (key, identifier) 永远落进同一个桶。
	ReasonPercent Reason = "percent"

	// ReasonOverride 表示套用了单次请求级覆盖（例如调试 header 或 cookie）。
	// 覆盖优先级高于普通规则，所以绝不能暴露给不受信任的调用方。
	ReasonOverride Reason = "override"

	// ReasonDefault 表示没有任何 provider 命中该 key，返回了调用方传入的默认值。
	// 这是调用方在「默认值被使用」时唯一会看到的 Reason。
	ReasonDefault Reason = "default"

	// ReasonError 表示 provider 尝试求值但失败了（例如环境变量格式非法）。
	// 此时返回默认值，并记下 error 原因供诊断。
	ReasonError Reason = "error"
)

// Decision 是一次开关求值的结构化结果。调用方一般用 Service.IsEnabled 或
// Service.Variant，它们会把 Decision 折叠成单个值；Decision 本身暴露给
// 诊断接口和测试用。
type Decision struct {
	// Key 是被求值的开关标识。
	Key string

	// Enabled 是决策的布尔投影。对 variant 类开关，当 Variant 非空且不等于
	// "off" 时为 true。
	Enabled bool

	// Variant 是 provider 产出的原始值。布尔开关用 "on"/"off"；variant 类
	// 开关用任意标识符，例如 "control"、"experiment-v2"。
	Variant string

	// Reason 记录这条决策的依据（见上方 Reason 常量）。
	Reason Reason

	// Source 是产出该决策的 provider 名字；没有任何 provider 命中时是
	// "default"。排查 ChainProvider 里到底哪一层配置在生效时很有用。
	Source string
}

// Provider 是功能开关 Service 背后的配置后端。
// 实现必须并发安全：Service 会在多个 goroutine 里不加额外锁地读 provider。
//
// Lookup 在 provider「认识」这个 key 时返回 (decision, true)；
// 不认识时返回 (零值, false)。调用方必须信这个布尔值而不是 Decision 内容，
// 因为 found=false 时 Decision 只是零值。
type Provider interface {
	// Lookup 用传入的上下文对单个开关求值。实现绝不应 panic；内部失败时
	// 应返回 Reason=ReasonError、found=true 的 Decision，这样 Service 能
	// 记下这次失败，而不会漏到更宽泛的下层 provider。
	Lookup(ctx context.Context, key string) (decision Decision, found bool)

	// Name 返回稳定、可读的标识符，用于 Decision.Source 与诊断接口。
	// 同类型的不同 provider 实例可以同名；并不要求全局唯一。
	Name() string
}
