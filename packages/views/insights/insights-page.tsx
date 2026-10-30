"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { aggregateInsightSessions } from "@multica/core/insights/aggregate";
import {
  billedTokensOf,
  estimateSessionCost,
  formatCost,
} from "@multica/context-insights/cost";
import { useModelPrices } from "./native-dist/client/modelPrices";
import { fmt } from "./native-dist/client/format";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";
import { makeContextView } from "./native-dist/client/components/contextView";
import { makeOverviewPanel } from "./native-dist/client/components/overviewPanel";
import { makeViewKit } from "./native-dist/client/viewkit";
import { createContextSettings } from "./native-dist/client/settings";
import { makeAgentHeads } from "./native-dist/client/agentHeads";
import { setPageFace } from "./native-dist/client/historyPage";
import { DICT_EN, DICT_ZH } from "./native-dist/client/i18n";
import type { ClientCtx, LocaleService } from "./native-dist/client/services";
import { setInsightsTransport } from "./native-dist/transport";
import { insightsFetch } from "./transport";
import { createInsightAttachments } from "./attachments-adapter";
import "./insights.css";

export interface InsightsProps {
  workspaceId?: string;
  issueId?: string;
  chatSessionId?: string;
  taskId?: string;
  sessionKey?: string;
}

export function InsightsPage(props: InsightsProps) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  return mounted ? <NativeInsights {...props} /> : null;
}

setInsightsTransport(insightsFetch);

function NativeInsights({
  workspaceId = "",
  issueId,
  chatSessionId,
  taskId,
  sessionKey,
}: InsightsProps) {
  const navigation = useNavigation();
  const { i18n } = useT("usage");
  const prefs = useQuery({
    queryKey: ["insights-preferences"],
    queryFn: () => api.getInsightPreferences(),
  });
  const locale = prefs.data?.language === "en" ? "en" : prefs.data?.language === "zh" ? "zh" : i18n.language.startsWith("zh") ? "zh" : "en";
  const dict = locale === "zh" ? DICT_ZH : DICT_EN;
  const preferenceNames: Record<string, string> =
    locale === "zh"
      ? {
          language: "显示语言",
          defaultGranularity: "趋势粒度",
          defaultTrendMode: "趋势模式",
          defaultDeltaBase: "增量基准",
          defaultToolSort: "工具排序",
          defaultFileSort: "文件排序",
          defaultDurationCurve: "耗时曲线",
          overviewAutoSteps: "趋势自动概览阈值",
          defaultOverviewRange: "总览时间范围",
        }
      : {
          language: "Display language",
          defaultGranularity: "Trend granularity",
          defaultTrendMode: "Trend mode",
          defaultDeltaBase: "Delta base",
          defaultToolSort: "Tool sorting",
          defaultFileSort: "File sorting",
          defaultDurationCurve: "Duration curve",
          overviewAutoSteps: "Automatic trend overview",
          defaultOverviewRange: "Overview date range",
        };
  const preferenceValues: Record<string, string> =
    locale === "zh"
      ? {
          auto: "跟随 Multica",
          zh: "简体中文",
          en: "English",
          step: "步骤",
          turn: "轮次",
          total: "总量",
          delta: "增量",
          count: "次数",
          size: "大小",
          name: "名称",
          lines: "行数",
          latest: "最近",
          path: "路径",
          show: "显示",
          hide: "隐藏",
          off: "关闭",
          today: "今天",
          "7d": "近 7 天",
          "30d": "近 30 天",
          all: "全部",
        }
      : {auto: "Follow Multica", zh: "简体中文", en: "English"};
  const rangeParam = navigation.searchParams.get("range");
  const routeRange = ["today", "7d", "30d", "all"].includes(rangeParam ?? "")
    ? rangeParam
    : undefined;
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["insights", workspaceId],
    queryFn: () => api.listContextInsights(workspaceId),
    refetchInterval: 2000,
  });
  const updatePrefs = useMutation({
    mutationFn: (value: Record<string, string>) =>
      api.saveInsightPreferences(value),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ["insights-preferences"] }),
  });
  const [showPrefs, setShowPrefs] = useState(false);
  const all = useMemo(() => list.data?.sessions ?? [], [list.data]);
  const own = useMemo(
    () =>
      all.filter(
        (s) =>
          (!issueId || s.issue_id === issueId) &&
          (!chatSessionId || s.chat_session_id === chatSessionId),
      ),
    [all, issueId, chatSessionId],
  );
  const runs = useQuery({
    queryKey: ["insights", "runs", issueId, chatSessionId],
    queryFn: () => api.listContextInsightRuns(issueId, chatSessionId),
    enabled: Boolean(issueId || chatSessionId),
    refetchInterval: 5000,
  });
  const [picked, setPicked] = useState(sessionKey ?? "");
  useEffect(() => setPicked(sessionKey ?? ""), [sessionKey]);
  const selected =
    own.find((s) => s.id === picked) ??
    (taskId ? own.find((s) => s.run_ids?.includes(taskId)) : undefined) ??
    own.find((s) => s.parent_session_id === "") ??
    own[0];
  const isContext = Boolean(issueId || chatSessionId || sessionKey);
  const detail = useQuery({
    queryKey: ["insights", "detail", selected?.id],
    queryFn: () => api.getContextInsight(selected!.id),
    enabled: isContext && Boolean(selected),
    refetchInterval: 2000,
  });
  const currentSession =
    detail.data?.session.id === selected?.id ? detail.data?.session : selected;
  const snapshot = useMemo(() => {
    const byId: Record<string, unknown> = {};
    const nativeToKey = new Map(all.map((s) => [`${s.daemon_id}\0${s.session_id}`, s.id]));
    for (const s of all)
      byId[s.id] = {
        id: s.id,
        cwd: s.cwd,
        title: s.title,
        displayTitle: `${s.chat_session_id ? (locale === "zh" ? "聊天" : "Chat") : locale === "zh" ? "任务" : "Task"} · ${s.title}`,
        updatedAt: new Date(s.updated_at).getTime(),
        projectionValues: s.projections,
        ...(s.parent_session_id
          ? {
              origin: "subagent",
              parentId: nativeToKey.get(`${s.daemon_id}\0${s.parent_session_id}`),
            }
          : {}),
      };
    return { ids: all.map((s) => s.id), byId, current: selected?.id };
  }, [all, selected?.id, locale]);
  const grouped = useMemo(() => {
    const groups = new Map<string, typeof all>();
    for (const session of all) {
      const id = session.chat_session_id
        ? `chat:${session.chat_session_id}`
        : `issue:${session.issue_id}`;
      groups.set(id, [...(groups.get(id) ?? []), session]);
    }
    const byId: Record<string, unknown> = {};
    const representatives = new Map<string, string>();
    const byWorkspace = new Map<
      string,
      { title: string; sessionIds: string[]; archivedSessionIds: string[] }
    >();
    for (const [id, sessions] of groups) {
      const value = aggregateInsightSessions(sessions);
      if (!value) continue;
      const s = value.latest;
      representatives.set(id, s.id);
      byId[id] = {
        id,
        title: s.title,
        displayTitle: `${s.chat_session_id ? (locale === "zh" ? "聊天" : "Chat") : locale === "zh" ? "任务" : "Task"} · ${s.title}`,
        updatedAt: new Date(s.updated_at).getTime(),
        projectionValues: {
          contextTimeline: value.timeline,
          contextActivity: value.activity,
        },
      };
      let ws = byWorkspace.get(s.workspace_id);
      if (!ws) {
        ws = {
          title: s.workspace_name,
          sessionIds: [],
          archivedSessionIds: [],
        };
        byWorkspace.set(s.workspace_id, ws);
      }
      ws.sessionIds.push(id);
      if (s.archived) ws.archivedSessionIds.push(id);
    }
    return {
      sessions: { ids: Object.keys(byId), byId },
      workspaces: { items: [...byWorkspace.values()] },
      representatives,
    };
  }, [all, locale]);
  const totals = useMemo(() => aggregateInsightSessions(own), [own]);
  const prices = useModelPrices();
  const totalCost = totals
    ? estimateSessionCost(
        totals.timeline.cost,
        prices.book,
        locale === "zh" ? "cny" : "usd",
      )
    : null;
  const snapshotRef = useRef(snapshot);
  snapshotRef.current = snapshot;
  const listeners = useRef(new Set<() => void>());
  useEffect(() => {
    for (const notify of listeners.current) notify();
  }, [snapshot]);

  const openSession = useCallback(
    (id: string) => {
      const key = grouped.representatives.get(id) ?? id;
      const s = all.find((row) => row.id === key);
      if (!s) return;
      if (isContext && own.some((row) => row.id === id)) {
        setPicked(id);
        return;
      }
      const base = `/${encodeURIComponent(s.workspace_slug)}`;
      navigation.push(
        s.chat_session_id
          ? `${base}/chat?session=${encodeURIComponent(s.chat_session_id)}&view=insights&insight=${key}`
          : `${base}/issues/${encodeURIComponent(s.issue_id)}?view=insights&insight=${key}`,
      );
    },
    [all, own, isContext, navigation, grouped.representatives],
  );
  const navigationRef = useRef(navigation);
  navigationRef.current = navigation;
  const bridge = useRef({
    openSession,
    refresh: () => list.refetch(),
    fields: {
      ...prefs.data,
      defaultPlacement: "tab",
      fleetTab: "hide",
      ...(routeRange ? { defaultOverviewRange: routeRange } : {}),
    },
    save: updatePrefs.mutateAsync,
  });
  bridge.current = {
    openSession,
    refresh: () => list.refetch(),
    fields: {
      ...prefs.data,
      defaultPlacement: "tab",
      fleetTab: "hide",
      ...(routeRange ? { defaultOverviewRange: routeRange } : {}),
    },
    save: updatePrefs.mutateAsync,
  };
  const preferenceListeners = useRef(new Set<() => void>());
  useEffect(() => {
    for (const listener of preferenceListeners.current) listener();
  }, [prefs.data, routeRange]);
  const attachments = useMemo(() => createInsightAttachments(), []);
  useEffect(() => () => attachments.dispose(), [attachments]);
  const host = useMemo(() => {
    const localeService: LocaleService = {
      register: () => () => {},
      bind: () => (key, params) => {
        let value = (dict as Record<string, string>)[key] ?? key;
        for (const [name, replacement] of Object.entries(params ?? {}))
          value = value.replaceAll(`{${name}}`, String(replacement));
        return value;
      },
      getLocale: () => ({ active: locale }),
    };
    const sessions = {
      list: {
        getSnapshot: () => snapshotRef.current,
        subscribe: (listener: () => void) => {
          listeners.current.add(listener);
          return () => {
            listeners.current.delete(listener);
          };
        },
      },
      refresh: () => bridge.current.refresh(),
      refreshSubagents: () => bridge.current.refresh(),
      subagentAddress: (id: string) => ({ kind: "session", sessionId: id }),
    };
    const services: Record<string, unknown> = {
      uiConversation: attachments,
      insightAttachments: attachments,
      locale: localeService,
      sessions,
      uiWorkspace: {
        openSession: (id: string) => bridge.current.openSession(id),
      },
      pluginNavigation: { openBundle: () => setShowPrefs(true) },
    };
    const ctx = {
      locale: localeService,
      get: (name: string) => services[name],
    } as unknown as ClientCtx;
    const settings = createContextSettings();
    const kit = makeViewKit(localeService.bind("dsh-context"), localeService);
    return {
      Context: makeContextView(ctx, kit, settings, makeAgentHeads()),
      Overview: makeOverviewPanel(ctx, kit, settings),
      settings,
    };
  }, [locale, dict, attachments]);
  useEffect(() => {
    return host.settings.attach({
      getSnapshot: () => ({
        status: "ready",
        value: bridge.current.fields,
        writable: true,
      }),
      subscribe: (listener) => {
        preferenceListeners.current.add(listener);
        return () => {
          preferenceListeners.current.delete(listener);
        };
      },
      set: async (field, value) => {
        if (field === "defaultOverviewRange") {
          const nav = navigationRef.current;
          const params = new URLSearchParams(nav.searchParams);
          params.set("range", String(value));
          nav.replace(`${nav.pathname}?${params}${nav.hash}`);
        }
        await bridge.current.save({
          ...bridge.current.fields,
          [field]: String(value),
        });
      },
    });
  }, [host]);
  useEffect(() => {
    setPageFace(async (request) => {
      const address = request.address;
      const id =
        address.kind === "session" ? address.sessionId : address.childSessionId;
      const page = await api.getContextInsightEvents(id, request.throughSeq);
      return { records: page.events.toReversed().map((event) => ({ event })) };
    });
  }, []);
  const useProjection = (key: string) => currentSession?.projections[key];
  const failure =
    list.error ??
    detail.error ??
    runs.error ??
    prefs.error ??
    updatePrefs.error;
  return (
    <section
      className="multica-insights h-full min-h-0 overflow-hidden"
      {...(typeof document !== "undefined" &&
      document.documentElement.classList.contains("dark")
        ? { "data-ds-dark-theme": "" }
        : {})}
    >
      {failure ? (
        <p role="alert" className="p-4 text-body text-destructive">
          {failure.message}
        </p>
      ) : null}
      <Dialog open={showPrefs} onOpenChange={setShowPrefs}>
        <DialogContent className="max-h-[85svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {locale === "zh" ? "洞察偏好" : "Insight preferences"}
            </DialogTitle>
            <DialogDescription>
              {locale === "zh"
                ? "图表与总览的默认显示偏好"
                : "Default display preferences for charts and overview"}
            </DialogDescription>
          </DialogHeader>
          {Object.entries({
            language: ["auto", "zh", "en"],
            defaultGranularity: ["step", "turn"],
            defaultTrendMode: ["total", "delta"],
            defaultDeltaBase: ["step", "turn"],
            defaultToolSort: ["count", "size", "name"],
            defaultFileSort: ["lines", "count", "latest", "path", "name"],
            defaultDurationCurve: ["show", "hide"],
            overviewAutoSteps: ["100", "200", "300", "off"],
            defaultOverviewRange: ["30d", "today", "7d", "all"],
          }).map(([field, options]) => (
            <label
              key={field}
              className="mb-3 flex flex-wrap items-center gap-3 text-body"
            >
              <span>{preferenceNames[field]}</span>
              <select
                aria-label={preferenceNames[field]}
                value={prefs.data?.[field] ?? options[0]}
                onChange={(e) =>
                  updatePrefs.mutate({ ...prefs.data, [field]: e.target.value })
                }
                className="rounded border bg-background p-2"
              >
                {options.map((option) => (
                  <option key={option} value={option}>
                    {preferenceValues[option] ?? option}
                  </option>
                ))}
              </select>
            </label>
          ))}
          <Button onClick={() => setShowPrefs(false)}>
            {locale === "zh" ? "确定" : "Done"}
          </Button>
        </DialogContent>
      </Dialog>
      {isContext ? (
        <div className="flex h-full min-h-0 flex-col">
          <div className="flex shrink-0 flex-wrap items-center gap-2 border-b p-2 text-caption">
            <select
              aria-label={locale === "zh" ? "DSH 会话" : "DSH session"}
              value={selected?.id ?? ""}
              onChange={(e) => {
                setPicked(e.target.value);
                const params = new URLSearchParams(navigation.searchParams);
                params.set("insight", e.target.value);
                navigation.replace(
                  `${navigation.pathname}?${params}${navigation.hash}`,
                );
              }}
              className="min-w-0 max-w-full rounded border bg-background p-2"
            >
              {own.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.session_id}
                  {s.parent_session_id ? " · child" : ""}
                </option>
              ))}
            </select>
            <Button variant="ghost" onClick={() => setShowPrefs(true)}>
              {locale === "zh" ? "偏好" : "Preferences"}
            </Button>
            {totals ? (
              <span className="ml-auto tabular-nums">
                {locale === "zh" ? "累计" : "Total"}{" "}
                {fmt(billedTokensOf(totals.timeline.cost))} ·{" "}
                {totalCost !== null
                  ? formatCost(totalCost, locale === "zh" ? "cny" : "usd")
                  : "—"}
              </span>
            ) : null}
            <select
              aria-label={locale === "zh" ? "单次运行" : "Run"}
              value={taskId ?? ""}
              onChange={(event) => {
                const params = new URLSearchParams(navigation.searchParams);
                params.delete("insight");
                setPicked("");
                if (event.target.value) params.set("run", event.target.value);
                else params.delete("run");
                navigation.replace(
                  `${navigation.pathname}?${params}${navigation.hash}`,
                );
              }}
              className="max-w-full rounded border bg-background p-2"
            >
              <option value="">
                {locale === "zh" ? "全部运行" : "All runs"}
              </option>
              {runs.data?.runs.map((run) => (
                <option key={run.id} value={run.id}>
                  {run.provider} · {new Date(run.started_at).toLocaleString()} ·{" "}
                  {run.status}
                </option>
              ))}
            </select>
          </div>
          {taskId && !own.some((s) => s.run_ids?.includes(taskId)) ? (
            <p className="p-4 text-body text-muted-foreground">
              {locale === "zh"
                ? "此运行没有已采集的 DSH 洞察。"
                : "This run has no collected DSH insights."}
            </p>
          ) : selected ? (
            <div className="min-h-0 flex-1">
              <host.Context
                key={selected.id}
                sessionId={selected.id}
                useProjection={useProjection}
              />
            </div>
          ) : (
            <p className="p-6 text-body text-muted-foreground">
              {list.isPending
                ? locale === "zh"
                  ? "正在加载…"
                  : "Loading…"
                : locale === "zh"
                  ? "暂无启用后采集的 DSH 洞察"
                  : "No DSH insights collected since activation"}
            </p>
          )}
        </div>
      ) : (
        <host.Overview
          useSessions={(selector: (value: unknown) => unknown) =>
            selector(grouped.sessions)
          }
          useWorkspaces={(selector: (value: unknown) => unknown) =>
            selector(grouped.workspaces)
          }
        />
      )}
    </section>
  );
}
