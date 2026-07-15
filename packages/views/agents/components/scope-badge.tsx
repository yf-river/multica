"use client";

import type { ResourceScope } from "@multica/core/types";
import { ResourceScopeBadge } from "../../common/resource-scope";
import { useT } from "../../i18n";

/** Read-only Agent scope; compact mode renders only the icon. */
export function ScopeBadge({
  value,
  compact = false,
  className = "",
}: {
  value: ResourceScope;
  compact?: boolean;
  className?: string;
}) {
  const { t } = useT("agents");
  const label = t(($) => $.scope[value].label);
  const tooltip = t(($) => $.scope[value].tooltip);

  return (
    <ResourceScopeBadge
      scope={value}
      label={label}
      tooltip={tooltip}
      compact={compact}
      className={className}
    />
  );
}
