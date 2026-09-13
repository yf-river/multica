"use client";

/** 技能详情页路由：解出 skill id 渲染共享详情视图。 */
import { use } from "react";
import { SkillDetailPage } from "@multica/views/skills/skill-detail-page";

/**
 * 技能详情路由。
 *
 * @param props.params - 动态参数，含技能 id（Promise 形式）。
 * @returns 技能详情页。
 */
export default function SkillDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <SkillDetailPage skillId={id} />;
}
