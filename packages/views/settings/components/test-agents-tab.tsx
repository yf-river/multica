"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Cpu } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core";
import {
  testAgentControlKey,
  testAgentControlOptions,
  type TestEnvironmentControl,
  type TestAgentPolicy,
} from "@multica/core/test-agents";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { toast } from "sonner";
import type { RuntimeModel } from "@multica/core/types";
import { ModelPicker } from "../../agents/components/inspector/model-picker";
import { PickerItem, PropertyPicker } from "../../issues/components/pickers";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import { useT } from "../../i18n";
import {
  SettingsTab,
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "./settings-layout";

type Runtime = NonNullable<
  TestEnvironmentControl["observed"]["runtimes"]
>[number];
type Model = NonNullable<Runtime["models"]>[number];

function CatalogPicker({
  label,
  value,
  options,
  disabled,
  runtimeIcon,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  disabled?: boolean;
  runtimeIcon?: boolean;
  onChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const selected = options.find((item) => item.value === value);
  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      align="start"
      width="w-[var(--anchor-width)] min-w-[16rem] max-w-md"
      searchable
      searchPlaceholder={label}
      onSearchChange={setSearch}
      triggerRender={
        <button
          type="button"
          aria-label={label}
          disabled={disabled}
          className="flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
        />
      }
      trigger={
        <>
          {runtimeIcon && value ? (
            <span aria-hidden="true">
              <ProviderLogo provider={value} className="h-4 w-4 shrink-0" />
            </span>
          ) : (
            <Cpu
              className="h-4 w-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
          )}
          <span className="min-w-0 flex-1 truncate">
            {selected?.label ?? (value || "—")}
          </span>
          <ChevronDown className="h-4 w-4 shrink-0" aria-hidden="true" />
        </>
      }
    >
      {options
        .filter((item) =>
          `${item.label} ${item.value}`
            .toLowerCase()
            .includes(search.toLowerCase()),
        )
        .map((item) => (
          <PickerItem
            key={item.value}
            selected={item.value === value}
            onClick={() => {
              onChange(item.value);
              setOpen(false);
            }}
          >
            {item.label}
          </PickerItem>
        ))}
    </PropertyPicker>
  );
}

function RuntimeDialog({
  initial,
  revision,
  runtimes,
  pending,
  onClose,
  onSave,
}: {
  initial: TestAgentPolicy;
  revision: number;
  runtimes: Runtime[];
  pending: boolean;
  onClose: () => void;
  onSave: (policy: TestAgentPolicy, revision: number) => void;
}) {
  const { t } = useT("settings");
  const [initialRevision] = useState(revision);
  const [provider, setProvider] = useState(initial.runtime || initial.provider);
  const [model, setModel] = useState(initial.model);
  const [thinking, setThinking] = useState(initial.thinking_level || "");
  const [tier, setTier] = useState(initial.service_tier || "");
  const options = Array.from(
    new Set(runtimes.map((runtime) => runtime.provider)),
  ).map((value) => ({ value, label: value }));
  const models = new Map<string, Model>();
  for (const runtime of runtimes)
    if (
      runtime.provider === provider &&
      runtime.catalog_observed_at &&
      Date.now() - Date.parse(runtime.catalog_observed_at) <= 300_000
    )
      for (const item of runtime.models ?? []) {
        if (!models.has(item.id)) models.set(item.id, item);
      }
  const selected = models.get(model);
  const levels = selected?.thinking?.supported_levels ?? [];
  const tiers = selected?.service_tiers ?? [];
  const standard = !!selected?.supports_explicit_standard_service_tier;
  const validThinking =
    !thinking || levels.some((item) => item.value === thinking);
  const validTier =
    !tier ||
    tiers.some((item) => item.id === tier) ||
    (tier === "default" && standard);
  const ready =
    options.some((item) => item.value === provider) &&
    !!selected &&
    validThinking &&
    validTier;
  const selectClass =
    "min-h-10 w-full rounded-lg border border-input bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.test_agents.runtime_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.test_agents.runtime_description)}
          </DialogDescription>
        </DialogHeader>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.test_agents.runtime)}
            size="select-wide"
          >
            <CatalogPicker
              label={t(($) => $.test_agents.runtime)}
              runtimeIcon
              value={provider}
              options={options}
              disabled={pending || !options.length}
              onChange={(value) => {
                setProvider(value);
                setModel("");
                setThinking("");
                setTier("");
              }}
            />
          </SettingsRow>
          <SettingsRow label={t(($) => $.test_agents.model)} size="select-wide">
            {/*
              与 Agent 设置运行时同一个模型选择器：可搜索、显示 provider/model 两行；
              目录来自控制行回写的真实目录，所以注入 catalog 而不发实时请求。
            */}
            <ModelPicker
              variant="field"
              showLabel={false}
              allowCustom={false}
              runtimeId={runtimes.find((item) => item.provider === provider)?.id ?? null}
              runtimeOnline={models.size > 0}
              value={model || null}
              canEdit={!pending && models.size > 0}
              catalog={{ models: Array.from(models.values()) as RuntimeModel[], supported: true }}
              ariaLabel={t(($) => $.test_agents.model)}
              onChange={(value: string) => {
                setModel(value);
                setThinking("");
                setTier("");
              }}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.test_agents.thinking)}
            size="select-wide"
          >
            <select
              aria-label={t(($) => $.test_agents.thinking)}
              className={selectClass}
              value={thinking}
              disabled={pending || (!levels.length && !thinking)}
              onChange={(event) => setThinking(event.target.value)}
            >
              <option value="">
                {levels.length
                  ? t(($) => $.test_agents.follow_runtime)
                  : t(($) => $.test_agents.unsupported)}
              </option>
              {thinking && !validThinking && (
                <option value={thinking}>
                  {thinking} · {t(($) => $.test_agents.unsupported)}
                </option>
              )}
              {levels.map((level) => (
                <option key={level.value} value={level.value}>
                  {level.label}
                </option>
              ))}
            </select>
          </SettingsRow>
          <SettingsRow label={t(($) => $.test_agents.speed)} size="select-wide">
            <select
              aria-label={t(($) => $.test_agents.speed)}
              className={selectClass}
              value={tier}
              disabled={pending || (!tiers.length && !standard && !tier)}
              onChange={(event) => setTier(event.target.value)}
            >
              <option value="">
                {tiers.length || standard
                  ? t(($) => $.test_agents.follow_runtime)
                  : t(($) => $.test_agents.unsupported)}
              </option>
              {tier && !validTier && (
                <option value={tier}>
                  {tier} · {t(($) => $.test_agents.unsupported)}
                </option>
              )}
              {standard && (
                <option value="default">
                  {t(($) => $.test_agents.standard)}
                </option>
              )}
              {tiers
                .filter((item) => !standard || item.id !== "default")
                .map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
            </select>
          </SettingsRow>
        </SettingsCard>
        {!models.size && (
          <p role="alert" className="text-caption text-destructive">
            {t(($) => $.test_agents.catalog_unavailable)}
          </p>
        )}
        {!!models.size && !ready && (
          <p role="alert" className="text-caption text-destructive">
            {t(($) => $.test_agents.choose_available)}
          </p>
        )}
        <p className="text-caption text-muted-foreground">
          {t(($) => $.test_agents.runtime_hint)}
        </p>
        <DialogFooter>
          <Button variant="outline" disabled={pending} onClick={onClose}>
            {t(($) => $.test_agents.cancel)}
          </Button>
          <Button
            disabled={pending || !ready}
            onClick={() =>
              onSave(
                {
                  provider,
                  runtime: provider,
                  model,
                  thinking_level: thinking,
                  service_tier: tier,
                },
                initialRevision,
              )
            }
          >
            {t(($) => $.test_agents.apply_all)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function TestAgentsTab() {
  const { t } = useT("settings");
  const ws = useWorkspaceId();
  const query = useQuery(testAgentControlOptions(ws));
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const mutation = useMutation({
    mutationFn: (body: Parameters<typeof api.configureTestAgents>[0]) =>
      api.configureTestAgents(body),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: testAgentControlKey(ws) });
      setOpen(false);
      toast.success(t(($) => $.test_agents.saved));
    },
    onError: (error: Error) => {
      if ("status" in error && error.status === 409) {
        setOpen(false);
        void client.invalidateQueries({ queryKey: testAgentControlKey(ws) });
      }
      toast.error(error.message || t(($) => $.test_agents.save_error));
    },
  });
  const rows = query.data ?? [];
  const defaultRow = rows.find((row) => row.name === "default");
  const runtimes = rows
    .filter(
      (row) =>
        row.name !== "default" &&
        row.observed.status !== "error" &&
        row.observed.available !== false &&
        row.observed_at &&
        Date.now() - Date.parse(row.observed_at) <= 180_000,
    )
    .flatMap((row) => row.observed.runtimes ?? []);
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
              <div className="flex flex-wrap items-center justify-between gap-3 p-4">
                <p className="min-w-0 break-words text-body">
                  {defaultRow?.desired.policy.runtime ||
                    defaultRow?.desired.policy.provider ||
                    "—"}{" "}
                  · {defaultRow?.desired.policy.model || "—"}
                </p>
                <Button onClick={() => setOpen(true)}>
                  {t(($) => $.test_agents.runtime_title)}
                </Button>
              </div>
            </SettingsCard>
          </SettingsSection>
          {!rows.some((row) => row.name !== "default") && (
            <p className="text-muted-foreground">
              {t(($) => $.test_agents.empty)}
            </p>
          )}
          {rows
            .filter((row) => row.name !== "default")
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
                      <p className="break-words text-body">
                        {row.desired.policy.runtime ||
                          row.desired.policy.provider}{" "}
                        · {row.desired.policy.model}
                      </p>
                      {row.observed.error && (
                        <p
                          role="status"
                          className="text-caption text-destructive"
                        >
                          {row.observed.error}
                        </p>
                      )}
                      {row.observed_at && (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.test_agents.updated)} ·{" "}
                          {new Date(row.observed_at).toLocaleString()}
                        </p>
                      )}
                    </div>
                    <details>
                      <summary className="cursor-pointer px-4 py-3 text-body font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                        {t(($) => $.test_agents.view_agents)}
                      </summary>
                      <div className="divide-y">
                        {agents.map((agent) => (
                          <div key={agent.id} className="p-4">
                            <p className="break-words text-body font-medium">
                              {agent.name}{" "}
                              {agent.archived_at && (
                                <span className="text-caption text-muted-foreground">
                                  · {t(($) => $.test_agents.archived)}
                                </span>
                              )}
                            </p>
                            <p className="break-all text-caption text-muted-foreground">
                              {agent.model ?? "—"}
                              {row.desired.overrides[agent.id]
                                ? ` · ${t(($) => $.test_agents.individual)}`
                                : ""}
                            </p>
                          </div>
                        ))}
                        {!agents.length && (
                          <p className="p-4 text-caption text-muted-foreground">
                            {t(($) => $.test_agents.no_agents)}
                          </p>
                        )}
                      </div>
                    </details>
                  </SettingsCard>
                </SettingsSection>
              );
            })}
          {open && (
            <RuntimeDialog
              initial={
                defaultRow?.desired.policy ?? {
                  provider: "",
                  model: "",
                  thinking_level: "",
                  service_tier: "",
                }
              }
              revision={defaultRow?.revision ?? 1}
              runtimes={runtimes}
              pending={mutation.isPending}
              onClose={() => setOpen(false)}
              onSave={(policy, revision) =>
                mutation.mutate({
                  action: "configure",
                  name: "default",
                  revision,
                  policy,
                })
              }
            />
          )}
        </>
      )}
    </SettingsTab>
  );
}
