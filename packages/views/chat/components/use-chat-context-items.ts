// 聊天输入框 "@" 提及的上下文来源：把"当前正在看的 issue/项目"与"最近打开过的
// issue/项目"汇总成可提及的条目列表（发聊天时的快捷上下文，类似 issue 评论写法）。
"use client";

import { issueStatusCategory } from "@multica/core/issues";
import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { selectRecentContexts, useRecentContextStore, type RecentContextEntry } from "@multica/core/chat";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import type { Issue, Project } from "@multica/core/types";
import type { MentionItem } from "../../editor/extensions/mention-suggestion";
import { useNavigation } from "../../navigation";

const MAX_RECENT_MENTION_ITEMS = 8;

function mentionKey(item: Pick<MentionItem, "type" | "id">): string {
  return `${item.type}:${item.id}`;
}

function issueToMentionItem(
  issue: Pick<Issue, "id" | "identifier" | "title" | "status"> & Partial<Pick<Issue, "status_category">>,
  group: "current" | "recent",
): MentionItem {
  return {
    id: issue.id,
    label: issue.identifier,
    type: "issue",
    description: issue.title,
    status: issue.status,
    // 这里要带着而不是丢掉状态类别：列表从类别决定图标字形和置灰效果，
    // 丢在这里会让 Current/Recent 里自定义的"已完成"状态被渲染成活动的
    // Todo 图标。（MUL-6243）
    statusCategory: issueStatusCategory(issue) ?? undefined,
    group,
  };
}

function projectToMentionItem(project: Pick<Project, "id" | "title" | "description" | "icon" | "status">, group: "current" | "recent"): MentionItem {
  return {
    id: project.id,
    label: project.title,
    type: "project",
    description: project.description ?? undefined,
    icon: project.icon,
    projectStatus: project.status,
    group,
  };
}

function recentEntryToMentionItem(entry: RecentContextEntry): MentionItem {
  return {
    id: entry.id,
    label: entry.label ?? entry.id,
    type: entry.type,
    description: entry.subtitle,
    status: entry.status,
    projectStatus: entry.projectStatus,
    icon: entry.icon,
    group: "recent",
  };
}

function hydrateRecentEntry(entry: RecentContextEntry, data: Issue | Project | undefined): MentionItem {
  if (!data) return recentEntryToMentionItem(entry);
  return entry.type === "issue"
    ? issueToMentionItem(data as Issue, "recent")
    : projectToMentionItem(data as Project, "recent");
}

/**
 * 从当前路由解析"正在看的上下文"：`/issues/:id`、`/projects/:id`，
 * 以及带 `?issue=` 参数的 inbox 页；都不是则返回 null。
 */
export function parseCurrentContextRoute(pathname: string, searchParams: URLSearchParams): { type: "issue" | "project"; id: string } | null {
  const issueMatch = pathname.match(/^\/[^/]+\/issues\/([^/]+)$/);
  if (issueMatch?.[1]) return { type: "issue", id: decodeURIComponent(issueMatch[1]) };

  const projectMatch = pathname.match(/^\/[^/]+\/projects\/([^/]+)$/);
  if (projectMatch?.[1]) return { type: "project", id: decodeURIComponent(projectMatch[1]) };

  const inboxMatch = pathname.match(/^\/[^/]+\/inbox$/);
  const inboxIssueId = searchParams.get("issue");
  if (inboxMatch && inboxIssueId) return { type: "issue", id: inboxIssueId };

  return null;
}

/**
 * 组装聊天输入框 @ 提及的候选列表：当前上下文的 issue/项目排在最前，
 * 后面跟最近打开过的条目（去重、截断）。每个条目都用实时查询把缓存里可能
 * 过期的最近条目水合（hydrate）成完整信息。
 */
export function useChatContextItems(wsId: string, enabled = true): MentionItem[] {
  const { pathname, searchParams } = useNavigation();
  const currentRoute = parseCurrentContextRoute(pathname, searchParams);
  const recentEntries = useRecentContextStore(selectRecentContexts(wsId));
  const visibleRecentEntries = useMemo(
    () => recentEntries.slice(0, MAX_RECENT_MENTION_ITEMS),
    [recentEntries],
  );

  const { data: currentIssue } = useQuery({
    ...issueDetailOptions(wsId, currentRoute?.type === "issue" ? currentRoute.id : ""),
    enabled: enabled && currentRoute?.type === "issue",
  });

  const { data: currentProject } = useQuery({
    ...projectDetailOptions(wsId, currentRoute?.type === "project" ? currentRoute.id : ""),
    enabled: enabled && currentRoute?.type === "project",
  });

  const recentQueries = useQueries({
    queries: enabled ? visibleRecentEntries.map((entry) => ({
      ...(entry.type === "issue"
        ? issueDetailOptions(wsId, entry.id)
        : projectDetailOptions(wsId, entry.id)),
      staleTime: 30_000,
    })) : [],
  });

  return useMemo(() => {
    const currentItems: MentionItem[] = [];
    if (currentIssue) currentItems.push(issueToMentionItem(currentIssue, "current"));
    if (currentProject) currentItems.push(projectToMentionItem(currentProject, "current"));

    const hidden = new Set(currentItems.map(mentionKey));
    const recentItems = visibleRecentEntries
      .map((entry, index) => hydrateRecentEntry(entry, recentQueries[index]?.data as Issue | Project | undefined))
      .filter((item) => !hidden.has(mentionKey(item)))
      .slice(0, MAX_RECENT_MENTION_ITEMS);

    return [...currentItems, ...recentItems];
  }, [currentIssue, currentProject, recentQueries, visibleRecentEntries]);
}
