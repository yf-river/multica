"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core";
import {
  testAgentControlKey,
  testAgentControlOptions,
  testAgentPolicies,
  type TestEnvironmentControl,
  type TestAgentPolicy,
} from "@multica/core/test-agents";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { SettingsTab, SettingsCard, SettingsSection } from "./settings-layout";

function PolicyEditor({
  initial,
  runtimes,
  disabled,
  label,
  onSave,
  button,
}: {
  initial: TestAgentPolicy;
  runtimes: { id: string; provider: string }[];
  disabled: boolean;
  label: string;
  onSave: (p: TestAgentPolicy) => void;
  button: string;
}) {
  // 目录缺失（daemon 未上报 runtimes）时退回内置预设，选择器不能被禁用成"点不动"。
  const options = runtimes.length
    ? runtimes.map((r) => ({ value: r.id, label: r.provider, provider: r.provider }))
    : Object.entries(testAgentPolicies).map(([key, preset]) => ({
        value: key,
        label: key,
        provider: preset.provider,
      }));
  const initialValue =
    options.find(
      (o) => o.provider === (initial.runtime ?? initial.provider),
    )?.value ?? options[0]?.value ?? "";
  const [value, setValue] = useState(initialValue);
  const selected = options.find((o) => o.value === value) ?? options[0];
  const presets: Record<string, TestAgentPolicy> = testAgentPolicies;
  const preset = selected ? presets[selected.provider] : undefined;
  const live = runtimes.find((r) => r.id === value);
  const models = useQuery({
    queryKey: ["runtime-models", live?.id ?? ""],
    queryFn: () => api.initiateListModels(live!.id),
    enabled: Boolean(live?.id),
    staleTime: 60_000,
    retry: false,
  });
  const catalog = models.data?.models ?? [];
  const [model, setModel] = useState(initial.model);
  const [thinking, setThinking] = useState(initial.thinking_level ?? '');
  const choices = catalog.length
    ? catalog.map((m) => ({ id: m.id, label: m.label }))
    : preset
      ? [{ id: preset.model, label: preset.model }]
      : [];
  const current =
    model && choices.some((c) => c.id === model)
      ? model
      : (choices[0]?.id ?? model);
  // 思考强度与 Agent 审查页一致：优先用模型目录上报的档位，其次按运行时给内置档位。
  const catalogModel = catalog.find((m) => m.id === current);
  const thinkingLevels: { value: string; label: string }[] =
    catalogModel?.thinking?.supported_levels?.length
      ? catalogModel.thinking.supported_levels
      : selected?.provider === 'codex'
        ? [
            { value: 'low', label: 'low' },
            { value: 'medium', label: 'medium' },
            { value: 'high', label: 'high' },
          ]
        : [];
  const thinkingValue =
    thinking && thinkingLevels.some((l) => l.value === thinking)
      ? thinking
      : (thinkingLevels[0]?.value ?? '');
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label className="sr-only" htmlFor={`${label}-runtime`}>
        {label}
      </label>
      <select
        id={`${label}-runtime`}
        aria-label={`${label} runtime`}
        value={selected?.value ?? ""}
        disabled={disabled || options.length === 0}
        onChange={(e) => {
          setValue(e.target.value);
          setModel("");
        }}
        className="min-h-9 min-w-0 max-w-full flex-1 rounded-md border border-input bg-background text-foreground px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-none"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <label className="sr-only" htmlFor={`${label}-model`}>
        {label}
      </label>
      <select
        id={`${label}-model`}
        aria-label={`${label} model`}
        value={current ?? ""}
        disabled={disabled || choices.length === 0}
        onChange={(e) => setModel(e.target.value)}
        className="min-h-9 min-w-0 max-w-full flex-1 rounded-md border border-input bg-background text-foreground px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-none"
      >
        {choices.map((c) => (
          <option key={c.id} value={c.id}>
            {c.label}
          </option>
        ))}
      </select>
      {thinkingLevels.length > 0 ? (
        <>
          <label className="sr-only" htmlFor={`${label}-thinking`}>
            {label}
          </label>
          <select
            id={`${label}-thinking`}
            aria-label={`${label} thinking`}
            value={thinkingValue}
            disabled={disabled}
            onChange={(e) => setThinking(e.target.value)}
            className="min-h-9 min-w-0 max-w-full flex-1 rounded-md border border-input bg-background text-foreground px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:flex-none"
          >
            {thinkingLevels.map((l) => (
              <option key={l.value} value={l.value}>
                {l.label}
              </option>
            ))}
          </select>
        </>
      ) : null}
      <Button
        size="sm"
        disabled={disabled || !selected || !current}
        onClick={() =>
          onSave({
            provider: selected?.provider ?? initial.provider,
            runtime: selected?.provider ?? initial.runtime,
            model: current ?? "",
            thinking_level: thinkingLevels.length > 0 ? thinkingValue : "",
            service_tier: preset?.service_tier ?? "",
          })
        }
      >
        {button}
      </Button>
    </div>
  );
}

export function TestAgentsTab() {
  const { t } = useT("settings");
  const ws = useWorkspaceId();
  const query = useQuery(testAgentControlOptions(ws));
  const client = useQueryClient();
  const mutation = useMutation({
    mutationFn: (body: Parameters<typeof api.configureTestAgents>[0]) =>
      api.configureTestAgents(body),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: testAgentControlKey(ws) });
      toast.success(t(($) => $.test_agents.saved));
    },
    onError: (error: Error) =>
      toast.error(error.message || t(($) => $.test_agents.save_error)),
  });
  const rows = query.data ?? [];
  const defaultRow = rows.find((x) => x.name === "default");
  function save(
    row: TestEnvironmentControl | undefined,
    policy: TestAgentPolicy,
    agent?: string,
  ) {
    mutation.mutate({
      action: "configure",
      name: row?.name ?? "default",
      revision: row?.revision ?? 1,
      policy,
      ...(agent ? { agent_id: agent } : {}),
    });
  }
  return (
    <SettingsTab
      title={t(($) => $.page.tabs.test_agents)}
      description={t(($) => $.test_agents.description)}
    >
      <div className="flex justify-end">
        <Button
          variant="outline"
          size="sm"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t(($) => $.test_agents.refresh)}
        </Button>
      </div>
      {query.isPending ? (
        <p role="status">{t(($) => $.test_agents.loading)}</p>
      ) : query.isError ? (
        <p role="alert" className="text-destructive">
          {t(($) => $.test_agents.load_error)}
        </p>
      ) : (
        <>
          <SettingsSection
            title={t(($) => $.test_agents.default_title)}
            description={t(($) => $.test_agents.default_description)}
          >
            <SettingsCard>
              <div className="p-4">
                <PolicyEditor
                  key={defaultRow?.revision ?? 0}
                  initial={
                    defaultRow?.desired.policy ?? testAgentPolicies.codebuddy
                  }
                  runtimes={defaultRow?.observed.runtimes ?? []}
                  disabled={mutation.isPending}
                  label={t(($) => $.test_agents.default_title)}
                  onSave={(p) => save(defaultRow, p)}
                  button={t(($) => $.test_agents.apply_all)}
                />
                <p className="mt-2 text-caption text-muted-foreground">
                  {t(($) => $.test_agents.codex_details)}
                </p>
              </div>
            </SettingsCard>
          </SettingsSection>
          {!rows.some((r) => r.name !== "default") ? (
            <p className="text-muted-foreground">
              {t(($) => $.test_agents.empty)}
            </p>
          ) : null}
          {rows
            .filter((r) => r.name !== "default")
            .map((row) => {
              const stale =
                !row.observed_at ||
                Date.now() - Date.parse(row.observed_at) > 180_000;
              const status = stale
                ? "stale"
                : row.observed.revision !== row.revision
                  ? "pending"
                  : (row.observed.status ?? "pending");
              const agents = row.observed.agents ?? [];
              return (
                <SettingsSection
                  key={row.name}
                  title={
                    <span className="inline-flex flex-wrap items-center gap-2">
                      {row.name}
                      <Badge variant="secondary">
                        {status === "applied"
                          ? t(($) => $.test_agents.applied)
                          : status === "error"
                            ? t(($) => $.test_agents.error)
                            : status === "stale"
                              ? t(($) => $.test_agents.stale)
                              : t(($) => $.test_agents.pending)}
                      </Badge>
                      <span className="text-caption font-normal text-muted-foreground">
                        {agents.length} {t(($) => $.test_agents.agents)}
                      </span>
                    </span>
                  }
                >
                  <SettingsCard>
                    <div className="space-y-2 p-4">
                      <PolicyEditor
                        key={row.revision}
                        initial={row.desired.policy}
                        runtimes={row.observed.runtimes ?? []}
                        disabled={mutation.isPending || stale}
                        label={row.name}
                        onSave={(p) => save(row, p)}
                        button={t(($) => $.test_agents.apply)}
                      />
                      {row.observed.error ? (
                        <p
                          role="status"
                          className="text-caption text-destructive"
                        >
                          {row.observed.error}
                        </p>
                      ) : null}
                      {row.observed_at ? (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.test_agents.updated)} ·{" "}
                          {new Date(row.observed_at).toLocaleString()}
                        </p>
                      ) : null}
                    </div>
                    <details className="group">
                      <summary className="cursor-pointer px-4 py-3 text-body font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                        {t(($) => $.test_agents.manage_agents)}
                      </summary>
                      <div className="divide-y">
                        {agents.map((agent) => (
                          <div
                            key={agent.id}
                            className="flex flex-wrap items-center justify-between gap-3 p-4"
                          >
                            <div className="min-w-0">
                              <p className="break-words text-body font-medium">
                                {agent.name}{" "}
                                {agent.archived_at ? (
                                  <span className="text-caption text-muted-foreground">
                                    · {t(($) => $.test_agents.archived)}
                                  </span>
                                ) : null}
                              </p>
                              <p className="break-all text-caption text-muted-foreground">
                                {agent.model ?? "—"}
                                {row.desired.overrides[agent.id]
                                  ? ` · ${t(($) => $.test_agents.individual)}`
                                  : ""}
                              </p>
                            </div>
                            <PolicyEditor
                              key={`${agent.id}-${row.revision}`}
                              initial={
                                row.desired.overrides[agent.id] ??
                                row.desired.policy
                              }
                              runtimes={row.observed.runtimes ?? []}
                              disabled={mutation.isPending || stale}
                              label={`${row.name}-${agent.id}`}
                              onSave={(p) => save(row, p, agent.id)}
                              button={t(($) => $.test_agents.apply)}
                            />
                          </div>
                        ))}
                        {!agents.length ? (
                          <p className="p-4 text-caption text-muted-foreground">
                            {t(($) => $.test_agents.no_agents)}
                          </p>
                        ) : null}
                      </div>
                    </details>
                  </SettingsCard>
                </SettingsSection>
              );
            })}
        </>
      )}
    </SettingsTab>
  );
}
