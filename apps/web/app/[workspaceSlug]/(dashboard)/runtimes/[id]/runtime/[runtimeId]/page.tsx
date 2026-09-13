"use client";

/** 运行时的单个 runtime 设置页路由：解出 machine id 与 runtime id 渲染设置视图。 */
import { use } from "react";
import { RuntimeSettingsPage } from "@multica/views/runtimes/runtime-settings-page";

/**
 * 运行时设置路由。
 *
 * @param props.params - 动态参数，含机器 id 与 runtime id（Promise 形式）。
 * @returns 运行时设置页。
 */
export default function RuntimeSettingsRoute({
  params,
}: {
  params: Promise<{ id: string; runtimeId: string }>;
}) {
  const { id, runtimeId } = use(params);
  return <RuntimeSettingsPage machineId={id} runtimeId={runtimeId} />;
}
