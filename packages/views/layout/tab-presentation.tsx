"use client";

import { useMemo } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  parseTabSubject,
  resolveTabPresentation,
  useCurrentWorkspace,
  type TabSubject,
  type TabVisual,
  type TabTitleSpec,
  type TabEntityData,
  type TabLabelKey,
} from "@multica/core/paths";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { autopilotDetailOptions } from "@multica/core/autopilots/queries";
import {
  skillDetailOptions,
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { runtimeDisplayName } from "@multica/core/runtimes";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import {
  inboxListOptions,
  archivedInboxPagesOptions,
  archivedInboxLookupOptions,
} from "@multica/core/inbox/queries";
import { useInboxFilters } from "@multica/core/inbox/filter-store";
import { cn } from "@multica/ui/lib/utils";
import { StatusIcon } from "../issues/components";
import { ProjectIcon } from "../projects/components/project-icon";
import { ActorAvatar } from "../common/actor-avatar";
import { getInboxDisplayTitle } from "../inbox/components/inbox-display";
import { useT } from "../i18n";
import { ROUTE_ICON_COMPONENTS } from "./route-icon-components";

/**
 * 布局模块：桌面端标签的展示信息。
 * 把一个标签 URL 转成前导视觉和标题，数据实时取自查询缓存。这是纯逻辑
 * 核心 `@multica/core/paths`（`parseTabSubject` + `resolveTabPresentation`）
 * 对应的视图半边。
 *
 * 只读缓存：`useTabEntityData` 里的每个查询都是 `enabled: false`。它观察
 * 页面/目录已经加载的数据，并在数据变化时重渲染，让已打开标签的图标和
 * 标题保持同步（项目改名、issue 状态变化、会话重命名）而不会放大请求。
 * 还没加载的资源先渲染稳定的类型兜底，直到它的页面填满缓存。
 *
 * 唯一的例外是 actor 标签的头像：`ResourceLeadingVisual` 渲染的
 * `ActorAvatar` 会自己去加载（工作区全局、侧边栏已预热的）member/agent/
 * squad 目录。这是有意为之——它解析出头像，进而也让本 hook 从同样的
 * 列表里读到了名字。
 */

// 该标签不需要的详情查询占位 id——它的 key 永远不会被填充，读取直接
// 返回 undefined 而不发起任何请求。
const NONE = "__tab_presentation_none__";

// 这些资源类型在实时数据加载期间，用持久化的标题作为首帧兜底比较合适。
// flow/unknown/attachment 始终使用各自的类型标签。
const PENDING_RESOURCE_KEYS: ReadonlySet<TabLabelKey> = new Set<TabLabelKey>([
  "issue",
  "project",
  "autopilot",
  "agent",
  "member",
  "squad",
  "skill",
  "machine",
  "runtime",
]);

/** 为一个 subject 收集缓存里的实体数据，所有读取都只走缓存。 */
function useTabEntityData(subject: TabSubject, wsId: string): TabEntityData {
  const { t: chatT } = useT("chat");

  // 两个收件箱列表都只读缓存；归档视图维护自己的列表，所以归档项要对着
  // 归档缓存解析——正是 `?view=archived` 生效时 InboxPage 填充的那个列表。
  const inboxList = useQuery({ ...inboxListOptions(wsId), enabled: false }).data;
  const inboxFilters = useInboxFilters(wsId);
  const archivedPages = useInfiniteQuery({ ...archivedInboxPagesOptions(wsId, inboxFilters), enabled: false }).data;
  const archivedLookup = useQuery({
    ...archivedInboxLookupOptions(wsId, subject.kind === "inbox" ? subject.selectedKey ?? "" : ""),
    enabled: false,
  }).data;
  const archivedInboxList = useMemo(() => [
    ...(archivedPages?.pages.flatMap((page) => page.items) ?? []),
    ...(archivedLookup?.items ?? []),
  ], [archivedPages, archivedLookup]);
  const activeInboxList =
    subject.kind === "inbox" && subject.archived ? archivedInboxList : inboxList;
  const inboxItem =
    subject.kind === "inbox" && subject.selectedKey
      ? (activeInboxList?.find(
          (i) => (i.issue_id ?? i.id) === subject.selectedKey,
        ) ?? null)
      : null;

  // 一个 issue 查询同时服务直接的 issue 标签和收件箱选中的 issue。
  const issueId =
    subject.kind === "issue"
      ? subject.id
      : subject.kind === "inbox"
        ? (subject.selectedKey ?? "")
        : "";
  // An issue tab's URL segment may be a human-readable identifier (`MUL-123`).
  // The route seeds that entry when it resolves, but only the UUID-keyed entry
  // receives realtime patches — so hop through it, or a tab opened by
  // identifier would freeze on the title and status it had when first opened.
  // Both reads stay cache-only, and a UUID segment resolves to itself.
  const rawIssue = useQuery({
    ...issueDetailOptions(wsId, issueId || NONE),
    enabled: false,
  }).data;
  const issue =
    useQuery({
      ...issueDetailOptions(wsId, rawIssue?.id || NONE),
      enabled: false,
    }).data ?? rawIssue;

  const project = useQuery({
    ...projectDetailOptions(wsId, subject.kind === "project" ? subject.id : NONE),
    enabled: false,
  }).data;
  const autopilot = useQuery({
    ...autopilotDetailOptions(
      wsId,
      subject.kind === "autopilot" ? subject.id : NONE,
    ),
    enabled: false,
  }).data;
  const skill = useQuery({
    ...skillDetailOptions(wsId, subject.kind === "skill" ? subject.id : NONE),
    enabled: false,
  }).data;

  const agents = useQuery({ ...agentListOptions(wsId), enabled: false }).data;
  const members = useQuery({ ...memberListOptions(wsId), enabled: false }).data;
  const squads = useQuery({ ...squadListOptions(wsId), enabled: false }).data;
  const runtimes = useQuery({ ...runtimeListOptions(wsId), enabled: false }).data;
  const sessions = useQuery({ ...chatSessionsOptions(wsId), enabled: false }).data;

  const data: TabEntityData = {};
  switch (subject.kind) {
    case "issue":
      if (issue) {
        data.issue = {
          identifier: issue.identifier,
          title: issue.title,
          status: issue.status,
        };
      }
      break;
    case "project":
      if (project) data.project = { icon: project.icon, title: project.title };
      break;
    case "autopilot":
      if (autopilot) data.autopilot = { title: autopilot.autopilot.title };
      break;
    case "skill":
      if (skill) data.skill = { name: skill.name };
      break;
    case "actor": {
      const name =
        subject.actorType === "agent"
          ? agents?.find((a) => a.id === subject.id)?.name
          : subject.actorType === "member"
            ? members?.find((m) => m.user_id === subject.id)?.name
            : squads?.find((s) => s.id === subject.id)?.name;
      if (name) data.actorName = name;
      break;
    }
    case "machine": {
      const rt = runtimes?.find((r) => r.id === subject.machineId);
      if (rt) data.machine = { name: runtimeDisplayName(rt) };
      break;
    }
    case "runtime": {
      const rt = runtimes?.find((r) => r.id === subject.runtimeId);
      if (rt) data.runtime = { name: runtimeDisplayName(rt) };
      break;
    }
    case "chat":
      if (subject.sessionId) {
        const s = sessions?.find((x) => x.id === subject.sessionId);
        if (s) data.chatSessionTitle = s.title?.trim() || chatT(($) => $.window.untitled);
      }
      break;
    case "inbox":
      if (issue) {
        // Issue-backed selection — answered by the issue cache alone.
        data.inboxSelection = {
          kind: "issue",
          identifier: issue.identifier,
          title: issue.title,
        };
      } else if (inboxItem && !inboxItem.issue_id) {
        // An issue-less notification (autopilot paused, quota exceeded, …)
        // carries its title only on the inbox row, so this branch is the one
        // case that still needs the list cache.
        data.inboxSelection = {
          kind: "item",
          title: getInboxDisplayTitle(inboxItem),
        };
      }
      break;
  }
  return data;
}

/**
 * Localize a title spec, preferring a persisted fallback while pending.
 *
 * `pending` is for identities the spec cannot report as pending on its own. A
 * resource tab that has not loaded resolves to its type label, which
 * `PENDING_RESOURCE_KEYS` recognizes; a CONTAINER tab with an unresolved
 * selection resolves to the container's own nav label, which is
 * indistinguishable from a container with nothing selected — so the caller,
 * which still has the subject, decides.
 */
function useTabTitle(
  spec: TabTitleSpec,
  fallbackTitle?: string,
  pending = false,
): string {
  const { t: layoutT } = useT("layout");
  const persisted = fallbackTitle?.trim();
  switch (spec.kind) {
    case "text":
      return spec.text;
    case "nav":
      return pending && persisted
        ? persisted
        : layoutT(($) => $.nav[spec.navKey]);
    case "tab": {
      if (pending || PENDING_RESOURCE_KEYS.has(spec.tabKey)) {
        if (persisted) return persisted;
      }
      return layoutT(($) => $.tab[spec.tabKey]);
    }
  }
}

/** 标签展示信息的返回结果。 */
export interface TabPresentationResult {
  /** 前导视觉（图标/状态/头像等）。 */
  visual: TabVisual;
  /** 解析后的标题。 */
  title: string;
}

/**
 * 把一个标签 URL 解析成实时的前导视觉和标题。
 *
 * `fallbackTitle`（标签的持久化标题）只用作仍处于加载中的资源的首帧兜底；
 * 一旦缓存解析出来，就以实时展示信息为准。
 * @param url 标签的 URL。
 * @param fallbackTitle 可选的兜底标题。
 */
export function useTabPresentation(
  url: string,
  fallbackTitle?: string,
): TabPresentationResult {
  const subject = useMemo(() => parseTabSubject(url), [url]);
  const ws = useCurrentWorkspace();
  const wsId = ws?.id ?? "";
  const data = useTabEntityData(subject, wsId);
  const { visual, title: titleSpec } = resolveTabPresentation(subject, data);
  // A selected notification whose identity has not resolved from cache yet is
  // pending in exactly the sense a not-yet-loaded issue is — keep the tab's
  // persisted title instead of collapsing it to the bare container label.
  const selectionPending =
    subject.kind === "inbox" && !!subject.selectedKey && !data.inboxSelection;
  const title = useTabTitle(titleSpec, fallbackTitle, selectionPending);

  // actor 头像要通过工作区目录查询解析，在工作区还没就绪时渲染会抛错。
  // 所以在那之前先显示一个类型图标。
  const safeVisual: TabVisual =
    visual.kind === "actor" && !wsId
      ? {
          kind: "icon",
          icon:
            visual.actorType === "squad"
              ? "Users"
              : visual.actorType === "member"
                ? "CircleUser"
                : "Bot",
        }
      : visual;

  return { visual: safeVisual, title };
}

/**
 * 把标签的前导视觉渲染进一个固定的 16×16 槽位，这样当视觉从类型兜底
 * 解析成真实身份时，标签不会发生重排。桌面端标签栏共用（任何想用同一套
 * 身份规则的资源行也可复用）。
 * @param visual 要渲染的前导视觉。
 * @param className 附加样式类名。
 */
export function ResourceLeadingVisual({
  visual,
  className,
}: {
  visual: TabVisual;
  className?: string;
}) {
  let inner: React.ReactNode;
  switch (visual.kind) {
    case "icon": {
      const Icon = ROUTE_ICON_COMPONENTS[visual.icon];
      inner = <Icon className="size-3.5" />;
      break;
    }
    case "issue-status":
      // null 状态（加载中）会渲染 StatusIcon 的中性兜底图标。
      inner = (
        <StatusIcon
          status={visual.status ?? ""}
          category={visual.category}
          className="size-3.5"
        />
      );
      break;
    case "project-icon":
      inner = <ProjectIcon project={{ icon: visual.icon }} size="sm" />;
      break;
    case "actor":
      inner = (
        <ActorAvatar
          actorType={visual.actorType}
          actorId={visual.id}
          size="xs"
          profileLink={false}
        />
      );
      break;
  }
  return (
    <span
      className={cn(
        "flex size-4 shrink-0 items-center justify-center",
        className,
      )}
    >
      {inner}
    </span>
  );
}
