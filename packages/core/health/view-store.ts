import { create } from "zustand";

interface HealthView {
  editorId: string | null;
  editorPreset: string | null;
  editorMode: "new" | "update" | "correct";
  editorRevision: number;
  profileOpen: boolean;
  profileRevision: number;
  targetRevision: number | null;
  extractionAttempt: number | null;
}

export const EMPTY_HEALTH_VIEW: HealthView = {
  editorId: null,
  editorPreset: null,
  editorMode: "new",
  editorRevision: 0,
  profileOpen: false,
  profileRevision: 0,
  targetRevision: null,
  extractionAttempt: null,
};

// 工作区与用户共同隔离视图指针；指标和报告仍从 Query 缓存读取。
export const useHealthViewStore = create<{
  views: Record<string, HealthView>;
  update: (key: string, patch: Partial<HealthView>) => void;
}>((set) => ({
  views: {},
  update: (key, patch) => set((state) => ({ views: { ...state.views, [key]: { ...(state.views[key] || EMPTY_HEALTH_VIEW), ...patch } } })),
}));
