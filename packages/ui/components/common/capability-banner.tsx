import { Lock } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

// 本文件是「无权限只读横幅」组件：当当前用户无权编辑某个资源（Agent/Skill/评论/运行时/工作区）时，
// 在详情页顶部展示一条带锁图标的提示，说明为什么不能编辑以及要怎样才能获得编辑权。

/** 受保护的资源类型：横幅文案里的名词由它决定 */
type Resource = "agent" | "skill" | "comment" | "runtime" | "workspace";

/** 无权编辑的原因，来自后端的权限判断结果 */
type Reason =
  | "allowed"
  | "not_authenticated"
  | "not_member"
  | "not_owner_role"
  | "not_admin_role"
  | "not_resource_owner"
  | "last_owner"
  | "private_visibility"
  | "unknown";

/** 每种资源在文案里对应的英文名词 */
const RESOURCE_NOUN: Record<Resource, string> = {
  agent: "agent",
  skill: "skill",
  comment: "comment",
  runtime: "runtime",
  workspace: "workspace",
};

/**
 * CapabilityBanner：资源详情页的只读横幅——当前用户无权编辑该资源时出现。
 * 单一组件统一维护所有文案分支，保证 Agent、Skill、运行时详情页的措辞一致。
 *
 * 当用户*可以*编辑（reason === "allowed"）时返回 `null`，所以调用方可以无条件挂载。
 *
 * @param props.reason 无权原因；"allowed"/"unknown" 时不渲染任何内容
 * @param props.resource 资源类型，决定文案里的名词
 * @param props.ownerName 资源所有者/创建者的显示名（可选），有名字时文案更具体
 * @param props.className 额外的外层 class 名
 * @returns 只读提示横幅元素；无权限限制时返回 null
 */
export function CapabilityBanner({
  reason,
  resource,
  ownerName,
  className,
}: {
  reason: Reason;
  resource: Resource;
  /** 资源所有者/创建者的显示名（可选）——缺失时文案自动降级为通用说法 */
  ownerName?: string;
  className?: string;
}) {
  if (reason === "allowed" || reason === "unknown") return null;

  const noun = RESOURCE_NOUN[resource];
  const message = getCopy(reason, noun, ownerName);

  return (
    <div
      role="status"
      className={cn(
        "flex items-center gap-2 rounded-md border border-dashed bg-muted/30 px-3 py-2 text-caption text-muted-foreground",
        className,
      )}
    >
      <Lock className="h-3.5 w-3.5 shrink-0" aria-hidden />
      <span>{message}</span>
    </div>
  );
}

/**
 * 根据「无权原因 + 资源名词 + 所有者名字」拼出横幅展示文案。
 * 每种原因对应固定的句式；ownerName 只在个别原因里用来让措辞更精确。
 */
function getCopy(reason: Reason, noun: string, ownerName?: string): string {
  switch (reason) {
    case "not_authenticated":
      return `Sign in to edit this ${noun}.`;
    case "not_member":
      return `Join this workspace to edit this ${noun}.`;
    case "not_owner_role":
      return `View only — only the workspace owner can manage this ${noun}.`;
    case "not_admin_role":
      return `View only — only workspace owners and admins can manage this ${noun}.`;
    case "not_resource_owner":
      if (ownerName) {
        return `View only — only ${ownerName} and workspace admins can edit this ${noun}.`;
      }
      return `View only — only the ${noun} owner and workspace admins can edit this ${noun}.`;
    case "last_owner":
      return `A workspace must keep at least one owner — promote another member first.`;
    case "private_visibility":
      if (ownerName) {
        return `Personal ${noun} — only ${ownerName} and workspace admins can use this.`;
      }
      return `Personal ${noun} — only the owner and workspace admins can use this.`;
    case "allowed":
    case "unknown":
      return ""; // 正常情况下走不到这里：组件在上面已经对 allowed/unknown 提前返回 null 了
  }
}
