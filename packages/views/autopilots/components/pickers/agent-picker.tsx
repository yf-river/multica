// autopilot 执行者选择器：在智能体与小队之间二选一（autopilot 由谁运行）。
// 搜索支持名称与拼音；未绑定运行时（runtime）的智能体/小队不可选，带原因提示。

"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bot } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import type { AutopilotAssigneeType } from "@multica/core/types";
import { ActorAvatar } from "../../../common/actor-avatar";
import {
  PropertyPicker,
  PickerItem,
  PickerSection,
  PickerEmpty,
} from "../../../issues/components/pickers/property-picker";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";

/** 一次执行者选择：类型（智能体/小队）+ 其 ID。 */
export interface AssigneeSelection {
  type: AutopilotAssigneeType;
  id: string;
}

/**
 * autopilot 执行者选择器。弹层分组列出可用的智能体与小队（已归档的排除），
 * 未绑定运行时的条目禁用并附 tooltip 解释；触发器显示当前选中项的头像与名称。
 * @param assignee 当前选择（类型 + ID），null 表示未选。
 * @param onChange 选中回调。
 * @param trigger 自定义触发器内容。
 * @param triggerRender 自定义触发器元素。
 * @param align 弹层对齐方式。
 */
export function AgentPicker({
  assignee,
  onChange,
  trigger: customTrigger,
  triggerRender,
  align = "start",
}: {
  assignee: AssigneeSelection | null;
  onChange: (next: AssigneeSelection) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  align?: "start" | "center" | "end";
}) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));

  const activeAgents = useMemo(() => agents.filter((a) => !a.archived_at), [agents]);
  const activeSquads = useMemo(() => squads.filter((s) => !s.archived_at), [squads]);
  const agentsById = useMemo(
    () => new Map(activeAgents.map((agent) => [agent.id, agent])),
    [activeAgents],
  );

  // 解析当前选中项对应的实体，用于触发器展示（名称/头像）与可用性判断。
  const selectedAgent =
    assignee?.type === "agent" ? activeAgents.find((a) => a.id === assignee.id) : undefined;
  const selectedSquad =
    assignee?.type === "squad" ? activeSquads.find((s) => s.id === assignee.id) : undefined;
  const selectedName = selectedAgent?.name ?? selectedSquad?.name;

  const query = filter.trim().toLowerCase();
  const matches = (name: string) =>
    !query || name.toLowerCase().includes(query) || matchesPinyin(name, query);
  const filteredAgents = activeAgents.filter((a) => matches(a.name));
  const filteredSquads = activeSquads.filter((s) => matches(s.name));

  const isSelected = (type: AutopilotAssigneeType, id: string) =>
    assignee?.type === type && assignee?.id === id;

  const handlePick = (type: AutopilotAssigneeType, id: string) => {
    onChange({ type, id });
    setOpen(false);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-56"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.agent_picker.filter_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      trigger={
        customTrigger ?? (
          <>
            {assignee && (selectedAgent || selectedSquad) ? (
              <>
                <ActorAvatar
                  actorType={assignee.type}
                  actorId={assignee.id}
                  size="sm"
                  showStatusDot={assignee.type === "agent"}
                />
                <span className="truncate">{selectedName}</span>
              </>
            ) : (
              <>
                <Bot className="size-3" />
                <span>{t(($) => $.agent_picker.select_assignee)}</span>
              </>
            )}
          </>
        )
      }
    >
      {filteredAgents.length === 0 && filteredSquads.length === 0 ? (
        <PickerEmpty />
      ) : (
        <>
          {filteredAgents.length > 0 && (
            <PickerSection label={t(($) => $.agent_picker.agents_group)}>
              {filteredAgents.map((a) => {
                const runtimeBound = isAgentRuntimeBound(a);
                return (
                  <PickerItem
                    key={a.id}
                    selected={isSelected("agent", a.id)}
                    disabled={!runtimeBound}
                    tooltip={
                      runtimeBound
                        ? undefined
                        : t(($) => $.agent_picker.agent_runtime_required)
                    }
                    onClick={() => handlePick("agent", a.id)}
                  >
                    <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                    <span className="truncate">{a.name}</span>
                  </PickerItem>
                );
              })}
            </PickerSection>
          )}
          {filteredSquads.length > 0 && (
            <PickerSection label={t(($) => $.agent_picker.squads_group)}>
              {filteredSquads.map((s) => {
                // 小队可用与否取决于它的队长是否绑定了运行时。
                const leader = agentsById.get(s.leader_id);
                const runtimeBound = !!leader && isAgentRuntimeBound(leader);
                return (
                  <PickerItem
                    key={s.id}
                    selected={isSelected("squad", s.id)}
                    disabled={!runtimeBound}
                    tooltip={
                      runtimeBound
                        ? undefined
                        : t(($) => $.agent_picker.squad_runtime_required)
                    }
                    onClick={() => handlePick("squad", s.id)}
                  >
                    <ActorAvatar actorType="squad" actorId={s.id} size="sm" />
                    <span className="truncate">{s.name}</span>
                  </PickerItem>
                );
              })}
            </PickerSection>
          )}
        </>
      )}
    </PropertyPicker>
  );
}
