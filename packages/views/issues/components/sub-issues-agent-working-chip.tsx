"use client";

// 本文件：SubIssuesAgentWorkingChip —— 问题详情"子问题"头部的人数聚合 chip。
// 行内的 IssueAgentActivityIndicator 回答"哪个子问题正在被处理"；
// 本 chip 回答"当前有多少 agent 正在动这个父问题的子问题"，列表折叠时也
// 始终可见。

import { memo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  HoverCard,
  HoverCardTrigger,
  HoverCardContent,
} from "@multica/ui/components/ui/hover-card";
import { useWorkspaceId } from "@multica/core/hooks";
import { workspaceWorkingAgentsOptions } from "@multica/core/agents";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { WorkingAgentsHoverContent } from "./workspace-agent-working-chip";
import { useT } from "../../i18n";

interface SubIssuesAgentWorkingChipProps {
  /** 本 chip 聚合其直接子问题的那个父问题。 */
  parentIssueId: string;
}

/**
 * 问题详情"子问题"头部的"N 个 agent 工作中"聚合 chip（multica#5825）。
 * 行内的 IssueAgentActivityIndicator 回答"哪个子问题正在被处理"；
 * 本 chip 回答"现在有多少 agent 在这个父问题的子问题上干活"，无需逐行
 * 扫列表，且列表折叠时信号依然可见。
 *
 * 它与 Issues 列表头部读同一份 /api/working-agents 投影，只是用 `parent`
 * 收窄。这是有意为之：头部数字是对某个作用域的主张，所以作用域与算术都
 * 由服务端负责——与 Issues 列表完全一致。若在此由工作区任务快照自行推导，
 * 客户端就会出现第二份"谁在工作中"的定义，数字与悬停正文还得各自重推。
 *
 * 行内指示器则继续读快照——"一份共享查询按行切片"对逐行提示是恰当形态，
 * 行装饰过时也无所谓。头部数字相反：它必须权威。
 */
export const SubIssuesAgentWorkingChip = memo(
  function SubIssuesAgentWorkingChip({
    parentIssueId,
  }: SubIssuesAgentWorkingChipProps) {
    const { t } = useT("issues");
    const wsId = useWorkspaceId();
    const { data: agents = [] } = useQuery(
      workspaceWorkingAgentsOptions(wsId, "issue", undefined, parentIssueId),
    );

    if (agents.length === 0) return null;

    const agentIds = agents.map((agent) => agent.id);

    return (
      <HoverCard>
        <HoverCardTrigger
          render={
            <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-muted/60 px-2 py-0.5" />
          }
        >
          <AgentAvatarStack agentIds={agentIds} size="xs" max={3} />
          {/* 不能加 leading-none：光晕动效用 background-clip: text 画字形，
              背景只覆盖行盒——行盒被压扁会让下降部笔画变透明。 */}
          <span className="animate-chat-text-shimmer text-micro font-medium tabular-nums">
            {t(($) => $.agent_activity.chip_agents_working, {
              count: agentIds.length,
            })}
          </span>
        </HoverCardTrigger>
        <HoverCardContent align="start" className="w-72">
          <WorkingAgentsHoverContent agents={agents} />
        </HoverCardContent>
      </HoverCard>
    );
  },
);
