package featureflag

// 本文件职责：实现框架层的「开关路由」Service——业务代码只问它要某个开关的
// 决策，它再转问配置好的 Provider。含默认值兜底、nil Service 容错与
// ReasonError 告警逻辑。

import (
	"context"
	"log/slog"
)

// Service 是框架层的开关路由器。业务代码向 Service 要开关决策，Service 再
// 转查自己配好的 Provider。Service 并发安全，也是调用方唯一需要持有引用的类型。
//
// nil *Service 也是合法的，行为等同「所有开关都缺失」：每次调用都返回调用方
// 传入的默认值，Reason=ReasonDefault。这样调用方组合 Service 时不用先防 nil——
// 实践中 nil 解引用正是功能开关相关 panic 最常见的来源。
type Service struct {
	provider Provider
	logger   *slog.Logger
}

// Option 用来配置 Service 的可选行为。
type Option func(*Service)

// WithLogger 给 Service 挂一个结构化日志器，用于对「配错的功能开关配置」
// 发出告警。默认 Service 是安静的，方便在测试里内嵌而不污染输出。
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// NewService 返回由给定 provider 支撑的 Service。允许传 nil provider，
// 效果等同于「永远走默认值」；理由见包文档。
func NewService(provider Provider, opts ...Option) *Service {
	s := &Service{provider: provider}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// IsEnabled 返回指定开关在 ctx 携带的 EvalContext 下是否处于「开」。开关未知
// 或 provider 出错时返回传入的默认值，让业务代码可以放心上线——缺失的开关
// 永远不会让一次请求崩溃。
//
// IsEnabled 是业务代码里最常见的开关接入点：
//
//	if flags.IsEnabled(ctx, "billing_new_invoice_email", false) {
//	    return s.sendNewInvoiceEmail(ctx, invoice)
//	}
//	return s.sendLegacyInvoiceEmail(ctx, invoice)
func (s *Service) IsEnabled(ctx context.Context, key string, defaultVal bool) bool {
	return s.Decision(ctx, key, defaultVal).Enabled
}

// Variant 返回指定开关的原始 variant 值；没有任何 provider 命中时回落到
// defaultVal。多臂开关（A/B/C 测试、"control"/"experiment"/"holdout"）
// 用 Variant；简单开/关开关优先用 IsEnabled。
func (s *Service) Variant(ctx context.Context, key string, defaultVal string) string {
	d := s.decisionWithVariantDefault(ctx, key, defaultVal)
	return d.Variant
}

// Decision 返回一个开关的完整结构化决策。当没有 provider 命中 key 时，
// 用传入的布尔默认值同时填 Variant 与 Enabled。诊断接口和测试走这个入口，
// 以暴露 Reason 和 Source。
func (s *Service) Decision(ctx context.Context, key string, defaultVal bool) Decision {
	if s == nil || s.provider == nil {
		return defaultDecision(key, boolToVariant(defaultVal), defaultVal)
	}
	d, ok := s.provider.Lookup(ctx, key)
	if !ok {
		return defaultDecision(key, boolToVariant(defaultVal), defaultVal)
	}
	if d.Reason == ReasonError && s.logger != nil {
		s.logger.WarnContext(ctx, "feature flag provider returned an error decision",
			slog.String("key", key),
			slog.String("source", d.Source),
		)
	}
	d.Key = key
	return d
}

// decisionWithVariantDefault 是 Decision 的 variant 感知孪生版。它保持私有：
// 在意 Reason 的调用方可以直接用 Decision + IsEnabled；Variant 只是便利入口。
func (s *Service) decisionWithVariantDefault(ctx context.Context, key, defaultVariant string) Decision {
	if s == nil || s.provider == nil {
		return defaultDecision(key, defaultVariant, variantEnabled(defaultVariant))
	}
	d, ok := s.provider.Lookup(ctx, key)
	if !ok {
		return defaultDecision(key, defaultVariant, variantEnabled(defaultVariant))
	}
	d.Key = key
	return d
}

// Provider 暴露被包裹的 Provider，让诊断接口能遍历已知开关。
// 调用方绝不能改动返回的 Provider——该契约是只读的。
func (s *Service) Provider() Provider {
	if s == nil {
		return nil
	}
	return s.provider
}

func defaultDecision(key, variant string, enabled bool) Decision {
	return Decision{
		Key:     key,
		Enabled: enabled,
		Variant: variant,
		Reason:  ReasonDefault,
		Source:  "default",
	}
}

// boolToVariant 把布尔值转成布尔开关的规范 variant 字符串。
// 用 "on"/"off" 而非 "true"/"false"，是避免字符串型 provider（如环境变量）
// 与用户自己「布尔转文本」的值撞车。
func boolToVariant(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// variantEnabled 判断一个 variant 字符串是否投影为「开」。
// 只有空串和 "off"（以及 "false"/"0"）算关；其余一切值——包括
// "experiment-v2" 这样的任意 variant 标识——都算开。在意具体 variant 的
// 调用方应该直接用 == 比较。
func variantEnabled(v string) bool {
	switch v {
	case "", "off", "false", "0":
		return false
	}
	return true
}
