"use client";

/** 项目详情页路由：解出项目 id 渲染共享详情视图。 */
import { use } from "react";
import { ProjectDetail } from "@multica/views/projects/project-detail";

/**
 * 项目详情路由。
 *
 * @param props.params - 动态参数，含项目 id（Promise 形式）。
 * @returns 项目详情页。
 */
export default function ProjectDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <ProjectDetail projectId={id} />;
}
