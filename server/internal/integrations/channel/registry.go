package channel

import (
	"fmt"
	"sort"
	"sync"
)

// 文件角色：Type → Factory 的注册表。它是"新增平台 = 在这里注册一个 factory"机制的
// 载体：核心通过 Registry.Build 按类型实例化渠道，永远不用改核心代码。

// ErrUnknownType 在 Registry.Build 遇到"没注册该 Type 的 Factory"时返回。调用方可以用
// errors.Is 判断。
var ErrUnknownType = fmt.Errorf("channel: no factory registered for type")

// Registry 把渠道 Type 映射到能构建它的 Factory。加平台就是"在这里注册一个 factory"，
// 绝不是"改核心"。Registry 支持并发使用（内部读写锁）。
//
// 注册语义是"后写者胜"：对一个已有 Factory 的 Type 再注册，会静默替换旧的。这与参考设计
// 的插件注册模式一致（MUL-3506）——最后一个注册某类型的适配器获胜，因此部署方可以在
// 启动后用自研适配器覆盖内置适配器，不需要额外的删除步骤。
type Registry struct {
	mu        sync.RWMutex
	factories map[Type]Factory
}

// NewRegistry 返回一个空的、可用的 Registry。
func NewRegistry() *Registry {
	return &Registry{factories: make(map[Type]Factory)}
}

// Register 把 factory 绑定到 t，替换掉之前注册在 t 上的 factory（后写者胜）。nil factory
// 或空 Type 会被忽略——记录两者只会让 Build 必然失败，所以 Registry 拒绝收录。
func (r *Registry) Register(t Type, factory Factory) {
	if t == "" || factory == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[t] = factory
}

// Lookup 返回 t 注册的 Factory 及是否存在。
func (r *Registry) Lookup(t Type) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[t]
	return factory, ok
}

// Build 用 cfg.Type 注册的 Factory 实例化一条 Channel。没注册对应 Factory 时返回
// ErrUnknownType（包装过、带类型名）；否则原样返回 Factory 的结果。
func (r *Registry) Build(cfg Config) (Channel, error) {
	factory, ok := r.Lookup(cfg.Type)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, cfg.Type)
	}
	return factory(cfg)
}

// Types 返回按字典序排序的已注册类型，保证多次调用的结果稳定（map 迭代顺序不稳定）。
// 用于诊断，以及枚举某次部署支持哪些平台。
func (r *Registry) Types() []Type {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Type, 0, len(r.factories))
	for t := range r.factories {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
