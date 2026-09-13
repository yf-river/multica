"use client";

/** 任务详情页路由：解出 issue id 并渲染详情视图，任务切换时重置错误边界。 */
import { use } from "react";
import { IssueDetailRoute } from "@multica/views/issues/issue-detail-route";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

/**
 * 任务详情路由。
 *
 * @param props.params - 动态参数，含任务 id（Promise 形式）。
 * @returns 任务详情页（带错误边界，id 变化时自动重置）。
 */
export default function IssueDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return (
    <ErrorBoundary resetKeys={[id]}>
      <IssueDetailRoute routeId={id} />
    </ErrorBoundary>
  );
}
