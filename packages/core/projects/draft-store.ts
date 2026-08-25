import type { CreateProjectRequest, ProjectStatus, ProjectPriority } from "../types";
import {
  createWorkspacePendingCreateStore,
  type PendingCreateOperation,
  type RecoverableOperationStore,
} from "../platform/recoverable-operation-store";
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

export const useProjectCreateOperationStore: RecoverableOperationStore<
  PendingCreateOperation<CreateProjectRequest>
> = createWorkspacePendingCreateStore<CreateProjectRequest>(
  "multica_project_create_operation",
);
