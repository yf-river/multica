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
  workspace_id: string;
  project_id: string | null;
  name: string;
  description: string;
  prompt_type: string;
  content: string;
  variables: PromptLibraryVariable[];
  tags: string[];
  status: PromptLibraryStatus;
  version: number;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface PromptLibraryVersion {
  id: string;
  prompt_id: string;
  workspace_id: string;
  project_id: string | null;
  version: number;
  name: string;
  description: string;
  prompt_type: string;
  content: string;
  variables: PromptLibraryVariable[];
  tags: string[];
  source: "手动创建" | "手动更新" | "优化候选发布" | "历史回填";
  source_candidate_id: string | null;
  change_note: string;
  created_by: string | null;
  created_at: string;
}

export interface PromptLibraryTrial {
  id: string;
  workspace_id: string;
  prompt_id: string;
  version_id: string;
  agent_id: string;
  chat_session_id: string | null;
  task_id: string | null;
  input: string;
  rendered_message: string;
  variables: Record<string, unknown>;
  status: string;
  output_preview: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface ListPromptLibraryItemsResponse {
  items: PromptLibraryItem[];
  total: number;
}

export interface ListPromptLibraryVersionsResponse {
  items: PromptLibraryVersion[];
  total: number;
}

export interface ListPromptLibraryTrialsResponse {
  items: PromptLibraryTrial[];
  total: number;
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
  input?: string;
  variables?: Record<string, string>;
}
