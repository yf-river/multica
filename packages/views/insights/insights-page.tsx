"use client";

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { aggregateInsightSessions } from "@multica/core/insights/aggregate";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";
import { makePluginConfigCard } from "./native-dist/client/components/settingsCard";
import { makeContextView } from "./native-dist/client/components/contextView";
import { makeOverviewPanel } from "./native-dist/client/components/overviewPanel";
import { makeViewKit } from "./native-dist/client/viewkit";
import type { SettingsState } from "./native-dist/client/settings";
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
    scope: { id: "insights-preferences" },
    mutationFn: async (changes: Record<string, string>) => {
      const current = qc.getQueryData<Record<string, string>>(["insights-preferences"])
        ?? await api.getInsightPreferences();
      return api.saveInsightPreferences({ ...current, ...changes });
    },
    onSuccess: (value) => {
      qc.setQueryData(["insights-preferences"], value);
      return qc.invalidateQueries({ queryKey: ["insights-preferences"] });
    },
  });
  const [showPrefs, setShowPrefs] = useState(false);
  const preferencesRef = useRef<HTMLDivElement>(null);
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
        const params = new URLSearchParams(navigation.searchParams);
        params.set("insight", id);
        navigation.replace(`${navigation.pathname}?${params}${navigation.hash}`);
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
      Preferences: makePluginConfigCard(kit),
      useSettings<T>(selector: (state: SettingsState) => T): T {
        return selector(useSyncExternalStore(settings.store.subscribe, settings.store.getSnapshot, settings.store.getSnapshot));
      },
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
        await bridge.current.save({ [field]: String(value) });
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
      <Dialog open={showPrefs} onOpenChange={(open, details) => {
        // 原版菜单自行处理 Escape，外层偏好弹窗只关闭最上层。
        if (!open && details.reason === "escape-key" && (details.event.defaultPrevented || preferencesRef.current?.querySelector('[aria-haspopup="menu"][aria-expanded="true"]'))) {
          details.cancel();
          return;
        }
        setShowPrefs(open);
      }}>
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
          <div className="multica-insights" ref={preferencesRef}>
            <label className="lc-settings-row">
              <span className="lc-settings-label">{locale === "zh" ? "显示语言" : "Display language"}</span>
              <select value={prefs.data?.language ?? "auto"} onChange={(event) => updatePrefs.mutate({ language: event.target.value })}>
                <option value="auto">{locale === "zh" ? "跟随 Multica" : "Follow Multica"}</option>
                <option value="zh">{"简体中文"}</option>
                <option value="en">{"English"}</option>
              </select>
            </label>
            <host.Preferences useContextSettings={host.useSettings} set={host.settings.set} />
          </div>
        </DialogContent>
      </Dialog>
      {isContext ? (
        <div className="flex h-full min-h-0 flex-col">
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
