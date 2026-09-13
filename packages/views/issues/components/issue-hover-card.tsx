"use client";

// 本文件：IssueHoverCard —— @提到的问题在悬停时展示的详情卡。
// 行内 chip 能放下的只有状态、标识符和截断后的标题；卡片承载 chip 装不下
// 的：完整标题、优先级、描述摘要、负责人与子问题进度。issue 提及是唯一带
// 悬停预览的提及类型。放在这里而非 packages/ui，是因为它要读工作区查询。

import { issueStatusCategory } from "@multica/core/issues";
import { useStatusLabel } from "./../utils/status-label";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { childIssueProgressOptions, issueDetailOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  HoverCard,
  HoverCardTrigger,
  HoverCardContent,
} from "@multica/ui/components/ui/hover-card";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";
import { ActorAvatar } from "../../common/actor-avatar";
import { descriptionPreview } from "./description-preview";
import { PriorityIcon } from "./priority-icon";
import { ProgressRing } from "./progress-ring";
import { StatusIcon } from "./status-icon";

interface IssueHoverCardProps {
  issueId: string;
  children: ReactNode;
  /**
   * 详情拉取失败时用来称呼该问题的标识符（如 "MUL-7"）。与 chip 降级显示
   * 用的同一个标签——加载不出来的卡片也仍然说得清它讲的是哪个问题。
   */
  fallbackLabel?: string;
  /**
   * 打开延迟覆盖值，毫秒。仅为测试而存在——生产环境什么都不传，走 Base UI
   * 默认的 600ms。测试传 0，悬停断言不必等真实延迟。
   */
  delay?: number;
}

/**
 * @提到的 issue 在悬停时的详情（职责见文件头）。
 *
 * 行内 chip 显示状态、标识符和尽可能长的标题（封顶在 `min(18rem, 100%)`）
 * ——长标题到达时是截断的。卡片承载 chip 装不下的：完整未截断标题、优先级、
 * 描述摘要、负责人与子问题进度。issue 提及是唯一带悬停预览的提及类型——
 * 成员与 agent 提及在 rich-content.tsx 里只渲染成普通 `.mention` span。
 *
 * 所有查询都有意放在 IssueHoverCardBody 而不是这里：Base UI 只在卡片打开时
 * 挂载弹层，所以本组件在卡片打开前不发任何请求。把查询提到 IssueHoverCard
 * 会让每次提及在渲染时就发一次。（触发器里的 chip 自带一次未解决 issue 的
 * 拉取；那与此无关。）
 */
export function IssueHoverCard({
  issueId,
  children,
  fallbackLabel,
  delay,
}: IssueHoverCardProps) {
  return (
    <HoverCard>
      {/* 渲染成 span 而不是触发器默认的锚点：children 本身已经是 AppLink，
          锚点套锚点是无效 HTML。 */}
      <HoverCardTrigger delay={delay} render={<span />}>
        {children}
      </HoverCardTrigger>
      <HoverCardContent align="start" className="w-auto min-w-56 max-w-80">
        <IssueHoverCardBody issueId={issueId} fallbackLabel={fallbackLabel} />
      </HoverCardContent>
    </HoverCard>
  );
}

/**
 * 卡片的负责人行。
 *
 * 独立成组件，是为了让 `useActorName`——它订阅工作区成员列表，一条本路径上
 * 其它东西都不会预热——只在实际有负责人要命名的卡片上挂载。若内联回正文，
 * 首次悬停任意提及就会去拉成员目录。
 */
function IssueHoverCardAssignee({
  actorType,
  actorId,
}: {
  actorType: string;
  actorId: string;
}) {
  const { getActorName } = useActorName();
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      {/* 这里本身就是悬停卡，agent live-peek 变体会再发自己的请求——
          所以不嵌套卡片、也不放链接。 */}
      <ActorAvatar
        actorType={actorType}
        actorId={actorId}
        size="sm"
        enableHoverCard={false}
        profileLink={false}
        className="shrink-0"
      />
      <span className="min-w-0 truncate text-caption text-foreground">
        {getActorName(actorType, actorId)}
      </span>
    </span>
  );
}

function IssueHoverCardBody({
  issueId,
  fallbackLabel,
}: {
  issueId: string;
  fallbackLabel?: string;
}) {
  const wsId = useWorkspaceId();
  const resolveStatusLabel = useStatusLabel(wsId);
  const detail = useQuery(issueDetailOptions(wsId, issueId));
  // 一份与 issues 列表和 issue detail 共享的工作区级进度快照，不是按问题
  // 的子节点拉取：打开卡片直接复用缓存。
  const { data: childProgress } = useQuery(childIssueProgressOptions(wsId));
  const { t } = useT("issues");

  // 用骨架屏而不是本地化加载文案：只有 pending 阶段用到它，因此已落定的
  // 查询绝不会永远转圈。
  if (detail.isPending) {
    return (
      <div data-testid="issue-hover-card-skeleton" className="flex flex-col gap-1.5">
        <Skeleton className="h-3 w-20" />
        <Skeleton className="h-4 w-48" />
        <Skeleton className="mt-1.5 h-3 w-44" />
        <Skeleton className="mt-1.5 h-4 w-24" />
      </div>
    );
  }

  const issue = detail.data;

  // 每种"详情没到"的终结态都走这里：重试后仍报错、问题被删除、或提及是
  // 读者读不到的。按 chip 降级的方式用标识符称呼它，卡片就仍说得清
  // 它讲的是哪个问题。
  if (detail.isError || !issue) {
    return (
      <div className="flex flex-col gap-1.5">
        {fallbackLabel && (
          <span className="text-caption font-medium text-muted-foreground">
            {fallbackLabel}
          </span>
        )}
        <p className="text-caption text-muted-foreground">{t(($) => $.detail.not_found)}</p>
      </div>
    );
  }

  const preview = issue.description ? descriptionPreview(issue.description) : "";
  const assigneeType = issue.assignee_type;
  const assigneeId = issue.assignee_id;
  const hasAssignee = !!assigneeType && !!assigneeId;
  const progress = childProgress?.get(issue.id);
  const hasProgress = !!progress && progress.total > 0;
  // 看板卡片与列表行把优先级字形画进固定的网格槽，所以"无"的那根短横
  // 在那里占住一列。本卡片没有列要占——在这已很挤的弹层里，短横只是一枚
  // 不携带任何信息的记号。
  const hasPriority = !!issue.priority && issue.priority !== "none";

  return (
    <div className="flex flex-col gap-1.5">
      {/* 优先级、状态、标识符——与 BoardCardContent 相同的"优先级打头"
          顺序，字号与状态字形一致。StatusIcon 与 PriorityIcon 都只画裸
          <svg>、不透传 aria props，所以每个字形都由一个 `role="img"`
          包装命名：放在无角色的 span 上，aria-label 会被辅助技术丢弃。 */}
      <div className="flex items-center gap-1.5">
        {hasPriority && (
          <span role="img" aria-label={t(($) => $.priority[issue.priority])} className="flex">
            <PriorityIcon priority={issue.priority} />
          </span>
        )}
        <span
          role="img"
          aria-label={resolveStatusLabel(issue.status)}
          className="flex shrink-0"
        >
          <StatusIcon
            status={issue.status}
            category={issueStatusCategory(issue) ?? undefined}
            className="h-3.5 w-3.5 shrink-0"
          />
        </span>
        <span className="text-caption font-medium text-muted-foreground">
          {issue.identifier}
        </span>
      </div>
      {/* 完整标题才是这张卡存在的意义，所以它绝不截断——但一个不换行的
          超长 token（如粘贴的 URL）没有断点就会顶破 max-w-80 上限。 */}
      <p className="text-body text-foreground break-words">{issue.title}</p>

      {preview && (
        <p className="mt-1 text-caption text-muted-foreground line-clamp-2">{preview}</p>
      )}

      {(hasAssignee || hasProgress) && (
        <div className="mt-1 flex items-center justify-between gap-3">
          {hasAssignee ? (
            <IssueHoverCardAssignee actorType={assigneeType} actorId={assigneeId} />
          ) : (
            <span />
          )}
          {hasProgress && (
            <span className="inline-flex shrink-0 items-center gap-1">
              <ProgressRing done={progress.done} total={progress.total} size={12} />
              <span className="text-caption font-medium tabular-nums text-muted-foreground">
                {progress.done}/{progress.total}
              </span>
            </span>
          )}
        </div>
      )}
    </div>
  );
}
