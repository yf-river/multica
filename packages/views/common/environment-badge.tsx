"use client";

import { useConfigStore } from "@multica/core/config";
import { cn } from "@multica/ui/lib/utils";

/** 登录前后都展示服务端提供的环境名，避免同名工作区被混淆。 */
export function EnvironmentBadge() {
  const name = useConfigStore((state) => state.environmentName);
  if (!name) return null;
  return (
    <span
      data-testid="environment-badge"
      className={cn(
        "inline-flex w-fit shrink-0 items-center rounded px-2 py-1 text-caption font-semibold",
        name === "生产" ? "bg-destructive/10 text-destructive" : "bg-primary/10 text-primary",
      )}
    >
      {name}环境
    </span>
  );
}
