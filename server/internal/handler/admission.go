// 本文件定义「同步入队（执行准入）」的统一契约（MUL-4525）：每个同步触发入口都必须用
// 同一形态回答「这次运行最终是 queued / coalesced / deferred / blocked」。包含跨层对齐的
// 准入状态与原因码（原因码别名自 dispatch 包，服务与 handler 不会漂移）、对调用方可见的
// 目标与结果结构，以及把「被拒绝」写回成结构化 403/409 响应和通用回退文案的辅助函数。
package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/dispatch"
)

// 统一「执行准入」契约（MUL-4525）。
//
// 每个同步入队入口（评论 @ 提及、autopilot 手动"立即运行"、issue 指派/升级/批量、手动
// 重跑、直接聊天）都要以同样的形态回答同一个问题：面对一个显式点名了执行目标的用户，
// 这次运行是被 `queued`（入队）、`coalesced`（合并进已有任务）、`deferred`（暂缓）还是
// `blocked`（被拒）了？静默无动作永远不可接受。
//
// 该契约要守护两条不变量：
//
//  1. 「业务对象是否已写入（评论已保存、issue 已更新）」与「agent 是否真的被触发」是两件
//     独立的事实。评论可以在某个 @ 提及被拒的同时照样持久化——调用方必须能表达部分成功。
//  2. 目标被拒的原因只以稳定、可本地化、可枚举的 code 对外暴露。绝不能向看不到该目标的
//     调用方泄露「是否存在某个私有 agent、它叫什么、owner 是谁」。确切原因只进受限服务端
//     日志，不上网络。

// DispatchStatus 是单次准入/入队尝试的领域层结果。
type DispatchStatus string

const (
	// DispatchQueued：新运行已入队。
	DispatchQueued DispatchStatus = "queued"
	// DispatchCoalesced：该触发合并进了同一目标已经 pending 的任务，而不是新建一个重复运行。
	DispatchCoalesced DispatchStatus = "coalesced"
	// DispatchDeferred：被接纳但刻意暂不启动（例如停在 backlog 直到升级的 issue，或
	// suppress_run）。
	DispatchDeferred DispatchStatus = "deferred"
	// DispatchBlocked：运行被拒绝。ReasonCode 说明原因。
	DispatchBlocked DispatchStatus = "blocked"
)

// DispatchReasonCode 是对外（wire）的准入原因码。它别名自 dispatch 包里权威的跨层枚举，
// 让「源头定因的服务」与「序列化它的 handler」永不漂移。可以新增码；客户端必须把未知码当
// 通用失败处理（它们用 default 分支兜底）。任何码都绝不编码调用者无权看到的目标的存在性、
// 名字或 owner。
type DispatchReasonCode = dispatch.ReasonCode

const (
	ReasonQueued                = dispatch.ReasonQueued
	ReasonCoalesced             = dispatch.ReasonCoalesced
	ReasonDeferred              = dispatch.ReasonDeferred
	ReasonInvocationNotAllowed  = dispatch.ReasonInvocationNotAllowed
	ReasonTargetUnavailable     = dispatch.ReasonTargetUnavailable
	ReasonRuntimeOffline        = dispatch.ReasonRuntimeOffline
	ReasonRuntimeUnusable       = dispatch.ReasonRuntimeUnusable
	ReasonAgentRuntimeRequired  = dispatch.ReasonAgentRuntimeRequired
	ReasonAttributionBlocked    = dispatch.ReasonAttributionBlocked
	ReasonAlreadyActive         = dispatch.ReasonAlreadyActive
	ReasonSelfTriggerSuppressed = dispatch.ReasonSelfTriggerSuppressed
	ReasonInternalError         = dispatch.ReasonInternalError
)

// DispatchTarget 是调用方可读的执行目标引用。只有当调用方被允许看到目标时才填 Name——
// 被拒的私有 agent 调用只回 Type/ID（本就来自调用方自己的请求），绝不回调用方本无权
// 知道的名字。
type DispatchTarget struct {
	Type string `json:"type"` // "agent" | "squad"
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// DispatchOutcome 是每个同步入队入口返回的统一「按目标」结果。它在线路上是追加式的：
// 忽略它的旧客户端照常工作。产出了运行/任务时 TaskID / RunID 才会被设置。
type DispatchOutcome struct {
	Status     DispatchStatus     `json:"status"`
	ReasonCode DispatchReasonCode `json:"reason_code"`
	Target     *DispatchTarget    `json:"target,omitempty"`
	TaskID     *string            `json:"task_id,omitempty"`
	RunID      *string            `json:"run_id,omitempty"`
}

// dispatchBlockedResponse 是被拒的同步入队（403/409）的结构化响应体。`error` 是给只读
// legacy 字段的旧客户端用的通用英文回退文案；`reason_code` 是新客户端做本地化的稳定机器
// 可读码。两者都不泄露私有目标细节。
type dispatchBlockedResponse struct {
	Error      string             `json:"error"`
	ReasonCode DispatchReasonCode `json:"reason_code"`
}

// writeDispatchBlocked 写出结构化的「准入被拒」错误。HTTP 状态码表达大类（403 权限、
// 409 冲突）；reason_code 表达稳定原因。任何调用方显式请求、且在写入前被拒的同步触发都
// 用它。
func (h *Handler) writeDispatchBlocked(w http.ResponseWriter, status int, code DispatchReasonCode) {
	writeJSON(w, status, dispatchBlockedResponse{
		Error:      dispatchBlockedFallbackMessage(code),
		ReasonCode: code,
	})
}

// dispatchBlockedFallbackMessage 是与原因码配对的 legacy `error` 文案。刻意通用且不点
// 名：它必须能安全地展示给「无权知道目标是否存在」的调用方。
func dispatchBlockedFallbackMessage(code DispatchReasonCode) string {
	switch code {
	case ReasonInvocationNotAllowed:
		return "you don't have permission to use this target"
	case ReasonTargetUnavailable:
		return "the target is unavailable"
	case ReasonRuntimeOffline:
		return "the target's runtime is offline"
	case ReasonRuntimeUnusable:
		return "the target's agent CLI cannot run on its machine"
	case ReasonAgentRuntimeRequired:
		return "the target needs a runtime"
	case ReasonAttributionBlocked:
		return "the run couldn't be attributed to a responsible member"
	case ReasonAlreadyActive:
		return "a run is already active for this target"
	default:
		return "the run was blocked"
	}
}
