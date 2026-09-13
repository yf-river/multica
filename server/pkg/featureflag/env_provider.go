package featureflag

// 本文件职责：实现 EnvProvider——从进程环境变量读开关配置。它是「急停开关、
// 本地开发、不重新部署就要翻转的运维开关」这类场景的主力，规则值形如
// FF_<KEY>=true / 42% / variant。

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// EnvProvider 从进程环境变量读取开关配置。它面向紧急覆盖、本地开发，以及
// 「不需要重新部署就能翻转的急停开关」这类 Ops 开关——这正是运维开关
// （Ops Toggles）被发明出来要解决的问题。
//
// 变量名 = Prefix + 开关 key 转成的大写下划线形式。Prefix 为 "FF_" 时，
// 名为 "checkout_new_payment_flow" 的开关对应环境变量
// FF_CHECKOUT_NEW_PAYMENT_FLOW。
//
// 支持的值格式（大小写不敏感）：
//
//	"true", "on", "1", "yes"      -> Enabled=true,  Variant="on"
//	"false", "off", "0", "no"     -> Enabled=false, Variant="off"
//	""                            -> Enabled=false, Variant="off"  (显式禁用)
//	"42%"                         -> 确定性百分比发布
//	其它任意非空值                  -> 当作 variant 标识符
//	                                 (Enabled=true, Variant=<原值>)
//
// 非法的百分比值（负数、>100、非数字）会产出 Reason=ReasonError 的 Decision。
// Service 仍把它当作一条真实决策，不会漏到更宽泛的下层 provider——设置
// FF_FOO=abc% 的运维工程师期待的是被告知「这里有问题」，而不是覆盖悄悄消失。
type EnvProvider struct {
	// Prefix 会在每次查表时拼在最前面。空串等于不启用前缀，
	// 但很少是你真正想要的。
	Prefix string

	// lookup 可被测试替换。变量「已设置」（哪怕设成空串）时返回
	// (value, true)；「未设置」时返回 ("", false)。默认是 os.LookupEnv。
	lookup func(string) (string, bool)
}

// NewEnvProvider 返回一个带给定前缀的 EnvProvider。传 "FF_" 即 multica
// 惯例前缀。
func NewEnvProvider(prefix string) *EnvProvider {
	return &EnvProvider{Prefix: prefix, lookup: os.LookupEnv}
}

// Name 实现 Provider 接口。
func (*EnvProvider) Name() string { return "env" }

// Lookup 实现 Provider 接口。
func (p *EnvProvider) Lookup(ctx context.Context, key string) (Decision, bool) {
	envName := p.Prefix + flagKeyToEnv(key)
	get := p.lookup
	if get == nil {
		get = os.LookupEnv
	}
	raw, present := get(envName)
	if !present {
		return Decision{}, false
	}

	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Decision{
			Key:     key,
			Enabled: false,
			Variant: "off",
			Reason:  ReasonStatic,
			Source:  "env",
		}, true
	}

	if strings.HasSuffix(trimmed, "%") {
		pctStr := strings.TrimSuffix(trimmed, "%")
		pct, err := strconv.Atoi(strings.TrimSpace(pctStr))
		if err != nil || pct < 0 || pct > 100 {
			return Decision{
				Key:     key,
				Enabled: false,
				Variant: "off",
				Reason:  ReasonError,
				Source:  "env",
			}, true
		}
		ec := EvalContextFrom(ctx)
		ident, _ := ec.Lookup("user_id")
		enabled := inPercent(key, ident, pct)
		return Decision{
			Key:     key,
			Enabled: enabled,
			Variant: boolToVariant(enabled),
			Reason:  ReasonPercent,
			Source:  "env",
		}, true
	}

	switch strings.ToLower(trimmed) {
	case "true", "on", "1", "yes":
		return Decision{
			Key:     key,
			Enabled: true,
			Variant: "on",
			Reason:  ReasonStatic,
			Source:  "env",
		}, true
	case "false", "off", "0", "no":
		return Decision{
			Key:     key,
			Enabled: false,
			Variant: "off",
			Reason:  ReasonStatic,
			Source:  "env",
		}, true
	}

	// 其余任何值都当作 variant 标识符。这里绝不能再往下解析 variant——
	// 每个 variant 的含义只有调用方自己知道。
	return Decision{
		Key:     key,
		Enabled: true,
		Variant: trimmed,
		Reason:  ReasonStatic,
		Source:  "env",
	}, true
}

// flagKeyToEnv 把开关 key 转成它对应的环境变量名：全部大写，任何非字母数字
// 的连续片段折叠成单个下划线。这个转换刻意是「有损」的（大小写不敏感、合并
// 标点串），所以 "checkout.newPayment" 与 "checkout-new-payment" 会路由到
// 同一个环境变量名；同一个 key 的不同写法想要各自独立的环境变量，就请用
// 不同的开关 key。
func flagKeyToEnv(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	prevUnderscore := false
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
