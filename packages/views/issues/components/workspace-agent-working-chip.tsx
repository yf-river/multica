"use client";

// 本文件：WorkspaceAgentWorkingChip —— 问题表面（列表/看板等）头部的
// "有 agent 在干活"过滤 chip。
//
// 数字权威地等于"点开后的行数"：它统计的是本表面作用域 + 当前活跃过滤器
// 会展示、且正有 agent 在跑的行——由表面控制器从服务端 `working_agents`
// facet（与行来自同一份编译查询）解析。MUL-5525 之前它自己发一次全工作区
// `/api/working-agents` 读取，因此在项目页上可能宣传"离项目十万八千里的
// agent 在工作"，点开却是空列表。
//
// `agents === undefined` 表示投影尚未解析。chip 的每个表面随之保持不确定态
// ——标签、紧凑数字、颜色档位、悬停正文都一样——因为"0"与"还不知道"
// 是不同的断言，一旦其中一个被渲染成另一个，读者便无从分辨。
//
// 点击只切换视图状态；控制器负责把"在跑的问题集合"翻译成查询里的
// `working_issue_ids` 过滤器。

import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Button } from "@multica/ui/components/ui/button";
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@multica/ui/components/ui/hover-card";
import { useActorName } from "@multica/core/workspace/hooks";
import type { WorkingAgentSummary } from "@multica/core/types";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { useT } from "../../i18n";

interface WorkspaceAgentWorkingChipProps {
  value: boolean;
  onToggle: () => void;
  /** 在这个头部所属的表面内正工作的 agents——已经按其作用域和所有活跃
   *  过滤器收窄。`undefined` = 尚未解析。 */
  agents: readonly WorkingAgentSummary[] | undefined;
}

/** 投影对本表面的活动性结论。`unknown` 是一等公民，不是 `none` 的同义词。 */
export type ChipActivity = "unknown" | "none" | "some";

/**
 * 由投影得出活动性三态。
 */
export function chipActivity(
  agents: readonly WorkingAgentSummary[] | undefined,
): ChipActivity {
  if (agents === undefined) return "unknown";
  return agents.length > 0 ? "some" : "none";
}

/**
 * chip 穿哪档配色，以及允许与它同框的唯一一批类。有活动用淡染，
 * 过滤器开启用实心品牌档，闲置表面保持中性。
 *
 * 中性档上的弱化文字才读作"这儿没动静"，所以未解析的投影拿到的是
 * *不带*弱化文字的中性档：我们还不知道这表面是否闲置，压暗它等于断言如此。
 */
export function chipAppearance(
  value: boolean,
  activity: ChipActivity,
): { variant: "brand" | "brandSubtle" | "outline"; className: string } {
  const layout = "h-8 px-2 md:h-7 md:px-2.5";
  if (value) return { variant: "brand", className: layout };
  if (activity === "some") return { variant: "brandSubtle", className: layout };
  if (activity === "unknown") return { variant: "outline", className: layout };
  return { variant: "outline", className: `${layout} text-muted-foreground` };
}

/**
 * 每个读取 working-agents 投影的表面共用的悬停正文——本表面的过滤器 chip
 * 和 issue detail 的子问题头部 chip 都用它。共享是为了让"收窄过的读数"与
 * "未收窄的读数"用同一种话术描述活动；两者唯一的差别是投影的作用域。
 *
 * 身份来自工作区 agent 目录而不是数据本身：表面投影只是 id 与计数的
 * 一个切面，在这里解析名字/头像让两个调用方对"某 agent 叫什么、长啥样"
 * 只有一份定义。
 *
 * 三种泾渭分明的状态，前两种绝不塌缩成彼此：`undefined` = 投影尚未解析，
 * `[]` = 已解析且本表面确实没人工作，非空列表 = 名单。给未解析的投影渲染
 * "空"的句子，等于在没有任何证据时断言"此刻没有 agent 在工作"——正是 chip
 * 计数当年犯过的那类无据断言（MUL-5525）。
 */
export function WorkingAgentsHoverContent({
  agents,
}: {
  agents: readonly WorkingAgentSummary[] | undefined;
}) {
  const { t } = useT("issues");
  const { getActorName, getActorInitials, getActorAvatarUrl } = useActorName();

  if (agents === undefined) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.agent_activity.unknown_hover)}
      </p>
    );
  }

  if (agents.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.agent_activity.empty_hover)}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="text-caption font-medium text-muted-foreground">
        {t(($) => $.agent_activity.hover_header, { count: agents.length })}
      </div>
      <div className="flex flex-col gap-1.5">
        {agents.map((agent) => (
          <div key={agent.id} className="flex items-center gap-2 text-caption">
            <ActorAvatar
              name={getActorName("agent", agent.id)}
              initials={getActorInitials("agent", agent.id)}
              avatarUrl={getActorAvatarUrl("agent", agent.id) ?? undefined}
              isAgent
              size="sm"
            />
            <span className="min-w-0 flex-1 truncate font-medium">
              {getActorName("agent", agent.id)}
            </span>
            <span className="shrink-0 tabular-nums text-muted-foreground">
              {t(($) => $.agent_activity.tasks_count, {
                count: agent.running_task_count,
              })}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * 问题表面头部的"agents 工作中"过滤器 chip（职责见文件头）。
 */
export function WorkspaceAgentWorkingChip({
  value,
  onToggle,
  agents,
}: WorkspaceAgentWorkingChipProps) {
  const { t } = useT("issues");
  const activity = chipActivity(agents);
  const agentIds = agents?.map((agent) => agent.id) ?? [];
  const label =
    activity === "unknown"
      ? t(($) => $.agent_activity.chip_agents_working_unknown)
      : t(($) => $.agent_activity.chip_agents_working, { count: agentIds.length });
  const appearance = chipAppearance(value, activity);

  const trigger = (
    <Button
      variant={appearance.variant}
      size="sm"
      className={appearance.className}
      onClick={onToggle}
      aria-pressed={value}
      aria-label={label}
    >
      {activity === "some" && (
        <AgentAvatarStack agentIds={agentIds} size="sm" max={3} />
      )}
      <span className="tabular-nums md:hidden">
        {activity === "unknown" ? "—" : agentIds.length}
      </span>
      <span className="hidden tabular-nums md:inline">{label}</span>
    </Button>
  );

  return (
    <HoverCard>
      <HoverCardTrigger render={trigger} />
      <HoverCardContent align="end" className="w-72">
        {/* 原样透传投影。在这里默认成 []，等于给一个尚未解析的状态
            硬塞一个明确的"没人在工作"。 */}
        <WorkingAgentsHoverContent agents={agents} />
      </HoverCardContent>
    </HoverCard>
  );
}
