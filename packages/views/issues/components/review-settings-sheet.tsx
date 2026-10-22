"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { reviewKeys, reviewSettingsOptions } from "@multica/core/review/queries";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { findModelCapabilityEntry } from "../../agents/components/inspector/model-capability";
import { useT } from "../../i18n";

const selectClass = "h-10 w-full rounded-md border bg-background px-3 text-body";

/**
 * 「审核中」列头菜单打开的审查设置：运行时、模型、思考等级与提示词。
 * 配置只有一份，挂在状态类别 in_review 上；多个审核类自定义列改的是同一份。
 */
export function ReviewSettingsSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const settings = useQuery(reviewSettingsOptions(wsId, open));
  const runtimes = useQuery({ ...runtimeListOptions(wsId), enabled: open });
  const data = settings.data;

  const [enabled, setEnabled] = useState(false);
  const [runtimeId, setRuntimeId] = useState("");
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("");
  const [instructions, setInstructions] = useState("");

  useEffect(() => {
    if (!data) return;
    setEnabled(data.enabled);
    setRuntimeId(data.runtime_id ?? "");
    setModel(data.model ?? "");
    setThinking(data.thinking_level ?? "");
    setInstructions(data.instructions ?? "");
  }, [data]);

  const usableRuntimes = useMemo(
    () => (runtimes.data ?? []).filter((runtime) => runtime.provider === "codex" || runtime.provider === "codebuddy"),
    [runtimes.data],
  );
  const selectedRuntime = usableRuntimes.find((runtime) => runtime.id === runtimeId);
  const catalog = useQuery({ ...runtimeModelsOptions(runtimeId || null), enabled: open && Boolean(runtimeId) });
  const models = catalog.data?.models ?? [];
  const capability = selectedRuntime && model ? findModelCapabilityEntry(models, model, selectedRuntime.provider) : undefined;
  const levels = capability?.thinking?.supported_levels ?? [];

  const save = useMutation({
    mutationFn: () => api.updateReviewSettings({
      enabled,
      runtime_id: runtimeId || null,
      model,
      thinking_level: thinking,
      instructions,
    }),
    onSuccess: (next) => {
      queryClient.setQueryData(reviewKeys.settings(wsId), next);
      void queryClient.invalidateQueries({ queryKey: reviewKeys.all(wsId) });
      toast.success(t(($) => $.review.saved));
      onClose();
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : String(error)),
  });

  const canManage = data?.can_manage ?? false;

  return (
    <Sheet open={open} onOpenChange={(value) => { if (!value) onClose(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-lg">
        <SheetHeader>
          <SheetTitle>{t(($) => $.review.settings)}</SheetTitle>
          <SheetDescription>{t(($) => $.review.settings_description)}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          {settings.isPending && <p className="text-body text-muted-foreground">{t(($) => $.review.loading)}</p>}
          {settings.isError && <p role="alert" className="text-body text-destructive">{String(settings.error)}</p>}

          <label className="flex items-start gap-3 rounded-lg border p-3 text-body">
            <input
              type="checkbox"
              className="mt-1 size-4 shrink-0"
              checked={enabled}
              disabled={!canManage}
              onChange={(event) => setEnabled(event.target.checked)}
            />
            <span>
              <span className="block font-medium">{t(($) => $.review.enable)}</span>
              <span className="mt-1 block text-caption text-muted-foreground">{t(($) => $.review.enable_hint)}</span>
            </span>
          </label>

          <label className="block space-y-2 text-body" htmlFor="review-runtime">
            <span>{t(($) => $.review.runtime)}</span>
            <select
              id="review-runtime"
              className={selectClass}
              value={runtimeId}
              disabled={!canManage}
              onChange={(event) => {
                setRuntimeId(event.target.value);
                setModel("");
                setThinking("");
              }}
            >
              <option value="">{t(($) => $.review.runtime_placeholder)}</option>
              {usableRuntimes.map((runtime) => (
                <option key={runtime.id} value={runtime.id}>
                  {`${runtime.custom_name || runtime.name}（${runtime.provider}）`}
                </option>
              ))}
            </select>
            <span className="block text-caption text-muted-foreground">{t(($) => $.review.runtime_hint)}</span>
          </label>

          <label className="block space-y-2 text-body" htmlFor="review-model">
            <span>{t(($) => $.review.model)}</span>
            <select
              id="review-model"
              className={selectClass}
              value={model}
              disabled={!canManage || !runtimeId}
              onChange={(event) => {
                setModel(event.target.value);
                setThinking("");
              }}
            >
              <option value="">{t(($) => $.review.model_default)}</option>
              {models.map((entry) => (
                <option key={entry.id} value={entry.id}>{entry.label || entry.id}</option>
              ))}
            </select>
          </label>

          {levels.length > 0 && (
            <label className="block space-y-2 text-body" htmlFor="review-thinking">
              <span>{t(($) => $.review.thinking)}</span>
              <select
                id="review-thinking"
                className={selectClass}
                value={thinking}
                disabled={!canManage}
                onChange={(event) => setThinking(event.target.value)}
              >
                <option value="">{t(($) => $.review.thinking_default)}</option>
                {levels.map((level) => (
                  <option key={level.value} value={level.value}>{level.label}</option>
                ))}
              </select>
            </label>
          )}

          <div className="space-y-2 text-body">
            <div className="flex items-center justify-between gap-2">
              <label htmlFor="review-instructions">{t(($) => $.review.instructions)}</label>
              <Button
                variant="ghost"
                size="sm"
                disabled={!canManage}
                onClick={() => setInstructions(data?.default_instructions ?? "")}
              >
                {t(($) => $.review.restore_default)}
              </Button>
            </div>
            <textarea
              id="review-instructions"
              className="min-h-64 w-full rounded-md border bg-background p-3 font-mono text-caption leading-relaxed"
              value={instructions}
              disabled={!canManage}
              onChange={(event) => setInstructions(event.target.value)}
            />
            <span className="block text-caption text-muted-foreground">
              {instructions.trim() === (data?.default_instructions ?? "").trim()
                ? t(($) => $.review.instructions_default_hint)
                : t(($) => $.review.instructions_custom_hint)}
            </span>
          </div>

          <div className="rounded-lg border bg-muted/30 p-3 text-caption text-muted-foreground">
            <p className="font-medium text-body text-foreground">{t(($) => $.review.policy)}</p>
            <p className="mt-1">{t(($) => $.review.policy_description, { rounds: data?.max_rounds ?? 6 })}</p>
          </div>

          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>{t(($) => $.review.cancel)}</Button>
            <Button
              disabled={!canManage || save.isPending || (enabled && !runtimeId)}
              onClick={() => save.mutate()}
            >
              {save.isPending ? t(($) => $.review.saving) : t(($) => $.review.save)}
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
