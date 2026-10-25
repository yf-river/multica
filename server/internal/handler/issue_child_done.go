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
	// Fire on a transition INTO a terminal status (done OR cancelled), not only
	// `done`. A cancelled child can close a stage too: isTerminalChildStatus
	// treats cancelled as terminal (a cancelled sibling never finishes, so it
	// must not hold the stage open), so the barrier has to be evaluated when the
	// last open child of a stage is cancelled. Keying on the transition also
	// makes a later cancelled -> done edit a no-op (terminal -> terminal), which
	// avoids a lagging duplicate wake.
	// Both sides of the transition are resolved to the canonical status they
	// inherit, so a move into a custom done/cancelled status fires the barrier
	// exactly like a move into Done or Cancelled. (MUL-6243)
	effective := h.childStatusResolver(ctx)
	prevStatus, err := effective(prev)
	if err != nil {
		slog.Warn("child done: failed to resolve previous child status", "error", err, "child_id", uuidToString(issue.ID))
		return
	}
	nowStatus, err := effective(issue)
	if err != nil {
		slog.Warn("child done: failed to resolve child status", "error", err, "child_id", uuidToString(issue.ID))
		return
	}
	prevTerminal := isTerminalChildStatus(prevStatus)
	nowTerminal := isTerminalChildStatus(nowStatus)
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
	// Custom terminal statuses close this out. Only the fixed backlog key parks it,
	// exactly like Done/Cancelled and Backlog do. (MUL-6243)
	parentStatus, err := effective(parent)
	if err != nil {
		slog.Warn("child done: failed to resolve parent status", "error", err, "parent_id", uuidToString(parent.ID))
		return
	}
	if parentStatus == "done" || parentStatus == "cancelled" {
		return
	}
	// A parent parked in backlog is deliberately held for later. Posting the
	// system comment would wake its assignee, and the woken agent can then
	// promote sibling backlog sub-issues into todo — the surprise auto-
	// activation reported in #4320 / MUL-3497. Skip the whole notification so
	// a backlog parent stays inert until the user explicitly promotes it.
	if parentStatus == "backlog" {
		return
	}
	// Human-assigned parents read their own timeline; an automated system
	// comment is just noise and there is no agent task to trigger. Skip the
	// whole notification (comment + mention + inbox row) — MUL-2538.
	if parent.AssigneeType.Valid && parent.AssigneeType.String == "member" {
		return
	}

	// Stage barrier (MUL-3508 / discussion #4320). The notification + assignee
	// wake fire only when this completion *closes a stage* — i.e. every sibling
	// in the lowest unfinished stage is now terminal. An unstaged sibling set is
	// one implicit stage, so this collapses to "wake once when the last
	// sub-issue finishes" instead of the old fire-on-every-child behavior that
	// caused the surprise cascade. A completion that does not close a stage is
	// silent: no comment, no wake. ListChildIssues already reflects this child's
	// committed terminal status (the status update commits before this runs).
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil {
		slog.Warn("child done: failed to list siblings for stage barrier",
			"error", err,
			"child_id", uuidToString(issue.ID),
			"parent_id", uuidToString(parent.ID))
		return
	}
	statuses, err := resolveChildStatuses(children, effective)
	if err != nil {
		slog.Warn("child done: failed to resolve sibling statuses", "error", err, "parent_id", uuidToString(parent.ID))
		return
	}
	if !stageBarrierClosed(children, issue, statuses.isTerminal) {
		return
	}
	staged := siblingsAreStaged(children)
	// When the set is staged and the barrier closed, the completed child is
	// guaranteed to carry a stage (stageBarrierClosed returns false for an
	// unstaged completed child in a staged set), so issue.Stage.Int32 is safe.
	var closedStage int32
	if staged {
		closedStage = issue.Stage.Int32
	}
	h.postChildDoneComment(ctx, parent, issue, children, staged, closedStage, false, statuses, nil)
}

// notifyParentsOfBatchChildDone emits child-done parent notifications for a
// whole batch AFTER every status write has committed. `completed` is the set of
// children that transitioned non-terminal -> terminal during the batch.
//
// Evaluating the stage barrier per-child inside the batch loop used the
// mid-batch sibling snapshot, so a batch that closed several stages at once
// fired one comment per intermediate stage: the first (stale) comment pinned the
// parent assignee's wake to an already-superseded "advance Stage N+1"
// instruction while the accurate final wake was swallowed by the pending-task
// dedup, and the outcome depended on issue_ids order (MUL-4155). Aggregating
// here makes the result order-independent — each affected parent gets at most
// one comment built from the final state, plus one wake pinned to that comment.
//
// Best-effort, mirroring notifyParentOfChildDone: a failure on one parent is
// logged and skipped; it never rolls back the committed batch.
func (h *Handler) notifyParentsOfBatchChildDone(ctx context.Context, completed []db.Issue) {
	if len(completed) == 0 {
		return
	}

	// Group the completed children by parent, preserving first-seen order so the
	// emitted comments (and any test assertions) are deterministic.
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

	effective := h.childStatusResolver(ctx)
	for _, g := range groups {
		parent, err := h.Queries.GetIssue(ctx, g.parentID)
		if err != nil {
			slog.Warn("batch child done: failed to load parent",
				"error", err, "parent_id", uuidToString(g.parentID))
			continue
		}
		// Same parent guards as the single path (see notifyParentOfChildDone).
		parentStatus, err := effective(parent)
		if err != nil {
			slog.Warn("batch child done: failed to resolve parent status", "error", err, "parent_id", uuidToString(parent.ID))
			continue
		}
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

		statuses, err := resolveChildStatuses(children, effective)
		if err != nil {
			slog.Warn("batch child done: failed to resolve sibling statuses", "error", err, "parent_id", uuidToString(parent.ID))
			continue
		}
		batch := len(g.children) > 1
		if !siblingsAreStaged(children) {
			// Unstaged: one implicit stage. Fire once iff every child is terminal
			// in the final state. stageBarrierClosed ignores `completed` on the
			// unstaged path, so any completed child stands in for the barrier check.
			if !stageBarrierClosed(children, g.children[0], statuses.isTerminal) {
				continue
			}
			h.postChildDoneComment(ctx, parent, g.children[0], children, false, 0, batch, statuses, g.children)
			continue
		}

		// Staged: announce the HIGHEST stage among this batch's completed children
		// whose barrier is closed in the final state. This is what makes the
		// result order-independent — whether the caller sent [stage1, stage2] or
		// [stage2, stage1], the final committed state is identical, so the same
		// top stage wins and stageProgressSummary's "Stage N is next" reflects
		// reality rather than a mid-batch snapshot. A lower closed stage would
		// re-introduce the stale "advance the next stage" instruction the bug was
		// about.
		rep, found := highestClosedBatchStage(children, g.children, statuses.isTerminal)
		if !found {
			continue
		}
		h.postChildDoneComment(ctx, parent, rep, children, true, rep.Stage.Int32, batch, statuses, g.children)
	}
}

// highestClosedBatchStage selects the first completed child in the highest
// closed stage of a staged sibling set. The terminal predicate must come from
// a pre-resolved child-status snapshot: all required statuses must be known
// before selection. A stage S is closed iff no non-terminal staged sibling has
// stage <= S, so finding the earliest open stage once reduces selection from
// O(N*K) to O(N+K).
func highestClosedBatchStage(children, completed []db.Issue, isTerminal func(db.Issue) bool) (db.Issue, bool) {
	var lowestCompleted pgtype.Int4
	for _, c := range completed {
		if c.Stage.Valid && (!lowestCompleted.Valid || c.Stage.Int32 < lowestCompleted.Int32) {
			lowestCompleted = c.Stage
		}
	}
	if !lowestCompleted.Valid {
		return db.Issue{}, false
	}
	var firstOpen pgtype.Int4
	for _, c := range children {
		if !c.Stage.Valid {
			continue // Unstaged siblings were not resolved and cannot block a stage.
		}
		if !isTerminal(c) {
			if c.Stage.Int32 <= lowestCompleted.Int32 {
				return db.Issue{}, false // This sibling blocks every candidate; do not scan the rest.
			}
			if !firstOpen.Valid || c.Stage.Int32 < firstOpen.Int32 {
				firstOpen = c.Stage
			}
		}
	}
	var rep db.Issue
	found := false
	for _, c := range completed {
		if !c.Stage.Valid || (firstOpen.Valid && c.Stage.Int32 >= firstOpen.Int32) {
			continue
		}
		if !found || c.Stage.Int32 > rep.Stage.Int32 {
			found = true
			rep = c
		}
	}
	return rep, found
}

// postChildDoneComment builds and posts the parent's child-done system comment
// for a closed stage barrier, then dispatches the parent-assignee trigger. It
// assumes every guard in notifyParentOfChildDone / notifyParentsOfBatchChildDone
// has already passed and that `completed` is a terminal child whose barrier is
// closed within `children` (the final sibling set).
//
// `completed` is the representative terminal child named in the comment.
// `staged`/`closedStage` describe the closed barrier (closedStage is unused for
// an unstaged set). `batch` selects batch-aware wording. `batchCompleted` is the
// set that transitioned to terminal in this batch; it is nil for single updates.
func (h *Handler) postChildDoneComment(ctx context.Context, parent, completed db.Issue, children []db.Issue, staged bool, closedStage int32, batch bool, statuses resolvedChildStatuses, batchCompleted []db.Issue) {
	prefix := h.getIssuePrefix(ctx, completed.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(completed.Number))
	childID := uuidToString(completed.ID)
	title := sanitizeChildTitleForSystemComment(completed.Title)
	parentID := uuidToString(parent.ID)
	completedStatus := statuses.status(completed)

	// 拼父级受理人的提及前缀。父级没有受理人、或受理人行已丢失（成员被删、工作区已失联的归档
	// agent 等）时前缀为空串。
	mentionPrefix := h.buildParentAssigneeMention(ctx, parent)

	var content string
	if staged {
		stageCancelledCount := countStageCancelled(children, closedStage, statuses.status)
		stageCancelled := stageCancelledCount > 0
		advanceHasCancelled := stageCancelled
		if batch {
			// A single batch can close several stages. Always preserve cancellation
			// already present in the named stage, and also account for lower stages
			// newly cancelled by this same batch without repeating older lower-stage
			// warnings.
			advanceHasCancelled = stageCancelled || batchClosedScopeHasCancelled(children, batchCompleted, closedStage, statuses.status)
		}
		summary, nextStage := stageProgressSummary(children, closedStage, statuses.status)
		advance := stageAdvanceInstruction(nextStage, parentID, stageCancelledCount, advanceHasCancelled)
		if !stageCancelled {
			// Keep the historical no-cancellation wording byte-identical for the
			// named stage. A lower stage cancelled in the same batch can still add
			// the dependency warning through advanceHasCancelled above.
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
		} else if batch {
			lastAction := "已完成"
			if completedStatus == "cancelled" {
				lastAction = "已取消"
			}
			content = fmt.Sprintf(
				"%s当前任务的第 %d 阶段已结束——该阶段的子任务已在批量更新中全部结束； 最近处理的是 [%s](mention://issue/%s) — \"%s\" — %s. 阶段进度：%s.%s",
				mentionPrefix, closedStage, identifier, childID, title, lastAction, summary, advance,
			)
		} else {
			lastAction := "刚刚完成"
			if completedStatus == "cancelled" {
				lastAction = "刚刚取消"
			}
			content = fmt.Sprintf(
				"%s当前任务的第 %d 阶段已结束——最后一个子任务 [%s](mention://issue/%s) — \"%s\" — %s. 阶段进度：%s.%s",
				mentionPrefix, closedStage, identifier, childID, title, lastAction, summary, advance,
			)
		}
	} else {
		hasCancelled := anyCancelledChildren(children, statuses.status)
		if !hasCancelled {
			// Keep the historical no-cancellation wording byte-identical.
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
		} else {
			lastAction := "已完成"
			if completedStatus == "cancelled" {
				lastAction = "已取消"
			}
			warning := unstagedCancellationInstruction()
			if !batch {
				lastAction = "刚刚完成"
				if completedStatus == "cancelled" {
					lastAction = "刚刚取消"
				}
				content = fmt.Sprintf(
					"%s所有子任务都已结束——最后一个子任务 [%s](mention://issue/%s) — \"%s\", %s.%s 请汇总子任务结果并继续推进父任务；如果已经没有后续工作，请运行 `multica issue status %s in_review` 将父任务标记为待评审。",
					mentionPrefix, identifier, childID, title, lastAction, warning, parentID,
				)
			} else {
				content = fmt.Sprintf(
					"%s所有子任务都已结束——本次批量更新后全部进入终态，最近处理的是 [%s](mention://issue/%s) — \"%s\" — %s.%s 请汇总子任务结果并继续推进父任务；如果已经没有后续工作，请运行 `multica issue status %s in_review` 将父任务标记为待评审。",
					mentionPrefix, identifier, childID, title, lastAction, warning, parentID,
				)
			}
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
// Resolve without rewriting the issue rows, which also supply the original
// user-selected status to downstream rendering. Built-in keys need no I/O.
// Unlike display-oriented Resolver callers, this side-effecting path must
// reject unresolved custom keys: parent or sibling rows can be newer than the
// catalog snapshot. A miss must not bypass a parked/terminal parent's guard.
func (h *Handler) childStatusResolver(ctx context.Context) func(db.Issue) (string, error) {
	resolvers := make(map[pgtype.UUID]*issuestatus.Resolver)
	return func(c db.Issue) (string, error) {
		if issuestatus.IsBuiltIn(c.Status) {
			return c.Status, nil
		}
		resolver := resolvers[c.WorkspaceID]
		if resolver == nil {
			resolver = issuestatus.NewResolver(c.WorkspaceID)
			resolvers[c.WorkspaceID] = resolver
		}
		status := resolver.Effective(ctx, h.issueStatusCatalog(), c.Status)
		if !issuestatus.IsCategory(status) {
			return "", fmt.Errorf("unresolved status %q in workspace %s", c.Status, uuidToString(c.WorkspaceID))
		}
		return status, nil
	}
}

// resolvedChildStatuses keeps the canonical status of every child needed by
// the barrier and comment builders. Keeping the three-state information here
// (open / done / cancelled) prevents the reporting path from mistaking
// "terminal" for "done" while still letting the barrier ask its narrower
// terminality question.
type resolvedChildStatuses map[pgtype.UUID]string

func (s resolvedChildStatuses) status(child db.Issue) string {
	return s[child.ID]
}

func (s resolvedChildStatuses) isTerminal(child db.Issue) bool {
	return isTerminalChildStatus(s.status(child))
}

// resolveChildStatuses checks every status needed by the stage barrier and
// progress summary before either can produce a notification. Stored status
// keys stay untouched; the returned snapshot contains their canonical values.
func resolveChildStatuses(children []db.Issue, effective func(db.Issue) (string, error)) (resolvedChildStatuses, error) {
	statuses := make(resolvedChildStatuses, len(children))
	staged := siblingsAreStaged(children)
	for _, child := range children {
		if staged && !child.Stage.Valid {
			continue // Neither stage helper considers unstaged siblings.
		}
		status, err := effective(child)
		if err != nil {
			return nil, fmt.Errorf("resolve child %s status %q: %w", uuidToString(child.ID), child.Status, err)
		}
		statuses[child.ID] = status
	}
	return statuses, nil
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

// stageProgressSummary renders a compact per-stage breakdown for the
// child-done system comment. `done` counts only genuinely completed children;
// canonical `cancelled` children are surfaced separately. nextStage is the
// lowest stage above closedStage that still has non-terminal children, or 0
// when none remain. Unstaged children are skipped.
func stageProgressSummary(children []db.Issue, closedStage int32, statusOf func(db.Issue) string) (summary string, nextStage int32) {
	type agg struct{ total, done, cancelled, terminal int }
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
		switch statusOf(c) {
		case "done":
			a.done++
			a.terminal++
		case "cancelled":
			a.cancelled++
			a.terminal++
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	parts := make([]string, 0, len(order))
	for _, s := range order {
		a := byStage[s]
		label := fmt.Sprintf("阶段 %d：已完成 %d/%d", s, a.done, a.total)
		if a.cancelled > 0 {
			label += fmt.Sprintf("，已取消 %d", a.cancelled)
		}
		if nextStage == 0 && s > closedStage && a.terminal < a.total {
			nextStage = s
			label += "（下一阶段）"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; "), nextStage
}

// countStageCancelled counts the cancelled children of one stage. The advance
// instruction reports the number rather than the fact, because the agent it is
// written for has to decide whether the next stage can start without that work:
// "2 sub-issues cancelled" is checkable against the layout, "includes cancelled
// items" is not.
func countStageCancelled(children []db.Issue, stage int32, statusOf func(db.Issue) string) int {
	n := 0
	for _, child := range children {
		if child.Stage.Valid && child.Stage.Int32 == stage && statusOf(child) == "cancelled" {
			n++
		}
	}
	return n
}

// subIssueCount renders a sub-issue count with the right plural.
func subIssueCount(n int) string {
	if n == 1 {
		return "1 个子任务"
	}
	return fmt.Sprintf("%d 个子任务", n)
}

// batchClosedScopeHasCancelled reports whether this batch newly cancelled work
// in any stage up to the highest closed stage represented by the aggregated
// notification. It reads stage/status from the final sibling snapshot, using
// the batch rows only as an ID set, so a concurrently moved child cannot make a
// stale stage number leak into the decision.
func batchClosedScopeHasCancelled(children, batchCompleted []db.Issue, closedStage int32, statusOf func(db.Issue) string) bool {
	completedIDs := make(map[pgtype.UUID]struct{}, len(batchCompleted))
	for _, child := range batchCompleted {
		completedIDs[child.ID] = struct{}{}
	}
	for _, child := range children {
		if _, ok := completedIDs[child.ID]; !ok {
			continue
		}
		if child.Stage.Valid && child.Stage.Int32 <= closedStage && statusOf(child) == "cancelled" {
			return true
		}
	}
	return false
}

func anyCancelledChildren(children []db.Issue, statusOf func(db.Issue) string) bool {
	for _, child := range children {
		if statusOf(child) == "cancelled" {
			return true
		}
	}
	return false
}

// stageAdvanceInstruction returns the trailing instruction appended to a
// staged child-done system comment, given the next stage with pending work
// among the sub-issues that currently exist (nextStage, 0 = none).
//
//   - nextStage > 0: a later stage with unfinished work already exists, so
//     point the leader at it.
//   - nextStage == 0: no later stage exists *among the sub-issues created so
//     far*. This deliberately does NOT assert that the workflow is finished.
//     The server has no declarative workflow model — stages are agent-driven
//     and often created lazily (stage N+1's sub-issues are only written after
//     stage N produces the inputs they depend on), so an intermediate stage in
//     such a pipeline reaches nextStage == 0 exactly like a true final stage
//     does. The old wording ("This was the final stage. Wrap up the parent")
//     asserted a finality the server cannot know and pushed leaders to wrap up
//     mid-workflow (MUL-4062 / #4927). The message now names both possibilities
//     and hands the create-next-vs-wrap-up decision back to the leader.
//   - stageCancelled: how many sub-issues of the stage this comment names were
//     cancelled. Non-zero also means the headline above calls the stage
//     `closed` rather than complete, so the instruction says "结束" to
//     match rather than contradicting its own comment with "完成".
//   - scopeCancelled: whether anything the update closed was cancelled, which
//     for a batch includes lower stages that carry no count of their own. It
//     decides whether the warning renders at all; stageCancelled decides
//     whether the warning can be specific. The server still does not decide
//     the dependency question itself either way.
func stageAdvanceInstruction(nextStage int32, parentID string, stageCancelled int, scopeCancelled bool) string {
	var instruction string
	if nextStage > 0 {
		instruction = fmt.Sprintf(
			" 下一步是阶段 %d。请先通过 `multica issue children %s` 查看完整结构；确认阶段 %d 的依赖已经满足后，再把其中处于 `backlog` 的子任务推进到 `todo`。请逐条阅读子任务描述，只推进明确满足依赖的任务；如果描述与父任务概览冲突，先保留在 `backlog` 并留言确认。",
			nextStage, parentID, nextStage,
		)
	} else {
		verb := "完成"
		if stageCancelled > 0 {
			verb = "结束"
		}
		instruction = fmt.Sprintf(" %s当前阶段不代表整个任务已经完成。请判断父任务是否真正完成：如果完成，请汇总结果并运行 `multica issue status %s in_review` 标记为待评审；如果仍需要下一阶段，请创建该阶段及其子任务。", verb, parentID)
	}
	if !scopeCancelled {
		return instruction
	}
	// Only the named stage has a count attached to it. A batch that also closed
	// lower stages has no single stage to name, so it keeps the general warning
	// rather than reporting a number that would not match any one stage.
	if stageCancelled == 0 {
		return instruction + " 刚结束的工作包含已取消项：推进前请确认被取消的工作不是后续任务的依赖。不确定时先不要推进或创建下一阶段，请留言确认。"
	}
	if nextStage > 0 {
		return instruction + fmt.Sprintf(" 刚结束的阶段有 %s已取消：推进前请确认阶段 %d 不依赖被取消的工作。不确定时先不要推进，请留言确认。", subIssueCount(stageCancelled), nextStage)
	}
	return instruction + fmt.Sprintf(" 刚结束的阶段有 %s已取消：推进前请确认被取消的工作不是后续任务的依赖。不确定时先不要创建下一阶段，请留言确认。", subIssueCount(stageCancelled))
}

func unstagedCancellationInstruction() string {
	return " 推进父任务前，请确认后续任务不需要被取消的工作。不确定时保留父任务当前状态，请留言确认。"
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
