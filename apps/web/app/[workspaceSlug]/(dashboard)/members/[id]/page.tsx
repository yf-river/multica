"use client";

/** 成员详情页路由：解出用户 id 渲染共享的成员详情视图。 */
import { use } from "react";
import { MemberDetailPage } from "@multica/views/members";

/**
 * 成员详情路由。
 *
 * @param props.params - 动态参数，含用户 id（Promise 形式）。
 * @returns 成员详情页。
 */
export default function MemberDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <MemberDetailPage userId={id} />;
}
