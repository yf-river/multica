"use client";

// 本文件：IssueChip —— 问题的紧凑展示 chip（`状态图标 编号 标题`，带边框）。
// 纯展示、刻意不是链接/按钮：由调用方按需包上可交互外壳（markdown 提及的 AppLink、
// 编辑器 NodeView 里支持 cmd 点击的 <a>、对话锚点卡片里普通 span 等）。
// 宽度上限 `min(18rem, 100%)` 同时约束内容与容器（详见下方接口注释）。
// "问题提及卡片"的观感以此处为唯一事实来源。

import { issueStatusCategory } from "@multica/core/issues";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { issueListOptions, issueDetailOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { StatusIcon } from "./status-icon";

/**
 * 问题的紧凑、纯展示表示——`<状态图标> <编号> <标题>`，带边框，标题超宽时
 * 以省略号截断。
 *
 * 宽度上限是 `min(18rem, 100%)`——两个限制各司其职：
 *   - `18rem` 把 chip 约束在*内容*上：长标题不可能把 chip 撑到足以霸占一行正文。
 *     没有它，chip 就是一块原子内联盒，放不下当前位置时整块换行，
 *     在上一行留下一段参差的空缺——即 #6732 的"引用密集型正文"问题。
 *   - `100%` 把它约束在*容器*内，让 chip 能住进窄父级（如聊天气泡）。
 * `ProjectChip` 用完全相同的上限；两者不能漂移。
 *
 * 上限就在这里（唯一事实来源）——包装方绝不能在外面再套一层自己的 flex 容器，
 * 否则百分比上限会在包装层的固有尺寸计算中失效，可点击区域与截断后的 chip 背离。
 *
 * 这是"问题提及卡片"外观的唯一事实来源。
 * 它刻意**不是**链接或按钮：调用方把它包进自己需要的任何交互外壳里
 *（markdown 提及用 AppLink、编辑器 NodeView 内支持 cmd 点击的 <a>、
 * 对话上下文锚点卡片中关闭按钮旁的普通 span……）。
 *
 * 尺寸预算：内联使用时必须塞进 14px 行高——因此用 `py-0.5` + text-caption
 *（计算过程见 MentionView 的说明）。
 */
export interface IssueChipProps {
  issueId: string;
  /** 问题解析不出来（已删除、在别的工作区……）时显示的文字。 */
  fallbackLabel?: string;
  /** 需要共享 chip 外壳的调用方可用它整体覆盖内容。 */
  children?: ReactNode;
  /** 额外 class——调用方在此叠交互暗示
   *  （如可导航变体的 `hover:bg-accent cursor-pointer`）。 */
  className?: string;
}

const BASE_CLASS =
  "issue-mention inline-flex min-w-0 max-w-[min(18rem,100%)] items-center gap-1.5 rounded-md border mx-0.5 px-2 py-0.5 text-caption";

/**
 * 渲染一个问题 chip。先从工作区问题列表缓存找 `issueId`；
 * 找不到（如问题在列表首屏之外、落在"已完成"等分页后）就按需查详情接口。
 * 传了 `children` 时不再自行解析问题，仅复用 chip 外壳。
 */
export function IssueChip({
  issueId,
  fallbackLabel,
  children,
  className,
}: IssueChipProps) {
  const wsId = useWorkspaceId();
  const { data: issues = [] } = useQuery(issueListOptions(wsId));
  const listIssue = issues.find((i) => i.id === issueId);

  // 兜底请求：问题可能不在列表首屏（例如已完成态）里。
  const { data: detailIssue } = useQuery({
    ...issueDetailOptions(wsId, issueId),
    enabled: !listIssue,
  });

  const issue = listIssue ?? detailIssue;
  const cls = className ? `${BASE_CLASS} ${className}` : BASE_CLASS;

  if (children !== undefined) {
    return <span className={cls}>{children}</span>;
  }

  if (!issue) {
    return (
      <span className={cls}>
        <span className="min-w-0 truncate font-medium text-muted-foreground">
          {fallbackLabel ?? issueId.slice(0, 8)}
        </span>
      </span>
    );
  }

  return (
    <span className={cls}>
      <StatusIcon
        status={issue.status}
        category={issueStatusCategory(issue) ?? undefined}
        className="h-3.5 w-3.5 shrink-0"
      />
      <span className="font-medium text-muted-foreground shrink-0">
        {issue.identifier}
      </span>
      <span className="min-w-0 truncate text-foreground">{issue.title}</span>
    </span>
  );
}
