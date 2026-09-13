"use client";

// Quick Create（快速创建议题）弹窗的偏好 store：记住用户上次挑的 actor
// （Agent / 小组）与"提交后保持打开"开关。actor 记忆按工作区分开存；
// Agent 提示词本体不在这里——它已并入统一的创建草稿的 `agent` 槽（draft-store，
// MUL-5181），与此 store 生命周期同进退。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import { registerDraftCleanup } from "../../drafts/cleanup-registry";

/** Quick Create 里可选的执行者类型：单个 Agent 或 Agent 小组（squad）。 */
export type QuickCreateActorType = "agent" | "squad";

// 记住用户上次在 Quick Create 弹窗里挑的 actor（Agent 或小组），按工作区分开。
// 下次打开时默认选它，让高频用户完全跳过选择器。用按工作区隔离的 StateStorage
// 持久化，切工作区会自动展示正确默认值；按用户隔离则免费来自 localStorage 本身
// 就是浏览器用户配置文件局部的——与 draft-store / issues-scope-store /
// comment-collapse-store 的命名空间方式一致。
//
// "上次的项目"刻意不记（MUL-5862）。actor 和项目看着对称其实不对称：议题的目标
// 项目是"这条议题的属性"，不是一项长期偏好——把上一个项目的记忆带进下一次提交，
// 用户一旦换地方就会猜错，把下一条议题悄悄投进一个他从没选过的项目。能存活的
// 两个种子背后都有用户意图：打开弹窗所在的项目页，和他自己未完成的草稿。
//
// lastActorType + lastActorId 取代了小组可选之前的旧 `lastAgentId` 字段。
// 有旧 Agent 偏好的用户回到"选择器当前第一个选项"；一次性重选好过把单个 UUID
// 重载成类型不明确的 tag。
//
// 进行中的 Agent 提示词已不在这里——它移进了统一议题创建草稿的 `agent` 槽
// （draft-store），与手动草稿共享同一生命周期（MUL-5181）。这个 store 只保留
// 最近一次成功的 actor 和共享的 keep-open 开关。
interface QuickCreateState {
  lastActorType: QuickCreateActorType | null;
  lastActorId: string | null;
  setLastActor: (type: QuickCreateActorType | null, id: string | null) => void;
  /** 提交后弹窗是否保持打开（便于连续创建）。 */
  keepOpen: boolean;
  setKeepOpen: (v: boolean) => void;
}

export const useQuickCreateStore = create<QuickCreateState>()(
  persist(
    (set) => ({
      lastActorType: null,
      lastActorId: null,
      setLastActor: (type, id) => set({ lastActorType: type, lastActorId: id }),
      keepOpen: false,
      setKeepOpen: (v) => set({ keepOpen: v }),
    }),
    {
      name: "multica_quick_create",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

// 切换工作区后重水合该工作区的 actor 记忆。
registerForWorkspaceRehydration(() => useQuickCreateStore.persist.rehydrate());

// 登出清理：重置按用户记的选择器记忆，别让它流进同一标签页的下一次登录。
// （提示词草稿现在住在 draft-store 里。）
registerDraftCleanup({
  storageKey: "multica_quick_create",
  workspaceScoped: true,
  resetInMemory: () =>
    useQuickCreateStore.setState({
      lastActorType: null,
      lastActorId: null,
      keepOpen: false,
    }),
});
