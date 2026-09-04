"use client";

import { use } from "react";
import { RuntimeDetailPage } from "@multica/views/runtimes/runtime-detail-page";

export default function RuntimeDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <RuntimeDetailPage runtimeId={id} />;
}
