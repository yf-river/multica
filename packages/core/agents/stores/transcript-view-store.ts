"use client";

// Agent 任务记录（transcript）面板的视图偏好 store：排序方向与过滤标签，
// 全局持久化（不按工作区分——纯个人阅读偏好）。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

/** 任务记录的排序方向：时间正序（chronological）或最新在前。 */
export type TranscriptSortDirection = "chronological" | "newest_first";
/** 过滤 key：要按哪些标签/维度过滤记录。 */
export type TranscriptFilterKey = string;

interface TranscriptViewState {
  sortDirection: TranscriptSortDirection;
  selectedFilterKeys: TranscriptFilterKey[];
  setSortDirection: (dir: TranscriptSortDirection) => void;
  setSelectedFilterKeys: (keys: TranscriptFilterKey[]) => void;
  toggleFilterKey: (key: TranscriptFilterKey) => void;
  clearFilterKeys: () => void;
}

const DEFAULTS = {
  sortDirection: "chronological" as TranscriptSortDirection,
  selectedFilterKeys: [] as TranscriptFilterKey[],
};

/** 过滤 key 清洗：去重、丢弃空串，保证状态里没有无效条目。 */
function uniqueFilterKeys(keys: TranscriptFilterKey[]): TranscriptFilterKey[] {
  return Array.from(new Set(keys.filter((key) => key.length > 0)));
}

export const useTranscriptViewStore = create<TranscriptViewState>()(
  persist(
    (set) => ({
      ...DEFAULTS,
      setSortDirection: (sortDirection) => set({ sortDirection }),
      setSelectedFilterKeys: (selectedFilterKeys) =>
        set({ selectedFilterKeys: uniqueFilterKeys(selectedFilterKeys) }),
      toggleFilterKey: (key) =>
        set((state) => ({
          selectedFilterKeys: state.selectedFilterKeys.includes(key)
            ? state.selectedFilterKeys.filter((candidate) => candidate !== key)
            : [...state.selectedFilterKeys, key],
        })),
      clearFilterKeys: () => set({ selectedFilterKeys: [] }),
    }),
    {
      name: "multica_transcript_view",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({
        sortDirection: state.sortDirection,
        selectedFilterKeys: state.selectedFilterKeys,
      }),
      merge: (persisted, current) => {
        // 无持久化值时用默认值，避免上一工作区的内存状态残留。
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<TranscriptViewState>;
        return {
          ...current,
          sortDirection: p.sortDirection ?? DEFAULTS.sortDirection,
          selectedFilterKeys: uniqueFilterKeys(p.selectedFilterKeys ?? []),
        };
      },
    },
  ),
);
