// 职责定位：这一整组 buildXxxDomainEvent 函数负责把「数据库里的一次写操作」包装成
// events.Event（进程内总线 + outbox 持久化共用的领域事件信封），是各 handler 在业务落库
// 后广播「发生了什么」的唯一入口。每类聚合（project/label/issue/squad/agent/workspace/
// member/invitation/autopilot 等）各有一个构造器，把数据库行转成响应同款 payload，
// 并填好三个对事件正确性至关重要的字段（语义见 events.Event 注释）：
//   - StreamKey：同一聚合的所有事件共用一个流键（如 "issue:<id>"），保证对同一行的
//     多次修改被串行认领、按序处理；
//   - IdempotencyKey：由「事件类型 + 聚合 ID + 版本分量」拼成。版本分量通常是该行的
//     UpdatedAt 纳秒（或自增 Revision）：同一笔已提交的修改重试时会复现同一把键 → 去重
//     跳过；而后续哪怕做的是同样操作，也会因版本前进而得到新事件；
//   - 删除类事件（如 EventProjectDeleted）故意不附被删对象的完整详情（行已不存在，
//     快照也没有意义），只在 payload 里留 ID。
//
// 约定：project 表没有 revision 列，故用 UpdatedAt 当版本；workspaceToResponseSnapshot
// 刻意不依赖 Handler 状态，保证在无请求上下文的地方也能构造。本文件只构造事件、不负责
// 发布/持久化，发布走调用方的 outbox/总线流程。
package handler

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildProjectDomainEvent 构造 project 变更的持久化事件信封。project 表没有独立的
// revision 列，因此用 UpdatedAt 的时间戳（纳秒字符串）充当幂等键里的版本分量，保证
// 同一笔更新重放时键不变、更新的行内容变化时键前进。
func buildProjectDomainEvent(eventType string, project db.Project, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = util.UUIDToString(project.ID)
	if eventType != protocol.EventProjectDeleted {
		payload["project"] = projectToResponse(project)
	}
	version := strconv.FormatInt(project.UpdatedAt.Time.UnixNano(), 10)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "project:" + eventType + ":" + util.UUIDToString(project.ID) + ":" + version,
		StreamKey:      "project:" + util.UUIDToString(project.ID),
		WorkspaceID:    util.UUIDToString(project.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildProjectResourceDomainEvent 构造 project 资源（project_resource 行）变更的事件。
// 幂等键的版本分量直接用自增 Revision；流键归到其所属 project 的流（"project:<id>"），
// 使同一 project 下资源的串行顺序与其行内 revision 语义一致。
func buildProjectResourceDomainEvent(eventType string, resource db.ProjectResource, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = util.UUIDToString(resource.ProjectID)
	payload["resource_id"] = util.UUIDToString(resource.ID)
	payload["resource"] = projectResourceToResponse(resource)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "project_resource:" + eventType + ":" + util.UUIDToString(resource.ID) + ":" + strconv.FormatInt(resource.Revision, 10),
		StreamKey:      "project:" + util.UUIDToString(resource.ProjectID),
		WorkspaceID:    util.UUIDToString(resource.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildLabelDomainEvent 构造标签变更的事件。标签挂在工作区下，流键是
// "workspace:<id>"（整表标签共享一个流）；幂等键用 UpdatedAt 的高精度时间戳做版本。
func buildLabelDomainEvent(eventType string, label db.IssueLabel, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["label_id"] = util.UUIDToString(label.ID)
	payload["label"] = labelToResponse(label)
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "label:" + eventType + ":" + util.UUIDToString(label.ID) + ":" + label.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(label.WorkspaceID),
		WorkspaceID:    util.UUIDToString(label.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildIssueLabelsChangedEvent 构造「issue 的标签集合发生变化」的事件。事件里带变化后
// 的完整标签列表与 issue 当前 revision，供监听方刷新整组标签。
func buildIssueLabelsChangedEvent(issue db.Issue, actorType, actorID string, labels []LabelResponse, revision int64) events.Event {
	return events.Event{
		Type:           protocol.EventIssueLabelsChanged,
		IdempotencyKey: "issue_labels:changed:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(issue.ID),
			"labels":         labels,
			"issue_revision": revision,
		},
	}
}

// buildPinDomainEvent 构造「用户级置顶（pin）」变更的事件。置顶属于某个用户而非工作区，
// 所以流键是 "user:<userID>"；幂等键直接由调用方传入的 key（即被置顶对象的唯一标识）
// 充当稳定分量——同一对象同一次操作重放时键不变。
func buildPinDomainEvent(eventType, workspaceID, userID, actorType, actorID string, payload map[string]any, key string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "pin:" + eventType + ":" + key,
		StreamKey:      "user:" + userID,
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildPropertyDomainEvent 构造 issue 自定义属性（字段）定义变更的事件。属性定义按
// workspace 维护，流键归工作区流；payload 里附上响应形状的属性（使用计数给 0，此处
// 不关心引用数）。
func buildPropertyDomainEvent(eventType string, property db.IssueProperty, actorType, actorID string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "property:" + eventType + ":" + util.UUIDToString(property.ID) + ":" + property.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(property.WorkspaceID),
		WorkspaceID:    util.UUIDToString(property.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"property": propertyToResponse(property, 0), "property_id": util.UUIDToString(property.ID)},
	}
}

// buildIssuePropertiesChangedEvent 构造「issue 的属性值集合发生变化」的事件，携带变化后
// 的属性映射与 issue revision；流键与标签变更事件一致，保证同一条 issue 的两种变更
// 在各自流的顺序内被消费。
func buildIssuePropertiesChangedEvent(issue db.Issue, actorType, actorID string, properties map[string]any) events.Event {
	return events.Event{
		Type:           protocol.EventIssuePropertiesChanged,
		IdempotencyKey: "issue_properties:changed:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(issue.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"issue_id": util.UUIDToString(issue.ID), "properties": properties, "issue_revision": issue.Revision},
	}
}

// buildSquadDomainEvent 构造 squad（小队）本身变更的事件。流键是 "squad:<id>"，
// 保证同一 squad 的修改被串行消费；删除事件不附带 squad 详情（行已删除），只留 ID。
func buildSquadDomainEvent(eventType string, squad db.Squad, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["squad_id"] = util.UUIDToString(squad.ID)
	if eventType != protocol.EventSquadDeleted {
		payload["squad"] = map[string]any{
			"id": util.UUIDToString(squad.ID), "workspace_id": util.UUIDToString(squad.WorkspaceID),
			"name": squad.Name, "leader_id": util.UUIDToString(squad.LeaderID),
		}
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "squad:" + eventType + ":" + util.UUIDToString(squad.ID) + ":" + squad.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "squad:" + util.UUIDToString(squad.ID), WorkspaceID: util.UUIDToString(squad.WorkspaceID),
		ActorType: actorType, ActorID: actorID, Payload: payload,
	}
}

// buildSquadMemberEvent 构造 squad 成员关系变更（加入/离开/角色调整）的事件。
// 幂等键由「动作 + squadID + 成员类型 + 成员ID」组成：同一成员同一动作重放不会重复，
// 且与成员在 squad 内的身份绑定；payload 记录动作方向供监听方做增量更新。
func buildSquadMemberEvent(eventType string, squadID, workspaceID, memberType, memberID, actorType, actorID, action string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "squad:member:" + action + ":" + squadID + ":" + memberType + ":" + memberID,
		StreamKey:      "squad:" + squadID, WorkspaceID: workspaceID, ActorType: actorType, ActorID: actorID,
		Payload: map[string]any{"squad_id": squadID, "member_type": memberType, "member_id": memberID, "action": action},
	}
}

// buildAgentDomainEvent 构造 agent 本体变更的持久化事件。关键约束：事件载荷绝不携带任何
// 含秘密的字段（环境变量/密钥等由其它通道下发），只带与实时 API 相同形状的脱敏快照
// （broadcastAgentResponse）。幂等键版本分量用 UpdatedAt 纳秒：同一笔已提交修改的重试
// 复用原键被去重；之后哪怕是同一逻辑动作的修改，也会因时间戳前进而得到新事件。
func (h *Handler) buildAgentDomainEvent(eventType string, agent db.Agent, actorType, actorID string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "agent:" + eventType + ":" + util.UUIDToString(agent.ID) + ":" + agent.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"agent":    broadcastAgentResponse(h.agentToResponse(agent)),
			"agent_id": util.UUIDToString(agent.ID),
		},
	}
}

// buildAgentSkillsDomainEvent 构造 agent 技能集变更（绑定/解绑）的事件。
func buildAgentSkillsDomainEvent(agent db.Agent, actorType, actorID, operation string, skillIDs []pgtype.UUID) events.Event {
	ids := make([]string, 0, len(skillIDs))
	for _, id := range skillIDs {
		ids = append(ids, util.UUIDToString(id))
	}
	// 先排序再拼幂等键：技能集是「集合」语义，绑定/解绑时传入的顺序不影响结果，
	// 幂等键也必须与顺序无关——否则同一次操作的两次提交（顺序不同）会被当成两个事件。
	sort.Strings(ids)
	key := strings.Join(ids, ",")
	return events.Event{
		Type:           protocol.EventAgentStatus,
		IdempotencyKey: "agent:skills:" + util.UUIDToString(agent.ID) + ":" + operation + ":" + key,
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"agent_id":  util.UUIDToString(agent.ID),
			"operation": operation,
			"skill_ids": ids,
		},
	}
}

// buildWorkspaceDomainEvent 构造 workspace 聚合变更的事件。独立于任何请求/响应上下文，
// 因此可脱离 HTTP handler 在别处（如后台任务）构造同样的 outbox 载荷。
func buildWorkspaceDomainEvent(eventType string, workspace db.Workspace, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["workspace_id"] = util.UUIDToString(workspace.ID)
	if eventType != protocol.EventWorkspaceDeleted {
		payload["workspace"] = workspaceToResponseSnapshot(workspace)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "workspace:" + eventType + ":" + util.UUIDToString(workspace.ID) + ":" + workspace.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(workspace.ID),
		WorkspaceID:    util.UUIDToString(workspace.ID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// workspaceToResponseSnapshot 把 workspace 行转成响应同款快照 map。刻意不依赖 Handler
// 状态（区别于依赖 h 的 workspaceToResponse）：本文件在构造该事件时，workspace 行里的
// avatar_url 已是规范完整值，无需走 Handler 补全逻辑。
func workspaceToResponseSnapshot(workspace db.Workspace) map[string]any {
	var settings any
	if len(workspace.Settings) > 0 {
		_ = json.Unmarshal(workspace.Settings, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	var repos any
	if len(workspace.Repos) > 0 {
		_ = json.Unmarshal(workspace.Repos, &repos)
	}
	if repos == nil {
		repos = []any{}
	}
	return map[string]any{
		"id": uuidToString(workspace.ID), "name": workspace.Name, "slug": workspace.Slug,
		"description": textToPtr(workspace.Description), "context": textToPtr(workspace.Context),
		"settings": settings, "repos": repos, "issue_prefix": workspace.IssuePrefix,
		"avatar_url": textToPtr(workspace.AvatarUrl), "created_at": timestampToString(workspace.CreatedAt),
		"updated_at": timestampToString(workspace.UpdatedAt),
	}
}

// buildMemberDomainEvent 构造工作区成员关系变更（加入/移除/改角色）的事件。幂等键的
// 版本分量用「角色 + CreatedAt」：同一成员同一时刻的同一次变更重放不重复；成员属于
// 工作区，故流键用 workspace 流。
func buildMemberDomainEvent(eventType string, member db.Member, actorType, actorID string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["member_id"] = util.UUIDToString(member.ID)
	payload["workspace_id"] = util.UUIDToString(member.WorkspaceID)
	payload["user_id"] = util.UUIDToString(member.UserID)
	if eventType != protocol.EventMemberRemoved {
		payload["member"] = memberToResponse(member)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "member:" + eventType + ":" + util.UUIDToString(member.ID) + ":" + member.Role + ":" + member.CreatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "workspace:" + util.UUIDToString(member.WorkspaceID),
		WorkspaceID:    util.UUIDToString(member.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildInvitationDomainEvent 构造工作区邀请（invitation）生命周期事件（创建/接受/过期/
// 撤销等）。幂等键附加 action 分量区分同一邀请的不同阶段；邀请事件走 workspace 流，
// 由调用方把具体业务详情放进 payload。
func buildInvitationDomainEvent(eventType string, invitation db.WorkspaceInvitation, actorType, actorID, action string, payload map[string]any) events.Event {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["invitation_id"] = util.UUIDToString(invitation.ID)
	payload["workspace_id"] = util.UUIDToString(invitation.WorkspaceID)
	payload["invitee_email"] = invitation.InviteeEmail
	payload["invitee_user_id"] = uuidToPtr(invitation.InviteeUserID)
	if eventType == protocol.EventInvitationCreated {
		payload["invitation"] = invitationToResponse(invitation)
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "invitation:" + eventType + ":" + util.UUIDToString(invitation.ID) + ":" + action,
		StreamKey:      "workspace:" + util.UUIDToString(invitation.WorkspaceID),
		WorkspaceID:    util.UUIDToString(invitation.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildAutopilotDomainEvent 构造自动化（autopilot）变更的持久化事件，载荷与实时 API
// 使用的响应形状一致。keySuffix 携带被改动行的版本或子行 id：两个不同的改动即使类型与
// action 相同，也会因 keySuffix 不同而成为两条独立事件，不会并成一条 outbox 记录。
func buildAutopilotDomainEvent(eventType string, autopilot db.Autopilot, actorType, actorID, action, keySuffix string, payload map[string]any) events.Event {
	// 持久事件会推送整个工作区，webhook 凭据只可经鉴权后的 HTTP 响应返回。
	if trigger, ok := payload["trigger"].(AutopilotTriggerResponse); ok {
		payload["trigger"] = broadcastAutopilotTriggerResponse(trigger)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["autopilot_id"] = util.UUIDToString(autopilot.ID)
	if action != "" {
		payload["action"] = action
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "autopilot:" + eventType + ":" + util.UUIDToString(autopilot.ID) + ":" + action + ":" + keySuffix,
		StreamKey:      "autopilot:" + util.UUIDToString(autopilot.ID),
		WorkspaceID:    util.UUIDToString(autopilot.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}
