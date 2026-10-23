// 上游同步设置：同步范围、执行体（运行时/模型/思考档/提示词）与自动同步计划。
// 执行体由服务端托管，这里只维护配置，不出现"新建智能体"。
"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { isRuntimeUsableForUser, runtimeModelsOptions } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useUpdateUpstreamConfig } from "@multica/core/upstream/mutations";
import { upstreamInstructionsOptions, type UpstreamSnapshot } from "@multica/core/upstream/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { RuntimePicker } from "../../agents/components/inspector/runtime-picker";
import { ModelPicker } from "../../agents/components/inspector/model-picker";
import { findModelCapabilityEntry } from "../../agents/components/inspector/model-capability";
import { useT } from "../../i18n";

const HOURS = Array.from({ length: 24 }, (_, hour) => hour);
// 同步执行体只跑在 Codex / CodeBuddy 上，服务端绑定其它 provider 会直接拒绝。
const SYNC_PROVIDERS = new Set(["codex", "codebuddy"]);
const fieldClass = "h-9 w-full rounded-md border border-input bg-background px-2 text-body";

export function UpstreamSettingsSheet({
  open,
  onClose,
  snapshot,
}: {
  open: boolean;
  onClose: () => void;
  snapshot?: UpstreamSnapshot;
}) {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const currentUser = useAuthStore((state) => state.user);
  const runtimes = useQuery({ ...runtimeListOptions(wsId), enabled: open });
  const members = useQuery({ ...memberListOptions(wsId), enabled: open });
  const defaults = useQuery({ ...upstreamInstructionsOptions(wsId), enabled: open });
  const save = useUpdateUpstreamConfig();

  const config = snapshot?.config;
  const [upstreamUrl, setUpstreamUrl] = useState("");
  const [upstreamBranch, setUpstreamBranch] = useState("");
  const [localRef, setLocalRef] = useState("");
  const [batchSize, setBatchSize] = useState(200);
  const [runtimeId, setRuntimeId] = useState("");
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("");
  const [instructions, setInstructions] = useState("");
  // 修改 1. 「用户动过提示词」单独记一个标记：动过就按输入框原值渲染（清空就是空的），
  //    没动过才跟随内置默认。不能用"值是否为空"反推，否则全选删除后立刻回填默认文本，
  //    再粘贴就接在默认后面，存成「内置默认 + 我的提示词」。
  const [instructionsTouched, setInstructionsTouched] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [scheduleHour, setScheduleHour] = useState(9);

  // 每次打开按服务端当前值灌入一次。快照会持续轮询，不能跟着刷新覆盖正在编辑的内容。
  const [synced, setSynced] = useState(false);
  useEffect(() => {
    if (!open) setSynced(false);
  }, [open]);
  useEffect(() => {
    if (!open || synced || !config) return;
    setUpstreamUrl(config.upstream_url);
    setUpstreamBranch(config.upstream_branch);
    setLocalRef(config.local_ref);
    setBatchSize(config.batch_size);
    setRuntimeId(config.runtime_id);
    setModel(config.model);
    setThinking(config.thinking_level);
    setInstructions(config.instructions);
    setInstructionsTouched(Boolean(config.instructions.trim()));
    setEnabled(config.sync_state !== "paused");
    setScheduleHour(config.schedule_hour);
    setSynced(true);
  }, [open, synced, config]);

  // 1. 选择器先按可执行 provider 过滤，避免选中一个只能被服务端 403 拒掉的运行时。
  const available = (runtimes.data ?? []).filter((item) => SYNC_PROVIDERS.has(item.provider));
  const selected = available.find((item) => item.id === runtimeId);
  const online = selected?.status === "online" && isRuntimeUsableForUser(selected, currentUser?.id ?? null);
  // 2. 运行时离线时模型目录本来就取不到，别再用空 id 请求一次目录接口拿 400。
  const catalog = useQuery({ ...runtimeModelsOptions(online ? runtimeId : null), enabled: open && Boolean(runtimeId) && online });
  const models = catalog.data?.models ?? [];
  const capability = model && selected ? findModelCapabilityEntry(models, model, selected.provider) : undefined;
  const levels = capability?.thinking?.supported_levels ?? [];
  const defaultText = defaults.data?.instructions ?? "";
  // 修改 2. 已自定义＝动过且不是空、也不等同于内置默认；空值保存后仍等价于跟随默认。
  const customized = instructionsTouched && instructions.trim() !== "" && instructions.trim() !== defaultText.trim();
  const displayText = instructionsTouched ? instructions : defaultText;
  // 清空后保存等价于跟随默认，这里要把这层关系说出来，而不是留着空框不解释。
  const instructionsCleared = instructionsTouched && instructions.trim() === "";
  // 有同步历史后，换仓库或比较基线会让游标和冻结清单失去意义。
  const repositoryLocked = Boolean(config?.bootstrap_base_sha);
  const canWrite = snapshot?.can_write ?? false;
  const batchValid = Number.isInteger(batchSize) && batchSize >= 1 && batchSize <= 500;

  const submit = () => {
    save.mutate(
      {
        upstream_url: upstreamUrl,
        upstream_branch: upstreamBranch,
        local_ref: localRef,
        batch_size: batchSize,
        schedule_hour: scheduleHour,
        enabled,
        runtime_id: runtimeId,
        model,
        thinking_level: thinking,
        instructions: customized ? instructions : "",
      },
      { onSuccess: () => onClose() },
    );
  };

  const statusHint = (() => {
    if (!canWrite) return t(($) => $.page.setup.readonly);
    if (snapshot?.executor_state === "unbound") return t(($) => $.page.setup.executor_unbound);
    if (runtimeId && !online) return t(($) => $.page.setup.runtime_offline);
    return "";
  })();

  return (
    <Sheet open={open} onOpenChange={(value) => { if (!value) onClose(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-lg">
        <SheetHeader>
          <SheetTitle>{t(($) => $.page.setup.title)}</SheetTitle>
          <SheetDescription>{t(($) => $.page.setup.description)}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          {statusHint ? (
            <p className="rounded-md border border-surface-border bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
              {statusHint}
            </p>
          ) : null}

          <section className="space-y-3">
            <h3 className="text-body font-medium">{t(($) => $.page.setup.section_scope)}</h3>
            {repositoryLocked ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.page.setup.repository_locked)}</p>
            ) : null}
            <label className="block space-y-1 text-body" htmlFor="upstream-url">
              <span>{t(($) => $.page.setup.repository)}</span>
              <input
                id="upstream-url"
                className={fieldClass}
                autoComplete="off"
                value={upstreamUrl}
                disabled={!canWrite || repositoryLocked}
                onChange={(event) => setUpstreamUrl(event.target.value)}
              />
            </label>
            <div className="grid grid-cols-2 gap-3">
              <label className="block space-y-1 text-body" htmlFor="upstream-branch">
                <span>{t(($) => $.page.setup.branch)}</span>
                <input
                  id="upstream-branch"
                  className={fieldClass}
                  autoComplete="off"
                  value={upstreamBranch}
                  disabled={!canWrite || repositoryLocked}
                  onChange={(event) => setUpstreamBranch(event.target.value)}
                />
              </label>
              <label className="block space-y-1 text-body" htmlFor="upstream-ref">
                <span>{t(($) => $.page.setup.local_ref)}</span>
                <input
                  id="upstream-ref"
                  className={fieldClass}
                  autoComplete="off"
                  value={localRef}
                  disabled={!canWrite || repositoryLocked}
                  onChange={(event) => setLocalRef(event.target.value)}
                />
              </label>
            </div>
            <label className="block space-y-1 text-body" htmlFor="upstream-batch">
              <span>{t(($) => $.page.setup.batch_size)}</span>
              <input
                id="upstream-batch"
                type="number"
                min={1}
                max={500}
                className={`${fieldClass} w-32`}
                value={batchSize}
                disabled={!canWrite}
                onChange={(event) => setBatchSize(Number(event.target.value) || 0)}
              />
            </label>
          </section>

          <section className="space-y-3 border-t border-surface-border pt-4">
            <h3 className="text-body font-medium">{t(($) => $.page.setup.section_executor)}</h3>
            <div className="space-y-2 text-body">
              <span>{t(($) => $.page.setup.runtime)}</span>
              <RuntimePicker
                variant="field"
                showLabel={false}
                value={runtimeId}
                runtimes={available}
                members={members.data ?? []}
                currentUserId={currentUser?.id ?? null}
                canEdit={canWrite}
                onChange={(id) => { setRuntimeId(id); setModel(""); setThinking(""); }}
              />
              <span className="block text-caption text-muted-foreground">{t(($) => $.page.setup.runtime_hint)}</span>
              {available.length === 0 ? <p className="text-caption text-warning">{t(($) => $.page.setup.runtime_empty)}</p> : null}
            </div>

            <div className="space-y-2 text-body">
              <span>{t(($) => $.page.setup.model)}</span>
              <ModelPicker
                allowCustom={false}
                variant="field"
                showLabel={false}
                runtimeId={runtimeId || null}
                runtimeOnline={Boolean(online)}
                value={model || null}
                canEdit={canWrite && Boolean(online) && catalog.isSuccess}
                onChange={(value) => { setModel(value); setThinking(""); }}
              />
              {runtimeId && !online ? <span className="block text-caption text-muted-foreground">{t(($) => $.page.setup.model_offline)}</span> : null}
            </div>

            <label className="block space-y-2 text-body" htmlFor="upstream-thinking">
              <span>{t(($) => $.page.setup.thinking)}</span>
              <select
                id="upstream-thinking"
                className="h-9 w-full rounded-md border border-input bg-background px-2 text-body"
                value={thinking}
                disabled={!canWrite || !levels.length}
                onChange={(event) => setThinking(event.target.value)}
              >
                <option value="">{t(($) => $.page.setup.thinking_default)}</option>
                {levels.map((level) => <option key={level.value} value={level.value}>{level.label}</option>)}
                {thinking && !levels.some((level) => level.value === thinking) ? <option value={thinking}>{thinking}</option> : null}
              </select>
            </label>

            <div className="space-y-2 text-body">
              <div className="flex items-center justify-between gap-2">
                <label htmlFor="upstream-instructions">{t(($) => $.page.setup.instructions)}</label>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={!canWrite || !instructionsTouched}
                  onClick={() => {
                    setInstructions("");
                    setInstructionsTouched(false);
                  }}
                >
                  {t(($) => $.page.setup.restore_default)}
                </Button>
              </div>
              <textarea
                id="upstream-instructions"
                className="min-h-56 w-full rounded-md border border-input bg-background p-3 font-mono text-caption leading-relaxed"
                value={displayText}
                disabled={!canWrite || !defaults.isSuccess}
                onChange={(event) => {
                  setInstructions(event.target.value);
                  setInstructionsTouched(true);
                }}
              />
              <p className="text-caption text-muted-foreground">
                {customized
                  ? t(($) => $.page.setup.instructions_custom)
                  : instructionsCleared
                    ? t(($) => $.page.setup.instructions_cleared)
                    : t(($) => $.page.setup.instructions_default)}
                {" · "}
                {t(($) => $.page.setup.instructions_count, { count: displayText.length })}
              </p>
            </div>
          </section>

          <section className="space-y-3 border-t border-surface-border pt-4">
            <h3 className="text-body font-medium">{t(($) => $.page.setup.section_schedule)}</h3>
            <label className="flex items-start gap-2 text-body">
              <input
                type="checkbox"
                className="mt-1 size-4 shrink-0"
                checked={enabled}
                disabled={!canWrite}
                onChange={(event) => setEnabled(event.target.checked)}
              />
              <span>{t(($) => $.page.setup.enabled)}</span>
            </label>
            <label className="block space-y-1 text-body" htmlFor="upstream-hour">
              <span>{t(($) => $.page.setup.schedule_hour)}</span>
              <select
                id="upstream-hour"
                className={`${fieldClass} w-40`}
                value={scheduleHour}
                disabled={!canWrite}
                onChange={(event) => setScheduleHour(Number(event.target.value))}
              >
                {HOURS.map((hour) => (
                  <option key={hour} value={hour}>{`${String(hour).padStart(2, "0")}:00`}</option>
                ))}
              </select>
            </label>
            <p className="text-caption text-muted-foreground">{t(($) => $.page.setup.schedule)}</p>
          </section>

          {save.isError ? <p role="alert" className="text-caption text-destructive">{save.error.message}</p> : null}

          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>{t(($) => $.page.setup.cancel)}</Button>
            <Button disabled={!canWrite || save.isPending || !batchValid || (enabled && !runtimeId)} onClick={submit}>
              {save.isPending ? t(($) => $.page.setup.saving) : t(($) => $.page.setup.save)}
            </Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
