"use client";

// 本文件是 Codex Agent 设置里的“服务档位（Fast/Standard…）”全宽字段：档位能力来自
// 当前模型实时目录，而不是写死的 Fast 开关。模型为空时跟随 config.toml、无法安全解析，
// 于是该字段宁可失败关闭——除非已有存值需要保留可见，供用户显式清掉。

import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Gauge } from "lucide-react";
import type { RuntimeModelServiceTier } from "@multica/core/types";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { SettingsRow } from "../../../settings/components/settings-layout";
import { useT } from "../../../i18n";
import { findModelCapabilityEntry } from "./model-capability";

/**
 * 服务档位设置字段。能力从“当前解析出的模型”的实时目录里取，而不是硬编码 Fast 开关。
 * 模型为空（跟随 config.toml）无法安全解析，字段失败关闭——除非还有存值
 * 需要留在界面上供显式清理。
 */
export function ServiceTierSettingField({
  label,
  runtimeId,
  runtimeOnline,
  provider,
  model,
  value,
  canEdit,
  onChange,
}: {
  label: ReactNode;
  runtimeId: string | null;
  runtimeOnline: boolean;
  provider: string;
  model: string;
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const models = modelsQuery.data?.models ?? [];
  const entry = findModelCapabilityEntry(models, model, provider);
  const tiers = entry?.service_tiers ?? [];
  const supportsExplicitStandard = models.some(
    (candidate) =>
      candidate.supports_explicit_standard_service_tier === true,
  );

  if (tiers.length === 0 && !supportsExplicitStandard && !value) return null;

  return (
    <SettingsRow label={label} size="select-wide">
      <ServiceTierPicker
        value={value}
        tiers={tiers}
        supportsExplicitStandard={supportsExplicitStandard}
        canEdit={canEdit}
        onChange={onChange}
      />
    </SettingsRow>
  );
}

/** 服务档位的实际选择器（read-only 时不弹层）：可选列表 = 目录档位 + 显式 Standard（若 CLI 支持）。 */
function ServiceTierPicker({
  value,
  tiers,
  supportsExplicitStandard,
  canEdit,
  onChange,
}: {
  value: string;
  tiers: RuntimeModelServiceTier[];
  supportsExplicitStandard: boolean;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const availableTiers = supportsExplicitStandard
    ? [
          {
            id: "default",
            name: t(($) => $.pickers.service_tier_standard),
            description: t(
              ($) => $.pickers.service_tier_standard_description,
            ),
          },
          ...tiers.filter((tier) => tier.id !== "default"),
      ]
    : tiers;
  const selected = value
    ? availableTiers.find((tier) => tier.id === value)
    : undefined;
  const triggerLabel =
    selected?.name || value || t(($) => $.pickers.service_tier_default);
  const triggerTitle = t(($) => $.pickers.service_tier_tooltip, {
    value: triggerLabel,
  });

  const select = async (next: string) => {
    setOpen(false);
    if (next !== value) await onChange(next);
  };

  const display = (
    <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
      <Gauge className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 truncate">{triggerLabel}</span>
    </div>
  );
  if (!canEdit) return display;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-[var(--anchor-width)] min-w-[14rem] max-w-md"
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className="flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          aria-label={triggerTitle}
        />
      }
      trigger={
        <>
          <Gauge
            className="h-4 w-4 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <span className="min-w-0 flex-1 truncate">{triggerLabel}</span>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
            aria-hidden="true"
          />
        </>
      }
    >
      {availableTiers.map((tier) => (
        <PickerItem
          key={tier.id}
          selected={tier.id === value}
          onClick={() => void select(tier.id)}
        >
          <span className="block min-w-0 flex-1 text-left">
            <span className="truncate text-label font-medium">
              {tier.name}
            </span>
            {tier.description ? (
              <span className="mt-0.5 block text-micro leading-snug text-muted-foreground">
                {tier.description}
              </span>
            ) : null}
          </span>
        </PickerItem>
      ))}
      {value ? (
        <button
          type="button"
          onClick={() => void select("")}
          className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
          title={t(($) => $.pickers.service_tier_clear_title)}
        >
          {t(($) => $.pickers.service_tier_clear)}
        </button>
      ) : null}
    </PropertyPicker>
  );
}
