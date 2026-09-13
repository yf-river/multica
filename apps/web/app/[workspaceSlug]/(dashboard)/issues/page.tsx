"use client";

/**
 * 工作区「任务」列表页路由：包一层 ErrorBoundary（出错可重试），
 * 并把列表的视图状态（筛选/排序等）与 URL 查询串双向同步。
 */
import { Suspense } from "react";
import { IssuesPage } from "@multica/views/issues/issues-page";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";
import { useIssueViewUrlSync } from "../../../../platform/use-issue-view-url-sync";

/** 视图状态与 URL 同步的桥接组件（空渲染，仅执行 hook 副作用）。 */
function IssueViewUrlSync() {
  // app router 下 useSearchParams 需要包在 Suspense 边界内。
  useIssueViewUrlSync({ scope_type: "workspace" });
  return null;
}

/**
 * 工作区任务列表页。
 *
 * @returns 任务列表（带错误边界与 URL 同步）。
 */
export default function Page() {
  return (
    <ErrorBoundary>
      <Suspense fallback={null}>
        <IssueViewUrlSync />
      </Suspense>
      <IssuesPage />
    </ErrorBoundary>
  );
}
