"use client";

// 本文件：AssigneePicker —— 问题"负责人"选择器。
// 可选范围 = 工作区成员 + Agent + 小队（squad），并可按分配频率排序、
// 支持拼音匹配搜索。
//
// 数据订阅较重（成员/Agent/小队/分配频率四个查询），因此在看板卡片、
// 列表行等高频场景走"延迟触发器"策略：自定义触发器内容的调用方在首次交互前
// 不挂载真实选择器，由 `DeferredPopup` 提供像素一致的仿触发器；
// 无自定义触发器的调用方保持急切挂载（默认触发器本身就要订阅 getActorName）。
//
// 分配权限判定统一收敛到 `canAssignAgentToIssue` Decision API；
// 本文件里的 `canAssignAgent` 只是给尚未迁移的旧调用方保留的兼容壳。

import { useMemo, useState } from "react";
import { Lock, UserMinus } from "lucide-react";
import type { Agent, IssueAssigneeType, UpdateIssueRequest } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions, squadListOptions, assigneeFrequencyOptions } from "@multica/core/workspace/queries";
import { ActorAvatar } from "../../../common/actor-avatar";
import { DeferredPopup } from "../../../common/deferred-popup";
import {
  PropertyPicker,
  PickerItem,
  PickerSection,
  PickerEmpty,
  PICKER_TRIGGER_CLASS,
} from "./property-picker";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";

/**
 * 兼容壳：保留给还没迁移到新 `canAssignAgentToIssue` Decision API 的调用方
 * （例如 `use-issue-actions.ts`）。内部直接转发到权威规则，保证行为同步。
 */
export function canAssignAgent(
  agent: Agent,
  userId: string | undefined,
  memberRole: string | undefined,
): boolean {
  return canAssignAgentToIssue(agent, {
    userId: userId ?? null,
    role: memberRole === "owner" || memberRole === "admin" || memberRole === "member"
      ? memberRole
      : null,
  }).allowed;
}

interface AssigneePickerProps {
  assigneeType: IssueAssigneeType | null;
  assigneeId: string | null;
  /**
   * `true` 表示批量选择跨越了不同负责人（"混合"）：此时没有任何一行被勾选，
   * 包括"未分配"行。这与 `assigneeType` / `assigneeId` 同为 `null` 不同——
   * 后者意味着每个选中问题都确实未分配，此时"未分配"行应当被勾选。
   */
  mixed?: boolean;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
}

/**
 * 挂载真实选择器会订阅成员/Agent/小队/频率查询——在每张看板卡片、每个列表
 * 行上成倍累积曾让切换标签页卡顿。自带触发器内容、非受控的调用方因此改用
 * 延迟仿触发器，选择器在首次交互时才挂载。默认触发器本身需要 `getActorName`
 * （一个成员/Agent 订阅），所以无自定义触发器的调用方保持急切挂载。
 */
export function AssigneePicker(props: AssigneePickerProps) {
  const hasDeferredTriggerContent =
    props.trigger !== undefined || props.triggerRender?.props.children != null;
  const canDefer =
    props.open === undefined &&
    props.onOpenChange === undefined &&
    hasDeferredTriggerContent;
  if (!canDefer) {
    return <AssigneePickerImpl {...props} />;
  }
  return (
    <DeferredPopup
      trigger={props.trigger}
      triggerRender={props.triggerRender}
      triggerClassName={PICKER_TRIGGER_CLASS}
    >
      {(open, onOpenChange) => (
        <AssigneePickerImpl {...props} open={open} onOpenChange={onOpenChange} />
      )}
    </DeferredPopup>
  );
}

function AssigneePickerImpl({
  assigneeType,
  assigneeId,
  mixed = false,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
}: AssigneePickerProps) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const [filter, setFilter] = useState("");
  const user = useAuthStore((s) => s.user);
  const wsId = useWorkspaceId();
  const pickerDataEnabled = open && !!wsId;
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: pickerDataEnabled,
  });
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: pickerDataEnabled,
  });
  const { data: squads = [] } = useQuery({
    ...squadListOptions(wsId),
    enabled: pickerDataEnabled,
  });
  const { data: frequency = [] } = useQuery({
    ...assigneeFrequencyOptions(wsId),
    enabled: pickerDataEnabled,
  });
  const shouldResolveTriggerLabel =
    !customTrigger && !triggerRender && !!assigneeType && !!assigneeId;
  const { getActorName } = useActorName({
    members: shouldResolveTriggerLabel && assigneeType === "member",
    agents: shouldResolveTriggerLabel && assigneeType === "agent",
    squads: shouldResolveTriggerLabel && assigneeType === "squad",
    plugins: false,
  });

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;

  // 由频率数据构建查找表，供排序使用。
  const freqMap = useMemo(() => {
    const map = new Map<string, number>();
    for (const entry of frequency) {
      map.set(`${entry.assignee_type}:${entry.assignee_id}`, entry.frequency);
    }
    return map;
  }, [frequency]);

  const getFreq = (type: string, id: string) => freqMap.get(`${type}:${id}`) ?? 0;

  const query = filter.trim().toLowerCase();
  const filteredMembers = members
    .filter((m) => m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query))
    .sort((a, b) => getFreq("member", b.user_id) - getFreq("member", a.user_id));
  const filteredAgents = agents
    .filter((a) => !a.archived_at && (a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query)))
    .sort((a, b) => getFreq("agent", b.id) - getFreq("agent", a.id));
  const filteredSquads = squads
    .filter((s) => !s.archived_at && (s.name.toLowerCase().includes(query) || matchesPinyin(s.name, query)))
    .sort((a, b) => getFreq("squad", b.id) - getFreq("squad", a.id));
  const runnableAgentIds = new Set(
    agents
      .filter((agent) => !agent.archived_at && isAgentRuntimeBound(agent))
      .map((agent) => agent.id),
  );

  const isSelected = (type: string, id: string) =>
    assigneeType === type && assigneeId === id;

  const triggerLabel =
    assigneeType && assigneeId
      ? getActorName(assigneeType, assigneeId)
      : t(($) => $.pickers.assignee.trigger_unassigned);

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v: boolean) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
      width="w-64"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.pickers.assignee.search_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      trigger={
        customTrigger ? customTrigger : assigneeType && assigneeId ? (
          <>
            <ActorAvatar actorType={assigneeType} actorId={assigneeId} size="sm" enableHoverCard showStatusDot />
            <span className="truncate">{triggerLabel}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
        )
      }
    >
      {/* 未分配 —— 始终是第一行，搜索开不开都在。App 里每个选择器都把空值
          放在那里，所以"清空此字段"的动作永不挪窝。 */}
      <PickerItem
        emptyValue
        selected={!mixed && !assigneeType && !assigneeId}
        onClick={() => {
          onUpdate({ assignee_type: null, assignee_id: null });
          setOpen(false);
        }}
      >
        <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
      </PickerItem>

      {/* 成员 */}
      {filteredMembers.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.members_group)}>
          {filteredMembers.map((m) => (
            <PickerItem
              key={m.user_id}
              selected={isSelected("member", m.user_id)}
              onClick={() => {
                onUpdate({
                  assignee_type: "member",
                  assignee_id: m.user_id,
                });
                setOpen(false);
              }}
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {/* Agent 分组 */}
      {filteredAgents.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.agents_group)}>
          {filteredAgents.map((a) => {
            const decision = canAssignAgentToIssue(a, {
              userId: user?.id ?? null,
              role:
                memberRole === "owner" ||
                memberRole === "admin" ||
                memberRole === "member"
                  ? memberRole
                  : null,
            });
            const runtimeBound = isAgentRuntimeBound(a);
            const allowed = decision.allowed && runtimeBound;
            return (
              <PickerItem
                key={a.id}
                selected={isSelected("agent", a.id)}
                disabled={!allowed}
                tooltip={
                  !decision.allowed
                    ? decision.message
                    : !runtimeBound
                      ? t(($) => $.pickers.assignee.agent_runtime_required)
                      : undefined
                }
                onClick={() => {
                  if (!allowed) return;
                  onUpdate({
                    assignee_type: "agent",
                    assignee_id: a.id,
                  });
                  setOpen(false);
                }}
              >
                <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                <span className={`truncate ${allowed ? "" : "text-muted-foreground"}`}>{a.name}</span>
                {a.visibility === "private" && (
                  <Lock className="ml-auto h-3 w-3 text-muted-foreground" />
                )}
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {/* 小队 —— 小组归属；分配给小队时由后端路由给它的 lead agent。 */}
      {filteredSquads.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.squads_group)}>
          {filteredSquads.map((s) => {
            const runtimeBound = runnableAgentIds.has(s.leader_id);
            return (
              <PickerItem
                key={s.id}
                selected={isSelected("squad", s.id)}
                disabled={!runtimeBound}
                tooltip={
                  runtimeBound
                    ? undefined
                    : t(($) => $.pickers.assignee.squad_runtime_required)
                }
                onClick={() => {
                  if (!runtimeBound) return;
                  onUpdate({
                    assignee_type: "squad",
                    assignee_id: s.id,
                  });
                  setOpen(false);
                }}
              >
                <ActorAvatar actorType="squad" actorId={s.id} size="sm" />
                <span className="truncate">{s.name}</span>
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {filteredMembers.length === 0 &&
        filteredAgents.length === 0 &&
        filteredSquads.length === 0 &&
        filter && <PickerEmpty />}
    </PropertyPicker>
  );
}
