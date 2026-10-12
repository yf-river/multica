"use client";

import { useEffect, useRef } from "react";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import { useMikaConversation } from "@multica/core/onboarding";
import { useWorkspacePaths } from "@multica/core/paths";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { SidebarMenuButton, SidebarMenuItem, useSidebar } from "@multica/ui/components/ui/sidebar";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

export function MikaSidebarEntry({ workspaceId, workspaceSlug }: { workspaceId: string; workspaceSlug: string }) {
  const { t } = useT("layout");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const userId = useAuthStore((state) => state.user?.id);
  const { setOpenMobile } = useSidebar();
  const sessionId = navigation.pathname === paths.chat() ? navigation.searchParams.get("session") : null;
  const { mika, open, isActive } = useMikaConversation(workspaceId, workspaceSlug, sessionId);
  const mounted = useRef(true);
  const opening = useRef(false);
  const current = useRef({ workspaceId, userId, agentId: mika?.id });
  current.current = { workspaceId, userId, agentId: mika?.id };
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  if (!mika) return null;

  const handleOpen = async () => {
    if (opening.current) return;
    opening.current = true;
    const context = current.current;
    try {
      const session = await open.mutateAsync();
      if (!mounted.current || current.current.workspaceId !== context.workspaceId || current.current.agentId !== context.agentId || useAuthStore.getState().user?.id !== context.userId) return;
      setOpenMobile(false);
      navigation.push(paths.chatSession(session.id));
    } catch {
      if (mounted.current && current.current.workspaceId === context.workspaceId) toast.error(t(($) => $.nav.mika_open_failed));
    } finally {
      opening.current = false;
    }
  };
  return <SidebarMenuItem><SidebarMenuButton tooltip={mika.name} aria-label={mika.name} aria-current={isActive ? "page" : undefined} isActive={isActive} disabled={open.isPending} onClick={() => void handleOpen()} className="text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground">
    <ActorAvatar name={mika.name} initials={mika.name.slice(0, 1)} avatarUrl={resolvePublicFileUrl(mika.avatar_url)} isAgent size="xs" />
    <span className="truncate">{mika.name}</span>
  </SidebarMenuButton></SidebarMenuItem>;
}
