// Agent 可见性（visibility）的展示文案：标签、选择器说明、只读徽标悬停提示。
import type { AgentVisibility } from "../types";

/**
 * Agent 可见性的展示标签。数据库把 `private` 存成该值，但界面上的名称是
 * "Personal"——放在 "Workspace" 旁边读起来更顺，也与访问选择器里的措辞一致。
 */
export const VISIBILITY_LABEL: Record<AgentVisibility, string> = {
  workspace: "Workspace",
  private: "Personal",
};

/**
 * 可见性"选择"场景（创建弹窗/选择器）的说明文案。其中 "Personal" 以
 * `permission_mode: "private"` 提交——仅所有者可调用，MUL-3963 之后不再有
 * 工作区管理员绕行（见 `server/internal/handler/agent_access.go` 的
 * `canInvokeAgent`）。旧版"…以及工作区管理员…"的文案早于该闸门，已不再真实。
 */
export const VISIBILITY_DESCRIPTION: Record<AgentVisibility, string> = {
  workspace: "All members can assign",
  private: "Only you can assign",
};

/**
 * 只读徽标在悬停/列表行上使用的提示文案。针对"已存在的 Agent"措辞——此处的
 * `visibility` 是权限模型的有损两态投影：`private` 既覆盖真正仅所有者的 Agent，
 * 也覆盖分享给特定人群的 Agent，所以文案点明所有者的授权范围，而不承诺任何极端。
 * 它绝不能声称工作区管理员可以指派——管理员只有管理与查看权，没有调用权。
 */
export const VISIBILITY_TOOLTIP: Record<AgentVisibility, string> = {
  workspace: "Workspace — all members can assign",
  private: "Personal — only the owner and people they allow can assign",
};

/** 取某可见性值对应的展示标签。 */
export function visibilityLabel(v: AgentVisibility): string {
  return VISIBILITY_LABEL[v];
}
