"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/paths";
import { supervisionKeys } from "@multica/core/supervision/queries";
import type { SupervisionConfig } from "@multica/core/supervision/schema";
import { isRuntimeUsableForUser, runtimeListOptions, runtimeModelsOptions } from "@multica/core/runtimes";
import { memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { useT } from "../../../i18n";
import { RuntimePicker } from "../../../agents/components/inspector/runtime-picker";
import { ModelPicker } from "../../../agents/components/inspector/model-picker";
import { findModelCapabilityEntry } from "../../../agents/components/inspector/model-capability";
import { errorText, moment } from "./supervision-shared";

type Props = { open: boolean; close: () => void; config: SupervisionConfig };

/** 监督设置：运行时、型号、提示词、周期。监督员由系统托管，不再选择 Agent。 */
export function SupervisionSettings({ open, close, config }: Props) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const currentUser = useAuthStore((state) => state.user);
  const [runtimeId, setRuntimeId] = useState(config.runtime_id ?? "");
  const [model, setModel] = useState(config.model ?? "");
  const [thinking, setThinking] = useState(config.thinking_level ?? "");
  // null 表示跟随内置默认；只有用户实际编辑后才落成字符串。
  const [instructions, setInstructions] = useState<string | null>(config.instructions_customized && config.instructions ? config.instructions : null);
  const [interval, setInterval] = useState<number | null>(null);
  const [investigation, setInvestigation] = useState<boolean | null>(null);

  const runtimes = useQuery({ ...runtimeListOptions(wsId), enabled: open });
  const members = useQuery({ ...memberListOptions(wsId), enabled: open });
  const defaults = useQuery({
    queryKey: [...supervisionKeys.all(wsId), "instructions"],
    queryFn: () => api.getSupervisionInstructions(),
    enabled: open,
    staleTime: Infinity,
  });
  const supported = (runtimes.data ?? []).filter((runtime) => runtime.provider === "codex" || runtime.provider === "codebuddy");
  const runtime = supported.find((item) => item.id === runtimeId);
  const online = runtime?.status === "online" && isRuntimeUsableForUser(runtime, currentUser?.id ?? null);
  const catalog = useQuery(runtimeModelsOptions(online ? runtimeId : null));
  const models = catalog.data?.models ?? [];
  const entry = model ? findModelCapabilityEntry(models, model, runtime?.provider ?? "") : undefined;
  const levels = entry?.thinking?.supported_levels ?? [];
  const defaultText = defaults.data?.instructions ?? "";
  const effectiveText = instructions ?? defaultText;
  const customized = instructions !== null && instructions.trim() !== defaultText.trim();

  const save = useMutation({
    mutationFn: (enabled: boolean) => api.updateSupervision({
      enabled,
      interval_seconds: interval ?? config.interval_seconds,
      investigation_enabled: investigation ?? config.investigation_enabled,
      runtime_id: runtimeId,
      model,
      thinking_level: thinking,
      // null 继续跟随默认；保存时显式传空串，避免把当前默认文本冻结成自定义。
      instructions: instructions ?? "",
    }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) });
      close();
    },
    onError: (error) => toast.error(errorText(error)),
  });
  const ready = Boolean(runtimeId) && defaults.isSuccess && !save.isPending;

  return (
    <Sheet open={open} onOpenChange={(value) => { if (!value) close(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-lg">
        <SheetHeader>
          <SheetTitle>{t(($) => $.supervision.settings)}</SheetTitle>
          <SheetDescription>{t(($) => $.supervision.settings_description)}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          <div className="rounded-lg border bg-muted/30 p-3 text-body">
            <p className="font-medium">{config.error ? t(($) => $.supervision.interrupted) : config.enabled ? t(($) => $.supervision.active) : t(($) => $.supervision.paused)}</p>
            <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.last_success)} {moment(config.last_success_at)}</p>
          </div>

          <div className="space-y-2 text-body">
            <span>{t(($) => $.supervision.runtime)}</span>
            <RuntimePicker
              variant="field"
              showLabel={false}
              value={runtimeId}
              runtimes={supported}
              members={members.data ?? []}
              currentUserId={currentUser?.id ?? null}
              canEdit={!save.isPending}
              onChange={(id) => { setRuntimeId(id); setModel(""); setThinking(""); }}
            />
            <span className="block text-caption text-muted-foreground">{t(($) => $.supervision.runtime_hint)}</span>
            {runtimes.isError && <p role="alert">{errorText(runtimes.error)}</p>}
          </div>

          <div className="space-y-2 text-body">
            <span>{t(($) => $.supervision.model)}</span>
            <ModelPicker
              allowCustom={false}
              variant="field"
              showLabel={false}
              runtimeId={runtimeId || null}
              runtimeOnline={Boolean(online)}
              value={model || null}
              canEdit={Boolean(online) && catalog.isSuccess}
              onChange={(value) => { setModel(value); setThinking(""); }}
            />
            {runtimeId && !online && <span className="block text-caption text-muted-foreground">{t(($) => $.supervision.model_offline)}</span>}
          </div>

          <label className="block space-y-2 text-body" htmlFor="supervision-thinking">
            <span>{t(($) => $.supervision.thinking)}</span>
            <select
              id="supervision-thinking"
              className="h-10 w-full rounded-md border bg-background px-3"
              value={thinking}
              disabled={!levels.length || save.isPending}
              onChange={(event) => setThinking(event.target.value)}
            >
              <option value="">{t(($) => $.supervision.thinking_default)}</option>
              {levels.map((level) => <option key={level.value} value={level.value}>{level.label}</option>)}
              {thinking && !levels.some((level) => level.value === thinking) && <option value={thinking}>{thinking}</option>}
            </select>
          </label>

          <div className="space-y-2 text-body">
            <div className="flex items-center justify-between gap-2">
              <span>{t(($) => $.supervision.instructions)}</span>
              <Button variant="ghost" size="sm" disabled={instructions === null || defaults.isFetching} onClick={() => setInstructions(null)}>
                {t(($) => $.supervision.restore_default)}
              </Button>
            </div>
            <textarea
              aria-label={t(($) => $.supervision.instructions)}
              className="min-h-64 w-full rounded-md border bg-background p-3 font-mono text-caption leading-relaxed"
              value={effectiveText}
              disabled={!defaults.isSuccess || save.isPending}
              onChange={(event) => setInstructions(event.target.value)}
            />
            <p className="text-caption text-muted-foreground">
              {customized ? t(($) => $.supervision.instructions_customized) : t(($) => $.supervision.instructions_default)}
              {" · "}
              {t(($) => $.supervision.instructions_count, { count: effectiveText.length })}
            </p>
          </div>

          <div className="text-body">
            <p className="font-medium">{t(($) => $.supervision.scope)}</p>
            <p className="mt-1 text-muted-foreground">{t(($) => $.supervision.scope_description)}</p>
          </div>
          <label className="block space-y-2 text-body" htmlFor="supervision-interval">
            <span>{t(($) => $.supervision.interval)}</span>
            <select
              id="supervision-interval"
              className="h-10 w-full rounded-md border bg-background px-3"
              value={interval ?? config.interval_seconds}
              onChange={(event) => setInterval(Number(event.target.value))}
            >
              <option value={60}>{t(($) => $.supervision.interval_short)}</option>
              <option value={300}>{t(($) => $.supervision.minute_five)}</option>
              <option value={900}>{t(($) => $.supervision.minute_fifteen)}</option>
            </select>
          </label>
          <label className="flex items-start gap-3 rounded-lg border p-3 text-body">
            <input
              type="checkbox"
              className="mt-1 size-4 shrink-0"
              checked={investigation ?? config.investigation_enabled}
              onChange={(event) => setInvestigation(event.target.checked)}
            />
            <span>
              <span className="block font-medium">{t(($) => $.supervision.investigation_enable)}</span>
              <span className="mt-1 block text-caption text-muted-foreground">{t(($) => $.supervision.investigation_hint)}</span>
            </span>
          </label>
          <p className="text-caption text-muted-foreground">{t(($) => $.supervision.interval_hint)}</p>
          <p className="text-caption text-muted-foreground">{t(($) => $.supervision.feedback_manual)}</p>
          <div className="flex flex-wrap gap-2">
            <Button disabled={!ready} onClick={() => save.mutate(true)}>
              {save.isPending ? t(($) => $.supervision.saving) : t(($) => $.supervision.save_enable)}
            </Button>
            {config.enabled && <Button variant="outline" disabled={save.isPending} onClick={() => save.mutate(false)}>{t(($) => $.supervision.pause)}</Button>}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
