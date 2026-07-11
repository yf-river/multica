"use client";

import { useMemo, useState } from "react";
import { Lock, UserMinus } from "lucide-react";
import type { IssueAssigneeType, UpdateIssueRequest } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions, squadListOptions, assigneeFrequencyOptions } from "@multica/core/workspace/queries";
import { ActorAvatar } from "../../../common/actor-avatar";
import {
  PropertyPicker,
  PickerItem,
  PickerSection,
  PickerEmpty,
} from "./property-picker";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";

export function AssigneePicker({
  assigneeType,
  assigneeId,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  triggerNativeButton,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
}: {
  assigneeType: IssueAssigneeType | null;
  assigneeId: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  triggerNativeButton?: boolean;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
}) {
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
  const shouldResolveTriggerLabel = !customTrigger && !triggerRender && !!assigneeType && !!assigneeId;
  const { getActorName } = useActorName({
    members: shouldResolveTriggerLabel && assigneeType === "member",
    agents: shouldResolveTriggerLabel && assigneeType === "agent",
    squads: shouldResolveTriggerLabel && assigneeType === "squad",
  });

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;

  // Build a lookup map from frequency data for sorting.
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
      triggerNativeButton={triggerNativeButton}
      trigger={
        customTrigger ? customTrigger : assigneeType && assigneeId ? (
          <>
            <ActorAvatar actorType={assigneeType} actorId={assigneeId} size={18} enableHoverCard showStatusDot />
            <span className="truncate">{triggerLabel}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
        )
      }
    >
      {/* Unassigned option — hidden when search is active */}
      {!query && (
        <PickerItem
          selected={!assigneeType && !assigneeId}
          onClick={() => {
            onUpdate({ assignee_type: null, assignee_id: null });
            setOpen(false);
          }}
        >
          <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
        </PickerItem>
      )}

      {/* Members */}
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
              <ActorAvatar actorType="member" actorId={m.user_id} size={18} />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {/* Agents */}
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
            const allowed = decision.allowed;
            return (
              <PickerItem
                key={a.id}
                selected={isSelected("agent", a.id)}
                disabled={!allowed}
                tooltip={!allowed ? decision.message : undefined}
                onClick={() => {
                  if (!allowed) return;
                  onUpdate({
                    assignee_type: "agent",
                    assignee_id: a.id,
                  });
                  setOpen(false);
                }}
              >
                <ActorAvatar actorType="agent" actorId={a.id} size={18} showStatusDot />
                <span className={`truncate ${allowed ? "" : "text-muted-foreground"}`}>{a.name}</span>
                {a.scope === "personal" && (
                  <Lock className="ml-auto h-3 w-3 text-muted-foreground" />
                )}
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {/* Squads — group ownership; assigning to a squad routes the issue to
          its leader agent on the backend. */}
      {filteredSquads.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.squads_group)}>
          {filteredSquads.map((s) => (
            <PickerItem
              key={s.id}
              selected={isSelected("squad", s.id)}
              onClick={() => {
                onUpdate({
                  assignee_type: "squad",
                  assignee_id: s.id,
                });
                setOpen(false);
              }}
            >
              <ActorAvatar actorType="squad" actorId={s.id} size={18} />
              <span className="truncate">{s.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {filteredMembers.length === 0 &&
        filteredAgents.length === 0 &&
        filteredSquads.length === 0 &&
        filter && <PickerEmpty />}
    </PropertyPicker>
  );
}
