"use client";

import { ShimmerText } from "@multica/ui/components/common/shimmer-text";

import { memo, useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  HoverCard,
  HoverCardTrigger,
  HoverCardContent,
} from "@multica/ui/components/ui/hover-card";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import type { AgentTask } from "@multica/core/types";
import type { AvatarSize } from "@multica/ui/lib/avatar-size";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { AgentActivityHoverContent } from "../../agents/components/agent-activity-hover-content";
import { selectIssueTasks, type IssueTaskGroups } from "../surface/activity";
import { useT } from "../../i18n";

const EMPTY_GROUPS: IssueTaskGroups = { running: [], queued: [] };

// 活动卡打开前的驻留阈值（MUL-5189）。
//
// 这个徽标是骑在密集滚动列表右缘（收件箱行、问题行、看板卡）的被动提示，
// 且在每个 agent 正碰过的问题上都会出现。Base UI 600ms 默认值是针对
// "用户瞄准的悬停目标"调的；这里指针前往行、归档按钮或下一行时总是会
// 穿过徽标，600ms 会在"路过"而非"意图"时触发，288px 的卡片会盖住
// 下面的行。
//
// 900ms 落在随意路过之后、却又在一个有意的"它在干嘛？"停顿之内。头部
// chip（issue-agent-header-chip）刻意保留 150ms：它是用户瞄准的一个大
// chip，不是逐行提示。
//
// 卡片正文只读——无链接、无按钮——所以没有悬停桥要保护，
// 关闭延迟只需吸收越过 4px 缝隙的指针抖动。
const OPEN_DELAY_MS = 900;
const CLOSE_DELAY_MS = 150;

interface IssueAgentActivityIndicatorProps {
  issueId: string;
  // 头像档位。保持很小——这是卡片角落的提示，不是主控件。
  // 默认 xs（16px）在典型看板密度下读起来像个点，
  // 但悬停放大时仍能看清 agent 的脸。
  size?: AvatarSize;
  // 是否悬停打开活动卡。当卡片唯一增量信息不值得一个弹层时选择关闭
  //（收件箱——见下）。
  hoverCard?: boolean;
}

/**
 * 展示在看板卡右上角、列表行标识符正后方的小徽标："此刻有没有 agent 在这
 * 条问题上干活"。状态派生自全工作区 agent 任务快照：
 *
 *   - 有 ≥1 运行中任务  → 小型头像堆叠 + 微光"Working"
 *   - 0 运行、≥1 排队   → 半透明堆叠 + 弱化"Queued"
 *   - 什么都没有          → 返回 null（无任何 UI 装饰或占位）
 *
 * 状态文案使用共享 ShimmerText，头像静态展示。
 *
 * 悬停打开 AgentActivityHoverContent，列出每个活跃任务（状态点 + 时长）。
 * 没有链接行——卡片本身就是跳转 issue detail 的导航目标。
 *
 * 只需要提示的表面可传 `hoverCard={false}` 单拿徽标。收件箱就这么做
 *（MUL-5189）：徽标已经显示谁在跑、在工作还是排队，所以对分诊表面而言
 * 卡片的唯一增量信息是已耗时——而它从不会改变收件箱行存在的那个决策
 *（"我要不要打开它？"）。问题列表与看板卡保留它：盯着在途工作正是那些
 * 视图的用途，已耗时在那里承重。
 *
 * 订阅唯一一份共享工作区快照查询，但用 `select` 收窄到本问题的任务。
 * React Query 的结构共享让该选中值在本问题任务未变时引用稳定——因此一次
 * 快照失效（WS task:* 事件，由 use-realtime-sync 驱动）只会重渲染那些
 * 自身任务真有动静的行，而不是整张列表。这正是 agent 繁忙时大问题列表
 * 仍便宜的降幅机制（MUL-4474）。30s staleTime 只是离线兜底。
 */
export const IssueAgentActivityIndicator = memo(function IssueAgentActivityIndicator({
  issueId,
  size = "xs",
  hoverCard = true,
}: IssueAgentActivityIndicatorProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const select = useCallback(
    (snapshot: AgentTask[]) => selectIssueTasks(snapshot, issueId),
    [issueId],
  );
  const { data: groups = EMPTY_GROUPS } = useQuery({
    ...agentTaskSnapshotOptions(wsId),
    select,
  });

  const { agentIds, opacity } = useMemo(() => {
    // 头像堆叠取主角：优先 running；0 个 running 时退回 queued。
    // 每种情形视觉都不同（running 带微光、queued 弱化文字），
    // 所以指示器总能提供一张脸来悬停。
    const primary = groups.running.length > 0 ? groups.running : groups.queued;
    const uniqueAgents = [...new Set(primary.map((t) => t.agent_id))];
    return {
      agentIds: uniqueAgents,
      opacity: (groups.running.length > 0 ? "full" : "half") as "full" | "half",
    };
  }, [groups]);

  if (agentIds.length === 0) return null;
  const isRunning = opacity === "full";

  const badge = (
    <>
      <AgentAvatarStack
        agentIds={agentIds}
        size={size}
        opacity={opacity}
        max={3}
      />
      <ShimmerText active={isRunning} className="text-micro text-muted-foreground">
        {isRunning
          ? t(($) => $.agent_activity.status_running)
          : t(($) => $.agent_activity.status_queued)}
      </ShimmerText>
    </>
  );

  if (!hoverCard) {
    return (
      <span className="inline-flex shrink-0 items-center gap-1">{badge}</span>
    );
  }

  const hoverTasks = [...groups.running, ...groups.queued];

  return (
    <HoverCard>
      <HoverCardTrigger
        delay={OPEN_DELAY_MS}
        closeDelay={CLOSE_DELAY_MS}
        render={
          <span className="inline-flex shrink-0 items-center gap-1" />
        }
      >
        {badge}
      </HoverCardTrigger>
      <HoverCardContent align="end" className="w-72">
        <AgentActivityHoverContent tasks={hoverTasks} />
      </HoverCardContent>
    </HoverCard>
  );
});
