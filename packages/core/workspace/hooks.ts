"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../paths";
import { memberListOptions, agentListOptions, squadListOptions } from "./queries";
import { resolvePublicFileUrl } from "./avatar-url";
import { nameInitials } from "./actor-display";
import type { Agent, MemberWithUser, Squad } from "../types";

type ActorNameQueryScope = {
  members?: boolean;
  agents?: boolean;
  squads?: boolean;
};

const DEFAULT_ACTOR_NAME_QUERY_SCOPE: Required<ActorNameQueryScope> = {
  members: true,
  agents: true,
  squads: true,
};
const EMPTY_MEMBERS: MemberWithUser[] = [];
const EMPTY_AGENTS: Agent[] = [];
const EMPTY_SQUADS: Squad[] = [];

export function useActorName(scope: ActorNameQueryScope = DEFAULT_ACTOR_NAME_QUERY_SCOPE) {
  const wsId = useWorkspaceId();
  const loadMembers = scope.members !== false;
  const loadAgents = scope.agents !== false;
  const loadSquads = scope.squads !== false;
  const { data: members = EMPTY_MEMBERS } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId && loadMembers,
  });
  const { data: agents = EMPTY_AGENTS } = useQuery({
    ...agentListOptions(wsId),
    enabled: !!wsId && loadAgents,
  });
  const { data: squads = EMPTY_SQUADS } = useQuery({
    ...squadListOptions(wsId),
    enabled: !!wsId && loadSquads,
  });

  const getActorName = useCallback((type: string, id: string) => {
    if (type === "member") {
      return members.find((member) => member.user_id === id)?.name ?? "未知成员";
    }
    if (type === "agent") {
      return agents.find((agent) => agent.id === id)?.name ?? "未知智能体";
    }
    if (type === "squad") {
      return squads.find((squad) => squad.id === id)?.name ?? "未知小队";
    }
    if (type === "system") return "Multica";
    return "系统";
  }, [agents, members, squads]);

  const getActorInitials = useCallback((type: string, id: string) => {
    return nameInitials(getActorName(type, id));
  }, [getActorName]);

  const getActorAvatarUrl = useCallback((type: string, id: string): string | null => {
    if (type === "member") return resolvePublicFileUrl(members.find((m) => m.user_id === id)?.avatar_url);
    if (type === "agent") return resolvePublicFileUrl(agents.find((a) => a.id === id)?.avatar_url);
    if (type === "squad") return resolvePublicFileUrl(squads.find((s) => s.id === id)?.avatar_url);
    return null;
  }, [agents, members, squads]);

  return useMemo(
    () => ({
      getActorName,
      getActorInitials,
      getActorAvatarUrl,
    }),
    [
      getActorAvatarUrl,
      getActorInitials,
      getActorName,
    ],
  );
}
