"use client";

import { useEffect, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useWorkspaceId } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useChatStore } from "@multica/core/chat";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { companionProfileOptions, useSetCompanionProfile } from "@multica/core/life";
import { PageHeader } from "../../layout/page-header";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

export function CompanionPage() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const profileQuery = useQuery(companionProfileOptions(wsId));
  const agentsQuery = useQuery(agentListOptions(wsId));
  const sessionsQuery = useQuery(chatSessionsOptions(wsId));
  const setProfile = useSetCompanionProfile();
  const setOpen = useChatStore((state) => state.setOpen);
  const setExpanded = useChatStore((state) => state.setExpanded);
  const setSelectedAgentId = useChatStore((state) => state.setSelectedAgentId);
  const setActiveSession = useChatStore((state) => state.setActiveSession);
  const activeSessionId = useChatStore((state) => state.activeSessionId);
  const initializedRef = useRef(false);
  const selectedSessionRef = useRef<string | null | undefined>(undefined);

  const profile = profileQuery.data?.profile ?? null;
  const availableAgents = useMemo(
    () => (agentsQuery.data ?? []).filter((agent) => !agent.archived_at),
    [agentsQuery.data],
  );

  useEffect(() => {
    if (!profile || initializedRef.current) return;
    initializedRef.current = true;
    setSelectedAgentId(profile.agent_id);
    setOpen(true);
    setExpanded(true);
  }, [profile, setExpanded, setOpen, setSelectedAgentId]);

  useEffect(() => {
    if (!profile || sessionsQuery.isLoading) return;
    const sessions = sessionsQuery.data ?? [];
    const activeSession = sessions.find((session) => session.id === activeSessionId);
    const targetSession = activeSession?.agent_id === profile.agent_id
      ? activeSession
      : sessions.find((session) => session.agent_id === profile.agent_id) ?? null;
    const targetSessionId = targetSession?.id ?? null;
    if (targetSessionId === selectedSessionRef.current) return;
    selectedSessionRef.current = targetSessionId;
    if (targetSessionId !== activeSessionId) setActiveSession(targetSessionId);
  }, [activeSessionId, profile, sessionsQuery.data, sessionsQuery.isLoading, setActiveSession]);

  useEffect(() => () => {
    setExpanded(false);
    setOpen(false);
  }, [setExpanded, setOpen]);

  if (profileQuery.isLoading || agentsQuery.isLoading) {
    return <div className="flex h-full items-center justify-center text-sm text-muted-foreground"><Loader2 className="mr-2 size-4 animate-spin" />{t(($) => $.companion.loading)}</div>;
  }

  if (profile) {
    return (
      <div className="flex h-full flex-col bg-background">
        <PageHeader><h1 className="text-sm font-medium">{t(($) => $.companion.title)}</h1></PageHeader>
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">{t(($) => $.companion.opening)}</div>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col bg-background">
      <PageHeader><h1 className="text-sm font-medium">{t(($) => $.companion.title)}</h1></PageHeader>
      <main className="mx-auto w-full max-w-2xl space-y-4 overflow-y-auto p-6">
        <div className="space-y-1">
          <h2 className="text-base font-medium">{t(($) => $.companion.setup_title)}</h2>
          <p className="text-sm text-muted-foreground">{t(($) => $.companion.setup_description)}</p>
        </div>
        {availableAgents.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t(($) => $.companion.empty_agents)}</p>
        ) : (
          <div className="space-y-2">
            {availableAgents.map((agent) => (
              <div key={agent.id} className="flex items-center gap-3 rounded-lg border p-3">
                <ActorAvatar actorType="agent" actorId={agent.id} className="size-8" profileLink={false} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{agent.name}</div>
                  <div className="truncate text-xs text-muted-foreground">{agent.description}</div>
                </div>
                <Button size="sm" disabled={setProfile.isPending} onClick={() => setProfile.mutate(agent.id)}>
                  {t(($) => $.companion.confirm)}
                </Button>
              </div>
            ))}
          </div>
        )}
      </main>
    </div>
  );
}
