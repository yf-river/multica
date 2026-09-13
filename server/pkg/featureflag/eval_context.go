package featureflag

// 本文件职责：定义 EvalContext——单次请求级的求值上下文，用于动态开关
// （百分比发布、按用户 allow/deny 列表）以及把它挂进 context.Context 的
// 存取辅助函数（WithEvalContext / EvalContextFrom）。

import "context"

// EvalContext 是单次请求的求值上下文，动态开关（百分比发布、按用户的
// allow/deny 列表）都会用到它。
//
// 所有字段可选。零值 EvalContext 合法，它不命中任何 targeting 规则——
// 百分比发布默认落到桶 0（永远关），allow/deny 查表静默落空。
type EvalContext struct {
	// UserID 是按用户 targeting 与默认百分比分桶用的规范标识。自由字符串，
	// 框架从不解析它。
	UserID string

	// WorkspaceID 标识发起请求的 multica workspace。workspace 维度的
	// 发布会用到。
	WorkspaceID string

	// Attributes 装调用方想暴露给规则的任何其它 targeting 属性，
	// 例如 "country"、"plan" 或 "client"。键区分大小写。
	Attributes map[string]string
}

// Lookup 按顺序 UserID、WorkspaceID、再 Attributes[name] 返回属性值。
// "user_id" 与 "workspace_id" 这两个耳熟能详的名字映射到专用字段，
// 规则可以直接按名字用它们，调用方不必再往 Attributes 里重复填一遍。
//
// 布尔返回值表示是否找到了「非空」的值，让调用方能区分「缺失」与
// 「显式为空」。
func (ec EvalContext) Lookup(name string) (string, bool) {
	switch name {
	case "user_id":
		if ec.UserID != "" {
			return ec.UserID, true
		}
		return "", false
	case "workspace_id":
		if ec.WorkspaceID != "" {
			return ec.WorkspaceID, true
		}
		return "", false
	}
	if ec.Attributes == nil {
		return "", false
	}
	v, ok := ec.Attributes[name]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

type evalContextKey struct{}

// WithEvalContext 返回一个携带 ec 的派生 context，之后可用 EvalContextFrom
// 取回。允许传零值 EvalContext，效果等同于清掉之前附着的上下文。
func WithEvalContext(parent context.Context, ec EvalContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, evalContextKey{}, ec)
}

// EvalContextFrom 取出先前用 WithEvalContext 附着的 EvalContext。
// context 里没有 EvalContext 时返回零值、绝不返回 nil，调用方可以无条件读字段。
func EvalContextFrom(ctx context.Context) EvalContext {
	if ctx == nil {
		return EvalContext{}
	}
	v, ok := ctx.Value(evalContextKey{}).(EvalContext)
	if !ok {
		return EvalContext{}
	}
	return v
}
