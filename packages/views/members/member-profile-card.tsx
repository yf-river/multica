"use client";

/**
 * members 模块：成员资料卡片。
 * 展示成员的姓名、邮箱、角色徽标与头像，以及该成员拥有的 agents 列表
 * （按近 30 天运行次数排序）。供引用成员的界面（如悬停卡、指派区）内嵌展示。
 */

import { useQuery } from "@tanstack/react-query";
import type { Agent, MemberRole } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core";
import { agentRunCounts30dOptions } from "@multica/core/agents";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useWorkspacePaths } from "@multica/core/paths";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ActorAvatar } from "../common/actor-avatar";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

/** 成员资料卡组件的属性。 */
interface MemberProfileCardProps {
  // 用户的 UUID——与 member.user_id、agent.owner_id 对应。接收 user_id
  // （而非 member.id），是因为每个现有调用点传的都是 user_id（指派者、评论
  // 者、owner 在多态 actor 模型里都是 User UUID）。
  userId: string;
}

// 对齐 AgentProfileCard 的结构，让两个悬停面看起来像一对（「agent 和人类成员
// 都是平级团队成员」）。内容刻意不对称：人类成员展示身份 + 他们拥有的 AI
// agents；没有状态点，因为今天还没有成员在线状态（member-presence）骨架，
// 我们也不打算臆造一个。
export function MemberProfileCard({ userId }: MemberProfileCardProps) {
  const { t } = useT("members");
  const wsId = useWorkspaceId();
  const { data: members = [], isLoading: membersLoading } = useQuery(
    memberListOptions(wsId),
  );
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runCounts = [] } = useQuery(agentRunCounts30dOptions(wsId));

  const member = members.find((m) => m.user_id === userId);

  if (membersLoading && !member) {
    return (
      <div className="flex items-center gap-3">
        <Skeleton className="h-10 w-10 rounded-full" />
        <div className="flex-1 space-y-1.5">
          <Skeleton className="h-4 w-28" />
          <Skeleton className="h-3 w-20" />
        </div>
      </div>
    );
  }

  if (!member) {
    return (
      <div className="text-caption text-muted-foreground">{t(($) => $.card.unavailable)}</div>
    );
  }

  const initials = member.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  // 按 30 天运行次数给拥有的 agents 排序（最常用的在前）；次数相同按名称，
  // 保证顺序稳定。运行次数来自驱动 Agents 列表 RUNS 列的同一个工作区级查询
  // ——不额外发请求。
  const runCountById = new Map(runCounts.map((r) => [r.agent_id, r.run_count]));
  const ownedAgents = agents
    .filter((a) => a.owner_id === userId && !a.archived_at)
    .sort((a, b) => {
      const ra = runCountById.get(a.id) ?? 0;
      const rb = runCountById.get(b.id) ?? 0;
      if (ra !== rb) return rb - ra;
      return a.name.localeCompare(b.name);
    });

  return (
    <div className="flex flex-col gap-3 text-left">
      {/* Header */}
      <div className="flex items-start gap-3">
        <ActorAvatarBase
          name={member.name}
          initials={initials}
          avatarUrl={resolvePublicFileUrl(member.avatar_url)}
          size="xl"
          className="rounded-full"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <p className="truncate text-body font-semibold">{member.name}</p>
            <RoleBadge role={member.role} />
          </div>
          <p className="mt-0.5 truncate text-caption text-muted-foreground">
            {member.email}
          </p>
        </div>
      </div>

      {/* Owned agents */}
      {ownedAgents.length > 0 && <OwnedAgentsSection agents={ownedAgents} />}
    </div>
  );
}

/** 成员角色的彩色文字徽标（owner/admin/member）。 */
function RoleBadge({ role }: { role: MemberRole }) {
  const { t } = useT("members");
  return (
    <span className="rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
      {role === "owner"
        ? t(($) => $.role.owner)
        : role === "admin"
          ? t(($) => $.role.admin)
          : t(($) => $.role.member)}
    </span>
  );
}

/** 该成员拥有的 agents 区块：至多展示前 2 个，每行链接到 agent 详情页。 */
function OwnedAgentsSection({ agents }: { agents: Agent[] }) {
  const { t } = useT("members");
  // 按频率取前 2（父级已排序），每行都链到 agent 详情页。在线状态点经
  // ActorAvatar 的 showStatusDot 叠在头像上——刻意不传 `enableHoverCard`，
  // 避免 popover 套 popover 的嵌套；点进去就够满足「想了解更多」。AppLink
  // 使用平台导航适配器，因此在 Web（Next 路由）与桌面（react-router-dom）上
  // 无需按应用分叉。
  const p = useWorkspacePaths();
  const visible = agents.slice(0, 2);
  const overflow = agents.length - visible.length;

  return (
    <div className="flex flex-col gap-1.5 text-caption">
      <span className="text-muted-foreground">{t(($) => $.card.agents_section, { count: agents.length })}</span>
      <div className="flex flex-col gap-0.5">
        {visible.map((a) => (
          <AppLink
            key={a.id}
            href={p.agentDetail(a.id)}
            className="group -mx-1 flex cursor-pointer items-start gap-2 rounded-md px-1 py-1 transition-colors hover:bg-accent"
          >
            <ActorAvatar
              actorType="agent"
              actorId={a.id}
              size="sm"
              showStatusDot
              className="mt-0.5 shrink-0"
            />
            <div className="min-w-0 flex-1">
              <div className="truncate font-medium">{a.name}</div>
              {a.description && (
                <div className="truncate text-muted-foreground">
                  {a.description}
                </div>
              )}
            </div>
            <span
              aria-hidden
              className="mt-0.5 shrink-0 font-normal text-brand opacity-0 transition-opacity group-hover:opacity-100"
            >
              {t(($) => $.card.detail_link)}
            </span>
          </AppLink>
        ))}
        {overflow > 0 && (
          <span className="text-muted-foreground">
            {t(($) => $.card.more_agents, { count: overflow })}
          </span>
        )}
      </div>
    </div>
  );
}
