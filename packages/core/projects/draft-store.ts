import type { CreateProjectRequest, ProjectStatus, ProjectPriority } from "../types";
import { createWorkspaceDraftStore } from "../platform/workspace-storage";

interface ProjectDraft {
  title: string;
  description: string;
  status: ProjectStatus;
  priority: ProjectPriority;
  leadType?: "member" | "agent";
  leadId?: string;
  icon?: string;
  pendingCreate?: {
    requestKey: string;
    request: CreateProjectRequest;
  };
}

const EMPTY_DRAFT: ProjectDraft = {
  title: "",
  description: "",
  status: "planned",
  priority: "none",
  leadType: undefined,
  leadId: undefined,
  icon: undefined,
  pendingCreate: undefined,
};

export const useProjectDraftStore = createWorkspaceDraftStore(
  "multica_project_draft",
  EMPTY_DRAFT,
);
