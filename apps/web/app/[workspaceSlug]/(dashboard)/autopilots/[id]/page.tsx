"use client";

/** Autopilot 详情页路由：从 URL 解出 autopilotId 渲染共享详情视图。 */
import { use } from "react";
import { AutopilotDetailPage } from "@multica/views/autopilots/autopilot-detail-page";

/**
 * Autopilot 详情路由。
 *
 * @param props.params - 动态参数，含 autopilot id（Promise 形式）。
 * @returns Autopilot 详情页。
 */
export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <AutopilotDetailPage autopilotId={id} />;
}
