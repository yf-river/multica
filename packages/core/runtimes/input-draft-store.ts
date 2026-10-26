import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { CreateRuntimeInput } from "../api/runtime-input-schema";
import { registerDraftCleanup } from "../drafts/cleanup-registry";
import { defaultStorage } from "../platform/storage";
import { createSafeId } from "../utils";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../platform/workspace-storage";

type InputDraft = CreateRuntimeInput & { updatedAt: number };
interface InputDraftStore {
  drafts: Record<string, InputDraft>;
  reserve: (taskId: string, kind: CreateRuntimeInput["kind"], content: string) => CreateRuntimeInput;
  clear: (taskId: string, id: string) => void;
}

// 网络结果未知时保留逻辑请求身份；页面刷新后的同文重试也只能领取同一条记录。
export const useRuntimeInputDraftStore = create<InputDraftStore>()(persist((set, get) => ({
  drafts: {},
  reserve: (taskId, kind, content) => {
    const existing = get().drafts[taskId];
    if (existing?.kind === kind && existing.content === content) return { id: existing.id, kind, content };
    const input = { id: createSafeId(), kind, content };
    set((state) => ({ drafts: { ...state.drafts, [taskId]: { ...input, updatedAt: Date.now() } } }));
    return input;
  },
  clear: (taskId, id) => set((state) => {
    if (state.drafts[taskId]?.id !== id) return state;
    const drafts = { ...state.drafts };
    delete drafts[taskId];
    return { drafts };
  }),
}), {
  name: "multica_runtime_input_drafts",
  storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
  merge: (persisted, current) => ({ ...current, drafts: (persisted as { drafts?: Record<string, InputDraft> } | undefined)?.drafts ?? {} }),
}));

registerForWorkspaceRehydration(() => useRuntimeInputDraftStore.persist.rehydrate());
registerDraftCleanup({
  storageKey: "multica_runtime_input_drafts", workspaceScoped: true,
  resetInMemory: () => useRuntimeInputDraftStore.setState({ drafts: {} }),
});
