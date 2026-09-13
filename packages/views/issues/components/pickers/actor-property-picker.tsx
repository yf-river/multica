"use client";

// 本文件：ActorPropertyPicker —— `actor` / `multi_actor` 自定义属性的
// 成员选择器与只读展示（MUL-6286）。
//
// 形状照着 AssigneePicker 的成员区做——同样的行、同样的头像——但 actor
// 属性只接受成员这一种引用。Agent 和小队可被"分配"，却不可被"引用"：
// agent 需要选择器回答可见性、调用权限等问题，被动引用没资格问；
// 小队则是一个路由目标，不是一个人。
//
// `multi_actor` 就地开关并保持弹层打开（镜像 multi_select）；`actor`
// 提交即关闭。
//
// 开关与展示都对"本构建无法解析的引用种类"保持无损——已装的客户端对着
// 更新的后端时，绝不能因为用户勾了一个成员就把不认识的数据删掉。

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { IssueProperty, IssuePropertyValue } from "@multica/core/types";
import {
  actorRefsFromValue,
  actorRefValuesFromValue,
  formatActorRef,
  MAX_ISSUE_PROPERTY_ACTOR_VALUES,
} from "@multica/core/types";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { ActorAvatar } from "../../../common/actor-avatar";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";
import { PropertyPicker, PickerItem, PickerSection, PickerEmpty } from "./property-picker";

/**
 * 在 `multi_actor` 值内开关一条引用并返回下一个值（`undefined` 清空属性）。
 *
 * 两条必须成立的属性，都承重：
 *
 *   1. `current` 是 RAW 存储列表，因此本构建看不懂的条目原样穿过。
 *      已装客户端对着更新的后端时，绝不能因为用户勾了成员就把它们删掉。
 *   2. 插入顺序得以保留。服务端不会规范化 actor 列表（不像 multi_select），
 *      所以这里的顺序就是最终落库的顺序。
 */
export function toggleActorRefValue(current: string[], key: string): string[] | undefined {
  if (current.includes(key)) {
    const next = current.filter((existing) => existing !== key);
    return next.length === 0 ? undefined : next;
  }
  if (current.length >= MAX_ISSUE_PROPERTY_ACTOR_VALUES) return current;
  return [...current, key];
}

/**
 * `actor` / `multi_actor` 自定义属性的值编辑器（职责见文件头）。
 */
export function ActorPropertyPicker({
  property,
  value,
  onChange,
  open,
  onOpenChange,
  trigger,
  triggerRender,
  emptyRow,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
  onChange: (value: IssuePropertyValue | undefined) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  emptyRow: React.ReactNode;
}) {
  const { t } = useT("issues");
  const [filter, setFilter] = useState("");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  const multiple = property.type === "multi_actor";
  // 用原始字符串而非解析后的引用：本构建不认识的种类（更新的后端扩展了
  // actorPropertyKinds）必须能在这次编辑里存活——不能因为用户开关一个成员
  // 就被悄悄丢掉。
  const selectedRaw = actorRefValuesFromValue(value);
  const selectedKeys = new Set(selectedRaw);
  const atCapacity = multiple && selectedRaw.length >= MAX_ISSUE_PROPERTY_ACTOR_VALUES;

  const query = filter.trim().toLowerCase();
  const matches = (name: string) => name.toLowerCase().includes(query) || matchesPinyin(name, query);
  const filteredMembers = members.filter((m) => matches(m.name));

  const commit = (kind: "member", id: string) => {
    const key = formatActorRef(kind, id);
    if (!multiple) {
      onChange(key);
      onOpenChange(false);
      return;
    }
    onChange(toggleActorRefValue(selectedRaw, key));
  };

  const rowsEmpty = filteredMembers.length === 0;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(next: boolean) => {
        onOpenChange(next);
        if (!next) setFilter("");
      }}
      width="w-64"
      align="start"
      searchable
      searchPlaceholder={t(($) => $.pickers.custom_property.actor_search_placeholder)}
      onSearchChange={setFilter}
      trigger={trigger}
      triggerRender={triggerRender}
    >
      {emptyRow}

      {filteredMembers.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.members_group)}>
          {filteredMembers.map((m) => {
            const key = formatActorRef("member", m.user_id);
            const isSelected = selectedKeys.has(key);
            return (
              <PickerItem
                key={m.user_id}
                selected={isSelected}
                disabled={atCapacity && !isSelected}
                onClick={() => commit("member", m.user_id)}
              >
                <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                <span className="truncate">{m.name}</span>
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {rowsEmpty && <PickerEmpty />}
    </PropertyPicker>
  );
}

/**
 * actor 属性值的只读渲染：每个引用一条"头像 + 名字"。
 *
 * 只装着本构建无法解析的引用的值渲染成"不可用"，*不是*空——渲染成空会
 * 引诱用户去"填"一个其实有值、只是他们看不见的字段。
 */
export function ActorPropertyDisplay({
  value,
  emptyLabel,
}: {
  value: IssuePropertyValue | undefined;
  emptyLabel: React.ReactNode;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const refs = actorRefsFromValue(value);
  if (refs.length === 0) {
    if (actorRefValuesFromValue(value).length > 0) {
      return (
        <span className="text-muted-foreground">
          {t(($) => $.pickers.custom_property.unknown_value)}
        </span>
      );
    }
    return <>{emptyLabel}</>;
  }
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-1">
      {refs.map((ref) => (
        <span
          key={formatActorRef(ref.kind, ref.id)}
          className="inline-flex max-w-32 items-center gap-1"
        >
          <ActorAvatar actorType={ref.kind} actorId={ref.id} size="sm" />
          <span className="truncate">{getActorName(ref.kind, ref.id)}</span>
        </span>
      ))}
    </span>
  );
}
