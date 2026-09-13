// 组装"评论触发任务"的回复指引文本：告诉 agent 本轮该去读哪些评论、
// 以及用哪个 --parent 以哪种安全方式回帖。这些块供每轮提示
// （daemon.buildCommentPrompt）与 CLAUDE.md 工作流（InjectRuntimeConfig）
// 共用，保证触发评论 id 与 --parent 值在各表面之间永不错位。

package execenv

import "fmt"

// BuildNewCommentsHint 生成 WARM 路径的"读评论"指引——agent 此前运行过该
// issue，因此存在 since 锚点。服务端统计的数量是"整 issue 范围"的（每个
// 线程，不只是触发的那一个），并且排除触发评论本身——因为其正文已被注入
// 提示词。它只携带数量与游标，绝不携带评论正文，这样服务端保持轻量，
// agent 按需自行拉取详情。
//
// agent 被告知 issue 范围内的总量，但被引导"先读触发它的（父）线程"，
// 而不是盲目追平所有线程。整 issue 范围的 `--since` 追平被降级为显式的
// "仅在需要时"回退。
//
// 自 MUL-5377 起，每轮提示（daemon.buildCommentPrompt）是唯一调用方——
// brief 不得携带按轮次变化的路由状态。
//
// 在冷启动（没有历史运行 → newCommentsSince 为空）、没有新评论
// （newCommentCount <= 0）或 issueID 为空时不渲染任何内容。这些情况下调用方
// 回退到 BuildResumedCommentsHint（当有活跃的历史会话时）或
// BuildColdCommentsHint。
func BuildNewCommentsHint(issueID, triggerCommentID, triggerThreadID, newCommentsSince string, newCommentCount int) string {
	if newCommentCount <= 0 || newCommentsSince == "" || issueID == "" {
		return ""
	}
	threadID := activeThreadID(triggerThreadID, triggerCommentID)
	// 知道触发线程时，引导 agent 先读"那一条"线程，而不是盲目地拉取 issue
	// 范围内每条新评论。整 issue 的 --since 追平被降级为"仅在需要时"的回退，
	// 措辞上是"去掉 --thread 重跑该命令"而非"第二条完整命令"：重复的命令会
	// 为毫无路由价值地再次复述 issue UUID 与锚点（MUL-5721 OPT-1）。
	if threadID != "" {
		return fmt.Sprintf(
			"%d new comment(s) on this issue since your last run — don't read them all blindly. "+
				"Start with the thread your triggering comment is in: "+
				"`multica issue comment list %s --thread %s --since %s --compact --output json` "+
				"(swap `--since` for `--tail 30` if you need the full thread, not just the delta). "+
				"Only if you need context from the other threads, rerun it without `--thread` for the issue-wide catch-up.\n\n",
			newCommentCount, issueID, threadID, newCommentsSince,
		)
	}
	// 防御性兜底：评论触发总会带 trigger id，但万一缺失就没有线程可锚定，
	// 退回朴素的整 issue 范围追平。
	return fmt.Sprintf(
		"%d new comment(s) on this issue since your last run. Catch up: "+
			"`multica issue comment list %s --since %s --compact --output json`.\n\n",
		newCommentCount, issueID, newCommentsSince,
	)
}

// BuildResumedCommentsHint 生成 WARM 但"无增量"路径的读评论指引：daemon 正在
// 恢复之前某个 provider 会话，而触发评论的正文已注入每轮提示。此处的
// newCommentCount == 0 表示自上次运行以来（除已注入的触发评论与 agent 自己的
// 回复外）issue 范围内没有新评论。让读取保持有界且有条件，同时明示：依赖
// 上下文的回复应当刷新触发它的那段对话，而不是只信任恢复出的会话记忆。
func BuildResumedCommentsHint(issueID, triggerCommentID, triggerThreadID string) string {
	threadID := activeThreadID(triggerThreadID, triggerCommentID)
	if issueID == "" || threadID == "" {
		return ""
	}
	// 这里不再有独立的一句"复述锚点"（MUL-5721 OPT-1）：下面的读命令已携带
	// 线程锚点，触发评论 id 会以回复 cookbook 的 `--parent` 值送到 agent 手上。
	return fmt.Sprintf(
		"You're resuming the prior session, and the triggering comment is already included above. "+
			"No other new comments on this issue since your last run. "+
			"If your reply depends on thread context, do not rely only on resumed session memory — "+
			"first pull the triggering conversation with: "+
			"`multica issue comment list %s --thread %s --tail 30 --compact --output json`.\n\n",
		issueID, threadID,
	)
}

// BuildColdCommentsHint 生成 COLD 路径的读评论指引——agent 在此 issue 上没有
// 历史运行，因此没有 since 锚点，BuildNewCommentsHint 渲染不出内容。与其倒出
// 整条扁平的评论时间线（从最旧开始，服务端上限 2000 条），不如把 agent 指向
// 触发它的那条"会话线程"：`--thread <trigger> --tail 30` 返回该线程的根评论
// 及其最新 30 条回复（根评论始终包含，即使 --tail 0 也一样）——这正是触发
// 评论真正需要的上下文。跨线程背景可以用便宜的 `--roots-only --summary`
// 扫描获取；指引刻意不提 `--recent`，它的"饱和陷阱"与分页只存在于 brief 的
// `## Available Commands` 一节（MUL-5372）。每轮指引只点名本轮真正想让它跑的
// 读取命令。
//
// 自 MUL-5377 起每轮提示是唯一调用方（与 BuildNewCommentsHint 相同）。
// 当没有可作线程起点的触发评论时返回 ""，让调用方保留最后的普通回退。
func BuildColdCommentsHint(issueID, triggerCommentID, triggerThreadID string) string {
	threadID := activeThreadID(triggerThreadID, triggerCommentID)
	if issueID == "" || threadID == "" {
		return ""
	}
	// 根评论扫描被措辞为"对上面那条线程命令的换旗标操作"，而非第二条完整
	// 命令：重复的命令只会为毫无路由价值地复述 issue UUID（MUL-5721 OPT-1）。
	return fmt.Sprintf(
		"Read the triggering conversation first: "+
			"`multica issue comment list %s --thread %s --tail 30 --compact --output json` "+
			"(that thread's root + its 30 newest replies). "+
			"Need cross-thread background? Rerun with `--roots-only --summary` replacing `--thread ... --tail 30` "+
			"to scan the other threads cheaply, and expand only what looks relevant.\n\n",
		issueID, threadID,
	)
}

// activeThreadID 决定线程锚点：显式给出触发线程就用它，否则退回触发评论 id
// （评论 id 在单层会话里即可充当线程标识）。
func activeThreadID(triggerThreadID, triggerCommentID string) string {
	if triggerThreadID != "" {
		return triggerThreadID
	}
	return triggerCommentID
}

// BuildCommentReplyInstructions 返回告诉 agent "如何为评论触发任务发布回复"
// 的规范文本块。每轮提示（daemon.buildCommentPrompt）与 CLAUDE.md 工作流
// （InjectRuntimeConfig）都调用它，于是触发评论 id 与 --parent 值不会在各
// 表面之间漂移。
//
// 之所以明确写 "do not reuse --parent from previous turns"，是因为被恢复的
// Claude 会话会把之前轮次的工具调用留在上下文里，否则 agent 会把旧的
// --parent UUID 一路复制下去。
//
// 该模板与平台无关、也与 provider 无关——它要防的失败发生在 shell 层，
// 无法限定到某一个 provider 或某一个 OS：
//
//   - 内联的 `--content "..."` 让 shell 在 CLI 收到正文"之前"改写它：被反引号
//     包住的令牌会变成一次失败的命令替换而被静默删除，存下来的评论不再符合
//     模型本意，而一旦模型察觉到不一致就可能无限重试（MUL-2904 / OKK-497）。
//     它还允许 Codex 在 `--content` 里发出字面 `\n` 转义（MUL-1467）。
//   - `--content-stdin` 配 HEREDOC 有两种模型看不见的失败模式：
//     1. 在 Windows 上，PowerShell 5.1 的 `$OutputEncoding` 在向原生命令管道
//     输出时默认是 ASCIIEncoding，会在字节到达 `multica.exe` 之前把非 ASCII
//     字符替换成 `?`（#2198 中文、#2236 中文、#2376 西里尔文）。
//     2. 在任何主机上，当模型发出多旗标命令（例如
//     `multica issue create --title ... --assignee-id ... --project ...`）时，
//     bash 的 heredoc/旗标边界很脆弱：正文里带尾随令牌的 `BODY \` "终止符
//     不会被识别为 heredoc 结束，于是其后的旗标行被吞进描述；或者一个干净的
//     终止符让尾随的 `--assignee ...` 行变成一条独立的 shell 语句而失败，
//     但此时 create 早已成功——只是没有 assignee。两条路径都以退出码 0
//     结束并静默丢弃旗标。GitHub issue #4182 记录了两个确证案例
//     （OXY-78、OXY-76）。
//
// 因此唯一安全的路径是：先用文件写入工具把正文写进 UTF-8 文件，用
// `--content-file` 发帖，然后删除该文件。所有旗标都在同一行 shell token 上；
// 正文从不经过 shell；不存在可供旗标泄漏越界的 heredoc 边界。这与长期以来的
// Windows 路径收敛，于是跨平台模板只有一种形态。
//
// provider 保留只是为了调用方对称以及未来可能出现的按 provider 微调；
// 这个护栏本身在所有 provider 与主机上刻意为同一形态。
func BuildCommentReplyInstructions(provider, issueID, triggerCommentID string, squadLeader bool) string {
	if triggerCommentID == "" {
		return ""
	}
	return buildCommentReplyInstructionsSlim(provider, issueID, triggerCommentID, squadLeader)
}

// buildCommentReplyInstructionsSlim 是 BuildCommentReplyInstructions 使用的
// 压缩版回复指引块。它于 MUL-3560 作为对旧式冗长形态的"精简"替代而引入；
// `runtime_brief_slim` 旗标此后已退役（MUL-4297），现在它是唯一形态。
//
// 精简块只携带"触发特有"的 cookbook（确切的 `--parent` UUID、文件路径、
// 清理行），外加测试钉死的两条行为规则（"do NOT reuse --parent"与
// "do not rely on `\n` escapes"）。关于 shell 风险的详细论证位于同一 brief
// 携带的规范 `## Comment Formatting` 一节，在每个评论触发的步骤 7 内联复述
// 只会是重复而非信号。
func buildCommentReplyInstructionsSlim(provider, issueID, triggerCommentID string, squadLeader bool) string {
	// 小队长的 `no_action` 退出（通过 `squad activity` 记录）是"不发评论才是
	// 正确"的唯一路径——这条祈使句必须自带例外，让后面的行永远不与被注入在
	// 其上的 no_action 规则相矛盾（MUL-5442 #6493 review）。
	lead := "Post your reply as a comment — always use the trigger comment ID below, "
	if squadLeader {
		lead = "Unless your outcome is `no_action`, post your reply as a comment — always use the trigger comment ID below, "
	}
	if runtimeGOOS == "windows" {
		return fmt.Sprintf(
			lead+
				"do NOT reuse --parent values from previous turns in this session.\n\n"+
				"Write the body file first — never pipe via `--content-stdin` (PowerShell drops non-ASCII; full rules: ## Comment Formatting above):\n\n"+
				"    multica issue comment add %s --parent %s --content-file ./reply.md\n"+
				"    Remove-Item ./reply.md\n\n"+
				"Do NOT write literal `\\n` escapes to simulate line breaks; the file preserves real newlines.\n",
			issueID, triggerCommentID,
		)
	}
	return fmt.Sprintf(
		lead+
			"do NOT reuse --parent values from previous turns in this session.\n\n"+
			"Write the body file first (rules: ## Comment Formatting above — MUL-2904 / #4182):\n\n"+
			"    multica issue comment add %s --parent %s --content-file ./reply.md\n"+
			"    rm ./reply.md\n\n"+
			"Do NOT write literal `\\n` escapes to simulate line breaks; the file preserves real newlines.\n",
		issueID, triggerCommentID,
	)
}

// ThreadReplyTarget 是一次"聚合运行"必须回答的"一个根线程分组"。
// ThreadID 标识该会话（其根评论 id）；ParentID 是 agent 必须传的确切
// `--parent`，使回复落进该线程内。
type ThreadReplyTarget struct {
	ThreadID string
	ParentID string
}

// BuildMultiThreadCommentReplyInstructions 是"聚合评论横跨多条根线程"
// （MUL-4348）时的回复分发块。它刻意在"这一次运行"上覆盖通用的"每次运行
// 只发一条评论"指引：三条在三个不同线程里提出的互不相关问题，必须以三条
// 线程内回答落地，而不是合并成一坨贴在单条线程下（或成为一条游离的根评论）。
//
// 分组由服务端计算，因此"同一线程内的追问"永远不会走到这里——它们在上游
// 就已塌缩成单个目标、走普通的单父路径。正因如此，才无条件地告诉 agent：
// 每个列出的线程恰好回一条、同一线程内绝不超过一条——"同一线程里的多次
// @提及"在发出本指引前已被合并，逐线程分发不会把它拆散。
//
// 该块只携带"多线程特有"的东西：分发宣告 + 覆盖、发帖顺序、逐线程的
// `--parent` 目标，以及唯一一处机械差异（每个线程用不同的正文文件）。
// 发帖机制本身——正文文件 → `--content-file` → 清理、内联/`--content-stdin`
// 禁令、`\n` 转义规则、按 OS 区分的清理命令——只在 brief 的
// `## Comment Formatting` 一处定义；此块曾复述全部内容外加两对示例命令，
// 使每个多线程轮次白白多写约 1KB 相同的 cookbook（MUL-5825）。去掉内嵌命令
// 也同时删掉了唯一的 OS 相关文本，因此该块不再按 runtimeGOOS 分支：
// `## Comment Formatting` 保留 OS 差异。
//
// 目标少于两个时返回 ""；调用方保留单父路径。
func BuildMultiThreadCommentReplyInstructions(issueID string, targets []ThreadReplyTarget, squadLeader bool) string {
	if issueID == "" || len(targets) < 2 {
		return ""
	}

	targetLines := ""
	for i, tgt := range targets {
		targetLines += fmt.Sprintf("%d. thread %s → reply with `--parent %s`\n", i+1, tgt.ThreadID, tgt.ParentID)
	}

	// 与单线程 cookbook 相同的例外（MUL-5442 #6493 review）：小队长的
	// no_action 退出不得被这条更晚的祈使句推翻，而且作用域句必须统辖整个
	// 分发块——下面每一条义务（"需要多条回复"、发帖顺序、逐线程文件规则）
	// 都挂在 "Otherwise" 之下——而不只是第一个动词。普通 agent 保持无条件
	// 形态逐字节不变。
	lead := "This run coalesced comments from %d DISTINCT threads. Post ONE reply per thread"
	if squadLeader {
		lead = "This run coalesced comments from %d DISTINCT threads. **If your outcome is `no_action`, skip this ENTIRE fan-out block — post no replies at all and exit via `multica squad activity` as your leader rules direct; everything below applies only otherwise.** Otherwise, post ONE reply per thread"
	}
	return fmt.Sprintf(
		lead+" — %d in total. This OVERRIDES the \"post exactly one comment per run\" rule: for THIS run multiple replies are required and correct. Do NOT merge separate threads into one comment or post twice in the same thread.\n\n"+
			"Reply targets, in posting order — OLDEST thread first, the newest (triggering) thread LAST. Use the exact `--parent` for each; never reuse a `--parent` from an earlier turn:\n"+
			"%s\n"+
			"Write and post each reply exactly as `## Comment Formatting` above directs, with ONE multi-thread delta: use a DISTINCT body file per thread (./reply-1.md, ./reply-2.md, …) so one reply's content can never leak into another's.\n",
		len(targets), len(targets), targetLines,
	)
}
