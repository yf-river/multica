// 新建聊天按钮与 agent 选择器：通过 ⊕ 按钮弹出可选 agent 列表，开始一段新聊天。
"use client";

import React, { useMemo, useState } from "react";
import { Plus } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  PickerEmpty,
  PickerItem,
  PickerSection,
  PropertyPicker,
} from "../../issues/components/pickers/property-picker";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import type { Agent } from "@multica/core/types";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { toast } from "sonner";
import { useT } from "../../i18n";

/**
 * Agent 选择器：一个放在 PropertyPicker 里的可搜索、分组（我的 agent / 其他人）的 agent 列表。
 * 触发元素由调用方提供。`currentAgentId` 会给某个 agent 打上勾选标记——
 * 当没有当前选中项需要高亮时省略它（“新建聊天”就是这种情况）。
 * @param agents - 可选 agent 列表。
 * @param userId - 当前用户 ID，用于区分“我的/其他人”。
 * @param currentAgentId - 当前选中的 agent ID（可选）。
 * @param onSelect - 选中 agent 后的回调。
 * @param trigger - 触发元素内容。
 * @param triggerRender - 触发元素的外壳组件。
 * @param side - 弹出方向。
 * @param align - 对齐方式。
 */
export function AgentPicker({
  agents,
  userId,
  currentAgentId,
  onSelect,
  trigger,
  triggerRender,
  side = "bottom",
  align = "start",
}: {
  agents: Agent[];
  userId: string | undefined;
  currentAgentId?: string;
  onSelect: (agent: Agent) => void;
  trigger: React.ReactNode;
  triggerRender: React.ReactElement;
  side?: "top" | "bottom";
  align?: "start" | "center" | "end";
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const { mine, others } = useMemo(() => {
    const mine: Agent[] = [];
    const others: Agent[] = [];
    for (const a of agents) {
      if (a.owner_id === userId) mine.push(a);
      else others.push(a);
    }
    return { mine, others };
  }, [agents, userId]);

  const query = filter.trim().toLowerCase();
  const matches = (name: string) =>
    !query || name.toLowerCase().includes(query) || matchesPinyin(name, query);
  const filteredMine = mine.filter((agent) => matches(agent.name));
  const filteredOthers = others.filter((agent) => matches(agent.name));

  const handlePick = (agent: Agent) => {
    onSelect(agent);
    setOpen(false);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-64"
      align={align}
      side={side}
      searchable
      searchPlaceholder={t(($) => $.window.agent_filter_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      trigger={trigger}
    >
      {filteredMine.length === 0 && filteredOthers.length === 0 ? (
        <PickerEmpty />
      ) : (
        <>
          {filteredMine.length > 0 && (
            <PickerSection label={t(($) => $.window.my_agents)}>
              {filteredMine.map((agent) => (
                <AgentPickerItem
                  key={agent.id}
                  agent={agent}
                  isCurrent={agent.id === currentAgentId}
                  onSelect={handlePick}
                />
              ))}
            </PickerSection>
          )}
          {filteredOthers.length > 0 && (
            <PickerSection label={t(($) => $.window.others)}>
              {filteredOthers.map((agent) => (
                <AgentPickerItem
                  key={agent.id}
                  agent={agent}
                  isCurrent={agent.id === currentAgentId}
                  onSelect={handlePick}
                />
              ))}
            </PickerSection>
          )}
        </>
      )}
    </PropertyPicker>
  );
}

function AgentPickerItem({
  agent,
  isCurrent,
  onSelect,
}: {
  agent: Agent;
  isCurrent: boolean;
  onSelect: (agent: Agent) => void;
}) {
  const { t } = useT("chat");
  const runtimeBound = isAgentRuntimeBound(agent);
  return (
    <PickerItem
      selected={isCurrent}
      disabled={!runtimeBound}
      tooltip={
        runtimeBound ? undefined : t(($) => $.window.agent_needs_runtime_hint)
      }
      onClick={() => onSelect(agent)}
    >
      <ActorAvatar
        actorType="agent"
        actorId={agent.id}
        size="md"
        enableHoverCard
        showStatusDot
      />
      <span className="truncate flex-1">{agent.name}</span>
      {!runtimeBound && (
        <span className="shrink-0 text-micro text-amber-600 dark:text-amber-400">
          {t(($) => $.window.agent_needs_runtime)}
        </span>
      )}
    </PickerItem>
  );
}

/**
 * “新建聊天”的 ⊕ 按钮。按 Chat V2 设计，开始新聊天就是选择 agent 的时机——
 * 所以它会打开 AgentPicker 并通过 `onStart` 上报所选。没有 agent 被预勾选：
 * 新聊天还没有“当前”agent。快捷逻辑：只有一个可用 agent 时立即开始（没必要
 * 显示只有一个选项的菜单）；一个都没有时仍触发 `onStart(null)`，
 * 让界面显示它的无 agent 空状态。
 * @param agents - 可选 agent 列表。
 * @param userId - 当前用户 ID。
 * @param onStart - 开始新聊天的回调（agent 可为 null）。
 * @param side - 弹出方向。
 */
export function NewChatButton({
  agents,
  userId,
  onStart,
  side = "bottom",
}: {
  agents: Agent[];
  userId: string | undefined;
  onStart: (agent: Agent | null) => void;
  side?: "top" | "bottom";
}) {
  const { t } = useT("chat");
  const label = t(($) => $.window.new_chat_tooltip);

  if (agents.length <= 1) {
    const only = agents[0] ?? null;
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className="rounded-full text-muted-foreground"
              aria-label={label}
              onClick={() => {
                if (only && !isAgentRuntimeBound(only)) {
                  toast.error(t(($) => $.input.runtime_required_toast));
                  return;
                }
                onStart(only);
              }}
            />
          }
        >
          <Plus />
        </TooltipTrigger>
        <TooltipContent side={side === "top" ? "top" : "bottom"}>{label}</TooltipContent>
      </Tooltip>
    );
  }

  return (
    <AgentPicker
      agents={agents}
      userId={userId}
      onSelect={(agent) => onStart(agent)}
      side={side}
      align="start"
      triggerRender={
        <Button
          variant="ghost"
          size="icon-sm"
          className="rounded-full text-muted-foreground"
          aria-label={label}
        />
      }
      trigger={<Plus />}
    />
  );
}
