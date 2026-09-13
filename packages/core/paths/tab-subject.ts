// 本文件是工作区导航的「语义目标」解析器。
//
// 标签页指向一个 URL，但标签页「是什么」——集合页、某个具体资源、带选中项
// 的容器、创建流程、或无法识别的东西——决定它该怎么呈现（图标 + 标题）。本
// 模块把 URL 转成那种语义化的 {@link TabSubject}。它是纯的：无 React、无
// Lucide、无查询——只有 URL 解析。视觉/标题映射在 `tab-presentation.ts`；
// React 粘合代码在 `@multica/views`。
//
// 工作区 URL 是 `/{slug}/{segment}/...`：slug 在下标 0、路由段在下标 1、
// 资源 id 在下标 2。容器的选中（Inbox、Chat）在查询串里，所以也要解析——
// URL 是「标签页开着什么、包括哪一项」的唯一真源。

import { pageForSegment, type WorkspacePageKey } from "./route-icons";

/** 标签页的「身份型」主体类型：agent / member / squad。 */
export type TabActorType = "agent" | "member" | "squad";

/** 一个标签页指向的语义目标。 */
export type TabSubject =
  /** 没有具体资源的集合或工具页。 */
  | { kind: "page"; page: WorkspacePageKey }
  /** 单个 issue 详情。 */
  | { kind: "issue"; id: string }
  /** 单个 project 详情。 */
  | { kind: "project"; id: string }
  /** 单个 autopilot 详情。 */
  | { kind: "autopilot"; id: string }
  /** agent / member / squad 详情（带头像身份）。 */
  | { kind: "actor"; actorType: TabActorType; id: string }
  /** 单个 skill 详情。 */
  | { kind: "skill"; id: string }
  /** runtime 机器详情。 */
  | { kind: "machine"; machineId: string }
  /** 嵌套在某台机器下的 runtime。 */
  | { kind: "runtime"; machineId: string; runtimeId: string }
  /** 附件预览。`filename` 是 `?name=` 提示，或 null。 */
  | { kind: "attachment"; id: string; filename: string | null }
  /**
   * Inbox 容器；`selectedKey` 是 `?issue=` 选中项或 null，
   * `archived` 是 `?view=archived` 子列表（自己的列表/缓存）。
   */
  | { kind: "inbox"; selectedKey: string | null; archived: boolean }
  /** Chat 容器；`sessionId` 是 `?session=` 选中项或 null。 */
  | { kind: "chat"; sessionId: string | null }
  /** 尚未产出资源的创建流程。 */
  | { kind: "flow"; flow: "create-agent" }
  /** 无法识别的 URL。绝不冒充真实页面。 */
  | { kind: "unknown" };

/** 把 URL 拆成 `/` 分隔的路径段与查询参数。 */
function splitUrl(url: string): { segments: string[]; query: URLSearchParams } {
  const hashIdx = url.indexOf("#");
  const withoutHash = hashIdx === -1 ? url : url.slice(0, hashIdx);
  const queryIdx = withoutHash.indexOf("?");
  const pathname = queryIdx === -1 ? withoutHash : withoutHash.slice(0, queryIdx);
  const search = queryIdx === -1 ? "" : withoutHash.slice(queryIdx + 1);
  return {
    segments: pathname.split("/").filter(Boolean),
    query: new URLSearchParams(search),
  };
}

/**
 * 把工作区标签页 URL 解析成 {@link TabSubject}。
 *
 * 只认工作区范围 URL（`/{slug}/{segment}/...`）。更短、空或带未知段的 URL
 * 一律解析为 `{ kind: "unknown" }`——绝不落成默认页面，让无法识别的标签页
 * 在外观上保持中性，而不是冒充 Issues。
 *
 * @param url - 工作区标签页 URL
 * @returns 语义目标
 */
export function parseTabSubject(url: string): TabSubject {
  const { segments, query } = splitUrl(url);
  // segments[0] 是工作区 slug；路由段在下标 1。
  const segment = segments[1] ?? "";
  const id = segments[2] ?? "";

  switch (segment) {
    case "issues":
      return id ? { kind: "issue", id } : { kind: "page", page: "issues" };
    case "my-issues":
      return { kind: "page", page: "myIssues" };
    case "projects":
      return id ? { kind: "project", id } : { kind: "page", page: "projects" };
    case "autopilots":
      return id ? { kind: "autopilot", id } : { kind: "page", page: "autopilots" };
    case "agents":
      if (id === "new") return { kind: "flow", flow: "create-agent" };
      return id
        ? { kind: "actor", actorType: "agent", id }
        : { kind: "page", page: "agents" };
    case "members":
      // 没有 members 集合路由；只有 `/members/:id`。
      return id ? { kind: "actor", actorType: "member", id } : { kind: "unknown" };
    case "squads":
      return id
        ? { kind: "actor", actorType: "squad", id }
        : { kind: "page", page: "squads" };
    case "usage":
      return { kind: "page", page: "usage" };
    case "inbox":
      return {
        kind: "inbox",
        selectedKey: query.get("issue") || null,
        archived: query.get("view") === "archived",
      };
    case "chat":
      return { kind: "chat", sessionId: query.get("session") || null };
    case "runtimes":
      if (!id) return { kind: "page", page: "runtimes" };
      // `/runtimes/:machineId/runtime/:runtimeId` —— 嵌套的 runtime。
      if (segments[3] === "runtime" && segments[4]) {
        return { kind: "runtime", machineId: id, runtimeId: segments[4] };
      }
      return { kind: "machine", machineId: id };
    case "skills":
      return id ? { kind: "skill", id } : { kind: "page", page: "skills" };
    case "settings":
      return { kind: "page", page: "settings" };
    case "attachments":
      // `/attachments/:id/preview?name=<filename>`。
      return id
        ? { kind: "attachment", id, filename: query.get("name") || null }
        : { kind: "unknown" };
    default: {
      // 裸 `/{slug}`（无路由段）在别处被归一成默认界面；其他未知段一律按
      // unknown 处理，绝不静默借用某页面的身份。
      const page = pageForSegment(segment);
      return page ? { kind: "page", page } : { kind: "unknown" };
    }
  }
}

/**
 * 主题的稳定字符串身份，可作 memo/渲染 key。应该渲染成同样视觉的不同主题
 * 共享同一 key；容器内选中项变了 key 也会变。
 */
export function tabSubjectKey(subject: TabSubject): string {
  switch (subject.kind) {
    case "page":
      return `page:${subject.page}`;
    case "issue":
      return `issue:${subject.id}`;
    case "project":
      return `project:${subject.id}`;
    case "autopilot":
      return `autopilot:${subject.id}`;
    case "actor":
      return `actor:${subject.actorType}:${subject.id}`;
    case "skill":
      return `skill:${subject.id}`;
    case "machine":
      return `machine:${subject.machineId}`;
    case "runtime":
      return `runtime:${subject.machineId}:${subject.runtimeId}`;
    case "attachment":
      return `attachment:${subject.id}:${subject.filename ?? ""}`;
    case "inbox":
      return `inbox:${subject.archived ? "archived" : "inbox"}:${subject.selectedKey ?? ""}`;
    case "chat":
      return `chat:${subject.sessionId ?? ""}`;
    case "flow":
      return `flow:${subject.flow}`;
    case "unknown":
      return "unknown";
  }
}
