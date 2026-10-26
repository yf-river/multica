/** OpenViking 工作区知识库的只读接口类型。 */

export interface KnowledgeStatus {
  enabled: boolean;
  healthy: boolean;
  base_url?: string;
  error?: string;
  health?: unknown;
}

export interface KnowledgeResourcesResponse {
  uri: string;
  result: unknown;
}

export interface KnowledgeSearchResponse {
  query: string;
  target_uri: string;
  result: unknown;
}

export interface KnowledgeUploadResponse {
  filename: string;
  result: unknown;
}
