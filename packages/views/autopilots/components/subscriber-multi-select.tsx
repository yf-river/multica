// 订阅者多选控件：选择哪些成员会收到该 autopilot 的通知。已选成员以可删除的
// chip 列出，加号触发器打开搜索选择器（支持按名称与拼音匹配）。完全受控——父级
// 持有选择状态并随创建/更新请求提交。刻意仅限成员（按 RFC，MUL-2533）。

"use client";

import { useMemo, useState } from "react";
import { Plus, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  PropertyPicker,
  PickerItem,
  PickerEmpty,
} from "../../issues/components/pickers/property-picker";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

/**
 * 订阅成员多选。已选成员渲染成带移除按钮的 chip；"＋"打开搜索选择器选人。
 * 搜索同时匹配显示名与拼音。
 * @param selectedIds 当前已选成员的用户 ID 列表。
 * @param onChange 选择变化时以新的完整列表回调。
 */
export function SubscriberMultiSelect({
  selectedIds,
  onChange,
}: {
  /** 当前已选订阅成员的用户 ID。 */
  selectedIds: ReadonlyArray<string>;
  /** 选择变化时回调新完整列表。 */
  onChange: (next: string[]) => void;
}) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);

  const query = filter.trim().toLowerCase();
  const filteredMembers = useMemo(
    () =>
      members.filter(
        (m) =>
          query === "" ||
          m.name.toLowerCase().includes(query) ||
          matchesPinyin(m.name, query),
      ),
    [members, query],
  );

  // 已选成员按其成员顺序排列（不做全量重排，保证 chip 顺序稳定）。
  const selectedMembers = useMemo(
    () => members.filter((m) => selectedSet.has(m.user_id)),
    [members, selectedSet],
  );

  // 在选择器里勾选/取消一个成员。
  const toggle = (userId: string) => {
    if (selectedSet.has(userId)) {
      onChange(selectedIds.filter((id) => id !== userId));
    } else {
      onChange([...selectedIds, userId]);
    }
  };

  // 在 chip 上移除一个成员。
  const remove = (userId: string) => {
    onChange(selectedIds.filter((id) => id !== userId));
  };

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {selectedMembers.map((m) => (
        <span
          key={m.user_id}
          className="inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 text-caption"
        >
          <ActorAvatar actorType="member" actorId={m.user_id} size="xs" />
          <span className="max-w-[10rem] truncate">{m.name}</span>
          <button
            type="button"
            onClick={() => remove(m.user_id)}
            className="text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            aria-label={t(($) => $.dialog.subscribers_remove_tooltip)}
          >
            <X className="size-3" />
          </button>
        </span>
      ))}
      <PropertyPicker
        open={open}
        onOpenChange={(v) => {
          setOpen(v);
          if (!v) setFilter("");
        }}
        width="w-64"
        align="start"
        searchable
        searchPlaceholder={t(($) => $.dialog.subscribers_search_placeholder)}
        onSearchChange={setFilter}
        trigger={
          <span
            className={cn(
              "inline-flex items-center gap-1 rounded-full border border-dashed px-2 py-0.5 text-caption text-muted-foreground",
              "hover:border-primary/40 hover:text-foreground transition-colors cursor-pointer",
            )}
          >
            <Plus className="size-3" />
            {t(($) => $.dialog.subscribers_add)}
          </span>
        }
      >
        {filteredMembers.length === 0 ? (
          <PickerEmpty />
        ) : (
          filteredMembers.map((m) => (
            <PickerItem
              key={m.user_id}
              selected={selectedSet.has(m.user_id)}
              onClick={() => toggle(m.user_id)}
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))
        )}
      </PropertyPicker>
    </div>
  );
}
