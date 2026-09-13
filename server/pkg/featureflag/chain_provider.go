package featureflag

// 本文件职责：实现 ChainProvider——把多个 Provider 按优先级串成一条链，
// 求值时返回第一个命中的结果。前面优先级高，所以装配顺序应是「最具体到最
// 通用」：单请求覆盖、环境变量、数据库、静态配置。

import "context"

// ChainProvider 组合多个 provider，返回第一个命中 key 的结果。
// 排在前面的 provider 优先，所以调用方应按「最具体 → 最通用」排序：
// 单请求覆盖、环境变量、数据库、静态配置。
//
// 包了零个 provider 的 ChainProvider 也是合法的，它永远返回 (零值, false)，
// 让 Service 回落到调用方的默认值。
type ChainProvider struct {
	providers []Provider
}

// NewChainProvider 返回一个按传入顺序求值的 ChainProvider。
// nil provider 会被静默跳过，这样调用方可以把可选的 provider 直接传进来，
// 而不用在每个接入点都做一次 nil 判断。
func NewChainProvider(providers ...Provider) *ChainProvider {
	cp := &ChainProvider{providers: make([]Provider, 0, len(providers))}
	for _, p := range providers {
		if p != nil {
			cp.providers = append(cp.providers, p)
		}
	}
	return cp
}

// Name 实现 Provider 接口。
func (*ChainProvider) Name() string { return "chain" }

// Lookup 实现 Provider 接口。按注册顺序依次问被包裹的 provider，
// 返回第一个产出决策的结果。
func (cp *ChainProvider) Lookup(ctx context.Context, key string) (Decision, bool) {
	for _, p := range cp.providers {
		if d, ok := p.Lookup(ctx, key); ok {
			return d, true
		}
	}
	return Decision{}, false
}

// Providers 返回被包裹 provider 的一个快照。切片本身是拷贝，
// 但 Provider 值是共享的，不得改动。
func (cp *ChainProvider) Providers() []Provider {
	out := make([]Provider, len(cp.providers))
	copy(out, cp.providers)
	return out
}
