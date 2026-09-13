"use client";

// 本文件：CustomStatusChip —— 在只显示"状态类别"的界面里给自定义状态正名。
// 看板列/列表分区都是"类别"，同一列里可能是不同自定义状态；
// 本 chip 在卡片/行上补出那个具体状态名。若某状态就是它所属类别的内置代表，
// 则什么都不渲染——表头已写了类别名，再重复一遍 chip 纯属噪音。

import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import type { IssueStatusCatalog } from "@multica/core/issue-statuses";
import { useWorkspaceId } from "@multica/core/hooks";
import type { IssueStatus } from "@multica/core/types";
import { StatusIcon } from "./status-icon";

/**
 * {@link CustomStatusChip} 是否会为这个状态渲染出东西。
 *
 * 把 chip 包进容器的布局需要它：否则当卡片上唯一的 chip（即状态 chip）
 * 决定保持沉默时，那个容器会渲染出一条带自身外边距的空 flex 行。
 */
export function useIsCustomStatus(status: IssueStatus): boolean {
  const wsId = useWorkspaceId();
  return isCustomStatus(useIssueStatuses(wsId), status);
}

/**
 * {@link useIsCustomStatus} 背后的纯谓词——已持有目录的组件无需为同一问题
 * 再开一个订阅观察器。
 */
function isCustomStatus(catalog: IssueStatusCatalog, status: IssueStatus): boolean {
  const entry = catalog.entryOf(status);
  if (!entry) return false;
  // `is_system` 是权威依据；key 比较兜底覆盖目录尚未加载的窗口期——
  // 那时内置状态同样必须保持沉默。
  return entry.is_system !== true && status !== catalog.categoryOf(status);
}

/**
 * 当所在界面只展示"类别"时，为某个问题的状态补上具体名字（MUL-6243）。
 *
 * 看板列与列表分区都是类别，因此同一"评审中"列里可能坐着两个不同状态
 * ——"代码评审"与"QA"——而卡片上没有任何信息能区分它们。本 chip 就是
 * 那条缺失的信号。
 *
 * 对"本身就是其类别内置代表"的状态渲染为空：列头已经写着"评审中"，
 * 再放一个复读机 chip 纯属噪音。因此从未定义自定义状态的工作区，
 * 视觉上完全不会有变化。
 */
export function CustomStatusChip({
  status,
  className = "",
}: {
  status: IssueStatus;
  className?: string;
}) {
  const wsId = useWorkspaceId();
  // 谓词与条目共用同一个目录观察器——订阅两次在网络上是免费的（React Query
  // 会去重请求），但在重渲染上不是。
  const catalog = useIssueStatuses(wsId);
  const entry = catalog.entryOf(status);

  if (!isCustomStatus(catalog, status) || !entry) return null;

  return (
    <span
      className={`inline-flex max-w-[160px] items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground ${className}`}
    >
      <StatusIcon
        status={status}
        category={entry.category}
        color={entry.color}
        className="size-3"
      />
      <span className="truncate">{entry.name}</span>
    </span>
  );
}
