import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import type { IssueStatus, IssuePriority, IssueAssigneeType, Attachment } from "../../types";
import { createWorkspaceAwareStorage, registerWorkspacePersistStore } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

interface IssueDraft {
  title: string;
  description: string;
  status: IssueStatus;
  priority: IssuePriority;
  assigneeType?: IssueAssigneeType;
  assigneeId?: string;
  startDate: string | null;
  dueDate: string | null;
  attachments: Attachment[];
}

const EMPTY_DRAFT: IssueDraft = {
  title: "",
  description: "",
  status: "todo",
  priority: "none",
  assigneeType: undefined,
  assigneeId: undefined,
  startDate: null,
  dueDate: null,
  attachments: [],
};

interface IssueDraftStore {
  draft: IssueDraft;
  setDraft: (patch: Partial<IssueDraft>) => void;
  clearDraft: () => void;
}

export const useIssueDraftStore = create<IssueDraftStore>()(
  persist(
    (set) => ({
      draft: { ...EMPTY_DRAFT },
      setDraft: (patch) =>
        set((s) => ({ draft: { ...s.draft, ...patch } })),
      clearDraft: () => set({ draft: { ...EMPTY_DRAFT } }),
    }),
    {
      name: "multica_issue_draft",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerWorkspacePersistStore(useIssueDraftStore);
