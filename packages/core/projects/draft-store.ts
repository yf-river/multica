// 本文件提供「项目创建表单」的草稿 store：新建项目时填到一半的内容可跨刷新
// 找回。

import type { ProjectStatus, ProjectPriority } from "../types";
import { createDraftStore } from "../drafts/create-draft-store";

/** 项目创建表单的草稿结构。 */
interface ProjectDraft {
  title: string;
  description: string;
  status: ProjectStatus;
  priority: ProjectPriority;
  leadType?: "member" | "agent";
  leadId?: string;
  icon?: string;
  // 日历日（"YYYY-MM-DD"）；空/undefined 表示未设置。
  startDate?: string;
  dueDate?: string;
}

const EMPTY_DRAFT: ProjectDraft = {
  title: "",
  description: "",
  status: "planned",
  priority: "none",
  leadType: undefined,
  leadId: undefined,
  icon: undefined,
  startDate: undefined,
  dueDate: undefined,
};

/** 项目创建草稿 store：有标题或描述即视为「有内容值得保存」。 */
export const useProjectDraftStore = createDraftStore<ProjectDraft>({
  storageKey: "multica_project_draft",
  emptyData: EMPTY_DRAFT,
  hasMeaningful: (d) => !!(d.title || d.description),
});
