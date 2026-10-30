"use client";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useLocale,useT } from "../i18n";
import { IconGauge, IconDatabase } from "./native-dist/client/primitives";
import { asRecord, timelineOf } from "./native-dist/client/services";
import { headlineOf } from "./native-dist/client/headline";
import {
  contextPressureOf,
  contextBreakdownOf,
} from "./native-dist/client/services";
import { formatCacheHitPercent } from "@multica/context-insights/official/cache-hit.js";

const count = (v: number) => {
  const scaled = (n: number) =>
    n >= 100 ? String(Math.round(n)) : String(Math.round(n * 10) / 10);
  return v < 1000
    ? String(v)
    : v < 1e6
      ? `${scaled(v / 1000)}K`
      : `${scaled(v / 1e6)}M`;
};
export function InsightsRealtimeBar({
  workspaceId,
  issueId,
  chatSessionId,
  onOpen,
}: {
  workspaceId: string;
  issueId?: string;
  chatSessionId?: string;
  onOpen: () => void;
}) {
  const {t}=useT("usage");
  const locale = useLocale();
  const prefs = useQuery({queryKey:["insights-preferences"],queryFn:()=>api.getInsightPreferences()});
  const zh = prefs.data?.language === "zh" || (prefs.data?.language !== "en" && locale.startsWith("zh"));
  const query = useQuery({
    queryKey: ["insights", workspaceId],
    queryFn: () => api.listContextInsights(workspaceId),
    refetchInterval: 2000,
  });
  const sessions = (query.data?.sessions ?? []).filter(
    (s) =>
      (!issueId || s.issue_id === issueId) &&
      (!chatSessionId || s.chat_session_id === chatSessionId),
  );
  const root = sessions.find((s) => !s.parent_session_id);
  if (!root) return null;
  const stats = asRecord(root.projections.multicaSessionStats);
  const timeline = timelineOf(root.projections.contextTimeline);
  if (!timeline) return null;
  const head = headlineOf(
    timeline,
    contextPressureOf(root.projections.contextPressure),
    contextBreakdownOf(root.projections.contextBreakdown),
  );
  let input = 0,
    read = 0,
    write = 0,
    output = 0;
  for (const s of [root]) {
    const cost = timelineOf(s.projections.contextTimeline)?.cost;
    for (const models of Object.values(cost ?? {}))
      for (const periods of Object.values(models))
        for (const bucket of Object.values(periods)) {
          if (bucket) {
            input += bucket.uncached;
            read += bucket.cacheRead;
            write += bucket.cacheWrite;
            output += bucket.output;
          }
        }
  }
  const hit = formatCacheHitPercent(read, input + read + write);
  const decodeMs = typeof stats?.decodeMs === "number" ? stats.decodeMs : 0;
  const decodeTokens =
    typeof stats?.decodeTokens === "number" ? stats.decodeTokens : 0;
  const tps = decodeMs > 0 ? decodeTokens / (decodeMs / 1000) : null;
  const speed =
    tps === null
      ? null
      : `${tps >= 10 ? Math.round(tps) : Math.round(tps * 10) / 10} tok/s`;
  const turns = typeof stats?.turns === "number" ? stats.turns : 0;
  const steps = typeof stats?.steps === "number" ? stats.steps : 0;
  const duration = (ms: number) => {
    const seconds = ms / 1000;
    if (seconds < 60) return `${Math.round(seconds * 10) / 10}s`;
    const whole = Math.round(seconds);
    return `${Math.floor(whole / 60)}m${whole % 60}s`;
  };
  const number = (key: string) => typeof stats?.[key] === "number" ? stats[key] as number : 0;
  const activityDetails = [
    zh ? `${turns} 轮 ${steps} 步` : `${turns} turns ${steps} steps`,
    `${zh ? "模型用时" : "Model time"}: ${duration(number("llmMs"))}`,
    `${zh ? "工具用时" : "Tool time"}: ${duration(number("toolMs"))}`,
    ...(number("ttftSteps") > 0 ? [`${zh ? "平均首 Token 延迟" : "Average time to first token"}: ${duration(number("ttftMs") / number("ttftSteps"))}`] : []),
    ...(speed ? [`${zh ? "输出速度" : "Output speed"}: ${speed}`] : []),
  ].join("\n");
  const usageDetails = [
    t($=>$.insights.token_total,{count:input+read+write+output}),
    `${zh ? "未缓存输入" : "Uncached input"}: ${input.toLocaleString()}`,
    `${zh ? "缓存读取" : "Cache read"}: ${read.toLocaleString()}`,
    `${zh ? "缓存写入" : "Cache write"}: ${write.toLocaleString()}`,
    `${zh ? "输出" : "Output"}: ${output.toLocaleString()}`,
  ].join("\n");

  return (
    <button
      type="button"
      onClick={onOpen}
      className="multica-insights flex shrink-0 flex-wrap items-center justify-end gap-x-0 gap-y-1 border-t px-3 py-1 text-caption tabular-nums text-muted-foreground hover:bg-muted"
      aria-label={zh ? "打开上下文洞察" : "Open Context Insights"}
    >
      <span
        className="inline-flex items-center gap-1.5 rounded-sm px-2 py-px"
        title={activityDetails}
      >
        <IconGauge size={14} />
        {zh ? `${turns} 轮 ${steps} 步` : `${turns} turns ${steps} steps`}
        {speed ? ` · ${speed}` : ""}
      </span>
      <span
        className="inline-flex items-center gap-1.5 rounded-sm px-2 py-px"
        title={usageDetails}
      >
        <IconDatabase size={14} />
        {count(input + read + write + output)}
        {hit !== null ? ` · ${zh ? "缓存命中" : "Cache hit"} ${hit}%` : ""}
      </span>
      <span
        className="inline-flex items-center gap-1.5 rounded-sm px-2 py-px"
        title={`${head.tokens} / ${head.window ?? "—"}`}
      >
        <svg width="16" height="16" viewBox="0 0 20 20" aria-hidden="true">
          <circle
            cx="10"
            cy="10"
            r="7"
            fill="none"
            stroke="currentColor"
            opacity=".2"
            strokeWidth="2"
          />
          <circle
            cx="10"
            cy="10"
            r="7"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeDasharray={`${(head.pct ?? 0) * 0.44} 44`}
            transform="rotate(-90 10 10)"
          />
        </svg>
        {head.pct === null ? "—" : `${head.pct}%`}
      </span>
    </button>
  );
}
