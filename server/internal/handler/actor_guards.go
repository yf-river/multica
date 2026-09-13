// 本文件实现「只有真人才能访问」的守卫：RequireHumanActor 中间件与它背后的
// isMachineCredentialActor 判定。它把机器凭据（agent 任务令牌 mat_、云节点 PAT mcn_）
// 发出的请求与真人请求区分开，供账单等账号级、只该由真人操作的范围使用。
package handler

import (
	"net/http"
)

// RequireHumanActor 是一个 chi 风格的中间件：拒绝用机器凭据认证的请求——目前指
// mat_ 任务令牌与 mcn_ 云节点 PAT。它服务于授权模型是「人类 owner 授权了这个操作」
// 而非「任何持有 owner 凭据的人都能操作」的端点。
//
// 为什么需要这道闸（请细读——这里的鉴权很微妙）：
//
// 通用 Auth 中间件（server/internal/middleware/auth.go）会把四种 bearer 格式归一到同一
// 形态——盖上 `X-User-ID` 头——这样下游 handler 不必关心调用方用了哪种令牌：
//
//   - JWT cookie / mul_ PAT → X-User-ID = 真人的用户 id，X-Actor-Source 留空。
//   - mat_ 任务令牌 → X-User-ID = 所属真人的用户 id，另带 X-Agent-ID、X-Task-ID，
//     以及由服务端权威盖上的 `X-Actor-Source: task_token`。
//   - mcn_ 云节点 PAT → X-User-ID = 所属真人的用户 id，另带 `X-Actor-Source:
//     cloud_pat`。该令牌让云端 runtime 的 EC2 节点以 owner 身份行事——授权意义上与
//     mat_ 同类（机器代表 owner 运行代码）。
//
// mat_ 与 mcn_ 设计（分别是 MUL-2600 与云节点 PAT 的故事）刻意如此：agent/节点发出的
// 每个请求都视为 owner 本人的操作，于是它们能像 owner 那样发评论、认领 issue、注册
// runtime 等。这对 issue/评论/chat 范围是对的——这些范围受 workspace 成员身份、任务或
// runtime 绑定约束。
//
// 但对账号级范围就不对了：
//
//   - 账单余额 / 交易 / 批次 / 充值列表是用户维度的。跑着的 agent 或云节点可以不经过
//     owner 批准就读走 owner 的钱包状态。
//
//   - 创建 checkout / portal 会话能移动资金。一旦机器凭据失窃——prompt 注入、恶意的
//     MCP 工具、草稿数据里逃逸的引号或节点逃逸——攻击者就能为受控邮箱开 checkout，
//     或打开泄露订阅/支付方式状态的 Billing Portal 会话。
//
// `X-Actor-Source` 只由服务端设置。Auth 中间件会先删掉任何客户端提供的值
// （见 auth.go 里的 `r.Header.Del("X-Actor-Source")`），然后只在 mat_ 与 mcn_ 分支重新
// 盖上。所以查这个头是判断「请求是否来自机器凭据」的安全、快速、单真源方式——不必重查
// 令牌表。
//
// 这里刻意不用 h.resolveActor()：
//
//   - resolveActor 的首要职责是「agent 还是 member」的归类，服务于归属/署名（issue
//     创建者、评论作者等）。它还有一条信任 X-Agent-ID + X-Task-ID 的兜底路径以兼容老
//     CLI 流程；那条兜底对解析署名有效，但在这里无关——账单授权只需要严格的「机器
//     凭据 → 拒绝」，别的都不要。
//   - resolveActor 需要 workspaceID 参数；账单路由没有 workspace 上下文，为调用它而
//     硬传一个反而误导。
//   - resolveActor 目前不归类 mcn_ 云节点 PAT——云节点不作用于需要作者署名的
//     workspace 资源。仅为了在这复用而把归类硬塞进 resolveActor 是错误的耦合。
//
// 用法：在 chi 路由组上 `r.Use(handler.RequireHumanActor)`。刻意不接进 router 的主 Auth
// 链——别处的默认契约（issue、chat 等）是「agent 与真人可互换」，加全局闸会误伤合法
// agent 流量。只在真正只允许真人的范围挂它。
//
// 扩展约定：今后若给 auth.go 新增机器凭据分支（例如假想的 service-account 令牌），必须
// 盖上不同的 X-Actor-Source 值，并同时按这道闸评审。下面的黑名单刻意显式——放行未知
// actor source 是有意为之（见 TestRequireHumanActor_IgnoresUnknownActorSource），但新增
// 取值的那一刻，正是决定它是「类真人」还是「类机器」的时刻。
func RequireHumanActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMachineCredentialActor(r) {
			writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isMachineCredentialActor 集中权威的 actor-source 检查，让敏感 handler 除了路由中间件
// 之外还能留一道 fail-closed 的兜底。未知的 actor source 在它的认证分支被这里显式归类
// 之前，一直按「类真人」处理。
func isMachineCredentialActor(r *http.Request) bool {
	// X-Actor-Source 只由服务端设置：Auth 中间件会先剥掉任何客户端提供的值再盖上自己的，
	// 所以这里识别出的取值是权威的。
	switch r.Header.Get("X-Actor-Source") {
	case "task_token", "cloud_pat":
		return true
	default:
		return false
	}
}
