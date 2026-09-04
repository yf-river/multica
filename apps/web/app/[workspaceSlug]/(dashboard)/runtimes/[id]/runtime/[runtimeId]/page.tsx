"use client";

import { use } from "react";
import { RuntimeSettingsPage } from "@multica/views/runtimes/runtime-settings-page";

export default function RuntimeSettingsRoute({
  params,
}: {
  params: Promise<{ id: string; runtimeId: string }>;
}) {
  const { id, runtimeId } = use(params);
  return <RuntimeSettingsPage machineId={id} runtimeId={runtimeId} />;
}
