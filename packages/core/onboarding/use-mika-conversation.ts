import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import { chatKeys } from "../chat/queries";
import { canAssignAgentToIssue } from "../permissions";
import { agentListOptions, memberListOptions } from "../workspace/queries";
import { isMikaAgent } from "./mika";

export function useMikaConversation(workspaceId: string, workspaceSlug: string, sessionId: string | null) {
  const userId = useAuthStore((state) => state.user?.id);
  const queryClient = useQueryClient();
  const agents = useQuery({ ...agentListOptions(workspaceId), enabled: Boolean(workspaceId && userId) });
  const members = useQuery({ ...memberListOptions(workspaceId), enabled: Boolean(workspaceId && userId) });
  const role = members.data?.find((member) => member.user_id === userId)?.role;
  const mika = !agents.isError && !members.isError && role && ["owner", "admin", "member"].includes(role)
    ? agents.data?.find((agent) => isMikaAgent(agent) && !agent.archived_at && canAssignAgentToIssue(agent, { userId: userId || null, role: role as "owner" | "admin" | "member" }).allowed)
    : undefined;
  const session = useQuery({
    queryKey: [...chatKeys.session(workspaceId, sessionId || ""), userId],
    queryFn: () => api.getChatSession(sessionId || ""),
    staleTime: Infinity,
    enabled: Boolean(mika && sessionId && workspaceId && userId),
    retry: false,
  });
  const open = useMutation({
    mutationKey: ["mika-conversation", workspaceId, userId, mika?.id],
    mutationFn: async () => {
      if (!mika || !workspaceSlug || !userId) throw new Error("Mika is not accessible");
      const result = await api.createChatSession({ agent_id: mika.id, title: mika.name, reuse_existing: true }, workspaceSlug);
      if (!result.id || result.agent_id !== mika.id || result.workspace_id !== workspaceId || result.creator_id !== userId) throw new Error("Invalid Mika conversation response");
      return result;
    },
    onSuccess: (result) => {
      queryClient.setQueryData(chatKeys.session(result.workspace_id, result.id), result);
      queryClient.invalidateQueries({ queryKey: chatKeys.sessions(result.workspace_id) });
    },
  });
  return { mika, open, isActive: Boolean(mika && !session.isError && session.data?.agent_id === mika.id && session.data?.workspace_id === workspaceId && session.data?.creator_id === userId) };
}
