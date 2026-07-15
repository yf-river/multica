"use client";

import { useQuery } from "@tanstack/react-query";
import type { RuntimeModel } from "@multica/core/types";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { PropRow } from "../../../common/prop-row";
import { useT } from "../../../i18n";
import { ThinkingPicker } from "./thinking-picker";

/**
 * Hides unsupported empty state, but exposes a persisted orphan value so the
 * user can clear it. Model discovery shares the inspector's cached query.
 */
export function ThinkingPropRow({
  runtimeId,
  runtimeOnline,
  model,
  value,
  canEdit,
  onChange,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
  model: string;
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );

  const models = modelsQuery.data?.models ?? [];
  const entry = pickModelEntry(models, model);
  const levels = entry?.thinking?.supported_levels ?? [];
  if (levels.length === 0 && !value) return null;

  return (
    <PropRow label={t(($) => $.inspector.prop_thinking)} interactive={false}>
      <ThinkingPicker
        value={value}
        levels={levels}
        canEdit={canEdit}
        onChange={onChange}
      />
    </PropRow>
  );
}

function pickModelEntry(
  models: RuntimeModel[],
  model: string,
): RuntimeModel | undefined {
  if (model) return models.find((m) => m.id === model);
  return models.find((m) => m.default) ?? models[0];
}
