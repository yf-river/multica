"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import { memberListOptions, agentListOptions, squadListOptions } from "./queries";
import { resolvePublicFileUrl } from "./avatar-url";

export type ActorNameQueryScope = {
  members?: boolean;
  agents?: boolean;
  squads?: boolean;
};

const DEFAULT_ACTOR_NAME_QUERY_SCOPE: Required<ActorNameQueryScope> = {
  members: true,
  agents: true,
  squads: true,
};

export function useActorName(scope: ActorNameQueryScope = DEFAULT_ACTOR_NAME_QUERY_SCOPE) {
  const wsId = useWorkspaceId();
  const loadMembers = scope.members !== false;
  const loadAgents = scope.agents !== false;
  const loadSquads = scope.squads !== false;
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId && loadMembers,
  });
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: !!wsId && loadAgents,
  });
  const { data: squads = [] } = useQuery({
    ...squadListOptions(wsId),
    enabled: !!wsId && loadSquads,
  });

  const getMemberName = useCallback((userId: string) => {
    const m = members.find((m) => m.user_id === userId);
    return m?.name ?? "未知成员";
  }, [members]);

  const getAgentName = useCallback((agentId: string) => {
    const a = agents.find((a) => a.id === agentId);
    return a?.name ?? "未知智能体";
  }, [agents]);

  const getSquadName = useCallback((squadId: string) => {
    const s = squads.find((s) => s.id === squadId);
    return s?.name ?? "未知小队";
  }, [squads]);

  const getActorName = useCallback((type: string, id: string) => {
    if (type === "member") return getMemberName(id);
    if (type === "agent") return getAgentName(id);
    if (type === "squad") return getSquadName(id);
    if (type === "system") return "Multica";
    return "系统";
  }, [getAgentName, getMemberName, getSquadName]);

  const getActorInitials = useCallback((type: string, id: string) => {
    const name = getActorName(type, id);
    return name
      .split(" ")
      .map((w) => w[0])
      .join("")
      .toUpperCase()
      .slice(0, 2);
  }, [getActorName]);

  const getActorAvatarUrl = useCallback((type: string, id: string): string | null => {
    if (type === "member") return resolvePublicFileUrl(members.find((m) => m.user_id === id)?.avatar_url);
    if (type === "agent") return resolvePublicFileUrl(agents.find((a) => a.id === id)?.avatar_url);
    if (type === "squad") return resolvePublicFileUrl(squads.find((s) => s.id === id)?.avatar_url);
    return null;
  }, [agents, members, squads]);

  return useMemo(
    () => ({
      getMemberName,
      getAgentName,
      getSquadName,
      getActorName,
      getActorInitials,
      getActorAvatarUrl,
    }),
    [
      getActorAvatarUrl,
      getActorInitials,
      getActorName,
      getAgentName,
      getMemberName,
      getSquadName,
    ],
  );
}
