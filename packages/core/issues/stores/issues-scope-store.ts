"use client";

// 议题页 / 项目页上"全部 / 成员 / Agent"标签页的归属范围 store：
// 每个页面各自记住自己的标签（按页面 key 分开存），并随工作区切换重水合。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/** 议题列表的归属范围：全部 / 只看成员 / 只看 Agent。 */
export type IssuesScope = "all" | "members" | "agents";

/**
 * "负责人类型标签页"所在的页面身份。每个页面各自记住自己的标签——
 * Issues 主页记在 "issues" 下、每个项目页记在 `project:<id>` 下——这样在一个项目
 * 里切换标签不会连累 Issues 主页（或别的项目）跟着变。
 */
export type IssuesScopePageKey = "issues" | `project:${string}`;

interface IssuesScopeState {
  scopes: Partial<Record<IssuesScopePageKey, IssuesScope>>;
  setScope: (page: IssuesScopePageKey, scope: IssuesScope) => void;
}

export const useIssuesScopeStore = create<IssuesScopeState>()(
  persist(
    (set) => ({
      scopes: {},
      setScope: (page, scope) =>
        set((state) => ({ scopes: { ...state.scopes, [page]: scope } })),
    }),
    {
      name: "multica_issues_scope",
      version: 1,
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      migrate: (persisted, version) => {
        // v0 只存了一个所有页面共享的标签；把它迁移成 Issues 主页的标签，
        // 各项目页则从 "all" 干净起步。
        if (version === 0) {
          const legacy = (persisted as { scope?: IssuesScope } | undefined)
            ?.scope;
          return {
            scopes:
              legacy === "members" || legacy === "agents"
                ? { issues: legacy }
                : {},
          } as IssuesScopeState;
        }
        return persisted as IssuesScopeState;
      },
    },
  ),
);

/** 读取某页面当前的标签；用户还没选过时为 "all"。 */
export function useIssuesScope(page: IssuesScopePageKey): IssuesScope {
  return useIssuesScopeStore((s) => s.scopes[page] ?? "all");
}

// 切换工作区后重水合该工作区各页面的标签记忆。
registerForWorkspaceRehydration(() => useIssuesScopeStore.persist.rehydrate());
