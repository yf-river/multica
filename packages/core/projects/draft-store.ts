import type { CreateProjectRequest, ProjectStatus, ProjectPriority } from "../types";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import { createWorkspaceDraftStore } from "../platform/workspace-storage";

interface ProjectDraft {
  title: string;
  description: string;
  status: ProjectStatus;
  priority: ProjectPriority;
  leadType?: "member" | "agent";
  leadId?: string;
  icon?: string;
}

const EMPTY_DRAFT: ProjectDraft = {
  title: "",
  description: "",
  status: "planned",
  priority: "none",
  leadType: undefined,
  leadId: undefined,
  icon: undefined,
};

export const useProjectDraftStore = createWorkspaceDraftStore(
  "multica_project_draft",
  EMPTY_DRAFT,
);

export const useProjectCreateOperationStore =
  createWorkspacePendingCreateStore<CreateProjectRequest>(
    "multica_project_create_operation",
  );
