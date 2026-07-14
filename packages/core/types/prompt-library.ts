export type PromptLibraryStatus = "启用" | "归档";

export interface PromptLibraryVariable {
  name: string;
  label?: string;
  required?: boolean;
  description?: string;
  default_value?: string;
}

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
  status?: PromptLibraryStatus;
}

export interface CreatePromptLibraryItemRequest {
  project_id?: string | null;
  name: string;
  description?: string;
  prompt_type?: string;
  content: string;
  variables?: PromptLibraryVariable[];
  tags?: string[];
  status?: PromptLibraryStatus;
}

export interface UpdatePromptLibraryItemRequest {
  project_id?: string | null;
  name?: string;
  description?: string;
  prompt_type?: string;
  content?: string;
  variables?: PromptLibraryVariable[];
  tags?: string[];
  status?: PromptLibraryStatus;
}

export interface CreatePromptLibraryVersionRequest {
  name?: string;
  description?: string;
  content: string;
  change_note?: string;
}

export interface CreatePromptLibraryVersionResponse {
  item: PromptLibraryItem;
  version: PromptLibraryVersion;
}

export interface CreatePromptLibraryTrialRequest {
  agent_id: string;
  variables?: Record<string, string>;
}
