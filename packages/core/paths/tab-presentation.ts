// 本文件是工作区导航的「纯展示解析器」。
//
// 给定 {@link TabSubject}（标签页指向什么）和当前从缓存能拿到的实体数据，
// 产出标签页的「主导视觉」与「标题 spec」。这是「这个标签页该长什么样」这一
// 决策唯一所在的地方——图标和标题不再来自两条无关的代码路径。
//
// 纯、无 React：视觉是一个描述符（由 `@multica/views` 的 `ResourceLeadingVisual`
// 渲染），标题是「字面文本」或「本地化 key」的 spec（由视图层本地化）。保持
// 纯让整套「URL + 数据 → 图标 + 标题」矩阵无需 React 就能做单元测试——这正是
// 标签页行为需要被守护的地方。
//
// 实体数据缺失是一等状态：数据未加载的资源渲染稳定的类型图标与类型文案，
// 绝不渲染错误或空的身份，也绝不借用 Issues 的图标。

import type { IssueStatus, IssueStatusCategory } from "../types";
import { issueStatusCategory } from "../issues/status-category";
import {
  WORKSPACE_PAGES,
  type NavLabelKey,
  type RouteIconName,
} from "./route-icons";
import type { TabActorType, TabSubject } from "./tab-subject";

/** 标签页应渲染的主导视觉。 */
export type TabVisual =
  /** 静态 Lucide 图标（页面图标或无资源时的类型图标）。 */
  | { kind: "icon"; icon: RouteIconName }
  /** issue 的实时状态字形。加载中为 `null`。`category` 决定选哪个字形——
   *  带在这里，让标签条永远不必自己去解析自定义状态 key。(MUL-6243) */
  | { kind: "issue-status"; status: IssueStatus | null; category?: IssueStatusCategory }
  /** project 自己的图标。`null` 回退到默认 project 字形。 */
  | { kind: "project-icon"; icon: string | null }
  /** 身份主体的头像，由视图层按 `actorType`+`id` 解析。 */
  | { kind: "actor"; actorType: TabActorType; id: string };

/** `layout.tab` 命名空间下、各标签页「类型标签」的本地化 key。 */
export type TabLabelKey =
  | "issue"
  | "project"
  | "autopilot"
  | "agent"
  | "member"
  | "squad"
  | "skill"
  | "machine"
  | "runtime"
  | "attachment"
  | "create_agent"
  | "unknown";

/** 标签页标题该怎么产出。 */
export type TabTitleSpec =
  /** 已完全解析的字面文本（资源自己的名字/标题）。 */
  | { kind: "text"; text: string }
  /** 页面名——用 `layout.nav.<navKey>` 本地化。 */
  | { kind: "nav"; navKey: NavLabelKey }
  /** 类型文案（加载中 / 流程 / 未知）——用 `layout.tab.<tabKey>` 本地化。 */
  | { kind: "tab"; tabKey: TabLabelKey };

/** 一个标签页的展示结果：主导视觉 + 标题 spec。 */
export interface TabPresentation {
  visual: TabVisual;
  title: TabTitleSpec;
}

/** 已解析的 Inbox 选中项，由视图层从缓存计算。 */
export type InboxSelectionData =
  | { kind: "issue"; identifier: string; title: string }
  | { kind: "item"; title: string };

/**
 * 视图层已从缓存解析出的、针对该标签页主体的实体数据。
 * 每个字段都可选：`undefined` 表示「还没加载」，产出「待定」（类型文案 +
 * 类型图标）的呈现。
 */
export interface TabEntityData {
  issue?: { identifier: string; title: string; status: IssueStatus };
  project?: { icon: string | null; title: string };
  autopilot?: { title: string };
  /** 身份主体已解析的展示名。 */
  actorName?: string;
  skill?: { name: string };
  machine?: { name: string };
  runtime?: { name: string };
  /** 已解析的聊天会话标题（已含 "New chat" 兜底）。 */
  chatSessionTitle?: string;
  /** 已解析的 Inbox 选中项（issue 标识/标题 或 条目标题）。 */
  inboxSelection?: InboxSelectionData;
}

/** 什么都解析不出来时用的中性视觉。 */
export const DEFAULT_TAB_VISUAL: TabVisual = { kind: "icon", icon: "FileQuestion" };

/** 文本非空时用字面文本，否则用给定的类型文案。 */
function textOr(text: string | undefined | null, tabKey: TabLabelKey): TabTitleSpec {
  const trimmed = text?.trim();
  return trimmed ? { kind: "text", text: trimmed } : { kind: "tab", tabKey };
}

const ACTOR_LABEL: Record<TabActorType, TabLabelKey> = {
  agent: "agent",
  member: "member",
  squad: "squad",
};

// 扩展名 → 文件类型图标。预览 URL 只带文件名，扩展名是能用的信号；无法识别
// 的一律用通用 File 字形。
const EXTENSION_ICON: Record<string, RouteIconName> = {};
const registerExtensions = (icon: RouteIconName, exts: string[]) => {
  for (const ext of exts) EXTENSION_ICON[ext] = icon;
};
registerExtensions("FileImage", [
  "png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif", "heic",
]);
registerExtensions("FileVideo", ["mp4", "mov", "webm", "mkv", "avi", "m4v"]);
registerExtensions("FileAudio", ["mp3", "wav", "ogg", "flac", "m4a", "aac"]);
registerExtensions("FileArchive", ["zip", "tar", "gz", "tgz", "rar", "7z", "bz2"]);
registerExtensions("FileCode", [
  "js", "jsx", "ts", "tsx", "json", "yaml", "yml", "py", "go", "rs",
  "java", "c", "h", "cpp", "cc", "rb", "php", "swift", "kt", "sh", "css", "scss",
]);
registerExtensions("FileText", [
  "txt", "md", "markdown", "pdf", "doc", "docx", "csv", "log",
  "html", "htm", "xml", "rtf", "odt",
]);

/** 从附件文件名选文件图标；取不到则用通用 File。 */
export function iconForAttachment(filename: string | null): RouteIconName {
  if (!filename) return "File";
  const dot = filename.lastIndexOf(".");
  if (dot < 0 || dot === filename.length - 1) return "File";
  const ext = filename.slice(dot + 1).toLowerCase();
  return EXTENSION_ICON[ext] ?? "File";
}

/**
 * 把「主体 + 可用数据」解析成标签页的主导视觉与标题。
 * 对每个 {@link TabSubject} 种类穷尽处理——新增路由/资源类型会强制给出显式
 * 的展示选择，而不是落入静默默认。
 *
 * @param subject - 标签页语义目标
 * @param data - 缓存中可用到的实体数据
 * @returns 展示结果
 */
export function resolveTabPresentation(
  subject: TabSubject,
  data: TabEntityData = {},
): TabPresentation {
  switch (subject.kind) {
    case "page": {
      const page = WORKSPACE_PAGES[subject.page];
      return {
        visual: { kind: "icon", icon: page.icon },
        title: { kind: "nav", navKey: page.navKey },
      };
    }
    case "issue":
      return {
        visual: {
          kind: "issue-status",
          status: data.issue?.status ?? null,
          ...(data.issue ? { category: issueStatusCategory(data.issue) ?? undefined } : {}),
        },
        title: data.issue
          ? { kind: "text", text: `${data.issue.identifier}: ${data.issue.title}` }
          : { kind: "tab", tabKey: "issue" },
      };
    case "project":
      return {
        visual: { kind: "project-icon", icon: data.project?.icon ?? null },
        title: textOr(data.project?.title, "project"),
      };
    case "autopilot":
      return {
        visual: { kind: "icon", icon: "Zap" },
        title: textOr(data.autopilot?.title, "autopilot"),
      };
    case "actor":
      return {
        visual: { kind: "actor", actorType: subject.actorType, id: subject.id },
        title: textOr(data.actorName, ACTOR_LABEL[subject.actorType]),
      };
    case "skill":
      return {
        visual: { kind: "icon", icon: "BookOpenText" },
        title: textOr(data.skill?.name, "skill"),
      };
    case "machine":
      return {
        visual: { kind: "icon", icon: "Monitor" },
        title: textOr(data.machine?.name, "machine"),
      };
    case "runtime":
      return {
        visual: { kind: "icon", icon: "Server" },
        title: textOr(data.runtime?.name, "runtime"),
      };
    case "attachment":
      // 预览 URL 带文件名（`?name=`），所以标题用它、图标按扩展名选匹配的
      // 文件图标。只有它缺失时才回退到通用 File 字形 + "Attachment" 文案。
      return {
        visual: { kind: "icon", icon: iconForAttachment(subject.filename) },
        title: subject.filename
          ? { kind: "text", text: subject.filename }
          : { kind: "tab", tabKey: "attachment" },
      };
    case "inbox": {
      // 容器图标恒定不变；只有标题跟随选中项。
      const sel = subject.selectedKey ? data.inboxSelection : undefined;
      let title: TabTitleSpec;
      if (!sel) {
        title = { kind: "nav", navKey: "inbox" };
      } else if (sel.kind === "issue") {
        title = { kind: "text", text: `${sel.identifier}: ${sel.title}` };
      } else {
        const text = sel.title.trim();
        title = text ? { kind: "text", text } : { kind: "nav", navKey: "inbox" };
      }
      return { visual: { kind: "icon", icon: "Inbox" }, title };
    }
    case "chat": {
      const title: TabTitleSpec =
        subject.sessionId && data.chatSessionTitle?.trim()
          ? { kind: "text", text: data.chatSessionTitle.trim() }
          : { kind: "nav", navKey: "chat" };
      return { visual: { kind: "icon", icon: "MessageSquare" }, title };
    }
    case "flow":
      return {
        visual: { kind: "icon", icon: "Bot" },
        title: { kind: "tab", tabKey: "create_agent" },
      };
    case "unknown":
      return {
        visual: DEFAULT_TAB_VISUAL,
        title: { kind: "tab", tabKey: "unknown" },
      };
  }
}
