// 职责定位：子任务完成（child done）的平台侧通知逻辑：当子任务进入终态、并因此关闭父级下
// 某个「关卡（stage）屏障」时，在父 issue 上发系统评论并唤醒父级受理 agent/squad，把推进
// 下一关的决定权交给被唤醒的 agent。
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// notifyParentOfChildDone 在子任务从「非终态」转入「终态」时，往父 issue 上发一条系统级顶层
// 评论。它替代旧的 agent-prompt 规则（PR #2918 用户反馈——agent 规则引发自我 @提及循环、
// planner 乒乓、以及 agent 不一定知道工作区前缀导致的误写 `MUL-` 前缀）。
//
// 是否真正发评论的守卫：
//   - 子任务必须从非终态「转入」终态（done 或 cancelled）。重复保存已终态的子任务不重发，
//     只有那次「进入」触发。cancelled 也算，因为被取消的兄弟永远不会完成、于是会关闭它的
//     关卡（见入口守卫与 isTerminalChildStatus）。
//   - issue.ParentIssueID 必须已设置
//   - 父级不能是 done/cancelled——父级已关闭，通知没有可驱动的后续
//   - 父级不能是 backlog——停在 backlog 的父级是被刻意压着等以后处理；唤醒它的受理人（可能
//     顺势把兄弟的 backlog 子任务推进成 todo）正是 #4320 / MUL-3497 报告的那种不想要的
//     自动激活。停在 backlog 的父级保持惰性，直到用户显式把它移出。
//   - 父级受理人不能是 member（人类）。人类手动看自己的 issue，自动化系统评论对他们纯属噪音，
//     而且人类受理人没有可「触发」的东西。完全跳过评论（Bohan 在 MUL-2538 的决定）也顺带
//     绕开提及问题——没有评论就没有提及、没有收件箱条目。
//   - 这次完成必须关闭一个「关卡屏障」（MUL-3508）。父级下的子任务可以经 issue.stage 分进
//     有序关卡；只有当最低未完成关卡里每个兄弟都终态时（stageBarrierClosed）才发通知 + 唤醒。
//     未分组的兄弟集合等价于一个隐式关卡，所以是「最后一个子任务完成时发一次」而不是每条子
//     任务都发——这是对 #4320 报告的 fire-on-every-child 级联的默认修复。被唤醒的受理人决定
//     是否推进下一关（agent 驱动推进）；服务端只检测屏障并唤醒。
//
// 评论经 db.Queries 直接插入（不经过 CreateComment HTTP handler），绕开通用 on_comment 触发
// 路径。父级受理人是 agent 或 squad 时，评论正文内嵌一条指向父级受理人的
// `mention://{agent,squad}/<id>`——Bohan 对 MUL-2538 的产品决定（"system child-done comment
// 无脑 mention parent assignee，member/squad/agent 都覆盖"，后来收窄为直接跳过 member
// 受理人）。为了让平台控制副作用，cmd/server 的通知 + 订阅者监听仍然整体跳过系统评论，因此
// 从子任务标题夹带进正文的提及不会点亮无关成员。父级受理人自己的触发由下面的
// dispatchParentAssigneeTrigger 显式触发，幂等守卫见那里的文档。
//
// 错误以 warn 级别记录后吞掉：这是一次成功的状态更新旁边的尽力而为通知；失败绝不能回滚用户
// 的状态变更。
func (h *Handler) notifyParentOfChildDone(ctx context.Context, prev, issue db.Issue) {
	if !issue.ParentIssueID.Valid {
		return
	}
	// 在「转入」终态（done 或 cancelled）时触发，而不只是 done。cancelled 的子任务也能关一个
	// 关卡：isTerminalChildStatus 把 cancelled 视作终态（被取消的兄弟永远不完成，不能让它撑住
	// 关卡），所以某关最后一个未完成子任务被取消时也必须评估屏障。以转换为触发条件，还让之后
	// cancelled -> done 的编辑变成空操作（终态 -> 终态），避免迟到的重复唤醒。
	// 转换两侧都先解析成它们继承的 canonical 状态，移进自定义 done/cancelled 状态与移进
	// Done/Cancelled 一样触发屏障。 (MUL-6243)
	prevTerminal := isTerminalChildStatus(issuestatus.Effective(ctx, h.Queries, prev.WorkspaceID, prev.Status))
	nowTerminal := isTerminalChildStatus(issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status))
	if prevTerminal || !nowTerminal {
		return
	}
	parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID)
	if err != nil {
		slog.Warn("child done: failed to load parent",
			"error", err,
			"child_id", uuidToString(issue.ID),
			"parent_id", uuidToString(issue.ParentIssueID))
		return
	}
	// 自定义状态会继承它命名的那个 canonical 状态：自定义终态状态一样能关屏障、自定义 backlog
	// 状态一样能让父级停驻——与内置的 Done/Cancelled、Backlog 完全一致。（MUL-6243）
	parentStatus := issuestatus.Effective(ctx, h.Queries, parent.WorkspaceID, parent.Status)
	if parentStatus == "done" || parentStatus == "cancelled" {
		return
	}
	// 停在 backlog 的父级是被刻意压着等以后处理的。发这条系统评论会唤醒它的受理人，被唤醒的
	// agent 随后可能把兄弟的 backlog 子任务推进成 todo——正是 #4320 / MUL-3497 报告的那种意外
	// 自动激活。整个通知（含唤醒）直接跳过，让 backlog 父级保持惰性，直到用户显式把它移出来。
	if parentStatus == "backlog" {
		return
	}
	// 人类受理的父级会自己读时间线，自动化系统评论对他们是纯噪音，也没有可「触发」的 agent
	// 任务。整个通知（评论 + 提及 + 收件箱行）直接跳过——MUL-2538。
	if parent.AssigneeType.Valid && parent.AssigneeType.String == "member" {
		return
	}

	// 关卡屏障（MUL-3508 / discussion #4320）。只有当这次完成「关闭了一个关卡」时——即最低
	// 未完成关卡里每个兄弟现在都已是终态——才发通知 + 唤醒受理人。未分组的兄弟集合等价于一个
	// 隐式关卡，于是退化成「最后一个子任务完成时只唤醒一次」，取代了导致那场意外级联的
	// fire-on-every-child 旧行为。没关闭任何关卡的完成保持静默：无评论、无唤醒。到这里时本子任务
	// 的 done 状态已提交（状态更新先于本函数执行），所以 ListChildIssues 已能看到它。
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil {
		slog.Warn("child done: failed to list siblings for stage barrier",
			"error", err,
			"child_id", uuidToString(issue.ID),
			"parent_id", uuidToString(parent.ID))
		return
	}
	if !stageBarrierClosed(children, issue, h.terminalChildPredicate(ctx)) {
		return
	}
	staged := siblingsAreStaged(children)
	// 有 stage 且屏障已关闭时，完成的那个子任务必定带 stage（stageBarrierClosed 对「有 stage 集合
	// 里未带 stage 的完成子任务」返回 false），所以此处取 issue.Stage.Int32 是安全的。
	var closedStage int32
	if staged {
		closedStage = issue.Stage.Int32
	}
	h.postChildDoneComment(ctx, parent, issue, children, staged, closedStage, false)
}

// notifyParentsOfBatchChildDone 在整个批次的状态写入全部提交之后，为一批子任务补发 child-done
// 父级通知。`completed` 是本次批次中从「非终态」转为「终态」的子任务集合。
//
// 为什么不能放在批次循环里逐条评估屏障：逐条评估用的是批次中途的兄弟快照，一批同时关闭多个
// 关卡时会为每个中间态关卡各发一条评论——第一条（已过期）评论把父受理人的唤醒钉在一条已被
// 取代的「推进到第 N+1 阶段」指令上，而真正准确的最终唤醒又被 pending-task 去重吞掉，最终
// 结果还依赖 issue_ids 的先后顺序（MUL-4155）。在这里聚合使结果与顺序无关——每个受影响的父级
// 至多收到一条基于最终状态构造的评论，外加一次钉在那条评论上的唤醒。
//
// 与 notifyParentOfChildDone 一致的尽力而为语义：某个父级失败只记录日志并跳过，绝不回滚已
// 提交的批次。
func (h *Handler) notifyParentsOfBatchChildDone(ctx context.Context, completed []db.Issue) {
	if len(completed) == 0 {
		return
	}

	// 按父级把完成的子任务分组，保持「首次出现」的顺序，让发出的评论（以及测试断言）结果确定。
	type parentGroup struct {
		parentID pgtype.UUID
		children []db.Issue
	}
	var groups []*parentGroup
	index := map[string]*parentGroup{}
	for _, c := range completed {
		if !c.ParentIssueID.Valid {
			continue
		}
		key := uuidToString(c.ParentIssueID)
		g, ok := index[key]
		if !ok {
			g = &parentGroup{parentID: c.ParentIssueID}
			index[key] = g
			groups = append(groups, g)
		}
		g.children = append(g.children, c)
	}

	for _, g := range groups {
		parent, err := h.Queries.GetIssue(ctx, g.parentID)
		if err != nil {
			slog.Warn("batch child done: failed to load parent",
				"error", err, "parent_id", uuidToString(g.parentID))
			continue
		}
		// 与单条路径相同的父级守卫（见 notifyParentOfChildDone）。
		parentStatus := issuestatus.Effective(ctx, h.Queries, parent.WorkspaceID, parent.Status)
		if parentStatus == "done" || parentStatus == "cancelled" {
			continue
		}
		if parentStatus == "backlog" {
			continue
		}
		if parent.AssigneeType.Valid && parent.AssigneeType.String == "member" {
			continue
		}

		children, err := h.Queries.ListChildIssues(ctx, parent.ID)
		if err != nil {
			slog.Warn("batch child done: failed to list siblings for stage barrier",
				"error", err, "parent_id", uuidToString(parent.ID))
			continue
		}

		isTerminal := h.terminalChildPredicate(ctx)
		batch := len(g.children) > 1
		if !siblingsAreStaged(children) {
			// 未分组：等价于一个隐式关卡，仅当最终状态里每个子任务都是终态时才发一次。
			// stageBarrierClosed 在未分组路径上会忽略 `completed` 参数，所以任取一个已完成的
			// 子任务来占位做屏障检查即可。
			if !stageBarrierClosed(children, g.children[0], isTerminal) {
				continue
			}
			h.postChildDoneComment(ctx, parent, g.children[0], children, false, 0, batch)
			continue
		}

		// 有分组：在「本批已完成的子任务」里，宣布最终状态下屏障已关闭的最高 stage。这正是结果
		// 与顺序无关的原因——无论调用方先送 [stage1, stage2] 还是 [stage2, stage1]，最终提交的
		// 状态都一样，胜出的总是同一个最高 stage，stageProgressSummary 里的「下一阶段是 N」也
		// 反映真实状态而不是批次中途快照。若选较低的已关闭 stage，就会重新引入本 bug 要修的
		// 过期「推进下一阶段」指令。
		var rep db.Issue
		var bestStage int32
		found := false
		for _, c := range g.children {
			if !c.Stage.Valid {
				continue // 有分组的集合里，未分组子任务关不掉任何关卡
			}
			if !stageBarrierClosed(children, c, isTerminal) {
				continue
			}
			if !found || c.Stage.Int32 > bestStage {
				found = true
				bestStage = c.Stage.Int32
				rep = c
			}
		}
		if !found {
			continue
		}
		h.postChildDoneComment(ctx, parent, rep, children, true, bestStage, batch)
	}
}

// postChildDoneComment 为一个已关闭的关卡屏障构造并发布父 issue 上的 child-done 系统评论，
// 然后派发父级受理人的触发器。调用方必须已通过 notifyParentOfChildDone /
// notifyParentsOfBatchChildDone 里的全部守卫，且 `completed` 是一个「其屏障在 `children`（最终
// 兄弟集合）内已关闭」的终态子任务。
//
// `completed` 是评论中点名的代表性子任务。`staged`/`closedStage` 描述被关闭的屏障（未分组集合
// 时 closedStage 用不到）。`batch` 决定措辞是否按批量处理：单条更新保持历史上逐字节一致的文案，
// 而一次完成多个子任务的批量更新不能声称「最后一个子任务刚刚结束」。
func (h *Handler) postChildDoneComment(ctx context.Context, parent, completed db.Issue, children []db.Issue, staged bool, closedStage int32, batch bool) {
	prefix := h.getIssuePrefix(ctx, completed.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(completed.Number))
	childID := uuidToString(completed.ID)
	title := sanitizeChildTitleForSystemComment(completed.Title)
	parentID := uuidToString(parent.ID)

	// 拼父级受理人的提及前缀。父级没有受理人、或受理人行已丢失（成员被删、工作区已失联的归档
	// agent 等）时前缀为空串。
	mentionPrefix := h.buildParentAssigneeMention(ctx, parent)

	var content string
	if staged {
		summary, nextStage := stageProgressSummary(children, closedStage, h.terminalChildPredicate(ctx))
		advance := stageAdvanceInstruction(nextStage, parentID)
		if batch {
			content = fmt.Sprintf(
				"%s当前任务的第 %d 阶段已完成——该阶段的子任务已在批量更新中全部结束，最近完成的是 [%s](mention://issue/%s)——“%s”。阶段进度：%s。%s",
				mentionPrefix, closedStage, identifier, childID, title, summary, advance,
			)
		} else {
			content = fmt.Sprintf(
				"%s当前任务的第 %d 阶段已完成——最后一个子任务 [%s](mention://issue/%s)——“%s”刚刚结束。阶段进度：%s。%s",
				mentionPrefix, closedStage, identifier, childID, title, summary, advance,
			)
		}
	} else {
		if batch {
			content = fmt.Sprintf(
				"%s所有子任务都已完成——它们刚刚在批量更新中一起结束，最近完成的是 [%s](mention://issue/%s)——“%s”。请汇总子任务结果并继续推进父任务；如果已经没有后续工作，请运行 `multica issue status %s in_review` 将父任务标记为待评审。",
				mentionPrefix, identifier, childID, title, parentID,
			)
		} else {
			content = fmt.Sprintf(
				"%s所有子任务都已完成——最后一个子任务 [%s](mention://issue/%s)——“%s”刚刚结束。请汇总子任务结果并继续推进父任务；如果已经没有后续工作，请运行 `multica issue status %s in_review` 将父任务标记为待评审。",
				mentionPrefix, identifier, childID, title, parentID,
			)
		}
	}

	// author_type='system'、author_id=全零 UUID。全零 UUID 是合法的 16 字节值，该列又是
	// NOT NULL；前端应判断 author_type === 'system' 而非比较 UUID 值。
	if h.TxStarter == nil {
		slog.Warn("child done: transaction starter unavailable",
			"child_id", childID, "parent_id", parentID)
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("child done: begin comment transaction failed",
			"error", err, "child_id", childID, "parent_id", parentID)
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     parent.ID,
		WorkspaceID: parent.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("child done: create system comment failed",
			"error", err,
			"child_id", childID,
			"parent_id", uuidToString(parent.ID))
		return
	}
	comment := created.Comment()
	createdEvent, err := eventoutbox.Enqueue(ctx, qtx, buildCommentCreatedEvent(parent,
		commentToResponse(comment, nil, nil), "system", ""))
	if err != nil {
		slog.Warn("child done: enqueue system comment event failed",
			"error", err, "child_id", childID, "parent_id", parentID)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("child done: commit comment transaction failed",
			"error", err, "child_id", childID, "parent_id", parentID)
		return
	}
	h.publishEvent(createdEvent)

	// 显式派发父级受理人的触发器/收件箱行。刻意不经过 listener 层的提及解析——notification 与
	// subscriber 两个 listener 都遇到 author_type='system' 会短路——这样从子任务标题夹带进来的
	// 提及保持惰性，平台也只需在这一处应用去环与幂等守卫。
	h.dispatchParentAssigneeTrigger(ctx, parent, comment)
}

// isTerminalChildStatus 判断子任务状态对「关卡屏障」而言是否算「已完成」。cancelled 也算终态：
// 被取消的兄弟永远不会完成，所以不能让它撑住某个关卡。
//
// 入参是 CANONICAL 状态。持有原始 `issue.status` 的调用方必须先经 terminalChildPredicate 转换，
// 这样处于 done / cancelled 类别的自定义状态也能像内置 Done/Cancelled 一样关闭关卡。
func isTerminalChildStatus(status string) bool {
	return status == "done" || status == "cancelled"
}

// terminalChildPredicate 返回作用于兄弟集合的终态判定，把每个子任务的 status 解析成它继承的
// canonical 状态。内置 key 无需查询就解析到自己，所以没有定义自定义状态的工作区里此函数零开销。
// (MUL-6243)
//
// 刻意返回谓词而不是改写后的 []db.Issue：同一份 slice 还要渲染进阶段进度评论，若在遍历时改写
// Status，会展示成类别而不是用户实际选的状态。
func (h *Handler) terminalChildPredicate(ctx context.Context) func(db.Issue) bool {
	return func(c db.Issue) bool {
		return isTerminalChildStatus(issuestatus.Effective(ctx, h.Queries, c.WorkspaceID, c.Status))
	}
}

// siblingsAreStaged 报告集合里是否有任何子任务带显式 stage。完全没有 stage 的集合被当作单个
// 隐式关卡处理。
func siblingsAreStaged(children []db.Issue) bool {
	for _, c := range children {
		if c.Stage.Valid {
			return true
		}
	}
	return false
}

// stageBarrierClosed 报告 `completed` 的完成是否在 `children`（同一父级下的完整兄弟集合，已经
// 反映了 completed 的终态）之间关闭了一个关卡屏障。
//
//   - 未分组的兄弟集合（没有任何子任务带 stage）：等价于单个隐式关卡，仅当每个子任务都是终态
//     时屏障才关闭——即「最后一个子任务完成时唤醒一次」的默认行为。
//   - 有分组的兄弟集合：只有带 stage 的子任务才参与分组。未分组子任务不参与（与 migration 123
//     一致：NULL stage 不参与 staged 分组）——完成一个未分组子任务关不掉任何关卡，一个非终态
//     的未分组子任务也从不撑住关卡。被完成的子任务其 stage 为 S 时，当且仅当每个 stage <= S 的
//     *已分组*子任务都是终态（前沿闭包）屏障才关闭。靠后的 stage 通常停在 `backlog`，所以不会
//     越序触发；调用方的幂等守卫会折叠任何重复唤醒。
func stageBarrierClosed(children []db.Issue, completed db.Issue, isTerminal func(db.Issue) bool) bool {
	if !siblingsAreStaged(children) {
		for _, c := range children {
			if !isTerminal(c) {
				return false
			}
		}
		return true
	}
	// 有分组的集合：未分组的完成子任务不属于任何关卡，关不掉任何东西。
	if !completed.Stage.Valid {
		return false
	}
	s := completed.Stage.Int32
	for _, c := range children {
		if !c.Stage.Valid {
			continue // 前沿计算忽略未分组子任务
		}
		if c.Stage.Int32 <= s && !isTerminal(c) {
			return false
		}
	}
	return true
}

// stageProgressSummary 为 child-done 系统评论渲染逐阶段的紧凑摘要（形如「阶段 1：已完成 3/3；
// 阶段 2：已完成 0/4」），并返回 closedStage 之上第一个仍含有非终态子任务的 stage——即下一步要
// 推进的那组——没有则返回 0。未分组子任务被跳过（它们不属于任何 stage），所以摘要里永远不会
// 出现「阶段 0」。
func stageProgressSummary(children []db.Issue, closedStage int32, isTerminal func(db.Issue) bool) (summary string, nextStage int32) {
	type agg struct{ total, done int }
	byStage := map[int32]*agg{}
	order := []int32{}
	for _, c := range children {
		if !c.Stage.Valid {
			continue // 未分组子任务不属于任何 stage
		}
		s := c.Stage.Int32
		a, ok := byStage[s]
		if !ok {
			a = &agg{}
			byStage[s] = a
			order = append(order, s)
		}
		a.total++
		if isTerminal(c) {
			a.done++
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	parts := make([]string, 0, len(order))
	for _, s := range order {
		a := byStage[s]
		label := fmt.Sprintf("阶段 %d：已完成 %d/%d", s, a.done, a.total)
		if nextStage == 0 && s > closedStage && a.done < a.total {
			nextStage = s
			label += "（下一阶段）"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; "), nextStage
}

// stageAdvanceInstruction 根据「当前已存在的子任务里、尚有待办工作的下一阶段」（nextStage，0 =
// 无）返回追加在 staged child-done 系统评论末尾的指引文案。
//
//   - nextStage > 0：已存在含有未完成工作的更靠后阶段，直接把 leader 指向它。
//   - nextStage == 0：在「目前已创建的子任务」范围内没有更靠后的阶段。这里刻意不断言流程
//     已经结束：服务端没有声明式工作流模型——阶段由 agent 驱动、常常是惰性创建的（第 N+1 阶段
//     的子任务要在第 N 阶段产出它们依赖的输入之后才写出来），所以这种流水线里的中间阶段和真正
//     的最终阶段一样会走到 nextStage == 0。旧文案（「这是最后阶段，收尾父任务吧」）断言了服务端
//     无法得知的最终性，会把 leader 往流程中途收尾上推（MUL-4062 / #4927）。现文案把两种可能
//     都点明，把「创建下一阶段 vs 收尾」的决策交还给 leader。
func stageAdvanceInstruction(nextStage int32, parentID string) string {
	if nextStage > 0 {
		return fmt.Sprintf(
			" 下一步是阶段 %d。请先通过 `multica issue children %s` 查看完整结构；确认阶段 %d 的依赖已经满足后，再把其中处于 `backlog` 的子任务推进到 `todo`。请逐条阅读子任务描述，只推进明确满足依赖的任务；如果描述与父任务概览冲突，先保留在 `backlog` 并留言确认。",
			nextStage, parentID, nextStage,
		)
	}
	return fmt.Sprintf(" 当前阶段结束不代表整个任务已经完成。请判断父任务是否真正完成：如果完成，请汇总结果并运行 `multica issue status %s in_review` 标记为待评审；如果仍需要下一阶段，请立即创建该阶段及其子任务。", parentID)
}

// sanitizeChildTitleForSystemComment 在子任务标题被嵌入父级系统评论前，去掉其中的提及式
// markdown。夹带进来的提及在 listener 路径上本来就无害（notification 与 subscriber 两个
// listener 都会跳过系统评论），但时间线仍会原样渲染标题——剥掉 markdown 既能保持渲染出的评论
// 可读，也能阻止恶意命名的子任务看起来像一条指令（比如「@all 请看」）。
func sanitizeChildTitleForSystemComment(title string) string {
	// 替换 markdown 链接目标，使提及正则不再命中它，同时保留给人读的标签文字。`]` 和 `(`
	// 是提及正则的最小定界符；替换掉 `(` 就足以切断匹配，又不会弄乱标签。
	cleaned := strings.ReplaceAll(title, "](mention://", "] (mention-stripped://")
	return cleaned
}

// buildParentAssigneeMention 返回系统评论开头应带上的 markdown 提及前缀（含结尾空格），让正文
// 读起来像一条正常的、以提及开头的评论。父级没有受理人或受理人行加载失败时返回空串。
func (h *Handler) buildParentAssigneeMention(ctx context.Context, parent db.Issue) string {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return ""
	}
	label, ok := h.resolveAssigneeMentionLabel(ctx, parent.WorkspaceID, parent.AssigneeType.String, parent.AssigneeID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("[@%s](mention://%s/%s) ", label, parent.AssigneeType.String, uuidToString(parent.AssigneeID))
}

// resolveAssigneeMentionLabel 返回提及链接里要渲染的标签文字。标签仅供人眼显示——提及正则认的
// 是 URL 路径而非标签——但一个合理的兜底能在前端尚未预载受理人信息时仍让评论保持可读。
// 受理人行加载失败时返回 ok=false；调用方此时应整体省略提及，而不是发一条坏链接。
func (h *Handler) resolveAssigneeMentionLabel(ctx context.Context, workspaceID pgtype.UUID, assigneeType string, assigneeID pgtype.UUID) (string, bool) {
	switch assigneeType {
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(agent.Name), true
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(squad.Name), true
	}
	return "", false
}

// sanitizeMentionLabel 去掉名称里会破坏提及 markdown 的字符。提及正则对标签是非贪婪匹配，多
// 余的 `]` 会提前截断它。名字里带 `]` 极其罕见，但防御成本很低。
func sanitizeMentionLabel(name string) string {
	cleaned := strings.ReplaceAll(name, "]", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "assignee"
	}
	return cleaned
}

// dispatchParentAssigneeTrigger 触发与系统评论正文 @提及链接配套的显式副作用——为 agent / squad
// leader 受理人入队一个 agent 任务。member 受理人永远走不到这里：notifyParentOfChildDone 直接
// 跳过他们。通用评论 listener 被刻意绕过（它遇到 author_type='system' 会短路），所以这里是平台
// 对 child-done 通知施加幂等守卫的唯一位置。
//
// 副作用语义（有意比普通 @提及更窄）：
//   - agent 父级：在父受理人上入队一次 EnqueueTaskForMention，与真实 @提及用同一触发面，因此
//     去重与就绪检查与用户已依赖的行为一致。
//   - squad 父级：只在 squad 的 LEADER 上入队一次 EnqueueTaskForSquadLeader。与人 @squad 提及
//     不同，这里不扇出到 squad 成员——child-done 是协调信号，由 leader 决定是否、以及如何唤醒
//     squad 其余成员。在此写清楚，免得评审把「系统提及」误读为继承了完整的成员扇出。关闭子任务
//     的 actor 与路由无关：目标永远是父级自己的 leader，它在 squad 分配时就被选定（并做过权限
//     检查），所以无需穿入 actor 身份——见 triggerChildDoneSquad。
//   - 不读取 notification_preference：这是指向「已拥有父级」的受理人的平台路由信号，不是
//     通用通知。用户的静默设置由下游 agent_task / inbox 管道在任务派发后再评估。
//   - notification_listeners.go 遇到 author_type='system' 短路，所以子任务标题夹带提及产生的
//     subscriber 邮件与 member 收件箱行保持惰性——只有下面这段显式派发会执行。
//
// 此处施加的守卫：
//   - 父级没有受理人行时直接空操作。
//   - agent 与 squad 两条路径都刻意不加 self-trigger 守卫。子任务完成时唤醒父受理人，是跨两条
//     不同 issue 的串行子任务交接，不是自环——按 isAgentRunningOnIssue 与 @提及自触发路径
//     （computeMentionedAgentCommentTriggers）属合法。squad 路径过去会跳过同 squad / 共享 leader
//     的子任务，理由是 leader 在子任务上通过自己的协调循环已经看到这份工作了。这搁浅了常见
//     模式——squad 把父级拆成本 squad 自己的子任务：带「推进下一阶段 / 收尾」指令的关卡屏障
//     系统评论落在父级上，子任务侧的唤醒永远送不到，于是父级悄悄停在 in_progress（MUL-3969）。
//     squad 路径现在与 agent 路径对齐（MUL-2808）：总是派发，仅受幂等约束。
//   - 幂等：HasPendingTaskForIssueAndAgent 对同一父级的连续入队（比如两个子任务背靠背完成）
//     去重；同时也限制任何重复触发，因为 leader 在父级上醒来本身不会把一个子任务推回终态转换。
//   - 就绪：归档 agent / 缺 runtime 的被静默跳过，避免已收尾的 agent 以幽灵受理人身份出现。
func (h *Handler) dispatchParentAssigneeTrigger(ctx context.Context, parent db.Issue, systemComment db.Comment) {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return
	}

	switch parent.AssigneeType.String {
	case "agent":
		h.triggerChildDoneAgent(ctx, parent, systemComment.ID)
	case "squad":
		h.triggerChildDoneSquad(ctx, parent, systemComment.ID)
	}
}

// triggerChildDoneAgent 为父级的 agent 受理人入队一个提及式任务。
//
// 这里刻意没有同 agent 自触发的守卫，与 squad 路径不同。子任务完成时唤醒父 agent 是两条不同
// issue 间的串行子任务交接，平台循环模型把它视为合法（「不是环、必须触发」——见
// isAgentRunningOnIssue）；只有重新进入同一条 issue 才是环。单独把父级拆成自己负责的子任务的
// agent 没有其它唤醒路径，旧的「子任务 owner == 父 agent」守卫会悄悄搁浅这些父级（MUL-2808）。
// 失控的重复触发由下面的 HasPendingTaskForIssueAndAgent 去重挡住，与 @提及自触发路径依赖它
// （见 computeMentionedAgentCommentTriggers）的方式完全一致。
func (h *Handler) triggerChildDoneAgent(ctx context.Context, parent db.Issue, triggerCommentID pgtype.UUID) {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parent.AssigneeID,
		WorkspaceID: parent.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: parent.ID,
		AgentID: parent.AssigneeID,
		// 在评审基准（reviewed head）上做关键去重（TEN-356）。
		HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, parent.ID),
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForMention(ctx, parent, parent.AssigneeID, triggerCommentID); err != nil {
		slog.Warn("child done: enqueue parent agent task failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"agent_id", uuidToString(parent.AssigneeID))
	}
}

// triggerChildDoneSquad 为父级的 squad 受理人入队一个 leader 角色任务。它与 agent 路径
// （见 triggerChildDoneAgent）完全镜像：
//
//   - 不加 self-trigger 守卫：即使完成的子任务由同 squad 或另一共享该 leader 的 squad 拥有，
//     leader 也必须在「父级」上被唤醒以推进下一阶段或收尾。先前的同 squad / 共享 leader 守卫
//     假设 leader 已通过自己的协调循环在子任务上观察过该工作，但那次唤醒落在「子任务」上、
//     从不携带父级的关卡屏障指令，于是搁浅了常见的「squad 把父级拆成本 squad 子任务」模式
//     （MUL-3969）。
//   - 不加 leader 调用门禁。子任务完成时唤醒父级自己的 squad leader，是对 leader 已拥有的 issue
//     做协调交接，不是一次全新调用——调用权限在父级分配给 squad 时已校验过
//     （validateAssigneePair）。agent 路径从未对此设门。这里代子任务的完成方（agent/system actor，
//     没有可解析的人类发起者）重查权限，会对默认的 private leader 失败关闭，让每个流程型 squad
//     管道在第一阶段后全部静默搁浅，而直达 leader agent 的父级却推进正常（MUL-4063 / GH #4928）。
//     已移除，让 agent 与 squad 的 child-done 走同一条路径；若将来要重新引入调用权限，必须两条
//     路径一起加。
//
// 重复触发由下面的 HasPendingTaskForIssueAndAgent 幂等检查约束，与 agent 路径的依赖方式一致。
func (h *Handler) triggerChildDoneSquad(ctx context.Context, parent db.Issue, triggerCommentID pgtype.UUID) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          parent.AssigneeID,
		WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return
	}

	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: parent.ID,
		AgentID: squad.LeaderID,
		// 在评审基准（reviewed head）上做关键去重（TEN-356）。
		HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, parent.ID),
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, parent, squad.LeaderID, squad.ID, triggerCommentID); err != nil {
		slog.Warn("child done: enqueue parent squad leader task failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID))
	}
}
