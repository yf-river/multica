"use client";

/** 运行时详情页路由：解出 runtime id 渲染共享详情视图。 */
import { use } from "react";
import { RuntimeDetailPage } from "@multica/views/runtimes/runtime-detail-page";

/**
 * 运行时详情路由。
 *
 * @param props.params - 动态参数，含 runtime id（Promise 形式）。
 * @returns 运行时详情页。
 */
export default function RuntimeDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <RuntimeDetailPage runtimeId={id} />;
}
