"use client";

// 项目纯展示小徽标：`<emoji> <项目名>`，带边框、超宽截断，用于任务行、提及等
// 需要内嵌引用项目的地方。内部自行按 id 查项目，调用方只需给一个 id。
import { useQuery } from "@tanstack/react-query";
import { projectListOptions, projectDetailOptions } from "@multica/core/projects/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { ProjectIcon } from "./project-icon";
import { useT } from "../../i18n";

/**
 * 项目的紧凑展示表示——`<emoji> <标题>`、带边框、到宽度上限后截断。是
 * IssueChip 的镜像版，包括同一个 `min(18rem, 100%)` 上限——为什么内容和容器
 * 两层上限都要，见 issue-chip 那个文件的说明。两个 chip 共用同一套渲染契约，
 * 绝不能各自漂移。
 *
 * 不是链接也不是按钮：调用方想包成什么交互外壳自己包。纯 UI——数据在内部按
 * id 查询，所以调用方只需传一个项目 id。
 */
export interface ProjectChipProps {
  projectId: string;
  /** 项目解析不出来时显示的兜底文案。 */
  fallbackLabel?: string;
  /** 追加的样式类——调用方把交互提示效果叠在这里。 */
  className?: string;
}

const BASE_CLASS =
  "project-chip inline-flex min-w-0 max-w-[min(18rem,100%)] items-center gap-1.5 rounded-md border mx-0.5 px-2 py-0.5 text-caption";

/**
 * 渲染一个项目徽标：先在工作区项目列表里找，找不到再按 id 单独拉详情。
 *
 * @param projectId 目标项目 id
 * @param fallbackLabel 解析失败时的兜底文案
 * @param className 追加样式类
 */
export function ProjectChip({
  projectId,
  fallbackLabel,
  className,
}: ProjectChipProps) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const listProject = projects.find((p) => p.id === projectId);

  const { data: detailProject } = useQuery({
    ...projectDetailOptions(wsId, projectId),
    enabled: !listProject,
  });

  const project = listProject ?? detailProject;
  const cls = className ? `${BASE_CLASS} ${className}` : BASE_CLASS;

  if (!project) {
    return (
      <span className={cls}>
        <ProjectIcon size="md" />
        <span className="min-w-0 truncate text-muted-foreground">
          {fallbackLabel ?? t(($) => $.chip.fallback_label)}
        </span>
      </span>
    );
  }

  return (
    <span className={cls}>
      <ProjectIcon project={project} size="md" />
      <span className="min-w-0 truncate text-foreground">{project.title}</span>
    </span>
  );
}
