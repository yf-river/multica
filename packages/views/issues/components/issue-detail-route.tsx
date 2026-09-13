"use client";

// 本文件：IssueDetailRoute —— `/{ws}/issues/{id}` 路由层对 IssueDetail 的包装。
// 承担两件"嵌在面板里的 IssueDetail 绝不该做"的差事：
//   - 渲染前把标识符段解析成问题 UUID，让下面所有缓存键保持 UUID 键控、
//     实时更新能落地（见 `useCanonicalIssue`）；
//   - 把地址栏改写为规范标识符 URL。这属于路由、只属于路由——收件箱在侧栏
//     面板里渲染 IssueDetail，若在那里替换 URL 会把用户导航出收件箱。

import { useEffect, useRef, useState } from "react";
import { useCanonicalIssue } from "@multica/core/issues/canonical-id";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useNavigation } from "../../navigation";
import { IssueDetail, IssueDetailSkeleton, IssueNotFound } from "./issue-detail";

interface IssueDetailRouteProps {
  /**
   * 原始的 `/{ws}/issues/{segment}` 参数。可能是 UUID（旧链接，以及拿到
   * 问题行之前应用自己连的任何东西），也可能是人类可读的标识符如 `MUL-123`。
   */
  routeId: string;
  onDelete?: () => void;
}

/**
 * 问题一旦已知，把 `/{ws}/issues/{uuid}` 改写为 `/{ws}/issues/{identifier}`，
 * 让地址栏和任何复制出的 URL 读作 `MUL-123`。
 *
 * 用 replace 而非 push：UUID URL 是同一个页面，若为它留一条历史记录，
 * 返回键会在同一问题的两种拼写之间来回弹跳用户。
 */
export function useCanonicalIssueUrl(
  routeId: string,
  identifier: string | undefined,
  hash = "",
) {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const canonicalHref = identifier ? `${paths.issueDetail(identifier)}${hash}` : null;
  // `useWorkspacePaths()` 与导航适配器每次渲染都会重建，所以由这个 ref——
  // 而不是依赖数组——来保证 replace 每个目标只跑一次，而非每次 commit 都跑。
  const replacedRef = useRef<string | null>(null);

  useEffect(() => {
    if (!canonicalHref || routeId === identifier) return;
    if (replacedRef.current === canonicalHref) return;
    replacedRef.current = canonicalHref;
    navigation.replace(canonicalHref);
  }, [canonicalHref, identifier, routeId, navigation]);
}

export function parseCommentHighlightHash(hash: string): string | undefined {
  const match = /^#comment-([A-Za-z0-9_-]+)$/.exec(hash);
  return match?.[1];
}

/**
 * 订阅 location.hash 变化，把 `#comment-<id>` 解析成 commentId 供高亮。
 */
function useCommentHighlightHash(): { hash: string; commentId?: string } {
  const read = () => typeof window === "undefined" ? "" : window.location.hash;
  const [hash, setHash] = useState(read);

  useEffect(() => {
    const onHashChange = () => setHash(read());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  return { hash, commentId: parseCommentHighlightHash(hash) };
}

/**
 * `/{ws}/issues/{id}` 的路由层组件（职责见文件头）。
 */
export function IssueDetailRoute({ routeId, onDelete }: IssueDetailRouteProps) {
  const wsId = useWorkspaceId();
  const { canonicalId, issue, isResolving, notFound } = useCanonicalIssue(wsId, routeId);
  const highlight = useCommentHighlightHash();

  useCanonicalIssueUrl(routeId, issue?.identifier, highlight.hash);

  if (isResolving) return <IssueDetailSkeleton />;

  // Render not-found here rather than handing the unresolved segment down.
  // `IssueDetail` would mount a second observer on the query that just failed,
  // refetch it, and restart this component's resolve/remount cycle — an
  // unbounded request loop that never settles. See `CanonicalIssue.notFound`.
  if (notFound || !canonicalId) return <IssueNotFound showBackLink={!onDelete} />;

  return (
    <IssueDetail
      issueId={canonicalId}
      onDelete={onDelete}
      highlightCommentId={highlight.commentId}
    />
  );
}
