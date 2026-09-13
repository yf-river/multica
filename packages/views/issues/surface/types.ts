// IssueSurface 对外暴露的类型：创建问题时的默认字段、支持的视图模式、以及 surface 的入参。
import type { IssueScope } from "@multica/core/issues/surface/scope";
import type { CreateIssueRequest } from "@multica/core/types";
import type { ViewMode } from "@multica/core/issues/stores/view-store";

/**
 * 创建问题弹窗的默认值。在 CreateIssueRequest 的基础上，把几个「通常不该被默认值
 * 覆盖」的字段（指派人/父问题/项目）单独放宽为可空，供调用方按需传种子值。
 */
export type IssueCreateDefaults = Partial<
  Omit<
    CreateIssueRequest,
    "assignee_type" | "assignee_id" | "parent_issue_id" | "project_id"
  >
> & {
  assignee_type?: CreateIssueRequest["assignee_type"] | null;
  assignee_id?: string | null;
  parent_issue_id?: string | null;
  /** 父问题的展示用标识符——仅供创建弹窗在父问题数据还没加载完时显示文案。 */
  parent_issue_identifier?: string;
  project_id?: string | null;
};

/** IssueSurface 支持的视图模式（取自 ViewMode 的子集）。 */
export type IssueSurfaceMode = Extract<
  ViewMode,
  "board" | "list" | "table" | "swimlane" | "gantt"
>;

/** IssueSurface 的入参。 */
export interface IssueSurfaceProps {
  scope: IssueScope;
  modes: IssueSurfaceMode[];
  surfaceKey?: string;
  createDefaults?: IssueCreateDefaults;
  /** 由服务端持有的成员搜索词，供非 Table 类问题界面共享。 */
  search?: string;
}
