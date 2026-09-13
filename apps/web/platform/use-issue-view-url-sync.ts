"use client";

/**
 * Web 专属的「当前保存视图 ↔ `?view=<id>`」双向同步：
 * 让链接可分享、刷新可保持。Next.js 导航属于平台层，所以这个 hook 放这里，
 * 页面包装组件把它和共享页面组件并排挂载。
 *
 * 挂载时 URL 说了算（粘贴链接会打开那个视图）；此后由 store 通过
 * history.replaceState 驱动 URL——不导航、不重置滚动、不重渲染路由树。
 */
import { useEffect, useRef } from "react";
import { useSearchParams } from "next/navigation";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueViewContainerKey,
  useActiveIssueViewStore,
} from "@multica/core/issue-views/active-view-store";
import type { IssueViewScope } from "@multica/core/issue-views/queries";

/**
 * 把激活视图与 URL 查询串同步（每次挂载一个作用域调一次）。
 *
 * @param scope - 视图作用域（如 workspace / my），用于定位 store 里的活跃视图。
 */
export function useIssueViewUrlSync(scope: IssueViewScope) {
  const wsId = useWorkspaceId();
  const searchParams = useSearchParams();
  const containerKey = issueViewContainerKey(wsId, scope);
  const setActive = useActiveIssueViewStore((s) => s.setActive);
  const activeViewId = useActiveIssueViewStore(
    (s) => s.active[containerKey] ?? null,
  );

  // 挂载时：采纳 URL 里的视图 id（深链 / 刷新）。
  const adopted = useRef(false);
  useEffect(() => {
    if (adopted.current) return;
    adopted.current = true;
    const fromUrl = searchParams.get("view");
    if (fromUrl) setActive(containerKey, fromUrl);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- adopt once per mount
  }, []);

  // store → URL。用 replaceState 保持后退历史干净：开合视图是「视图状态」，
  // 不是导航。
  useEffect(() => {
    if (!adopted.current) return;
    const url = new URL(window.location.href);
    const current = url.searchParams.get("view");
    if ((activeViewId ?? null) === (current ?? null)) return;
    if (activeViewId) url.searchParams.set("view", activeViewId);
    else url.searchParams.delete("view");
    window.history.replaceState(window.history.state, "", url.toString());
  }, [activeViewId]);
}
