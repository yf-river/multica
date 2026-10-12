// 本文件提供「单例草稿 store」的工厂函数：统一实现草稿的读写、清空、判空、
// 按工作区持久化、迁移与清理注册，避免每个草稿模块重复手抄同一套结构。

import { create, type StoreApi, type UseBoundStore } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";
import { registerDraftCleanup } from "./cleanup-registry";

/**
 * 草稿 store 的配置。各字段概括了每个草稿模块唯一的差异点。
 */
export interface DraftStoreConfig<TData> {
  /** 持久化 key（在拼上工作区 slug 后缀之前）。必须全局唯一。 */
  storageKey: string;
  /** 空/初始草稿。清空草稿和新建 store 都从这里起步。 */
  emptyData: TData;
  /**
   * 判断这份草稿里是否装着「值得恢复的用户输入」。必须覆盖界面当作
   * 「未保存工作」的所有信号——正文、有意义的字段选择、附件——而不只是
   * 主文本字段。
   */
  hasMeaningful: (data: TData) => boolean;
  /** 是否通过 `createWorkspaceAwareStorage` 按工作区 slug 分桶持久化，默认 true。 */
  workspaceScoped?: boolean;
  /**
   * 给「早于后来新增字段的旧构建」持久化下来的草稿补默认值，让读取方可以
   * 信赖声明的结构。入参是原始持久化 `data`，返回值是要覆盖到 emptyData 上的
   * 那些字段。
   */
  migrateData?: (persistedData: unknown) => Partial<TData>;
}

/** 草稿 store 对外暴露的接口。 */
export interface DraftStore<TData> {
  draft: TData;
  setDraft: (patch: Partial<TData>) => void;
  clearDraft: () => void;
  hasDraft: () => boolean;
}

/**
 * 创建单例草稿 store。
 *
 * 在它出现之前，每个简单草稿 store（`projects/draft-store`、
 * `projects/draft-store`，结构上还有 issue 草稿 store）都手抄同一套形状：
 * 一个 `draft` 对象、`setDraft(patch)`、`clearDraft()`、`hasDraft()`、
 * 接好 `createWorkspaceAwareStorage` 的 `persist` 配置，以及一次
 * `registerForWorkspaceRehydration` 调用。各 store 唯一的差异只是数据形状、
 * persist key 和「这份草稿算不算有意义」的判断——也就是上面
 * {@link DraftStoreConfig} 里的字段。
 *
 * 工厂额外接上了两件各副本反复做错的事：
 *   1. `registerDraftCleanup` —— 自动登记自己的 persist key，这样删除工作区
 *      和登出时能清理它，无需人工维护一份 key 清单。
 *   2. 一个内存态 `resetInMemory` —— 登出会重置 Zustand 单例，避免上一个
 *      用户的草稿在同标签页的「下次登录」里残留。
 *
 * @param config - 该草稿类型的配置
 * @returns 一个 React 可用的 bound store（Zustand hook）
 */
export function createDraftStore<TData extends object>(
  config: DraftStoreConfig<TData>,
): UseBoundStore<StoreApi<DraftStore<TData>>> {
  const workspaceScoped = config.workspaceScoped ?? true;
  const empty = () => structuredCloneData(config.emptyData);

  const useStore = create<DraftStore<TData>>()(
    persist(
      (set, get) => ({
        draft: empty(),
        setDraft: (patch) => set((s) => ({ draft: { ...s.draft, ...patch } })),
        clearDraft: () => set({ draft: empty() }),
        hasDraft: () => config.hasMeaningful(get().draft),
      }),
      {
        name: config.storageKey,
        storage: createJSONStorage(() =>
          workspaceScoped
            ? createWorkspaceAwareStorage(defaultStorage)
            : defaultStorage,
        ),
        merge: (persistedState, currentState) => {
          const persisted = (persistedState ?? {}) as Partial<DraftStore<TData>>;
          const persistedData = persisted.draft as unknown;
          const overlay = config.migrateData
            ? config.migrateData(persistedData)
            : (persistedData as Partial<TData> | undefined);
          return {
            ...currentState,
            ...persisted,
            // 深拷贝 emptyData，避免「缺某个嵌套字段的持久化草稿」直接引用到
            // 模块级常量的数组/对象引用。
            draft: { ...structuredCloneData(config.emptyData), ...overlay },
          };
        },
      },
    ),
  );

  if (workspaceScoped) {
    registerForWorkspaceRehydration(() => useStore.persist.rehydrate());
  }

  registerDraftCleanup({
    storageKey: config.storageKey,
    workspaceScoped,
    resetInMemory: () => useStore.getState().clearDraft(),
  });

  return useStore;
}

// 深拷贝放在本地且带防御：空草稿可能包含嵌套数组/对象（labelIds、
// propertyValues、attachments），绝不能让初始状态与之后的 `clearDraft` 通过
// 引用共享它们。
function structuredCloneData<T>(value: T): T {
  return typeof structuredClone === "function"
    ? structuredClone(value)
    : (JSON.parse(JSON.stringify(value)) as T);
}
