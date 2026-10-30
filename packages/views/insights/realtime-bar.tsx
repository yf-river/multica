"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useLocale } from "../i18n";
import { asRecord, timelineOf } from "./native-dist/client/services";
import { ActivityPill, UsagePill, type Translate } from "./official/stats";
import { ContextMeter } from "./official/context-meter";
import statsLocales from "./official/stats-locales.json";
import contextLocales from "./official/context-meter-locales.json";
import "./insights.css";
import "./official/footer.css";

function translate(dict: Record<string, string>): Translate {
  return (key, params) => {
    let value = dict[key] ?? key;
    for (const [name, replacement] of Object.entries(params ?? {})) {
      value = value.replaceAll(`{${name}}`, String(replacement));
    }
    return value;
  };
}
const usePerformanceUsage = <T,>(selector: (mode: string) => T) => selector("detailed");

export function InsightsRealtimeBar({ workspaceId, issueId, chatSessionId }: {
  workspaceId: string;
  issueId?: string;
  chatSessionId?: string;
}) {
  const locale = useLocale();
  const prefs = useQuery({ queryKey: ["insights-preferences"], queryFn: () => api.getInsightPreferences() });
  const language = prefs.data?.language === "zh" || (prefs.data?.language !== "en" && locale.startsWith("zh")) ? "zh" : "en";
  const query = useQuery({
    queryKey: ["insights", workspaceId],
    queryFn: () => api.listContextInsights(workspaceId),
    refetchInterval: 2000,
  });
  const root = query.data?.sessions.find((s) =>
    !s.parent_session_id && (!issueId || s.issue_id === issueId) && (!chatSessionId || s.chat_session_id === chatSessionId));
  if (!root) return null;
  const stats = asRecord(root.projections.multicaSessionStats);
  const usage = { uncachedInputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, outputTokens: 0 };
  const timeline = timelineOf(root.projections.contextTimeline);
  for (const models of Object.values(timeline?.cost ?? {}))
    for (const periods of Object.values(models))
      for (const bucket of Object.values(periods)) {
        if (!bucket) continue;
        usage.uncachedInputTokens += bucket.uncached;
        usage.cacheReadTokens += bucket.cacheRead;
        usage.cacheWriteTokens += bucket.cacheWrite;
        usage.outputTokens += bucket.output;
      }
  const useProjection = (key: string) => key === "sessionStats" ? stats : key === "tokenUsage" ? usage : root.projections[key];
  const t = translate(statsLocales[language]);
  return (
    <div className="multica-insights uV2eYG_root shrink-0">
      <div className="uV2eYG_dock" data-insights-footer="">
      {stats ? <ActivityPill key={`activity:${root.id}`} useProjection={useProjection} usePerformanceUsage={usePerformanceUsage} t={t} /> : null}
      <UsagePill key={`usage:${root.id}`} useProjection={useProjection} usePerformanceUsage={usePerformanceUsage} t={t} />
      <ContextMeter key={`context:${root.id}`} useProjection={useProjection} t={translate(contextLocales[language])} />
      </div>
    </div>
  );
}
