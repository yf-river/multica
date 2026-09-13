package featureflag

// 本文件职责：实现 StaticProvider——线程安全的内存态 Provider，规则要么
// 编程式写入、要么从配置文件加载。它定义了单条规则的求值语义 Rule（Deny >
// Allow > Percent > Default 的命中顺序），是生产环境的推荐基线 provider。

import (
	"context"
	"slices"
	"sync"
)

// Rule 描述 StaticProvider 如何求值单个开关。所有字段可选；空的 Rule
// 对所有人求值为 Default（false）。
//
// 求值顺序（先命中者胜）：
//
//  1. Deny：EvalContext 里 DenyBy（默认 "user_id"）对应的值命中 Deny 列表
//     任一项，开关为 OFF。
//  2. Allow：值命中 AllowBy（默认 "user_id"）对应列表任一项，开关为 ON。
//  3. Percent：若 Percent 非 nil 且 (key, identifier) 的分桶落在
//     Percent.Percent 内，开关为 ON。
//  4. Default：以上都没命中就返回它。
//
// Allow / Deny 刻意做成两份独立列表（而不是单个 targeting 谓词），因为运维上
// 它们覆盖不同的场景——Allow 是「仅内部用户」，Deny 是「对这些租户急停」——
// 分开写方便在代码仓库里审计。
type Rule struct {
	// Default 是没有 targeting 规则命中时返回的值。
	Default bool

	// Variant 是规则求值为 enabled=true 时返回的 variant 标识。
	// 多臂实验时把 Variant 设为实验臂标识（如 "experiment-v2"）；
	// 纯开/关开关留空即可。
	//
	// 当规则求值为 enabled=false（默认关、命中 Deny、percent 未命中……）时，
	// 最终 Decision 的 Variant 永远是规范的 "off"。这是刻意为之：
	// 否则一个以 Variant("checkout_algo", "control") 分支的调用方，即使
	// 用户根本没滚进实验组，也会被路由进实验臂。
	Variant string

	// Allow 是一组标识值，命中就强制把开关拨到 ON。
	Allow []string

	// AllowBy 是 Allow 查表用的 EvalContext 属性名。
	// 空串时默认 "user_id"。
	AllowBy string

	// Deny 是一组标识值，命中就强制把开关拨到 OFF。
	// Deny 压过 Allow。
	Deny []string

	// DenyBy 是 Deny 查表用的 EvalContext 属性名。
	// 空串时默认 "user_id"。
	DenyBy string

	// Percent 启用确定性的百分比发布。为 nil 时不应用百分比发布，
	// 回落到 Default。
	Percent *PercentRollout
}

// PercentRollout 描述一次确定性的百分比发布。
//
// 分桶由（开关 key, EvalContext 属性 By）用 FNV-1a 算出，保证同一标识在
// 不同进程、不同重启之间永远落进同一个桶。这正是调用方需要的：用户不会
// 在两次请求之间反复进出实验。
type PercentRollout struct {
	// Percent 是发布规模，取值范围 [0, 100]。0 表示关闭发布；
	// 100 表示对所有人开启。越界值会被钳制。
	Percent int

	// By 选择用作分桶标识的 EvalContext 属性。默认 "user_id"；
	// 要做 workspace 维度的发布就写 "workspace_id"。
	By string
}

// StaticProvider 是线程安全的内存态 Provider，规则要么编程式写入、要么从
// 配置文件加载。它是生产环境的推荐基线 provider：配置活在代码仓库里，随
// 二进制走 CD 流水线，改动需要一次部署——这正是 Martin Fowler 对发布开关
// （Release Toggles）和绝大多数权限开关（Permissioning Toggles）推荐的
// 持续交付姿态。
//
// 需要动态开关（急停、产品侧改的 A/B 实验）时，把 StaticProvider 和 DB 后端
// 的 Provider 一起放进 ChainProvider 组合使用。
type StaticProvider struct {
	mu    sync.RWMutex
	rules map[string]Rule
}

// NewStaticProvider 返回一个空的 StaticProvider。用 Set 或 LoadRules 填充。
func NewStaticProvider() *StaticProvider {
	return &StaticProvider{rules: map[string]Rule{}}
}

// Name 实现 Provider 接口。
func (*StaticProvider) Name() string { return "static" }

// Set 安装或替换 key 对应的规则。并发的写方会被串行化；读者（Lookup）
// 不会长时间阻塞写方。
func (p *StaticProvider) Set(key string, rule Rule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules[key] = rule
}

// LoadRules 把传入的 map 原子地替换掉 provider 里的全部规则。从配置文件
// 重载时用这个入口：若是逐个替换，部分失败可能让 provider 处于新旧配置
// 混杂的中间态。
func (p *StaticProvider) LoadRules(rules map[string]Rule) {
	clone := make(map[string]Rule, len(rules))
	for k, v := range rules {
		clone[k] = v
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = clone
}

// Keys 返回该 provider 已知开关 key 的排序集合，诊断接口会用到。
// 返回的切片是拷贝；改动它不影响 provider 内部。
func (p *StaticProvider) Keys() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.rules))
	for k := range p.rules {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Lookup 实现 Provider 接口。
func (p *StaticProvider) Lookup(ctx context.Context, key string) (Decision, bool) {
	p.mu.RLock()
	rule, ok := p.rules[key]
	p.mu.RUnlock()
	if !ok {
		return Decision{}, false
	}
	ec := EvalContextFrom(ctx)
	return evaluateRule(key, rule, ec), true
}

// evaluateRule 按 Deny > Allow > Percent > Default 的顺序对单条规则求值。
func evaluateRule(key string, rule Rule, ec EvalContext) Decision {
	// Deny 压过一切。急停开关必须在任何 targeting 命中时都能生效。
	denyBy := orDefault(rule.DenyBy, "user_id")
	if len(rule.Deny) > 0 {
		if v, ok := ec.Lookup(denyBy); ok && slices.Contains(rule.Deny, v) {
			return decisionFromRule(key, rule, false, ReasonStatic)
		}
	}

	allowBy := orDefault(rule.AllowBy, "user_id")
	if len(rule.Allow) > 0 {
		if v, ok := ec.Lookup(allowBy); ok && slices.Contains(rule.Allow, v) {
			return decisionFromRule(key, rule, true, ReasonStatic)
		}
	}

	if rule.Percent != nil {
		by := orDefault(rule.Percent.By, "user_id")
		identifier, _ := ec.Lookup(by)
		// 空的标识符仍然会得到确定性的桶（空串哈希出的桶是稳定的），
		// 但实际含义是「所有没有 id 的人都落进同一个桶」。这对百分比发布
		// 在边缘情况下的表现正是想要的：匿名用户按开关各自共享一个发布
		// 决策，而不是每个人得到一次均匀随机。
		if inPercent(key, identifier, rule.Percent.Percent) {
			return decisionFromRule(key, rule, true, ReasonPercent)
		}
		return decisionFromRule(key, rule, false, ReasonPercent)
	}

	return decisionFromRule(key, rule, rule.Default, ReasonStatic)
}

// decisionFromRule 把一条规则的求值结果打包成 Decision。
func decisionFromRule(key string, rule Rule, enabled bool, reason Reason) Decision {
	// Variant 策略：rule.Variant 是「开」时的 variant。规则求值为 false 时
	// 返回规范的 "off"，这样按 Variant() 分支的调用方不可能因为用户没有
	// 滚进实验组而误入实验臂。
	variant := boolToVariant(enabled)
	if enabled && rule.Variant != "" {
		variant = rule.Variant
	}
	return Decision{
		Key:     key,
		Enabled: enabled,
		Variant: variant,
		Reason:  reason,
		Source:  "static",
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
