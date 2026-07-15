export interface PromptLibraryItem {
  id: string;
  name: string;
  description: string;
  content: string;
  version: number;
}

export interface PromptLibraryVersion {
  id: string;
  version: number;
  name: string;
  description: string;
  content: string;
  source_candidate_id: string | null;
  change_note: string;
  created_at: string;
}

export interface PromptLibraryTrial {
  id: string;
  agent_id: string;
  variables: Record<string, unknown>;
  status: string;
  output_preview: string;
  created_at: string;
}

export interface ListPromptLibraryItemsParams {
  project_id?: string;
  prompt_type?: string;
  status?: "启用" | "归档";
}

export interface CreatePromptLibraryItemRequest {
  name: string;
  description?: string;
  prompt_type?: string;
  content: string;
}

export type CreatePromptLibraryVersionRequest = Partial<Pick<
  CreatePromptLibraryItemRequest,
  "name" | "description"
>> & Pick<CreatePromptLibraryItemRequest, "content"> & {
  change_note?: string;
};

export interface CreatePromptLibraryVersionResponse {
  item: PromptLibraryItem;
  version: PromptLibraryVersion;
}

export interface CreatePromptLibraryTrialRequest {
  agent_id: string;
  variables?: Record<string, string>;
}
