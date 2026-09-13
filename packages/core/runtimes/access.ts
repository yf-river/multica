// 判断某成员能否在指定 runtime 上运行 Agent（picker 与后端共用同一套规则）。
import type { RuntimeDevice } from "../types";

/**
 * 该成员是否可以在 `runtime` 上运行 Agent。
 *
 * 私有 runtime 仅其所有者可用；公开的供工作区任意成员；无所有者的谁都不能用。
 * 工作区角色不参与判断：私有 runtime 是某个人的私有机器，连管理员都不能把 Agent
 * 绑上去，也只有所有者能把它翻成公开。服务器执行的正是这个谓词
 * （`canUseRuntimeForAgent`），所以选择器与 API/CLI 永不一致（MUL-6126）。
 *
 * 未知查看者（还没有会话）按"允许"处理，这样仍在加载的认证状态永远不会藏起一台
 * 用户确实拥有的 runtime——每条写路径都会在服务端复查。
 *
 * 这是"这台 runtime 能不能被选中"的唯一真相来源：每个创建 / 复制 / 构建器 /
 * 改绑界面都必须调用它，而不是自己重新推导规则。
 * @param runtime 待判断的 runtime。
 * @param currentUserId 当前用户 id（无会话时为 null）。
 * @returns 是否可用于该用户的 Agent。
 */
export function isRuntimeUsableForUser(
  runtime: RuntimeDevice,
  currentUserId: string | null,
): boolean {
  // 无所有者的 runtime 谁都不能用，包括公开的在内：服务器需要一位所有者来铸造
  // Agent 的任务 token，所以在这里绑定只会得到"认领时任务被取消"的 Agent
  // （MUL-3292）。
  if (!runtime.owner_id) return false;
  if (!currentUserId) return true;
  if (runtime.owner_id === currentUserId) return true;
  return runtime.visibility === "public";
}
