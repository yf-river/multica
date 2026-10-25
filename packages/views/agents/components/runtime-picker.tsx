"use client";

// 本文件是创建/编辑 Agent 时的“运行环境选择器”下拉：按“我的 / 全部”切换归属、
// 超过阈值出现搜索框、按机器分组展示在线优先的列表。还可用来触发父表单的自动选中。

import { useEffect, useMemo, useState } from "react";
import { ChevronDown, Cloud, Loader2, Lock, Search } from "lucide-react";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  isRuntimeUsableForUser,
  runtimeDisplayName,
} from "@multica/core/runtimes";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";
import {
  buildRuntimeMachines,
  filterRuntimeMachines,
  runtimeRowLabel,
} from "../../runtimes/components/runtime-machines";

export type RuntimeFilter = "mine" | "all";

// 运行环境超过这个数量时，纯平铺列表就不好扫了，于是额外提供搜索框。
// 而“按机器分组”只要机器多于一台就独立生效。
const SEARCH_THRESHOLD = 6;

/**
 * 运行环境选择下拉。触发器显示当前选中项；面板内可切换“我的/全部”、搜索，
 * 并把运行环境按机器分组、在线优先排列。任何人不能用的私有机（isRuntimeUsableForUser
 * 判定）会置灰加锁标识。
 * @param runtimes 全部候选运行环境。
 * @param members 工作区成员（用于显示每台机器的拥有者名字）。
 * @param currentUserId 当前用户 id；为 null 时不考虑“我的”筛选。
 * @param selectedRuntimeId 当前选中的 id；空串表示“尚未选中”并会自动播种首个可用项。
 * @param onSelect 选中变化回调。
 */
export function RuntimePicker({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  selectedRuntimeId,
  onSelect,
  disabled = false,
  autoSelect = true,
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  selectedRuntimeId: string;
  onSelect: (id: string) => void;
  /** 选中动作暂不可兑现（如有 builder 回复或运行环境重绑在途）时，禁止打开选择器。 */
  disabled?: boolean;
  /** False when selecting also changes a live session's server-side binding. */
  autoSelect?: boolean;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<RuntimeFilter>("mine");
  const [search, setSearch] = useState("");

  const getOwnerMember = (ownerId: string | null) => {
    if (!ownerId) return null;
    return members.find((m) => m.user_id === ownerId) ?? null;
  };

  const hasOtherRuntimes = runtimes.some((r) => r.owner_id !== currentUserId);

  // 基准列表响应“我的/全部”切换并驱动自动选中；它刻意与搜索框解耦——
  // 这样打字搜索永远不会改变已播种的选中项。
  const filteredRuntimes = useMemo(
    () => computeFilteredRuntimes(runtimes, filter, currentUserId),
    [runtimes, filter, currentUserId],
  );

  // 把（搜索过的）基准列表按机器分组，让 20+ 台机器读起来像几台有名的机器：
  // 在线优先，且当前机器排最前。
  const machines = useMemo(() => {
    const all = buildRuntimeMachines(filteredRuntimes, {
      now: Date.now(),
    });
    return filterRuntimeMachines(all, search, "all");
  }, [filteredRuntimes, search, currentUserId]);

  const showSearch = runtimes.length > SEARCH_THRESHOLD;

  const selectedRuntime =
    runtimes.find((d) => d.id === selectedRuntimeId) ?? null;

  // 父层选中为空时播种的唯一入口：首次挂载还没有模板运行环境、运行环境稍后经 WS
  // 才到达、或切换筛选后集合里没有可用项，都走这里。只在 `selectedRuntimeId === ""`
  // 时触发，保证“复制模式”预填的模板运行环境绝不会被悄悄覆盖。
  useEffect(() => {
    if (!autoSelect || selectedRuntimeId !== "") return;
    const firstUsable = filteredRuntimes.find((r) =>
      isRuntimeUsableForUser(r, currentUserId),
    );
    if (firstUsable) onSelect(firstUsable.id);
  }, [autoSelect, filteredRuntimes, selectedRuntimeId, currentUserId, onSelect]);

  // 切换“我的/全部”时，把选择重算到新集合里的第一个可用项。无匹配就置成 `""`，
  // 此时上面的播种 effect 是空操作（正确：没有可用项可选）。
  const handleFilterChange = (next: RuntimeFilter) => {
    if (next === filter) return;
    setFilter(next);
    if (!autoSelect) return;
    const nextList = computeFilteredRuntimes(runtimes, next, currentUserId);
    const firstUsable = nextList.find((r) =>
      isRuntimeUsableForUser(r, currentUserId),
    );
    onSelect(firstUsable?.id ?? "");
  };

  return (
    <div className="flex flex-col min-w-0">
      <div className="flex h-6 items-center justify-between">
        <Label className="text-caption text-muted-foreground">
          {t(($) => $.create_dialog.runtime_label)}
        </Label>
        {hasOtherRuntimes && (
          // 这两个 tab 不只是“视图筛选”：切换 tab 会把选择重选到新列表里第一个可用项，
          // 因此它是触发 onSelect 的另一条路径，和触发器一样必须尊重 `disabled`。
          <div className="flex items-center gap-0.5 rounded-md bg-muted p-0.5">
            <button
              type="button"
              disabled={disabled}
              onClick={() => handleFilterChange("mine")}
              className={`rounded px-2 py-0.5 text-caption font-medium transition-colors disabled:pointer-events-none disabled:opacity-50 ${
                filter === "mine"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t(($) => $.create_dialog.runtime_filter_mine)}
            </button>
            <button
              type="button"
              disabled={disabled}
              onClick={() => handleFilterChange("all")}
              className={`rounded px-2 py-0.5 text-caption font-medium transition-colors disabled:pointer-events-none disabled:opacity-50 ${
                filter === "all"
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t(($) => $.create_dialog.runtime_filter_all)}
            </button>
          </div>
        )}
      </div>
      <Popover
        open={open && !disabled}
        onOpenChange={(next) => {
          if (disabled) return;
          setOpen(next);
          if (!next) setSearch("");
        }}
      >
        <PopoverTrigger
          disabled={disabled || (runtimes.length === 0 && !runtimesLoading)}
          className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1.5 text-left text-body transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
        >
          {runtimesLoading ? (
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
          ) : selectedRuntime ? (
            <ProviderLogo
              provider={selectedRuntime.provider}
              className="h-4 w-4 shrink-0"
            />
          ) : (
            <Cloud className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">
                {runtimesLoading
                  ? t(($) => $.create_dialog.runtime_loading)
                  : selectedRuntime
                    ? runtimeDisplayName(selectedRuntime)
                    : !autoSelect && runtimes.length > 0
                      ? t(($) => $.model_dropdown.select_runtime_first)
                      : t(($) => $.create_dialog.runtime_none)}
              </span>
              {selectedRuntime?.runtime_mode === "cloud" && (
                <span className="shrink-0 rounded bg-info/10 px-1.5 py-0.5 text-caption font-medium text-info">
                  {t(($) => $.create_dialog.runtime_cloud_badge)}
                </span>
              )}
            </div>
            {selectedRuntime && (
              <div className="truncate text-caption text-muted-foreground">
                {getOwnerMember(selectedRuntime.owner_id)?.name ??
                  selectedRuntime.device_info}
              </div>
            )}
          </div>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
          />
        </PopoverTrigger>
        <PopoverContent
          align="start"
          className="w-[var(--anchor-width)] p-1 flex flex-col max-h-72"
        >
          {showSearch && (
            <div className="relative mb-1 shrink-0">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="text"
                autoFocus
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t(($) => $.create_dialog.runtime_search_placeholder)}
                className="w-full rounded-md border border-border bg-background py-1.5 pl-8 pr-2 text-body outline-none focus:ring-1 focus:ring-ring"
              />
            </div>
          )}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {machines.length === 0 ? (
              <div className="px-3 py-6 text-center text-caption text-muted-foreground">
                {t(($) => $.create_dialog.runtime_no_results)}
              </div>
            ) : (
              machines.map((machine) => (
                <div key={machine.id}>
                  {/* 机器头始终显示——哪怕搜索或单机工作区把它收窄成一组——
                      好让分组样式保持一致，而不是塌缩成扁平列表。 */}
                  <div className="flex items-center justify-between gap-2 px-2 pb-0.5 pt-2 text-micro font-medium text-muted-foreground">
                    <span className="truncate">{machine.title}</span>
                    <span className="shrink-0 tabular-nums">
                      {t(($) => $.create_dialog.runtime_group_online, {
                        online: machine.onlineCount,
                        total: machine.runtimes.length,
                      })}
                    </span>
                  </div>
                  {machine.runtimes.map((device) => {
                    const ownerMember = getOwnerMember(device.owner_id);
                    const disabled = !isRuntimeUsableForUser(
                      device,
                      currentUserId,
                    );
                    const disabledTitle = disabled
                      ? t(($) => $.create_dialog.runtime_private_locked_tooltip)
                      : undefined;
                    return (
                      <button
                        key={device.id}
                        type="button"
                        disabled={disabled}
                        title={disabledTitle}
                        onClick={() => {
                          if (disabled) return;
                          onSelect(device.id);
                          setOpen(false);
                        }}
                        className={`flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-body transition-colors ${
                          disabled
                            ? "cursor-not-allowed opacity-50"
                            : device.id === selectedRuntimeId
                              ? "bg-accent"
                              : "hover:bg-accent/50"
                        }`}
                      >
                        <ProviderLogo
                          provider={device.provider}
                          className="h-4 w-4 shrink-0"
                        />
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2">
                            <span className="truncate font-medium">
                              {runtimeRowLabel(device, machine.title)}
                            </span>
                            {device.runtime_mode === "cloud" && (
                              <span className="shrink-0 rounded bg-info/10 px-1.5 py-0.5 text-caption font-medium text-info">
                                {t(($) => $.create_dialog.runtime_cloud_badge)}
                              </span>
                            )}
                            {disabled && (
                              <span className="shrink-0 inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
                                <Lock className="h-3 w-3" />
                                {t(($) => $.create_dialog.runtime_private_badge)}
                              </span>
                            )}
                          </div>
                          <div className="mt-0.5 flex items-center gap-1 text-caption text-muted-foreground">
                            {ownerMember ? (
                              <>
                                <ActorAvatar
                                  actorType="member"
                                  actorId={ownerMember.user_id}
                                  size="xs"
                                />
                                <span className="truncate">
                                  {ownerMember.name}
                                </span>
                              </>
                            ) : (
                              <span className="truncate">
                                {device.device_info}
                              </span>
                            )}
                          </div>
                        </div>
                        <span
                          className={`h-2 w-2 shrink-0 rounded-full ${
                            device.status === "online"
                              ? "bg-success"
                              : "bg-muted-foreground/40"
                          }`}
                        />
                      </button>
                    );
                  })}
                </div>
              ))
            )}
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

/** 按“我的/全部”过滤，并排序：自己的排前 → 可用的排前（稳定排序）。 */
function computeFilteredRuntimes(
  runtimes: RuntimeDevice[],
  filter: RuntimeFilter,
  currentUserId: string | null,
): RuntimeDevice[] {
  const filtered =
    filter === "mine" && currentUserId
      ? runtimes.filter((r) => r.owner_id === currentUserId)
      : runtimes;
  return filtered.toSorted((a, b) => {
    const aMine = a.owner_id === currentUserId;
    const bMine = b.owner_id === currentUserId;
    if (aMine && !bMine) return -1;
    if (!aMine && bMine) return 1;
    const aUsable = isRuntimeUsableForUser(a, currentUserId);
    const bUsable = isRuntimeUsableForUser(b, currentUserId);
    if (aUsable && !bUsable) return -1;
    if (!aUsable && bUsable) return 1;
    return 0;
  });
}
