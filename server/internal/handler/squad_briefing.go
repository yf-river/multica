package handler

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// squadOperatingProtocol is the hard-coded system-level briefing prepended to
// every squad-leader claim. It explains the leader's coordinator role, the
// @mention dispatch mechanism, and the stop-after-dispatch contract.
//
// Keep the mention syntax exactly aligned with util.MentionRe — the roster
// block below renders concrete examples that round-trip through
// util.ParseMentions, and the protocol text refers to that format.
const squadOperatingProtocol = `## 小队负责人操作协议

你是一个 squad 的负责人。你的职责是**协调**，不是亲自执行工作。

你的职责按顺序是：

1. **阅读 issue**（标题、描述、最新评论、验收标准），判断哪位小队成员最适合处理。
2. **通过 @mention 委派。** 在这个 issue 下发布一条评论，@mentions 你选中的成员，并告诉他们要做什么。
   - **保持简短。** 每个 Multica agent 已经拥有这个 issue（标题、描述、所有历史评论、附件）以及工作区上下文。不要在委派评论里复述 issue 正文、历史讨论或已知事实；他们会自己阅读。
   - 只说明无法从 issue 中推断出的内容：你选择谁、为什么选他（一个短从句即可），以及你希望他们遵守的任何*额外*约束、提示或顺序。通常两三句话就够了。
   - 使用下面小队名单中展示的准确 mention markdown；只输入普通的 "@name" 不会触发任何人。
3. **记录你的评估。** 每次被触发后，无论你已经委派、判断无需行动，还是遇到错误，都要记录：
   ` + "`" + `multica squad activity <issue-id> <outcome> --reason "<short reason>"` + "`" + `
   Outcome values（outcome 取值）: ` + "`" + `action` + "`" + `（你已委派或已行动），
   ` + "`" + `no_action` + "`" + `（你已评估并决定无需行动），
   ` + "`" + `failed` + "`" + `（你遇到了错误）。
   这是每一轮都必须执行的动作；它会把你的决策记录到 issue 时间线里，方便人类看到你已经评估过这次触发。
4. **委派后停止。** 一旦你的委派评论已经发布、评估已经记录，就结束本轮。不要继续工作，不要写代码，不要打开文件。以下情况会自动重新触发你：
   - 被委派成员发布更新或向你提问；
   - 被委派成员完成工作并推动 issue 前进；
   - 有人再次在这个 issue 中 @mentions 你。
5. **每次触发都重新评估。** 当你再次醒来时，阅读新的活动，并决定是委派下一步、升级给人类报告者，还是收尾。如果无需行动（例如成员发布了不需要回复的进度更新），记录 ` + "`" + `no_action` + "`" + ` 后静默退出。

硬性规则：
- 每次委派都必须使用完整的 mention markdown 语法
  ` + "`" + `[@Name](mention://<type>/<UUID>)` + "`" + `，格式必须与下面小队名单完全一致。普通 "@name" 或裸名称不会触发 agent；如果缺少 mention link，任务就不会送达，issue 会卡住。这条不可协商：没有 mention link 就没有委派。
- 不要在委派中复述 issue 正文或历史评论；被指派者已经拥有这些上下文。重复上下文只会掩盖真正的指令。
- 除非 squad 没有其他合适成员，否则不要亲自做实现工作。squad 的存在就是为了拆分工作；绕过它会违背目的。
- 不要 @mention 未出现在下面小队名单中的成员；他们不属于这个 squad。
- 每轮一条委派评论就够了。避免刷出多条近似重复的评论。
- 如果 squad 中没有成员能够完成任务，发布评论说明能力缺口（如果可能，@mention issue 的报告者），不要静默地自己动手。
- 结束本轮前始终调用 ` + "`" + `multica squad activity` + "`" + `，即使 outcome 是 no_action。
- 你用 ` + "`" + `--status todo` + "`" + ` 创建并指派给 agent 的子 issue 已经会自动触发该 agent；这个指派本身就是触发器。如果你又在父 issue 上为同一项工作 @mention 同一个 agent，这个 agent 会并行运行两次（一次来自 mention，一次来自指派）。只能选择一条路径：要么在这个 issue 上通过 @mention 委派，要么创建一个指派给他们的 ` + "`" + `todo` + "`" + ` 子 issue。不要对同一项工作两者都做。
- 创建任何子 issue 前，先运行 ` + "`" + `multica issue children <当前 issue id> --output json` + "`" + ` 查看已有子任务；如果已有同一目标项目、同一工作意图或同一验收范围的 child issue，只能引用、补充或推进已有 child，禁止再创建新的重复 child。SOP 后续阶段看到 PM/队长已经拆出的跨项目 child 时，不要二次拆分。
- 创建子 issue 时，必须显式带上 ` + "`" + `--parent <当前 issue id>` + "`" + `，否则新 issue 会变成独立任务。跨项目子 issue 还必须显式带上 ` + "`" + `--project <目标 project UUID>` + "`" + `；只带 ` + "`" + `--parent` + "`" + ` 会继承父 issue 的项目，不能表达 gateway/config 等其它项目协作。先用 ` + "`" + `multica project list --output json` + "`" + ` 查目标 project UUID，不要用项目名、issue identifier 或猜测值替代 UUID。`

// buildSquadLeaderBriefing composes the full system briefing appended to a
// squad leader's Instructions when it claims a task on a squad-assigned
// issue. The returned string contains three sections:
//
//  1. Squad operating protocol (constant, system-level rules).
//  2. Squad roster (data — leader self-row + members with literal
//     `[@Name](mention://<type>/<UUID>)` strings ready to paste).
//  3. Squad instructions (user-defined `squad.instructions`, omitted when
//     empty so we don't leave a dangling heading).
//
// Archived agent members are skipped — there's no point asking the leader
// to delegate to a retired agent. Members whose underlying record can't be
// loaded (deleted user/agent races, FK weirdness) are also skipped silently.
func buildSquadLeaderBriefing(ctx context.Context, q *db.Queries, squad db.Squad) string {
	var sb strings.Builder
	sb.WriteString(squadOperatingProtocol)
	sb.WriteString("\n\n")
	sb.WriteString(buildSquadRoster(ctx, q, squad))
	if profile := buildSquadSOPProfile(squad.SopProfile); profile != "" {
		sb.WriteString("\n\n")
		sb.WriteString(profile)
	}

	if trimmed := strings.TrimSpace(squad.Instructions); trimmed != "" {
		sb.WriteString("\n\n## 小队说明 (")
		sb.WriteString(squad.Name)
		sb.WriteString(")\n\n")
		sb.WriteString(trimmed)
	}
	return sb.String()
}

type squadSOPProfile struct {
	ProfileKey              string           `json:"profile_key"`
	Project                 string           `json:"project"`
	Repo                    string           `json:"repo"`
	Mode                    string           `json:"mode"`
	Roles                   []map[string]any `json:"roles"`
	Steps                   []map[string]any `json:"steps"`
	ModelPolicy             map[string]any   `json:"model_policy"`
	StageSkills             []string         `json:"stage_skills"`
	OperationSkills         []string         `json:"operation_skills"`
	CrossProjectChildIssues []map[string]any `json:"cross_project_child_issues"`
	Acceptance              []string         `json:"acceptance"`
	ArchivePolicy           string           `json:"archive_policy"`
	ForbiddenActions        []string         `json:"forbidden_actions"`
}

func buildSquadSOPProfile(raw []byte) string {
	if len(raw) == 0 || string(raw) == "{}" {
		return ""
	}
	var profile squadSOPProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return ""
	}
	if profile.Project == "" && profile.ProfileKey == "" && len(profile.Steps) == 0 && len(profile.StageSkills) == 0 && len(profile.OperationSkills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## 项目 SOP 配置\n\n")
	if profile.ProfileKey != "" {
		sb.WriteString("- 模板：")
		sb.WriteString(profile.ProfileKey)
		sb.WriteString("\n")
	}
	if profile.Project != "" {
		sb.WriteString("- 项目：")
		sb.WriteString(profile.Project)
		sb.WriteString("\n")
	}
	if profile.Repo != "" {
		sb.WriteString("- 仓库：`")
		sb.WriteString(profile.Repo)
		sb.WriteString("`\n")
	}
	if profile.Mode != "" {
		sb.WriteString("- 执行方式：")
		sb.WriteString(profile.Mode)
		sb.WriteString("\n")
	}
	if len(profile.StageSkills) > 0 {
		sb.WriteString("- SOP 阶段链：")
		sb.WriteString(strings.Join(profile.StageSkills, " → "))
		sb.WriteString("\n")
	}
	if len(profile.Steps) > 0 {
		stepNames := make([]string, 0, len(profile.Steps))
		for _, step := range profile.Steps {
			name := sopStringField(step, "name", "key", "step_key")
			if name != "" {
				stepNames = append(stepNames, name)
			}
		}
		if len(stepNames) > 0 {
			sb.WriteString("- SOP 步骤链：")
			sb.WriteString(strings.Join(stepNames, " → "))
			sb.WriteString("\n")
			sb.WriteString("- 当前默认阶段：")
			sb.WriteString(stepNames[0])
			sb.WriteString("\n")
		}
	}
	if len(profile.OperationSkills) > 0 {
		sb.WriteString("- 可调用操作技能：")
		sb.WriteString(strings.Join(profile.OperationSkills, "、"))
		sb.WriteString("\n")
	}
	if len(profile.CrossProjectChildIssues) > 0 {
		sb.WriteString("- 跨项目子任务规则：如果当前 issue 需要其它项目配合，不要只在父 issue 上描述依赖；先运行 `multica issue children <当前 issue id> --output json` 查看已有 child，再运行 `multica project list --output json` 找到目标项目 UUID。只有确认没有同一目标项目、同一工作意图或同一验收范围的 child issue 时，才创建新的子 issue；如果已有 child，只能引用、补充或推进已有 child，禁止重复创建。命令形态必须包含 `--parent <当前 issue id>` 和 `--project <目标项目 id>`，例如 `multica issue create --title \"...\" --description-file ./child.md --status backlog --parent <当前 issue id> --project <目标项目 id> --output json`。如果父 issue 或任务说明明确给出目标小队 UUID，必须在同一条创建命令中传 `--assignee-id <目标小队 UUID>`，并逐项核对“目标项目 UUID + 目标小队 UUID”映射，不能按 project list 的输出顺序猜测；如果没有明确目标小队 UUID，才不要额外传 `--assignee` 或 `--assignee-id`，平台会把未指派的项目 issue 自动交给项目负责人。创建子 issue 后，不要再为同一项工作 @mention 同一个负责人，避免双触发。SOP 后续阶段看到 PM/队长已经拆出的跨项目 child 时，不要二次拆分。\n")
		for _, child := range profile.CrossProjectChildIssues {
			target := sopStringField(child, "target_project", "project", "name")
			trigger := sopStringField(child, "trigger", "when")
			assignee := sopStringField(child, "assignee", "owner")
			title := sopStringField(child, "title", "title_template")
			body := sopStringField(child, "body", "description", "instruction")
			parts := make([]string, 0, 5)
			if target != "" {
				parts = append(parts, "目标项目="+target)
			}
			if trigger != "" {
				parts = append(parts, "触发条件="+trigger)
			}
			if assignee != "" {
				parts = append(parts, "指派建议="+assignee)
			}
			if title != "" {
				parts = append(parts, "标题="+title)
			}
			if body != "" {
				parts = append(parts, "描述要点="+body)
			}
			if len(parts) > 0 {
				sb.WriteString("  - ")
				sb.WriteString(strings.Join(parts, "；"))
				sb.WriteString("\n")
			}
		}
	}
	if len(profile.Acceptance) > 0 {
		sb.WriteString("- 验收要求：")
		sb.WriteString(strings.Join(profile.Acceptance, "；"))
		sb.WriteString("\n")
	}
	if strings.TrimSpace(profile.ArchivePolicy) != "" {
		sb.WriteString("- 归档口径：")
		sb.WriteString(strings.TrimSpace(profile.ArchivePolicy))
		sb.WriteString("\n")
	}
	if len(profile.Roles) > 0 {
		sb.WriteString("- 角色分工：")
		roleParts := make([]string, 0, len(profile.Roles))
		for _, role := range profile.Roles {
			name := sopStringField(role, "name", "key")
			responsibility := sopStringField(role, "responsibility", "boundary")
			if name == "" {
				continue
			}
			if responsibility != "" {
				roleParts = append(roleParts, name+"："+responsibility)
			} else {
				roleParts = append(roleParts, name)
			}
		}
		sb.WriteString(strings.Join(roleParts, "；"))
		sb.WriteString("\n")
	}
	if len(profile.ModelPolicy) > 0 {
		sb.WriteString("- 模型策略：")
		parts := make([]string, 0, len(profile.ModelPolicy))
		for key, value := range profile.ModelPolicy {
			parts = append(parts, key+"="+sopAnyString(value))
		}
		sb.WriteString(strings.Join(parts, "；"))
		sb.WriteString("\n")
	}
	if len(profile.ForbiddenActions) > 0 {
		sb.WriteString("- 禁止事项：")
		sb.WriteString(strings.Join(profile.ForbiddenActions, "；"))
		sb.WriteString("\n")
	}
	sb.WriteString("\n当 issue 指派给这个小队时，先按 SOP 阶段链推进；记录当前阶段、验收要求和证据后，再把具体工作委派给对应操作技能或小队成员。")
	return sb.String()
}

func sopStringField(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sopAnyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// buildSquadRoster renders the squad roster section: a leader self-row
// plus one row per non-archived member, with literal mention markdown.
func buildSquadRoster(ctx context.Context, q *db.Queries, squad db.Squad) string {
	var sb strings.Builder
	sb.WriteString("## 小队名单\n\n")

	// Leader self-row. Leaders are always agents (FK enforced in schema).
	leaderName := "负责人"
	if leader, err := q.GetAgent(ctx, squad.LeaderID); err == nil {
		leaderName = leader.Name
	}
	sb.WriteString("负责人（你）：\n")
	sb.WriteString("- ")
	sb.WriteString(leaderName)
	sb.WriteString(" — agent — `")
	sb.WriteString(formatMention(leaderName, "agent", util.UUIDToString(squad.LeaderID)))
	sb.WriteString("`\n")

	members, err := q.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		members = nil
	}

	rows := make([]string, 0, len(members))
	for _, m := range members {
		// Skip the leader if they happen to also be in the member list —
		// they're already shown above and we don't want self-delegation.
		if m.MemberType == "agent" && util.UUIDToString(m.MemberID) == util.UUIDToString(squad.LeaderID) {
			continue
		}
		row := renderMemberRow(ctx, q, m)
		if row != "" {
			rows = append(rows, row)
		}
	}

	if len(rows) == 0 {
		sb.WriteString("\n成员：（无；你是这个 squad 的唯一成员）\n")
		return sb.String()
	}

	sb.WriteString("\n成员：\n")
	for _, r := range rows {
		sb.WriteString(r)
	}
	return sb.String()
}

// renderMemberRow renders a single roster row, returning "" if the member
// can't be resolved or should be skipped (e.g. archived agent).
func renderMemberRow(ctx context.Context, q *db.Queries, m db.SquadMember) string {
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
		return formatRosterRow(ag.Name, "agent", role, formatMention(ag.Name, "agent", id))
	case "member":
		user, err := q.GetUser(ctx, m.MemberID)
		if err != nil {
			return ""
		}
		// Mention syntax for humans uses the user_id (matches the rest of
		// the product — see util.MentionRe and frontend mention payloads).
		userID := util.UUIDToString(m.MemberID)
		return formatRosterRow(user.Name, "member（人类）", role, formatMention(user.Name, "member", userID))
	default:
		return ""
	}
}

func formatRosterRow(name, kind, role, mention string) string {
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
	sb.WriteString(" — `")
	sb.WriteString(mention)
	sb.WriteString("`\n")
	return sb.String()
}

// formatMention emits a mention markdown string that round-trips through
// util.ParseMentions. The label is the human display name; the link target
// uses the mention:// scheme with the entity type and UUID.
func formatMention(name, mentionType, id string) string {
	return "[@" + name + "](mention://" + mentionType + "/" + id + ")"
}
