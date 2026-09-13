"use client";

// 本文件是 Agent 设置表单里的两级“运行环境选择器”：第一级列机器（daemon），
// 第二级列该机器下真正可区分的各运行环境。用户从“我的/全部”切换归属范围，
// 打开时直接落在当前所选运行环境所在机器里。

import { useMemo, useState } from "react";
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Lock,
  Monitor,
} from "lucide-react";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import type { AgentRuntime, MemberWithUser } from "@multica/core/types";
import { ActorAvatar } from "../../../common/actor-avatar";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { ProviderLogo } from "../../../runtimes/components/provider-logo";
import {
  buildRuntimeMachines,
  runtimeRowLabel,
  type RuntimeMachine,
} from "../../../runtimes/components/runtime-machines";
import { Label } from "@multica/ui/components/ui/label";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

type Filter = "mine" | "all";

// 机器行预览多少枚 provider 图标，超出折叠成 “+N”。
const MACHINE_PROVIDER_PREVIEW = 4;

/**
 * Agent 设置表单用的两级运行环境选择器。机器的重命名会把同一个自定义名字盖到该
 * daemon 的每台运行环境上（MUL-4217），所以旧的扁平列表会渲染 N 行难以区分的
 * “Jiayuan's MacBook Pro”。第一级列机器；钻进去后按“真正有差异的东西”——运行环境
 * 本身——来列该机器下的运行环境。打开时落在当前所选运行环境所在机器，
 * 让最常见操作（同一台机器上换运行环境）零多余点击。
 */
export function RuntimePicker({
  value,
  runtimes,
  members,
  currentUserId,
  canEdit = true,
  variant = "chip",
  showLabel = true,
  onChange,
}: {
  value: string;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  /** false 时渲染静态只读展示，不弹层。 */
  canEdit?: boolean;
  variant?: "chip" | "field";
  showLabel?: boolean;
  onChange: (runtimeId: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<Filter>("mine");
  // 第二级的目标机器。`null` 显示机器列表；某个机器 id 显示该机器下的运行环境。
  // 若 id 已解析不到（如 daemon 经 WS 被回收），渲染时回退到机器列表。
  const [machineId, setMachineId] = useState<string | null>(null);

  const selected = runtimes.find((r) => r.id === value) ?? null;

  // 与创建 / 复制 / builder 各界面共用的同一判定，保证这里锁住的运行环境，
  // 正是 API 与 CLI 拒绝的那台（MUL-6126）——两边都不存在工作区角色的例外。
  const isDisabled = (r: AgentRuntime): boolean =>
    !isRuntimeUsableForUser(r, currentUserId);

  // 在“未过滤全量列表”上做机器分组——无论当前范围是“我的/全部”，都能据此解析出
  // 所选运行环境属于哪台机器（供触发器标签用）；当范围是“全部”时这份分组直接复用为列表。
  const allMachines = useMemo(
    () => buildRuntimeMachines(runtimes, { now: Date.now() }),
    [runtimes, currentUserId],
  );
  const machines = useMemo(
    () =>
      filter === "mine" && currentUserId
        ? buildRuntimeMachines(
            runtimes.filter((r) => r.owner_id === currentUserId),
            { now: Date.now() },
          )
        : allMachines,
    [runtimes, filter, currentUserId, allMachines],
  );

  const machineOf = (machineList: RuntimeMachine[], runtimeId: string) =>
    machineList.find((m) => m.runtimes.some((r) => r.id === runtimeId)) ?? null;

  const selectedMachine = selected ? machineOf(allMachines, selected.id) : null;
  const selectedLabel = selected
    ? runtimeRowLabel(selected, selectedMachine?.title ?? "")
    : null;
  // 组合成 “Claude · Jiayuan's MacBook Pro” 这样的串，供紧凑表面使用
  //（chip、只读字段、tooltip）。去重保护覆盖“整个标签本身就是机器标题”的运行环境
  //（单台未命名的云 worker 等）。
  const combinedLabel = selected
    ? selectedMachine && selectedMachine.title !== selectedLabel
      ? `${selectedLabel} · ${selectedMachine.title}`
      : (selectedLabel ?? "")
    : t(($) => $.pickers.runtime_none);

  const isOnline = selected?.status === "online";

  if (!canEdit) {
    const icon = selected ? (
      <ProviderLogo provider={selected.provider} className="h-4 w-4 shrink-0" />
    ) : (
      <Monitor
        className="h-4 w-4 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
    );
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
          {icon}
          <span className="min-w-0 flex-1 truncate">{combinedLabel}</span>
          {selected ? (
            <span
              className={`h-2 w-2 shrink-0 rounded-full ${
                isOnline ? "bg-success" : "bg-muted-foreground/40"
              }`}
              aria-hidden="true"
            />
          ) : null}
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_runtime)}</Label>
          <div className="mt-1.5">{control}</div>
        </div>
      );
    }
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5 px-1.5 py-0.5 text-caption text-muted-foreground">
        {selected ? (
          <ProviderLogo
            provider={selected.provider}
            className="h-3 w-3 shrink-0"
          />
        ) : (
          <Monitor className="h-3 w-3 shrink-0" />
        )}
        <span className="min-w-0 truncate font-mono">{combinedLabel}</span>
        {selected && (
          <span
            className={`ml-auto h-1.5 w-1.5 shrink-0 rounded-full ${
              isOnline ? "bg-success" : "bg-muted-foreground/40"
            }`}
          />
        )}
      </span>
    );
  }

  const triggerTitle = selected
    ? t(($) => $.pickers.runtime_tooltip, {
        name: combinedLabel,
        status: isOnline ? t(($) => $.pickers.runtime_online) : t(($) => $.pickers.runtime_offline),
      })
    : t(($) => $.pickers.runtime_tooltip_none);

  const hasOtherRuntimes = runtimes.some((r) => r.owner_id !== currentUserId);

  const getOwner = (id: string | null) =>
    id ? members.find((m) => m.user_id === id) ?? null : null;

  // 同一台机器上的运行环境共享一个拥有者；机器合并了多个拥有者的运行环境时
  //（按设备名而不是 daemon id 分组的旧数据可能出现）返回 null。
  const machineOwner = (machine: RuntimeMachine): MemberWithUser | null => {
    const ownerIds = new Set(
      machine.runtimes.map((r) => r.owner_id).filter(Boolean),
    );
    if (ownerIds.size !== 1) return null;
    return getOwner(
      machine.runtimes.find((r) => r.owner_id)?.owner_id ?? null,
    );
  };

  const handleOpenChange = (next: boolean) => {
    if (next) {
      // 落在选中项所在处。先判断是否需要把范围扩到“全部”：选中的运行环境不是我的时，
      // “我的”列表里永远不会有它那台机器。没选中时，若机器只有一台就直接钻进去，
      // 免去多余一步。
      const nextFilter: Filter =
        selected && currentUserId && selected.owner_id !== currentUserId
          ? "all"
          : filter;
      setFilter(nextFilter);
      const visible =
        nextFilter === "mine" && currentUserId
          ? buildRuntimeMachines(
              runtimes.filter((r) => r.owner_id === currentUserId),
              { now: Date.now() },
            )
          : allMachines;
      const landing = selected
        ? machineOf(visible, selected.id)
        : visible.length === 1
          ? visible[0]
          : null;
      setMachineId(landing?.id ?? null);
    }
    setOpen(next);
  };

  const select = async (id: string) => {
    setOpen(false);
    if (id !== value) await onChange(id);
  };

  const drilled = machineId
    ? machines.find((m) => m.id === machineId) ?? null
    : null;

  const onlineCountLabel = (machine: RuntimeMachine) =>
    t(($) => $.create_dialog.runtime_group_online, {
      online: machine.onlineCount,
      total: machine.runtimes.length,
    });

  const picker = (
    <PropertyPicker
      open={open}
      onOpenChange={handleOpenChange}
      width={
        variant === "field"
          ? "w-[var(--anchor-width)] min-w-[18rem] max-w-md"
          : "w-auto min-w-[18rem] max-w-md"
      }
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className={
            variant === "field"
              ? `${showLabel ? "mt-1.5 " : ""}flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50`
              : CHIP_CLASS
          }
          aria-label={triggerTitle}
        />
      }
      trigger={
        <>
          {selected ? (
            <ProviderLogo
              provider={selected.provider}
              className={
                variant === "field" ? "h-4 w-4 shrink-0" : "h-3 w-3 shrink-0"
              }
            />
          ) : (
            <Monitor
              className={
                variant === "field"
                  ? "h-4 w-4 shrink-0 text-muted-foreground"
                  : "h-3 w-3 shrink-0 text-muted-foreground"
              }
              aria-hidden="true"
            />
          )}
          {variant === "field" && selected ? (
            <span className="min-w-0 flex-1 truncate">
              {selectedLabel}
              {selectedMachine && selectedMachine.title !== selectedLabel && (
                <span className="text-muted-foreground">
                  {" · "}
                  {selectedMachine.title}
                </span>
              )}
            </span>
          ) : (
            <span
              className={
                variant === "field"
                  ? "min-w-0 flex-1 truncate"
                  : "min-w-0 truncate font-mono"
              }
            >
              {combinedLabel}
            </span>
          )}
          {selected && (
            <span
              className={`ml-auto h-1.5 w-1.5 shrink-0 rounded-full ${
                isOnline ? "bg-success" : "bg-muted-foreground/40"
              }`}
            />
          )}
          {variant === "field" ? (
            <ChevronDown
              className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                open ? "rotate-180" : ""
              }`}
              aria-hidden="true"
            />
          ) : null}
        </>
      }
      header={
        drilled ? (
          <button
            type="button"
            onClick={() => setMachineId(null)}
            aria-label={t(($) => $.pickers.runtime_back_to_machines)}
            className="flex w-full items-center gap-2 px-2 py-2 text-left text-body transition-colors hover:bg-muted/60"
          >
            <ChevronLeft className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate font-medium">
              {drilled.title}
            </span>
            <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
              {onlineCountLabel(drilled)}
            </span>
          </button>
        ) : hasOtherRuntimes ? (
          <div className="p-2">
            <div className="flex items-center gap-0.5 rounded-md bg-muted p-0.5">
              <FilterButton
                active={filter === "mine"}
                onClick={() => setFilter("mine")}
              >
                {t(($) => $.scope.mine)}
              </FilterButton>
              <FilterButton
                active={filter === "all"}
                onClick={() => setFilter("all")}
              >
                {t(($) => $.scope.all)}
              </FilterButton>
            </div>
          </div>
        ) : undefined
      }
    >
      {drilled ? (
        drilled.runtimes.map((rt) => {
          const owner = getOwner(rt.owner_id);
          const rtOnline = rt.status === "online";
          const locked = isDisabled(rt);
          const label = runtimeRowLabel(rt, drilled.title);
          const tooltip = [
            label,
            owner ? t(($) => $.pickers.runtime_owned_by, { name: owner.name }) : null,
            rtOnline ? t(($) => $.pickers.runtime_online) : t(($) => $.pickers.runtime_offline),
            locked ? t(($) => $.create_dialog.runtime_private_locked_tooltip) : null,
          ]
            .filter(Boolean)
            .join(" · ");
          return (
            <PickerItem
              key={rt.id}
              selected={rt.id === value}
              disabled={locked}
              onClick={() => {
                if (locked) return;
                void select(rt.id);
              }}
              tooltip={tooltip}
            >
              <ProviderLogo
                provider={rt.provider}
                className="h-4 w-4 shrink-0"
              />
              <span className="min-w-0 flex-1 truncate text-body font-medium">
                {label}
              </span>
              {rt.runtime_mode === "cloud" && (
                <span className="shrink-0 rounded bg-info/10 px-1 text-micro font-medium text-info">
                  {t(($) => $.create_dialog.runtime_cloud_badge)}
                </span>
              )}
              {locked && (
                <span className="shrink-0 inline-flex items-center gap-0.5 rounded bg-muted px-1 text-micro font-medium text-muted-foreground">
                  <Lock className="h-2.5 w-2.5" />
                  {t(($) => $.create_dialog.runtime_private_badge)}
                </span>
              )}
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  rtOnline ? "bg-success" : "bg-muted-foreground/40"
                }`}
                aria-label={rtOnline ? t(($) => $.pickers.runtime_online) : t(($) => $.pickers.runtime_offline)}
              />
            </PickerItem>
          );
        })
      ) : machines.length === 0 ? (
        <p className="px-2 py-3 text-center text-caption text-muted-foreground">
          {t(($) => $.pickers.runtime_empty)}
        </p>
      ) : (
        machines.map((machine) => {
          const owner = machineOwner(machine);
          const containsSelection = machine.runtimes.some(
            (r) => r.id === value,
          );
          const extraProviders =
            machine.providerNames.length - MACHINE_PROVIDER_PREVIEW;
          return (
            <button
              key={machine.id}
              type="button"
              data-picker-item
              onClick={() => setMachineId(machine.id)}
              className="flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left text-body transition-colors hover:bg-accent"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-body font-medium">
                    {machine.title}
                  </span>
                  {machine.mode === "cloud" && (
                    <span className="shrink-0 rounded bg-info/10 px-1 text-micro font-medium text-info">
                      {t(($) => $.create_dialog.runtime_cloud_badge)}
                    </span>
                  )}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-caption text-muted-foreground">
                  {owner && (
                    <span className="flex min-w-0 items-center gap-1">
                      <ActorAvatar
                        actorType="member"
                        actorId={owner.user_id}
                        size="xs"
                      />
                      <span className="truncate">{owner.name}</span>
                    </span>
                  )}
                  {owner && machine.providerNames.length > 0 && (
                    <span className="text-faint-foreground">·</span>
                  )}
                  {machine.providerNames.length > 0 && (
                    <span className="flex shrink-0 items-center gap-1">
                      {machine.providerNames
                        .slice(0, MACHINE_PROVIDER_PREVIEW)
                        .map((provider) => (
                          <ProviderLogo
                            key={provider}
                            provider={provider}
                            className="h-3 w-3"
                          />
                        ))}
                      {extraProviders > 0 && (
                        <span className="text-micro tabular-nums">
                          +{extraProviders}
                        </span>
                      )}
                    </span>
                  )}
                </div>
              </div>
              <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
                {onlineCountLabel(machine)}
              </span>
              {containsSelection && (
                <Check className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
              <ChevronRight className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
            </button>
          );
        })
      )}
    </PropertyPicker>
  );

  if (variant === "field") {
    if (!showLabel) return picker;
    return (
      <div className="flex min-w-0 flex-col">
        <Label>{t(($) => $.inspector.prop_runtime)}</Label>
        {picker}
      </div>
    );
  }

  return picker;
}

/** “我的/全部”小切换按钮。 */
function FilterButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`flex-1 rounded px-2 py-0.5 text-caption font-medium transition-colors ${
        active
          ? "bg-background text-foreground shadow-sm"
          : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
    </button>
  );
}
