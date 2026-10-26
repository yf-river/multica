"use client";

// 本文件是 Agent 设置里的“运行权限（文件系统沙箱档位）”全宽字段。
//
// 档位是 Multica 自己的字段，不是某个 runtime 的原生配置：daemon 在 claim 时
// 按 provider 决定是否执行它。今天只有 DSH 会真正切换沙箱（其它 runtime 的
// 策略是守护进程固定的），所以绑定非 DSH runtime 的 agent 上控件保持可见但
// 禁用，并用一行说明讲清“为什么改了不生效”——隐藏字段只会让人以为设置丢了。

import type { AgentSandboxMode } from "@multica/core/types";
import { SettingsRow } from "../../../settings/components/settings-layout";
import { useT } from "../../../i18n";

const SANDBOX_MODES: AgentSandboxMode[] = [
  "danger-full-access",
  "workspace-write",
  "read-only",
];

const SELECT_CLASS =
  "min-h-11 w-full min-w-0 rounded-lg border border-input bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";

/**
 * 运行权限字段。
 * @param label 行标题（由调用方本地化，和其他执行配置行一致）。
 * @param value 当前档位；缺省按默认完全访问展示。
 * @param provider 绑定的 runtime 的 provider；只有 dsh 会执行该档位。
 * @param canEdit 当前用户是否有权改这个 agent。
 * @param onChange 提交新档位。
 */
export function SandboxSettingField({
  label,
  value,
  provider,
  canEdit,
  onChange,
}: {
  label: string;
  value: AgentSandboxMode | undefined;
  provider: string;
  canEdit: boolean;
  onChange: (next: AgentSandboxMode) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const effective: AgentSandboxMode = value ?? "danger-full-access";
  const enforced = provider === "dsh";
  const description = !enforced
    ? t(($) => $.inspector.sandbox_hint_other_runtime)
    : effective === "workspace-write"
      ? t(($) => $.inspector.sandbox_hint_workspace_write)
      : t(($) => $.inspector.sandbox_hint);

  return (
    <SettingsRow label={label} description={description} size="select-wide">
      <select
        aria-label={label}
        className={SELECT_CLASS}
        value={effective}
        disabled={!canEdit || !enforced}
        onChange={(event) => {
          const next = event.target.value as AgentSandboxMode;
          if (next !== effective) void onChange(next);
        }}
      >
        {SANDBOX_MODES.map((mode) => (
          <option key={mode} value={mode}>
            {mode === "read-only"
              ? t(($) => $.inspector.sandbox_read_only)
              : mode === "workspace-write"
                ? t(($) => $.inspector.sandbox_workspace_write)
                : t(($) => $.inspector.sandbox_danger_full_access)}
          </option>
        ))}
      </select>
    </SettingsRow>
  );
}
