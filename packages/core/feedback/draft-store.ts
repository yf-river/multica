import { createWorkspaceDraftStore } from "../platform/workspace-storage";

interface FeedbackDraft {
  message: string;
}

const EMPTY_DRAFT: FeedbackDraft = {
  message: "",
};

export const useFeedbackDraftStore = createWorkspaceDraftStore(
  "multica_feedback_draft",
  EMPTY_DRAFT,
);
