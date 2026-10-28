"use client";

// 测试环境的运行时设置弹窗：与 Agent 设置运行时同一个形态（四行 + 应用按钮），
// 只是数据来自发布器回写的真实目录，而不是当前环境的实时接口。

import { useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import type { RuntimeModel } from "@multica/core/types";
import type { TestAgentPolicy } from "@multica/core/test-agents";
import { ModelPicker } from "../../agents/components/inspector/model-picker";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow } from "./settings-layout";

export interface TestAgentRuntimeOption {
  id: string;
  provider: string;
  models?: {
    id: string;
    label: string;
    thinking?: { supported_levels?: { value: string; label: string }[] };
    service_tiers?: { id: string; name: string }[];
  }[];
}

const selectClass =
  "min-h-11 w-full min-w-0 rounded-lg border border-input bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";

export function TestAgentRuntimeDialog({
  title,
  description,
  environment,
  initial,
  runtimes,
  disabled,
  applyLabel,
  onClose,
  onSave,
}: {
  title: string;
  description: string;
  environment: string;
  initial: TestAgentPolicy;
  runtimes: TestAgentRuntimeOption[];
  disabled: boolean;
  applyLabel: string;
  onClose: () => void;
  onSave: (policy: TestAgentPolicy) => void;
}) {
  const { t } = useT("agents");
  // 同一个 provider 可能有多台运行时，策略只记 provider，所以按 provider 归并。
  const options = Array.from(new Map(runtimes.map((r) => [r.provider, r])).values());
  const initialProvider = initial.runtime ?? initial.provider;
  const [provider, setProvider] = useState(
    options.find((o) => o.provider === initialProvider)?.provider ??
      options[0]?.provider ??
      initialProvider,
  );
  const [model, setModel] = useState(initial.model);
  const [thinking, setThinking] = useState(initial.thinking_level ?? "");
  const [tier, setTier] = useState(initial.service_tier ?? "");

  const selected = options.find((o) => o.provider === provider);
  const models = (selected?.models ?? []) as RuntimeModel[];
  const effectiveModel = models.some((m) => m.id === model) ? model : (models[0]?.id ?? "");
  const entry = models.find((m) => m.id === effectiveModel);
  const levels = entry?.thinking?.supported_levels ?? [];
  const tiers = entry?.service_tiers ?? [];
  const effectiveThinking = levels.some((l) => l.value === thinking) ? thinking : (levels[0]?.value ?? "");
  const effectiveTier = tiers.some((x) => x.id === tier) ? tier : (tiers[0]?.id ?? "");

  return (
    <Dialog open onOpenChange={(open) => { if (!open && !disabled) onClose(); }}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <SettingsCard>
          <SettingsRow label={t(($) => $.inspector.prop_runtime)} size="select-wide">
            <select
              aria-label={`${environment} runtime`}
              className={selectClass}
              value={provider}
              disabled={disabled || options.length === 0}
              onChange={(event) => {
                setProvider(event.target.value);
                setModel("");
                setThinking("");
                setTier("");
              }}
            >
              {options.length === 0 ? <option value={provider}>{provider}</option> : null}
              {options.map((option) => (
                <option key={option.id} value={option.provider}>
                  {option.provider}
                </option>
              ))}
            </select>
          </SettingsRow>
          <SettingsRow label={t(($) => $.inspector.prop_model)} size="select-wide">
            <ModelPicker
              variant="field"
              showLabel={false}
              allowCustom={false}
              runtimeId={selected?.id ?? null}
              runtimeOnline={Boolean(selected)}
              value={effectiveModel || null}
              canEdit={!disabled && Boolean(selected)}
              catalog={{ models, supported: true }}
              onChange={(next) => {
                setModel(next);
                const nextEntry = models.find((m) => m.id === next);
                const nextLevels = nextEntry?.thinking?.supported_levels ?? [];
                const nextTiers = nextEntry?.service_tiers ?? [];
                setThinking(nextLevels[0]?.value ?? "");
                setTier(nextTiers[0]?.id ?? "");
              }}
            />
          </SettingsRow>
          <SettingsRow label={t(($) => $.inspector.prop_thinking)} size="select-wide">
            <select
              aria-label={`${environment} thinking`}
              className={selectClass}
              value={effectiveThinking}
              disabled={disabled || levels.length === 0}
              onChange={(event) => setThinking(event.target.value)}
            >
              {levels.length === 0 ? (
                <option value="">{t(($) => $.batch_runtime.unsupported)}</option>
              ) : null}
              {levels.map((level) => (
                <option key={level.value} value={level.value}>
                  {level.label}
                </option>
              ))}
            </select>
          </SettingsRow>
          <SettingsRow label={t(($) => $.batch_runtime.speed)} size="select-wide">
            <select
              aria-label={`${environment} speed`}
              className={selectClass}
              value={effectiveTier}
              disabled={disabled || tiers.length === 0}
              onChange={(event) => setTier(event.target.value)}
            >
              {tiers.length === 0 ? (
                <option value="">{t(($) => $.batch_runtime.unsupported)}</option>
              ) : null}
              {tiers.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </select>
          </SettingsRow>
        </SettingsCard>
        <DialogFooter>
          <Button variant="outline" disabled={disabled} onClick={onClose}>
            {t(($) => $.batch_runtime.cancel)}
          </Button>
          <Button
            disabled={disabled || !selected || !effectiveModel}
            onClick={() =>
              onSave({
                provider,
                runtime: provider,
                model: effectiveModel,
                thinking_level: effectiveThinking,
                service_tier: effectiveTier,
              })
            }
          >
            {applyLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
