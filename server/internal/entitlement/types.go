// types.go 定义 entitlement 包的核心类型与判词：Gate（某个执行点应当怎么做）、
// Decision（带着可供审计的来源信息）、Provider/Observer 两个接口，以及一批可复用的
// 常量（门类型、动作、原因）。这些类型不含任何 Cloud 私有输入。
package entitlement

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SchemaVersion 是策略 schema 版本号（本地解析策略 JSON 时使用）。
const SchemaVersion = 1

// GateName 标识一种可被强制执行的「门」。
type GateName string

const (
	GateIssueCount    GateName = "issue_count"    // issue 数量门
	GateAutopilotRuns GateName = "autopilot_runs" // autopilot 运行次数门
)

func (n GateName) valid() bool {
	switch n {
	case GateIssueCount, GateAutopilotRuns:
		return true
	default:
		return false
	}
}

// Action 是某个门给出的动作语义：关闭 / 观察 / 强制执行。
type Action string

const (
	ActionOff     Action = "off"     // 不执行任何限制
	ActionObserve Action = "observe" // 只记录，不拦
	ActionEnforce Action = "enforce" // 强制执行限制
)

// Reason 是作出某条 Decision 的原因（便于审计，也便于区分「正常」与「降级」）。
type Reason string

const (
	ReasonDisabled          Reason = "disabled"           // 本包整体禁用
	ReasonInvalidWorkspace  Reason = "invalid_workspace"  // workspace 无效
	ReasonUnknownGate       Reason = "unknown_gate"       // 不认识的 gate
	ReasonCacheFresh        Reason = "cache_fresh"        // 缓存仍新鲜，直接命中
	ReasonRefreshed         Reason = "refreshed"          // 刚刷新过
	ReasonStale             Reason = "stale"              // 缓存已陈旧
	ReasonUnavailable       Reason = "unavailable"        // Cloud 不可用
	ReasonInvalidPolicy     Reason = "invalid_policy"     // 策略内容非法
	ReasonVersionRegression Reason = "version_regression" // 版本回退
	ReasonStub              Reason = "stub"               // 打桩（测试）
)

// Gate 是对一个通用执行点的「有效指令」。额度与周期边界都来自 Cloud；本包不做推导。
type Gate struct {
	Action      Action
	Limit       *int
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	ResetAt     *time.Time
}

// Decision 携带足够的来源信息，供消费者审计「某个门为什么被执行/观察/禁用」，
// 且不暴露 Cloud 的私有输入。
type Decision struct {
	Gate                Gate
	Reason              Reason
	PolicyRevision      int64 // Cloud 策略修订号
	SubscriptionVersion int64 // 订阅版本号
	CloudValidUntil     time.Time
}

// Provider 是 issue-count 与 autopilot 消费者唯一需要的接口。它刻意没有 error 返回：
// 任何失败都用「失败即打开(fail-open)」的 Decision 表示——ActionOff（或陈旧快照仍可
// 观察时给 ActionObserve，因已无法强制执行）。
type Provider interface {
	Gate(ctx context.Context, workspaceID uuid.UUID, name GateName) Decision
}

// Observer 是低频的埋点缝隙。实现方绝不能用 workspace ID 或策略值当指标标签。
type Observer interface {
	RecordEntitlementCache(outcome string)
	RecordEntitlementRefresh(outcome string, durationSeconds float64)
	RecordEntitlementDecision(gate, action, reason string)
	RecordEntitlementVersionRegression()
}

// offDecision 构造一条「动作=off + 给定原因」的决策。
func offDecision(reason Reason) Decision {
	return Decision{Gate: Gate{Action: ActionOff}, Reason: reason}
}

// cloneDecision 深拷贝一条 Decision（Gate 里的指针字段也要复制）。
func cloneDecision(in Decision) Decision {
	in.Gate = cloneGate(in.Gate)
	return in
}

// cloneGate 深拷贝 Gate：把每个指针字段都复制出独立的一份，防止外层改写共享状态。
func cloneGate(in Gate) Gate {
	out := in
	if in.Limit != nil {
		value := *in.Limit
		out.Limit = &value
	}
	if in.PeriodStart != nil {
		value := *in.PeriodStart
		out.PeriodStart = &value
	}
	if in.PeriodEnd != nil {
		value := *in.PeriodEnd
		out.PeriodEnd = &value
	}
	if in.ResetAt != nil {
		value := *in.ResetAt
		out.ResetAt = &value
	}
	return out
}
