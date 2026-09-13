// 本文件定义「标签（Label）」相关类型。标签是 workspace 级、与议题多对多
// 关联的轻量元数据。

/**
 * 议题标签——workspace 内可见，与议题多对多关联。
 *
 * 标签是轻量元数据（名称 + 颜色），与项目不同：项目把相关工作归组，标签是
 * 横切式的标记（bug、feature、性能……）。颜色统一规范为小写 `#RRGGBB`。
 */
export type LabelResourceType = "issue" | "agent" | "skill";

/** 标签实体。 */
export interface Label {
  id: string;
  workspace_id: string;
  resource_type?: LabelResourceType;
  name: string;
  description?: string;
  /** 规范化的小写十六进制颜色，如 `#3b82f6`。 */
  color: string;
  usage_count?: number;
  created_at: string;
  updated_at: string;
}

/** 新建标签的请求体。 */
export interface CreateLabelRequest {
  resource_type?: LabelResourceType;
  name: string;
  description?: string;
  color: string;
}

/** 更新标签的请求体（只更新传入的字段）。 */
export interface UpdateLabelRequest {
  name?: string;
  description?: string;
  color?: string;
}

/** 标签列表接口响应。 */
export interface ListLabelsResponse {
  labels: Label[];
  total: number;
}

/** 某议题当前挂的标签及该议题的修订号。 */
export interface IssueLabelsResponse {
  labels: Label[];
  issue_revision?: number;
}

/** 通用资源（agent/skill）的标签响应，形状与议题标签一致。 */
export type ResourceLabelsResponse = IssueLabelsResponse;
