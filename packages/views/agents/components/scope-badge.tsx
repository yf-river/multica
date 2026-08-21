"use client";

import type { AgentScope } from "@multica/core/types";
import {
  agentResourceScope,
  ResourceScopeBadge,
} from "../../common/resource-scope";
import { useT } from "../../i18n";

/**
 * Read-only scope badge — used wherever a user should see an agent's
 * scope (Personal / Workspace) without being able to change it. Replaces
 * the interactive `<ScopePicker>` for non-managers on the detail page,
 * and is also the canonical badge for hover cards and list rows.
 *
 * `compact` drops the text label and shows just the icon — for tight spaces
 * like the agent table where the column header already labels the field.
 */
export function ScopeBadge({
  value,
  compact = false,
  className = "",
}: {
  value: AgentScope;
  compact?: boolean;
  className?: string;
}) {
  const { t } = useT("agents");
  const scope = agentResourceScope(value);
  const label = t(($) => $.scope[value].label);
  const tooltip = t(($) => $.scope[value].tooltip);

  return (
    <ResourceScopeBadge
      scope={scope}
      label={label}
      tooltip={tooltip}
      compact={compact}
      className={className}
    />
  );
}
