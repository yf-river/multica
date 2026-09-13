"use client";

import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { pluginInvocationsOptions } from "@multica/core/plugins";
import type { PluginHook, PluginInvocation } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useLocale, useT } from "../i18n";

/**
 * plugins 模块：插件 hook 的调用活动与失败提示。
 *
 * 解释插件 hook 为什么在失败——展示给安装它的管理员看。
 *
 * `event` hook 的失败是刻意静默的：没有什么被阻塞、没人在盯着调用，连续失败
 * 几次后熔断器干脆不再尝试。这些行为都正确，但没有本组件就完全不可见：插件
 * 显示「已启用」，实际却什么都没做。
 *
 * 当最近的调用全部成功时什么都不渲染。一个健康工作的插件不需要关于它的面板。
 */

/** 熔断器在窗口期内达到多少次失败就跳闸；见 plugin_hook.go。 */
const BREAKER_THRESHOLD = 5;

/** 汇总一段调用记录：总数、失败数、从最新端数的连续失败数、最近一次错误。 */
export function summarizeInvocations(invocations: readonly PluginInvocation[]) {
  const recent = invocations.slice(0, 20);
  const failures = recent.filter((invocation) => invocation.status !== "ok");
  return {
    total: recent.length,
    failures,
    // 从最新端往前数连续失败：先失败后恢复的 hook，和此刻正在失败的 hook，
    // 并不是同一个故事。
    consecutiveFailures: countLeadingFailures(recent),
    lastError: failures[0]?.error,
  };
}

/** 从开头起数连续的非 ok 记录个数（遇到 ok 即停）。 */
function countLeadingFailures(invocations: readonly PluginInvocation[]): number {
  let count = 0;
  for (const invocation of invocations) {
    if (invocation.status === "ok") break;
    count += 1;
  }
  return count;
}

/**
 * 展示某个安装的 hook 最近失败情况的小横幅；没有任何失败时返回 null。
 * 颜色只在真正熔断（投递已停止）时才变红。
 */
export function PluginHookActivity({ wsId, installationId }: { wsId: string; installationId: string }) {
  const { t } = useT("settings");
  const { data } = useQuery(pluginInvocationsOptions(wsId, installationId));

  const summary = summarizeInvocations(data ?? []);
  if (summary.failures.length === 0) return null;

  const tripped = summary.consecutiveFailures >= BREAKER_THRESHOLD;

  return (
    <div
      className={cn(
        "flex items-start gap-2 rounded-md border px-3 py-2 text-caption",
        // 只在投递真的停下来时才用红色。端点闪断两次的插件值得提一句，不必
        // 拉响警报——而且永远红色的横幅不会再有人看。
        tripped
          ? "border-destructive/40 bg-destructive/5 text-destructive"
          : "border-surface-border text-muted-foreground",
      )}
    >
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
      <div className="min-w-0 space-y-0.5">
        <div className="font-medium">
          {tripped
            ? t(($) => $.plugins.hook_circuit_open)
            : t(($) => $.plugins.hook_recent_failures, { count: summary.failures.length })}
        </div>
        {summary.lastError ? <div className="truncate">{summary.lastError}</div> : null}
      </div>
    </div>
  );
}

/** 展示带定时调度（schedule）的 hook 最近一次调用的时间与状态；无调度 hook 时返回 null。 */
export function PluginScheduleActivity({
  wsId,
  installationId,
  hooks,
}: {
  wsId: string;
  installationId: string;
  hooks: readonly PluginHook[];
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const { data } = useQuery(pluginInvocationsOptions(wsId, installationId));
  const scheduled = hooks.filter((hook) => hook.schedule !== undefined);
  if (scheduled.length === 0) return null;

  return (
    <div className="space-y-1.5">
      <div className="text-caption font-medium">{t(($) => $.plugins.schedule.activity_title)}</div>
      <ul className="space-y-1">
        {scheduled.map((hook) => {
          const last = (data ?? []).find((invocation) => (
            invocation.trigger === "schedule" && invocation.hook_key === hook.key
          ));
          return (
            <li key={hook.key} className="text-caption text-muted-foreground">
              <span className="font-medium text-foreground">{hook.name}</span>
              {last ? (
                <span>
                  {" · "}
                  {t(($) => $.plugins.schedule.activity_last, {
                    status: last.status,
                    planned: formatInvocationTime(last.planned_at, locale),
                    actual: formatInvocationTime(last.created_at, locale),
                    attempt: last.attempt,
                  })}
                </span>
              ) : (
                <span>{" · "}{t(($) => $.plugins.schedule.activity_empty)}</span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** 把时间字符串格式化成当前 locale 的可读时间；缺失或非法时返回 "—"。 */
function formatInvocationTime(value: string | undefined, locale: string): string {
  if (!value) return "—";
  const timestamp = Date.parse(value);
  if (Number.isNaN(timestamp)) return "—";
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(timestamp));
}
