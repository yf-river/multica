// 本文件：把 squad 领袖的任务简报拼装出来——系统级「作战协议」+ 名单（roster）+ 用户自定义
// 指示。简报是拼字符串的纯函数，注入到领袖 Agent 认领 squad 归属 issue 时的 Instructions 里。
package handler

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// squadOperatingProtocolHeader 是硬编码的系统级简报，加在每一次 squad 领袖认领的前面。
// 它说明领袖的协调者角色、@mention 派发机制，以及「派完即止」的契约。第 6 条职责（父 issue
// 状态）由 squadOperatingProtocolFor 单独追加——它是唯一随「这支 squad 是否真拥有该 issue」
// 而变化的部分。
//
// 这段文字保持纯英文（与既有 agent-harness 惯例一致），mention 语法务必与 util.MentionRe
// 精确对齐——下面的 "Squad Roster" 块渲染的实例要能经 util.ParseMentions 往返，协议文本
// 引用的正是那个格式。
const squadOperatingProtocolHeader = `## Squad Operating Protocol

**If you are reading this section, you have been activated as a squad LEADER
for this task — regardless of how the work reached you (direct assignment,
an @squad mention in a comment, quick-create, or autopilot).** Your job is to
**coordinate**, NOT to do the work yourself. Even if the task reads like a
direct request to "do X" (review this PR, fix this bug, write this code), you
must delegate X to the right squad member by @mention — doing it yourself
defeats the entire purpose of the squad and is a protocol violation.

Your responsibilities, in order:

1. **Read the issue** (title, description, latest comments, acceptance
   criteria) and decide which squad member is best suited to do the work.
   Match the task to each member's listed **skills** and role in the Squad
   Roster below — prefer the member whose skills cover the work.
2. **Delegate by @mention.** Post a single comment on this issue that
   @mentions the chosen member(s) and tells them what to do.
   - **Be terse.** Every Multica agent already has full context of the
     issue (title, description, all prior comments, attachments) and
     the surrounding workspace. Do NOT restate or summarise the
     issue body, prior discussion, or known facts in your delegation
     comment — they read it themselves.
   - Say only what cannot be inferred from the issue: who you're
     picking, why them (one short clause), and any *additional*
     constraints, hints, or sequencing you want them to follow.
     Two or three sentences is usually plenty.
   - Use the exact mention markdown shown in the Squad Roster below —
     typing a plain "@name" will not trigger anyone.
3. **Record your evaluation.** After every trigger — whether you delegated,
   decided no action is needed, or encountered an error — record it:
   ` + "`" + `multica squad activity <issue-id> <outcome> --reason "<short reason>"` + "`" + `
   Outcome values: ` + "`" + `action` + "`" + ` (you delegated or acted),
   ` + "`" + `no_action` + "`" + ` (you evaluated and decided nothing is needed),
   ` + "`" + `failed` + "`" + ` (you hit an error).
   This is mandatory on every turn — it records your decision in the
   issue timeline so humans can see you evaluated the trigger.
   Record it against the issue THIS turn is running on (the issue id in
   your task context). It does not need to be assigned to your squad.
   If the call fails, make sure the turn still leaves a record, without
   breaking the one-comment-per-turn rule: post a short comment with the
   outcome and the error ONLY if you have not already commented this turn
   (the no_action case). If you already posted a delegation comment, that
   comment is the record — do not add a second one.
4. **Stop after dispatching.** Once your delegation comment is posted
   and evaluation recorded, end your turn. Do not continue working,
   do not write code, do not open files. You will be re-triggered
   automatically when:
   - a delegated member posts an update or asks you a question;
   - a delegated member finishes and the issue moves forward;
   - someone @mentions you again on this issue.
5. **Re-evaluate on each trigger.** When you wake up again, read the new
   activity and decide whether to delegate the next step, escalate to
   the human reporter, or close the loop. If no action is needed
   (e.g. a member posted a progress update that requires no response),
   record ` + "`" + `no_action` + "`" + ` and exit silently.`

// squadParentStatusOwned 是第 6 条职责，用于「本次唤醒领袖的 issue 归属本 squad」的情形。
// 只有这种情形领袖才拥有父 issue 的状态弧。
//
// 自 MUL-6417 起，runtime 简报没有按名字消费本段的授权路由：统一 workflow 随工作进展写状态
// （某轮开始推进 issue 诉求时置 in_progress，轮末写到达态），领袖在简报侧的唯一边界特例是
// 「派发轮让父 issue 停在 in_progress」。本段真正的职责是「拥有者/客人」权限边界，画在
// Agent Identity 层（指令优先级让它压过 workflow）。拥有者领袖需要下面这条常驻收尾指令——
// @mention 派发形态（没有子 issue，就没有「子完成」系统评论）永远不会产出要求 in_review 的
// 评论，缺了它父 issue 会永远停在 in_progress；客人领袖拿到的则是禁令版
// （squadParentStatusNotOwned）。两种拼装都由 handler/squad_parent_status_contract_test.go
// 钉住。
const squadParentStatusOwned = `6. **Own the parent issue status.** This issue is assigned to your squad,
   so its status is yours to manage (unless Agent Identity forbids status
   changes). On the first assignment turn, move the parent to
   ` + "`" + `in_progress` + "`" + ` and keep it there while members work — a successful
   dispatch is not completion. On later turns, do not flip status for
   routine progress updates. When you confirm the overall goal is met, run
   ` + "`" + `multica issue status <issue-id> in_review` + "`" + ` — this responsibility is
   itself the standing instruction that authorizes that change, so do it even
   when no comment asked you to. Leave ` + "`" + `done` + "`" + ` to a human reviewer or
   existing integrations (for example a PR with close intent that merges).`

// squadParentStatusNotOwned 是其余所有领袖路径下的第 6 条职责：别人拥有的 issue 上被
// @squad mention（MUL-3724），以及 quick-create（这一轮还没有 issue）。在那里授状态权，
// 等于让一支只是被拉进来答问题的 squad，把另一个负责人的进行中 issue 推到 in_review。
const squadParentStatusNotOwned = `6. **Do NOT change this issue's status.** This issue is not assigned to your
   squad — you were pulled in by an @mention (or this is a quick-create turn,
   where the issue does not exist yet). Its status belongs to its own
   assignee. Answer, delegate, or escalate as usual, but never run
   ` + "`" + `multica issue status` + "`" + ` on it, no matter how complete the work looks
   to you.`

const squadOperatingProtocolHardRules = `Hard rules:
- EVERY delegation MUST use the full mention markdown syntax
  ` + "`" + `[@Name](mention://<type>/<UUID>)` + "`" + ` exactly as shown in the Squad
  Roster. A plain "@name" or bare name does NOT trigger the agent —
  if you skip the mention link, the task is never delivered and the
  issue stalls. This is non-negotiable: no mention link = no delegation.
- Do NOT restate the issue body or prior comments in your delegation —
  the assignee already has them. Repeating context is noise that
  buries the actual instruction.
- Do NOT do the implementation work yourself unless the squad has no
  other suitable members. The squad exists so work is split — bypassing
  it defeats the point.
- Do NOT @mention members who don't appear in the Squad Roster below;
  they are not part of this squad.
- One delegation comment per turn is enough. Avoid spamming multiple
  near-identical comments.
- If the squad has no member capable of the task, post a comment
  explaining the gap (and @mention the issue's reporter if possible)
  rather than silently doing the work.
- ALWAYS call ` + "`" + `multica squad activity` + "`" + ` before ending your turn —
  even when the outcome is no_action. If it errors on a turn where you
  posted no comment, leave one short comment instead; never let an
  evaluation end with no record at all, and never post a second comment
  just to report the error.
- A child issue you create with ` + "`" + `--status todo` + "`" + ` and an agent assignee
  already fires that agent automatically — the assignment IS the trigger.
  If you also @mention the same agent on this parent issue for the same
  work, the agent runs twice in parallel (once from the mention, once
  from the assignment). Pick exactly one path: either delegate by
  @mention on this issue, or create a ` + "`" + `todo` + "`" + ` child issue assigned to
  them. Never both for the same work.`

// squadOperatingProtocolFor 拼装协议正文，按这位领袖对该 issue 的真实权限选择第 6 条
// 「父状态」职责。
func squadOperatingProtocolFor(ownsIssueStatus bool) string {
	status := squadParentStatusNotOwned
	if ownsIssueStatus {
		status = squadParentStatusOwned
	}
	return squadOperatingProtocolHeader + "\n" + status + "\n\n" + squadOperatingProtocolHardRules
}

// buildSquadLeaderBriefing 拼出完整的系统简报，领袖在 squad 归属 issue 上认领任务时追加到
// 它的 Instructions 后面。返回字符串含三节：
//
//  1. Squad Operating Protocol（常量，系统级规则）。
//  2. Squad Roster（数据——领袖自述行 + 成员行，带可直接粘贴的
//     `[@Name](mention://<type>/<UUID>)` 字面串）。
//  3. Squad Instructions（用户定义的 `squad.instructions`，为空时省略，免得留下悬空的标题）。
//
// ownsIssueStatus 只在「本次任务绑定的 issue 归属这支 squad 本身」时为 true。简报会注入
// 到每一条领袖路径——包括 squad 只是别人 issue 上客人的那些——所以正是这个 flag 防止状态
// 权随名单一起泄露出去。
//
// 已归档的 Agent 成员会被跳过——让领袖去派发给退役 Agent 没意义。底层记录加载不了
// （用户/Agent 删除竞态、外键怪相）的成员也静默跳过。
func buildSquadLeaderBriefing(ctx context.Context, q *db.Queries, squad db.Squad, ownsIssueStatus bool) string {
	var sb strings.Builder
	sb.WriteString(squadOperatingProtocolFor(ownsIssueStatus))
	sb.WriteString("\n\n")
	sb.WriteString(buildSquadRoster(ctx, q, squad))

	if trimmed := strings.TrimSpace(squad.Instructions); trimmed != "" {
		sb.WriteString("\n\n## Squad Instructions (")
		sb.WriteString(squad.Name)
		sb.WriteString(")\n\n")
		sb.WriteString(trimmed)
	}
	return sb.String()
}

// buildSquadRoster 渲染 "## Squad Roster" 节：领袖自述行 + 每个未归档成员一行，带字面的
// mention markdown。
func buildSquadRoster(ctx context.Context, q *db.Queries, squad db.Squad) string {
	var sb strings.Builder
	sb.WriteString("## Squad Roster\n\n")

	// 领袖自述行。领袖永远是 Agent（schema 层外键强制）。
	leaderName := "Leader"
	if leader, err := q.GetAgent(ctx, squad.LeaderID); err == nil {
		leaderName = leader.Name
	}
	sb.WriteString("Leader (you):\n")
	sb.WriteString("- ")
	sb.WriteString(leaderName)
	sb.WriteString(" — agent — `")
	sb.WriteString(formatMention(leaderName, "agent", util.UUIDToString(squad.LeaderID)))
	sb.WriteString("`\n")

	members, err := q.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		members = nil
	}

	skillNamesByAgentID, skillsLoaded := loadSquadMemberSkillNames(ctx, q, members, util.UUIDToString(squad.LeaderID))

	rows := make([]string, 0, len(members))
	for _, m := range members {
		// 领袖若恰好也在成员列表里就跳过——上面已展示，也不想要自我派发。
		if m.MemberType == "agent" && util.UUIDToString(m.MemberID) == util.UUIDToString(squad.LeaderID) {
			continue
		}
		row := renderMemberRow(ctx, q, m, skillNamesByAgentID, skillsLoaded)
		if row != "" {
			rows = append(rows, row)
		}
	}

	if len(rows) == 0 {
		sb.WriteString("\nMembers: (none — you are the only member of this squad)\n")
		return sb.String()
	}

	sb.WriteString("\nMembers:\n")
	for _, r := range rows {
		sb.WriteString(r)
	}
	return sb.String()
}

// loadSquadMemberSkillNames 批量取成员 Agent 的技能名（去重、排除领袖），按 agent id 归组。
// 查询失败返回 ok=false，让调用方退化成不带技能的旧行。
func loadSquadMemberSkillNames(ctx context.Context, q *db.Queries, members []db.SquadMember, leaderID string) (map[string][]string, bool) {
	agentIDs := make([]pgtype.UUID, 0)
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		if m.MemberType != "agent" {
			continue
		}
		id := util.UUIDToString(m.MemberID)
		if id == leaderID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		agentIDs = append(agentIDs, m.MemberID)
	}
	if len(agentIDs) == 0 {
		return map[string][]string{}, true
	}
	rows, err := q.ListAgentSkillNamesByAgentIDs(ctx, agentIDs)
	if err != nil {
		return nil, false
	}
	byAgentID := make(map[string][]string, len(agentIDs))
	for _, row := range rows {
		id := util.UUIDToString(row.AgentID)
		byAgentID[id] = append(byAgentID[id], row.Name)
	}
	return byAgentID, true
}

// renderMemberRow 渲染单行名单；成员解析不出来或该跳过（如已归档 Agent）就返回 ""。
func renderMemberRow(ctx context.Context, q *db.Queries, m db.SquadMember, skillNamesByAgentID map[string][]string, skillsLoaded bool) string {
	id := util.UUIDToString(m.MemberID)
	role := strings.TrimSpace(m.Role)
	switch m.MemberType {
	case "agent":
		ag, err := q.GetAgent(ctx, m.MemberID)
		if err != nil {
			return ""
		}
		if ag.ArchivedAt.Valid {
			return ""
		}
		// Agent 带技能；亮出来，领袖就能按能力派活而不是对着自由文本的角色标签瞎猜。
		return formatRosterRow(ag.Name, "agent", role, agentSkillsRosterSegment(skillNamesByAgentID, skillsLoaded, id), formatMention(ag.Name, "agent", id))
	case "member":
		user, err := q.GetUser(ctx, m.MemberID)
		if err != nil {
			return ""
		}
		// 人类成员用 user_id 的 mention 语法（与产品其余部分一致——见 util.MentionRe 与前端
		// mention payload）。人类没有 Multica 技能，所以不渲染技能段。
		userID := util.UUIDToString(m.MemberID)
		return formatRosterRow(user.Name, "member (human)", role, "", formatMention(user.Name, "member", userID))
	default:
		return ""
	}
}

// agentSkillsRosterSegment 返回描述某个 Agent 成员已分配技能的名单段：有技能时是
// "skills: a, b"（名字已被 ListAgentSkillNamesByAgentIDs 排好序）；没有任何技能时写
// "no skills assigned"，让领袖知道这个能力真的缺失；只有查询失败时才返回 ""——一次瞬态 DB
// 错误降级成原来的 name+role 行，而不是断言一句误导性的 "no skills"。
func agentSkillsRosterSegment(skillNamesByAgentID map[string][]string, skillsLoaded bool, agentID string) string {
	if !skillsLoaded {
		return ""
	}
	names := skillNamesByAgentID[agentID]
	if len(names) == 0 {
		return "no skills assigned"
	}
	return "skills: " + strings.Join(names, ", ")
}

// formatRosterRow 拼单行名单："- 名字 — 类型[, role: "…"] [— skills] — `mention`"。
func formatRosterRow(name, kind, role, skills, mention string) string {
	var sb strings.Builder
	sb.WriteString("- ")
	sb.WriteString(name)
	sb.WriteString(" — ")
	sb.WriteString(kind)
	if role != "" {
		sb.WriteString(`, role: "`)
		sb.WriteString(role)
		sb.WriteString(`"`)
	}
	if skills != "" {
		sb.WriteString(" — ")
		sb.WriteString(skills)
	}
	sb.WriteString(" — `")
	sb.WriteString(mention)
	sb.WriteString("`\n")
	return sb.String()
}

// formatMention 产出能经 util.ParseMentions 往返的 mention markdown。label 是人类可读的
// 显示名；链接目标是 mention:// 协议带上实体类型与 UUID。
func formatMention(name, mentionType, id string) string {
	return "[@" + name + "](mention://" + mentionType + "/" + id + ")"
}
