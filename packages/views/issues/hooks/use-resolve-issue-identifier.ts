"use client";

// 问题标识符解析 hook：把一条裸的问题标识符（如 "MUL-123"）解析成当前工作区里真实
// 的问题，解析不到返回 null。支撑类似 Linear 风格的自动链接渲染路径。
import { useQuery } from "@tanstack/react-query";
import { issueIdentifierOptions } from "@multica/core/issues/queries";
import { useCurrentWorkspace } from "@multica/core/paths";
import { isIssueIdentifier } from "@multica/ui/markdown";
import type { Issue } from "@multica/core/types";

/**
 * 把裸的问题标识符（"MUL-123"）解析为当前工作区中的真实问题，否则返回 null。
 * 供自动链接渲染路径使用。
 *
 * 服务端状态 → TanStack Query（key 含 wsId + 标识符，所以多条评论/消息里相同的
 * 标识符会共享同一次请求）。
 *
 * 以下情况直接短路（不发网络请求）：
 *   - 当前没有工作区，或
 *   - token 长得不像标识符，或
 *   - 标识符前缀不可能是本工作区 `issue_prefix` 的前缀。
 *
 * 前缀未知时（工作区列表还没加载完）就放行到查询，由 `issueIdentifierOptions` 里的
 * 精确匹配过滤来决定。
 */
export function useResolveIssueIdentifier(identifier: string): Issue | null {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const prefix = workspace?.issue_prefix;
  const prefixMatches =
    !prefix ||
    identifier.toUpperCase().startsWith(`${prefix.toUpperCase()}-`);

  const { data } = useQuery({
    ...issueIdentifierOptions(wsId, identifier),
    enabled: Boolean(wsId) && isIssueIdentifier(identifier) && prefixMatches,
  });

  return data ?? null;
}
