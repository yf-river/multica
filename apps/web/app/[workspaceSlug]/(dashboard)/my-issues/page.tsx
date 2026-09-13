"use client";

/**
 * 「我的任务」页路由：把该视图的状态与 URL 同步（scope 限定为「我负责的」）。
 */
import { Suspense } from "react";
import { MyIssuesPage } from "@multica/views/my-issues/my-issues-page";
import { useIssueViewUrlSync } from "../../../../platform/use-issue-view-url-sync";

/** 视图状态与 URL 同步的桥接组件（空渲染，仅执行 hook 副作用）。 */
function IssueViewUrlSync() {
  // app router 下 useSearchParams 需要包在 Suspense 边界内。
  useIssueViewUrlSync({ scope_type: "my" });
  return null;
}

/**
 * 我的任务列表页。
 *
 * @returns 我的任务列表。
 */
export default function Page() {
  return (
    <>
      <Suspense fallback={null}>
        <IssueViewUrlSync />
      </Suspense>
      <MyIssuesPage />
    </>
  );
}
