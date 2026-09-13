// 任务运行类别（taskKind）的定义与分类：把一次运行要走的"分发路径"
// 归类成互斥的几种，决定精简版 runtime brief 按哪种形态组装。

package execenv

// taskKind 标识给定 TaskContextForEnv 的 runtime brief 该沿哪条分发路径
// 组装。它被 `buildMetaSkillContentSlim` 使用（即 MUL-3560 的精简 brief；
// 曾经用来把它与旧的冗长 brief 二选一的 `runtime_brief_slim` 开关已在
// MUL-4297 退役，因此现在它是唯一的 brief）。
//
// 共四种类别，实践中互斥。classifyTask 记录了一旦未来某个调用方不小心
// 违反互斥时的裁决规则。
type taskKind int

const (
	// kindLifeCognition 是受管的后台 Life 任务。它没有仓库、issue 或一般
	// 意义上的平台改动面。
	kindLifeCognition taskKind = iota
	// kindIssue：本次运行作用于一个真实的 Multica issue。它刻意"不"区分
	// "评论触发"与"指派触发"两种运行。
	//
	// 直到 MUL-5377 前它们曾是两种类别。拆分它们会让渲染出的 brief——
	// Claude Code 会把它加载进 messages[0]、位于整段对话之前——在同一
	// 会话恢复后"首次（指派时）运行"与"后续每次（评论）运行"之间产生差异，
	// 从而在每次恢复时使整段历史的提示缓存失效。而"本次究竟由哪种触发"
	// 属于每轮（per-turn）状态，现在随每轮的用户消息
	// （daemon.BuildPrompt）传递，该消息被追加在已缓存的公共前缀之后。
	// 参见 runtime_config_sections.go:writeWorkflowIssue。
	kindIssue
	// kindAutopilotRunOnly：autopilot 以纯运行模式（未创建也未关联 issue）
	// 触发的一次运行。
	kindAutopilotRunOnly
	// kindQuickCreate：一次性"从自然语言提示创建 issue"的任务。
	kindQuickCreate
	// kindChat：交互式聊天会话，没有 issue。
	kindChat
)

// classifyTask 把 TaskContextForEnv 映射成精简 brief 应为其组装的唯一
// taskKind。优先级（供撞车裁决时参考；实际上 daemon 从不同时设置两个
// 具体类别标志）：chat → quick-create → autopilot run-only → issue。
//
// 它刻意不去读 ctx.TriggerCommentID：分类结果绝不能在同一会话恢复后的
// 各次运行之间变化，否则 brief 的字节就会变，messages[0] 往后的提示缓存
// 也就全部失效了（MUL-5377）。
func classifyTask(ctx TaskContextForEnv) taskKind {
	switch {
	case ctx.LifeJobID != "":
		return kindLifeCognition
	case ctx.ChatSessionID != "":
		return kindChat
	case ctx.QuickCreatePrompt != "":
		return kindQuickCreate
	case ctx.AutopilotRunID != "":
		return kindAutopilotRunOnly
	default:
		return kindIssue
	}
}

// hasIssueContext 对"运行在真实 Multica issue 上、因此可以读取/钉住
// issue 范围状态"的类别返回 true。精简分发器用这个谓词门控下面两节：
//
//   - Issue Metadata（issue 元数据）
//   - Sub-issue Creation（子 issue 创建）
//
// 两者在"无 issue 的类别"（chat / quick-create / autopilot run-only）上
// 都没有意义，要么渲染出空壳正文，要么把 agent 引向注定失败的 CLI 调用。
// 注意它是"基于类别"的谓词，而不是对 ctx.IssueID 的检查——kindIssue
// 按构造必然携带 issue id（否则 daemon 拒绝分发它），而其他三种类别
// 永远不会带。
func (k taskKind) hasIssueContext() bool {
	return k == kindIssue
}
