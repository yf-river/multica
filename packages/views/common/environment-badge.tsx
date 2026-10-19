"use client";

import { useContext } from "react";
import { BuildInfoContext, useConfigStore } from "@multica/core/config";
import { cn } from "@multica/ui/lib/utils";

/** 登录前后都展示服务端提供的环境名，避免同名工作区被混淆。 */
export function EnvironmentBadge() {
  const name = useConfigStore((state) => state.environmentName);
  const { build, updateAvailable, reload } = useContext(BuildInfoContext);
  if (!name) return null;
  const date = build ? new Date(build.builtAt) : null;
  const parts = date ? new Intl.DateTimeFormat("en-GB", {
    timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(date) : [];
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find(item => item.type === type)?.value;
  const stamp = `${part("month")}.${part("day")}-${part("hour")}:${part("minute")}`;
  const detail = build ? `${part("year")}-${part("month")}-${part("day")} ${part("hour")}:${part("minute")} UTC+8 · ${build.commit.slice(0, 12)} · 运行产物构建完成时间` : undefined;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
    <span
      data-testid="environment-badge"
      className={cn(
        "inline-flex w-fit shrink-0 items-center rounded px-2 py-1 text-caption font-semibold",
        name === "生产" ? "bg-destructive/10 text-destructive" : "bg-primary/10 text-primary",
      )}
    >
      {name}环境
    </span>
    {build && <span title={detail} tabIndex={0} aria-label={`构建 ${detail}`}
      className="rounded bg-muted px-2 py-1 text-caption text-muted-foreground tabular-nums">
      构建 {stamp}
    </span>}
    {updateAvailable && <button type="button" onClick={reload}
      className="rounded px-2 py-1 text-caption text-primary underline underline-offset-2 focus-visible:outline focus-visible:outline-2">
      新版本已上线，刷新更新
    </button>}
    </div>
  );
}
