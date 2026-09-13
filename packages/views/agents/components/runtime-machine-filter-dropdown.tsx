import { useMemo } from "react";
import { ChevronDown, Server } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import type { RuntimeMachine, RuntimeMachineSection } from "../../runtimes/components/runtime-machines";
import { useT } from "../../i18n";

// ---------------------------------------------------------------------------
// 运行环境（机器）筛选下拉框——放在搜索框旁边。触发器上显示当前选中的机器名
// （或“全部运行环境”）；展开的菜单按分组（Remote / Cloud）排列机器，
// 与 Runtimes 页侧栏一致，用户在两个页面间切换时看到的分组、数量口径一致。
// ---------------------------------------------------------------------------

const RUNTIME_MACHINE_SECTIONS: RuntimeMachineSection[] = [
  "remote",
  "cloud",
];

/**
 * Agent 列表的“运行环境筛选”下拉。单选一台机器（或“全部”），选中后列表按该机器过滤。
 * @param machines 所有可选机器。
 * @param value 当前选中的机器 id；null 表示“全部”。
 * @param onChange 选择变更回调，id 为 null 即回到“全部”。
 * @param agentCountByMachine 每台机器名下绑定的 Agent 数，用于菜单里的计数角标。
 * @param totalAgentCount 全部 Agent 总数。
 */
export function RuntimeMachineFilterDropdown({
  machines,
  value,
  onChange,
  agentCountByMachine,
  // 这个总数单独从“范围内的 Agent 列表”算出来（不从 `agentCountByMachine` 推），
  // 是为了让“全部运行环境”的数字保持准确——即使某个范围内的 Agent 绑定的机器
  // 已被回收、不再出现在任何机器名下。
  totalAgentCount,
}: {
  machines: RuntimeMachine[];
  value: string | null;
  onChange: (id: string | null) => void;
  agentCountByMachine: Map<string, number>;
  totalAgentCount: number;
}) {
  const { t } = useT("agents");
  const selected =
    value === null
      ? null
      : machines.find((machine) => machine.id === value) ?? null;

  const triggerLabel = selected ? selected.title : t(($) => $.runtime_filter.all);
  // 触发器上也总显示计数（包括“全部”态），方便和旁边其他工具栏控件一样一眼扫到。
  const triggerCount = selected
    ? (agentCountByMachine.get(selected.id) ?? 0)
    : totalAgentCount;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            className="h-8 gap-1.5 px-2 text-caption"
            data-testid="agents-runtime-filter"
          />
        }
      >
        <Server className="h-3 w-3 text-muted-foreground" />
        <span className="max-w-[12rem] truncate">{triggerLabel}</span>
        <span className="font-mono tabular-nums text-muted-foreground">
          {triggerCount}
        </span>
        <ChevronDown className="h-3 w-3 text-faint-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72 p-0">
        <RuntimeMachineFilterMenu
          machines={machines}
          value={value}
          onChange={onChange}
          totalAgentCount={totalAgentCount}
          agentCountByMachine={agentCountByMachine}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** 下拉面板：按固定分组顺序把机器切成几段（Remote / Cloud），段内保持原排序。 */
function RuntimeMachineFilterMenu({
  machines,
  value,
  onChange,
  totalAgentCount,
  agentCountByMachine,
}: {
  machines: RuntimeMachine[];
  value: string | null;
  onChange: (id: string | null) => void;
  totalAgentCount: number;
  agentCountByMachine: Map<string, number>;
}) {
  const { t } = useT("agents");
  // 按分组聚合机器，同时保持 `buildRuntimeMachines` 已排好的顺序
  //（先 section、再在线数、再标题）。这里顺着分组列表逐个切片即可保住原序。
  const grouped = useMemo(() => {
    const result: Array<{
      section: RuntimeMachineSection;
      machines: RuntimeMachine[];
    }> = [];
    for (const section of RUNTIME_MACHINE_SECTIONS) {
      const inSection = machines.filter((machine) => machine.section === section);
      if (inSection.length > 0) result.push({ section, machines: inSection });
    }
    return result;
  }, [machines]);

  return (
    <div className="max-h-80 overflow-y-auto py-1">
      <RuntimeMachineFilterItem
        active={value === null}
        onClick={() => onChange(null)}
        label={t(($) => $.runtime_filter.all)}
        count={totalAgentCount}
      />
      {grouped.map((group) => (
        <div key={group.section}>
          <div className="flex items-center gap-2 px-3 pb-1 pt-3 text-micro font-medium uppercase tracking-wider text-muted-foreground">
            <span>{t(($) => $.runtime_filter[`section_${group.section}`])}</span>
            <span className="h-px flex-1 bg-border" />
          </div>
          {group.machines.map((machine) => (
            <RuntimeMachineFilterItem
              key={machine.id}
              active={value === machine.id}
              onClick={() => onChange(machine.id)}
              label={machine.title}
              subtitle={machine.subtitle}
              count={agentCountByMachine.get(machine.id) ?? 0}
            />
          ))}
        </div>
      ))}
      {machines.length === 0 && (
        <div className="px-3 py-2 text-caption text-muted-foreground">
          {t(($) => $.runtime_filter.empty)}
        </div>
      )}
    </div>
  );
}

/** 菜单里的单行选项：一行机器名（含副标题）+ 右侧 Agent 计数；高亮表示选中。 */
function RuntimeMachineFilterItem({
  active,
  onClick,
  label,
  subtitle,
  count,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  subtitle?: string | null;
  count: number;
}) {
  const { t } = useT("agents");
  // DropdownMenuItem（Base UI Menu.Item）会把它接入菜单的方向键导航、前缀输入、
  // ARIA role="menuitem" 语义，并在选中后自动关菜单（closeOnClick: true）。
  // “选中 vs 闲置”的视觉是额外用 `data-active` 叠上去的，这样不会被 Base 的
  // 聚焦/悬停样式盖掉。
  return (
    <DropdownMenuItem
      onClick={onClick}
      data-active={active || undefined}
      data-testid={active ? "agents-runtime-filter-active" : undefined}
      className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-caption transition-colors ${
        active
          ? "bg-accent text-accent-foreground"
          : "text-foreground hover:bg-muted/60 data-highlighted:bg-muted/60"
      }`}
    >
      <span className="min-w-0 flex-1 truncate">
        <span className="block truncate font-medium">{label}</span>
        {subtitle && (
          <span className="block truncate text-micro font-normal text-muted-foreground">
            {subtitle}
          </span>
        )}
      </span>
      <span className="font-mono tabular-nums text-muted-foreground">
        {t(($) => $.runtime_filter.agent_count, { count })}
      </span>
    </DropdownMenuItem>
  );
}
