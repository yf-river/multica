"use client";

// 本文件：IssueAgentHeaderChip —— 问题详情头部"现在有 agent 在忙吗"chip。
// 放在头部（而非可滚动正文），让"正在工作"的信号停在固定位置，
// 不跟正文列未来的吸顶横幅抢地盘。取代原先正文里的吸顶 live 卡。
//
// 与右侧面板执行日志共用同一份按问题读的任务列表
// （共享 `issueKeys.tasks(issueId)` 缓存），所以头部 chip 与日志对
// "什么在跑"永远一致。两个表面都派生自同一个查询，消除了旧的全工作区
// agent 任务快照比本按问题列表拉得慢、chip 落后于日志"agent 工作中"
// 指示的竞态。
//
// 折叠态刻意保持浅显：
//   - 一个 agent 在跑 → 头像 + "{名字} 工作中"
//   - 多个在跑       → 头像堆叠 + "N 个 agent 工作中"
//   - 仅排队         → "{名字} 已排队" / "N 个 agent 已排队"，
//                      半透明头像 / 弱化文字（无光束动画）
//
// 悬停 chip 展开一张紧凑 Popover 卡，行内容与右侧面板的活动行一致
// （点击 / 键盘同样可切换，供触屏与无障碍使用）。那些行只显示必要的
// 状态/时间与任务入口动作，不渲染事件计数、也不为计数预取任务消息。

import { memo, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { useActorName } from "@multica/core/workspace/hooks";
import { cn } from "@multica/ui/lib/utils";
import { BorderBeam } from "@multica/ui/components/common/border-beam";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import type { AgentTask } from "@multica/core/types";
import { TranscriptButton } from "../../common/task-transcript";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { ActiveTaskRow } from "./execution-log-section";
import { useT } from "../../i18n";

interface IssueAgentHeaderChipProps {
  issueId: string;
}

/**
 * 头部 live chip：本问题有 agent 在跑 / 排队时渲染（职责见文件头）。
 */
export const IssueAgentHeaderChip = memo(function IssueAgentHeaderChip({
  issueId,
}: IssueAgentHeaderChipProps) {
  const { t } = useT("issues");
  // 与 ExecutionLogSection 同款查询选项，让两处共用一个缓存条目。
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });

  const { running, queued } = useMemo(() => {
    const running: AgentTask[] = [];
    const queued: AgentTask[] = [];
    // 列表已被接口限定在本问题范围内，所以这里只需要按状态切分。
    for (const task of tasks) {
      if (task.status === "running") running.push(task);
      else if (
        task.status === "queued" ||
        task.status === "dispatched" ||
        // 因 busy local_directory 被 daemon 停放——仍然活跃，只是在等一个
        // 路径锁。归入 live chip，不能丢掉。
        task.status === "waiting_local_directory"
      )
        queued.push(task);
      // 终态属于执行日志的故事，不是 live chip 的。
    }
    return { running, queued };
  }, [tasks]);

  const [openedTranscriptTaskSnapshot, setOpenedTranscriptTaskSnapshot] =
    useState<AgentTask | null>(null);
  const openedTranscriptTask = openedTranscriptTaskSnapshot
    ? tasks.find((task) => task.id === openedTranscriptTaskSnapshot.id) ??
      openedTranscriptTaskSnapshot
    : null;

  // 没有活跃工作 → 什么都不渲染。
  if (running.length === 0 && queued.length === 0 && !openedTranscriptTask) return null;

  return (
    <>
      {running.length > 0 || queued.length > 0 ? (
        <ActiveChip
          issueId={issueId}
          running={running}
          queued={queued}
          onTranscriptOpenChange={(task, open) => {
            setOpenedTranscriptTaskSnapshot(open ? task : null);
          }}
        />
      ) : null}
      {openedTranscriptTask ? (
        <TranscriptButton
          task={openedTranscriptTask}
          agentName=""
          isLive={openedTranscriptTask.status === "running"}
          title={t(($) => $.execution_log.transcript_tooltip)}
          renderButton={false}
          open
          onOpenChange={(open) => {
            if (!open) setOpenedTranscriptTaskSnapshot(null);
          }}
        />
      ) : null}
    </>
  );
});

interface ActiveChipProps {
  issueId: string;
  running: AgentTask[];
  queued: AgentTask[];
  onTranscriptOpenChange: (task: AgentTask, open: boolean) => void;
}

/**
 * chip 本体 + 悬停卡片（活动行复用 ActiveTaskRow）。
 */
function ActiveChip({
  issueId,
  running,
  queued,
  onTranscriptOpenChange,
}: ActiveChipProps) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();

  const activeTasks = [...running, ...queued];
  const agentIds = [...new Set(activeTasks.map((task) => task.agent_id))];
  const anyRunning = running.length > 0;
  const isSingle = agentIds.length === 1;
  // 文案必须跟着真实状态走：只有确实在跑时才说"工作中"。什么都不跑
  // （排队 / dispatched / 等路径锁）时 chip 读作"已排队"，
  // 免得还没开工的 agent 被误标成"工作中"。
  const label = isSingle
    ? t(
        ($) =>
          anyRunning ? $.agent_live.is_working : $.agent_live.is_queued,
        { name: getActorName("agent", agentIds[0] ?? "") },
      )
    : t(
        ($) =>
          anyRunning
            ? $.agent_activity.hover_header
            : $.agent_activity.hover_header_queued,
        { count: agentIds.length },
      );

  return (
    <div className="flex items-center gap-1">
      <Popover>
        {/* 悬停即打开卡片，让"正在工作"读起来是可扫视的状态表面而不是
            一个点击目标。Base UI 里悬停配置挂在 Trigger 上（popover 可以有
            多个触发器），不在 Root 上。触发器保持真实按钮，因此点击与键盘
            （Enter/Space）对触屏与无障碍同样可用。短打开延迟避免指针
            只是掠过 chip 时的闪烁；关闭延迟让指针跨越悬停桥进入可交互行时
            卡片保持打开。 */}
        <PopoverTrigger
          openOnHover
          delay={150}
          closeDelay={200}
          render={
            <button
              type="button"
              aria-label={label}
              // agent 确实在跑时，chip 戴上品牌色描边光束——一道扫过圆角边的
              // 高光——让被触发的运行在头部无可错认地"活着"。仅排队的
              // 状态保持平静（无光束），把动效留给真正在途的工作。
              className={cn(
                "flex h-9 min-w-9 max-w-[11rem] items-center justify-center gap-1.5 rounded-md px-2 text-muted-foreground outline-none transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring md:h-7 md:min-w-0 md:justify-start md:px-1.5",
                anyRunning && "relative bg-brand/5",
              )}
            />
          }
        >
          <AgentAvatarStack
            agentIds={agentIds}
            size="sm"
            max={3}
            opacity={anyRunning ? "full" : "half"}
          />
          <span
            className={cn(
              "hidden min-w-0 truncate text-caption md:inline",
              anyRunning ? "text-info" : "text-muted-foreground",
            )}
          >
            {label}
          </span>
          {anyRunning && <BorderBeam />}
        </PopoverTrigger>
        <PopoverContent align="end" keepMounted className="w-80">
          <div className="text-caption font-medium text-muted-foreground">
            {t(
              ($) =>
                anyRunning
                  ? $.agent_activity.hover_header
                  : $.agent_activity.hover_header_queued,
              { count: agentIds.length },
            )}
          </div>
          <div className="flex flex-col gap-0.5">
            {activeTasks.map((task) => (
              <ActiveTaskRow
                key={task.id}
                task={task}
                issueId={issueId}
                onTranscriptOpenChange={(open) => {
                  onTranscriptOpenChange(task, open);
                }}
              />
            ))}
          </div>
        </PopoverContent>
      </Popover>
      {/* 与动作按钮隔开——chip 是状态段，不是又一个按钮，
          用一条发丝线让两组保持可辨。 */}
      <span className="h-4 w-px bg-border" aria-hidden="true" />
    </div>
  );
}
