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
import {
  TestAgentRuntimeDialog,
  type TestAgentRuntimeOption,
} from "./test-agent-runtime-dialog";
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
  runtimes: TestAgentRuntimeOption[];
  disabled: boolean;
  label: string;
  onSave: (p: TestAgentPolicy) => void;
  button: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="min-w-0 break-all text-caption text-muted-foreground">
        {initial.runtime ?? initial.provider} · {initial.model}
        {initial.thinking_level ? ` · ${initial.thinking_level}` : ""}
      </p>
      <Button size="sm" variant="outline" disabled={disabled} onClick={() => setOpen(true)}>
        {button}
      </Button>
      {open ? (
        <TestAgentRuntimeDialog
          title={label}
          description={label}
          environment={label}
          initial={initial}
          runtimes={runtimes}
          disabled={disabled}
          applyLabel={button}
          onClose={() => setOpen(false)}
          onSave={(policy) => {
            setOpen(false);
            onSave(policy);
          }}
        />
      ) : null}
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
