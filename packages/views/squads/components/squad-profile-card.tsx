"use client";

/**
 * squads 模块：小队资料卡片。
 * 展示一个小队的头像、名称、简介和成员列表，供引用小队的地方（如 issue
 * 的关联区、成员详情页）内嵌展示小队信息。
 */

import { useQuery } from "@tanstack/react-query";
import type { SquadMemberPreview } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  squadListOptions,
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ActorAvatar } from "../../common/actor-avatar";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

/** 小队资料卡片的属性。 */
interface SquadProfileCardProps {
  /** 要展示的小队 id。 */
  squadId: string;
}

/**
 * 小队资料卡主组件。
 * 从工作区的目录查询里找到指定小队并渲染信息；小队还没加载出来时先显示
 * 骨架屏，小队不存在时显示"不可用"占位文案。
 * @param props.squadId 要展示的小队 id。
 */
export function SquadProfileCard({ squadId }: SquadProfileCardProps) {
  const { t } = useT("squads");
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { data: squads = [], isLoading: squadsLoading } = useQuery(
    squadListOptions(wsId),
  );
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: wsMembers = [] } = useQuery(memberListOptions(wsId));

  const squad = squads.find((s) => s.id === squadId);

  if (squadsLoading && !squad) {
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

  if (!squad) {
    return (
      <div className="text-caption text-muted-foreground">
        {t(($) => $.profile_card.unavailable)}
      </div>
    );
  }

  const isArchived = !!squad.archived_at;
  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  const memberPreview = squad.member_preview ?? [];
  const memberCount = squad.member_count ?? memberPreview.length;

  return (
    <div className="group flex flex-col gap-3 text-left">
      <div className="flex items-start gap-3">
        <ActorAvatarBase
          name={squad.name}
          initials={initials}
          avatarUrl={squad.avatar_url}
          isSquad
          size="xl"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <p className="truncate text-body font-semibold">{squad.name}</p>
            {isArchived && (
              <span className="rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
                {t(($) => $.profile_card.archived)}
              </span>
            )}
          </div>
        </div>
        {!isArchived && (
          <AppLink
            href={p.squadDetail(squad.id)}
            className="mr-1 mt-0.5 shrink-0 text-caption font-normal text-brand opacity-0 transition-opacity group-hover:opacity-100"
          >
            {t(($) => $.profile_card.detail_link)}
          </AppLink>
        )}
      </div>

      {squad.description && (
        <p className="line-clamp-2 text-caption text-muted-foreground">
          {squad.description}
        </p>
      )}

      {memberCount > 0 && (
        <MembersList
          members={memberPreview}
          memberCount={memberCount}
          leaderId={squad.leader_id}
          agents={agents}
          wsMembers={wsMembers}
        />
      )}
    </div>
  );
}

/**
 * 小队资料卡内的成员列表。
 * 至多展示前 3 个成员，多余数量用"还有 N 个"提示。每个成员解析其名称、
 * 头像与详情链接：agent 成员跳 agent 详情、member 成员跳成员详情。
 */
function MembersList({
  members,
  memberCount,
  leaderId,
  agents,
  wsMembers,
}: {
  members: SquadMemberPreview[];
  memberCount: number;
  leaderId: string;
  agents: { id: string; name: string }[];
  wsMembers: { user_id: string; name: string; role: string }[];
}) {
  const { t } = useT("squads");
  const p = useWorkspacePaths();
  const visible = members.slice(0, 3);
  const overflow = Math.max(0, memberCount - visible.length);

  return (
    <div className="flex flex-col gap-1.5 text-caption">
      <span className="text-muted-foreground">
        {t(($) => $.profile_card.members_section)}
        <span className="ml-1 tabular-nums">· {memberCount}</span>
      </span>
      <div className="flex flex-col gap-0.5">
        {visible.map((m) => {
          const isLeader =
            m.member_type === "agent" && m.member_id === leaderId;
          const name =
            m.member_type === "agent"
              ? agents.find((a) => a.id === m.member_id)?.name ??
                m.member_id.slice(0, 8)
              : wsMembers.find((u) => u.user_id === m.member_id)?.name ??
                m.member_id.slice(0, 8);
          const href =
            m.member_type === "agent"
              ? p.agentDetail(m.member_id)
              : p.memberDetail(m.member_id);
          const memberRole =
            m.member_type === "member"
              ? wsMembers.find((u) => u.user_id === m.member_id)?.role ?? null
              : null;

          return (
            <AppLink
              key={`${m.member_type}-${m.member_id}`}
              href={href}
              className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 transition-colors hover:bg-accent/60"
            >
              <ActorAvatar
                actorType={m.member_type}
                actorId={m.member_id}
                size="sm"
                showStatusDot={m.member_type === "agent"}
                className="shrink-0"
              />
              <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
              {isLeader && (
                <span className="max-w-[4rem] shrink-0 truncate rounded-md bg-amber-100 px-1 py-0.5 text-micro font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400">
                  {t(($) => $.members_tab.leader_chip)}
                </span>
              )}
              {m.member_type === "member" && memberRole && (
                <span className="max-w-[3.5rem] shrink-0 truncate text-muted-foreground">
                  {memberRole}
                </span>
              )}
            </AppLink>
          );
        })}
        {overflow > 0 && (
          <span className="px-2 py-0.5 text-muted-foreground">
            {t(($) => $.profile_card.more_members, { count: overflow })}
          </span>
        )}
      </div>
    </div>
  );
}
