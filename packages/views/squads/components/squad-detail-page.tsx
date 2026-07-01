"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { isImeComposing } from "@multica/core/utils";
import { useTimeAgo } from "../../i18n";
import { agentListOptions, memberListOptions, squadMemberStatusOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import { CreateAgentDialog } from "../../agents/components/create-agent-dialog";
import { useNavigation } from "../../navigation";
import { AppLink } from "../../navigation";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { PageHeader } from "../../layout/page-header";
import { AlertCircle, Archive, ArchiveRestore, Users, Plus, Trash2, ArrowUpRight, Crown, Camera, Loader2, Pencil, FileText, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { ActorAvatar } from "../../common/actor-avatar";
import { ObservabilitySummaryCard } from "../../common/observability-summary-card";
import { ContentEditor } from "../../editor/content-editor";
import {
  PickerItem,
  PickerSection,
  PickerEmpty,
} from "../../issues/components/pickers/property-picker";
import { ChevronDown, UserPlus } from "lucide-react";
import { toast } from "sonner";
import type { Squad, SquadMember, SquadMemberStatus, SquadMemberStatusValue, Agent, CreateAgentRequest, MemberWithUser } from "@multica/core/types";
import { useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { sopStageDisplayName } from "../../common/sop-stage-labels";

type SquadSOPProfile = {
  profile_key?: string;
  project: string;
  repo: string;
  mode: string;
  roles?: Array<Record<string, unknown>>;
  steps?: Array<Record<string, unknown>>;
  model_policy?: Record<string, unknown>;
  stage_skills: string[];
  operation_skills: string[];
  acceptance: string[];
  archive_policy?: string;
  forbidden_actions?: string[];
};

const USER_CENTER_SOP_PROFILE: SquadSOPProfile = {
  profile_key: "generic-project-sop-flow",
  project: "<target-project>",
  repo: "<target-repo-from-project-resource>",
  mode: "stage_chain",
  roles: [
    { key: "pm", name: "PM", responsibility: "接收 issue/TAPD 输入，读取项目资源和 source_context，检查阶段产物，处理阻断，推进 pm -> 01-clarify -> 02-design -> 03-task-split -> 04-implement -> 05-verify。" },
    { key: "01-clarify", name: "01-需求澄清", responsibility: "执行目标项目的 01-clarify，明确需求边界、验收口径、目标仓库、可用/缺失 operation skill 和 handoff。" },
    { key: "02-design", name: "02-方案设计", responsibility: "执行目标项目的 02-design，输出方案、影响面、接口/数据契约、项目 skill 调用计划和 handoff。" },
    { key: "03-task-split", name: "03-任务拆分", responsibility: "执行目标项目的 03-task-split，输出任务拆分、跨项目依赖、operation graph 和 handoff。" },
    { key: "04-implement", name: "04-开发", responsibility: "执行目标项目的 04-implement，按边界和对应项目 operation skill 实现并保留证据。" },
    { key: "05-verify", name: "05-测试", responsibility: "执行目标项目的 05-verify，独立验证、总结证据和最终 handoff。" },
  ],
  steps: [
    { key: "pm", name: "PM 调度", role_key: "pm" },
    { key: "01-clarify", name: "01-需求澄清", role_key: "01-clarify", skill: "<target-project>/01-clarify" },
    { key: "02-design", name: "02-方案设计", role_key: "02-design", skill: "<target-project>/02-design" },
    { key: "03-task-split", name: "03-任务拆分", role_key: "03-task-split", skill: "<target-project>/03-task-split" },
    { key: "04-implement", name: "04-开发", role_key: "04-implement", skill: "<target-project>/04-implement" },
    { key: "05-verify", name: "05-测试", role_key: "05-verify", skill: "<target-project>/05-verify" },
  ],
  stage_skills: [
    "<target-project>/01-clarify",
    "<target-project>/02-design",
    "<target-project>/03-task-split",
    "<target-project>/04-implement",
    "<target-project>/05-verify",
  ],
  operation_skills: ["<target-project>/<operation-skill>"],
  acceptance: ["阶段产物完整", "测试证据完整", "交接说明明确"],
  archive_policy: "06-archive 不作为必跑阶段；最终结论、证据摘要和 handoff 状态由 05-verify 输出。",
  forbidden_actions: ["跳过验收直接完成", "缺少测试证据时宣称完成", "未确认目标项目就调用项目 skill", "把 06-archive 当作必跑验收阶段"],
};

const MULTICA_CODING_SOP_PROFILE: SquadSOPProfile = {
  profile_key: "multica-coding",
  project: "multica",
  repo: "/data/ida/goal-test",
  mode: "coding_squad",
  roles: [
    {
      key: "captain",
      name: "队长",
      responsibility: "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。",
      boundary: "只做调度和最终汇总，不代替验收者宣布通过。",
      input: "宏观目标、上下文、约束、验收要求。",
      output: "任务拆分、角色分派、进度和风险记录。",
      forbidden: "不泄露密钥，不跳过方案确认。",
    },
    {
      key: "designer",
      name: "方案设计者",
      responsibility: "编写技术方案、影响面、任务拆解、测试方案。",
      boundary: "方案先给人看，确认后再开发。",
      input: "需求、代码现状、历史文档。",
      output: "中文技术方案、验收清单、风险点。",
      forbidden: "不直接落大范围代码改动。",
    },
    {
      key: "developer",
      name: "开发者",
      responsibility: "按分配范围改代码，包括前端、后端、测试或部署中的一块。",
      boundary: "只改自己负责的文件和模块。",
      input: "已确认方案、任务边界、相关测试。",
      output: "代码改动、局部验证结果。",
      forbidden: "不能随手改别人负责的范围。",
    },
    {
      key: "acceptor",
      name: "验收者",
      responsibility: "独立检查代码、测试结果、漏改和回归风险。",
      boundary: "不参与原实现的自证，独立给结论。",
      input: "diff、测试日志、验收条件。",
      output: "验收记录、缺陷清单、放行或退回结论。",
      forbidden: "开发者不能自己说通过。",
    },
    {
      key: "spec-maintainer",
      name: "规约维护者",
      responsibility: "判断是否同步流程文档、测试数据说明、接口索引、技能说明。",
      boundary: "只维护与本次改动相关的规约材料。",
      input: "代码 diff、接口变化、流程变化。",
      output: "文档同步记录、未同步原因。",
      forbidden: "不让文档停留在旧版本。",
    },
    {
      key: "operator",
      name: "部署运行者",
      responsibility: "负责端口、环境变量、数据库、启动服务、健康检查、部署验证。",
      boundary: "只做运行环境和验证，不改业务代码。",
      input: "部署目标、环境变量说明、健康检查命令。",
      output: "启动记录、健康检查、端口和服务状态。",
      forbidden: "不能泄露密钥，不能随便改业务代码。",
    },
  ],
  steps: [
    { key: "receive", name: "接收需求", role_key: "captain" },
    { key: "design_review", name: "方案设计与确认", role_key: "designer" },
    { key: "implementation", name: "分工开发", role_key: "developer" },
    { key: "independent_acceptance", name: "独立验收", role_key: "acceptor" },
    { key: "spec_sync", name: "规约同步", role_key: "spec-maintainer" },
    { key: "deploy_verify", name: "部署运行验证", role_key: "operator" },
    { key: "final_report", name: "证据汇总", role_key: "captain" },
  ],
  model_policy: {
    默认模型: "minimax",
    代码测试复杂审查: "gpt",
    策略说明: "minimax 用于大批量普通执行；涉及代码、测试、复杂审查时使用 gpt。",
  },
  stage_skills: [],
  operation_skills: [],
  acceptance: ["方案经确认", "代码范围清晰", "验收者独立给结论", "测试证据完整", "规约同步或说明无需同步", "运行验证完成"],
  forbidden_actions: ["泄露密钥", "开发者越权改范围外代码", "未独立验收就完成", "跳过测试证据", "文档接口语义停留在旧版本"],
};

export function SquadDetailPage() {
  const { t } = useT("squads");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { pathname, push } = useNavigation();
  const queryClient = useQueryClient();
  const squadId = pathname.split("/").pop() ?? "";

  const { data: squad, refetch: refetchSquad } = useQuery<Squad>({
    queryKey: [...workspaceKeys.squads(wsId), squadId],
    queryFn: () => api.getSquad(squadId),
    enabled: !!workspace?.id && !!squadId,
  });

  const { data: members = [], refetch: refetchMembers } = useQuery<SquadMember[]>({
    queryKey: [...workspaceKeys.squads(wsId), squadId, "members"],
    queryFn: () => api.listSquadMembers(squadId),
    enabled: !!workspace?.id && !!squadId,
  });

  // Per-squad working/idle/offline + active-issue snapshot. WS task / agent /
  // daemon events invalidate this via use-realtime-sync; the staleTime is a
  // tab-focus safety net. Indexed by member_id so SquadMembersTab can look up
  // its row in O(1).
  const { data: memberStatusResp } = useQuery({
    ...squadMemberStatusOptions(wsId, squadId),
    enabled: !!workspace?.id && !!squadId,
  });
  const memberStatusById = useMemo(() => {
    const map = new Map<string, SquadMemberStatus>();
    for (const s of memberStatusResp?.members ?? []) map.set(s.member_id, s);
    return map;
  }, [memberStatusResp]);

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: wsMembers = [] } = useQuery(memberListOptions(wsId));

  // Runtimes are only fetched when the Create Agent dialog might open;
  // gating on isWorkspaceAdmin below means non-admins never trigger the
  // request. The runtime list mirrors the agents page so the picker
  // (and the "only my runtimes" filter) behaves identically here.
  const currentUser = useAuthStore((s) => s.user);
  const myRole = useMemo(() => {
    if (!currentUser) return null;
    return wsMembers.find((m) => m.user_id === currentUser.id)?.role ?? null;
  }, [wsMembers, currentUser]);
  const isWorkspaceAdmin = myRole === "owner" || myRole === "admin";

  const { data: runtimes = [], isLoading: runtimesLoading } = useQuery({
    ...runtimeListOptions(wsId),
    enabled: !!wsId && isWorkspaceAdmin,
  });

  const [showAddMember, setShowAddMember] = useState(false);
  const [showCreateAgent, setShowCreateAgent] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);

  const updateSquadMut = useMutation({
    mutationFn: (data: { name?: string; description?: string; instructions?: string; avatar_url?: string; leader_id?: string; sop_profile?: Record<string, unknown> }) => api.updateSquad(squadId, data),
    onSuccess: () => {
      refetchSquad();
      refetchMembers();
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
    },
  });

  const addMemberMut = useMutation({
    mutationFn: (input: { type: "agent" | "member"; id: string; role?: string }) =>
      api.addSquadMember(squadId, {
        member_type: input.type,
        member_id: input.id,
        role: input.role?.trim() || undefined,
      }),
    onSuccess: () => { refetchMembers(); toast.success("Member added"); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : "Failed to add member"),
  });

  const removeMemberMut = useMutation({
    mutationFn: (m: SquadMember) => api.removeSquadMember(squadId, { member_type: m.member_type, member_id: m.member_id }),
    onSuccess: () => { refetchMembers(); toast.success("Member removed"); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : "Failed to remove member"),
  });

  const updateRoleMut = useMutation({
    mutationFn: (input: { member: SquadMember; role: string }) =>
      api.updateSquadMemberRole(squadId, {
        member_type: input.member.member_type,
        member_id: input.member.member_id,
        role: input.role,
      }),
    onSuccess: () => { refetchMembers(); toast.success("Role updated"); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : "Failed to update role"),
  });

  const setLeaderMut = useMutation({
    mutationFn: (agentId: string) => api.updateSquad(squadId, { leader_id: agentId }),
    onSuccess: () => {
      refetchSquad();
      refetchMembers();
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      toast.success("Leader updated");
    },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : "Failed to update leader"),
  });

  const deleteMut = useMutation({
    mutationFn: () => api.deleteSquad(squadId),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) }); push(p.squads()); toast.success(t(($) => $.archive_dialog.success)); },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : "归档小队失败"),
  });

  const restoreMut = useMutation({
    mutationFn: () => api.restoreSquad(squadId),
    onSuccess: () => {
      refetchSquad();
      refetchMembers();
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      toast.success(t(($) => $.archive_dialog.restore_success));
    },
    onError: (err) =>
      toast.error(err instanceof Error && err.message ? err.message : t(($) => $.archive_dialog.restore_failed)),
  });

  // CreateAgentDialog's onCreate contract: hit POST /api/agents and
  // return the created agent so the dialog can run its skill follow-up.
  // We deliberately do NOT navigate to the agent detail page (that's
  // the agents-page behaviour) — the user clicked Create Agent from
  // inside this squad, so the dialog will stay open just long enough
  // to also call addSquadMember (handled by the dialog when squadId
  // is set), then close the user back to Members where they can
  // verify the new agent appeared. Cache-update keeps the agents list
  // fresh for any pickers that read from it.
  const handleCreateAgent = async (data: CreateAgentRequest): Promise<Agent> => {
    const agent = await api.createAgent(data);
    queryClient.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (current = []) => {
      const exists = current.some((a) => a.id === agent.id);
      return exists ? current.map((a) => (a.id === agent.id ? agent : a)) : [...current, agent];
    });
    queryClient.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    return agent;
  };

  const getEntityName = (type: string, id: string) => {
    if (type === "agent") {
      const agentName = agents.find((a: Agent) => a.id === id)?.name;
      return sopStageDisplayName(agentName) || id.slice(0, 8);
    }
    return wsMembers.find((m) => m.user_id === id)?.name ?? id.slice(0, 8);
  };

  if (!squad) {
    return <SquadDetailSkeleton />;
  }

  const availableAgents = agents.filter((a: Agent) => !a.archived_at && !members.some((m) => m.member_type === "agent" && m.member_id === a.id));
  const availableMembers = wsMembers.filter((m) => !members.some((sm) => sm.member_type === "member" && sm.member_id === m.user_id));
  const isLeader = (m: SquadMember) => m.member_type === "agent" && squad.leader_id === m.member_id;
  const isArchived = (m: SquadMember) =>
    m.member_type === "agent" && !!agents.find((a: Agent) => a.id === m.member_id)?.archived_at;
  const canManageSquad = isWorkspaceAdmin || squad.creator_id === currentUser?.id;
  const isSquadArchived = !!squad.archived_at;
  const canEditSquad = canManageSquad && !isSquadArchived;

  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <BreadcrumbHeader
        segments={[{ href: p.squads(), label: t(($) => $.page.title) }]}
        leaf={
          <>
            <SquadHeaderAvatar squad={squad} initials={initials} />
            <h1 className="truncate text-sm font-medium text-foreground">{squad.name}</h1>
          </>
        }
        actions={canManageSquad ? (
          isSquadArchived ? (
            <Button size="sm" variant="outline" disabled={restoreMut.isPending} onClick={() => restoreMut.mutate()}>
              {restoreMut.isPending ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <ArchiveRestore className="size-3.5" />
              )}
              {t(($) => $.inspector.restore_button)}
            </Button>
          ) : (
            <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setConfirmArchive(true)}>
              <Archive className="size-3.5 mr-1" />
              {t(($) => $.inspector.archive_button)}
            </Button>
          )
        ) : null}
      />

      {isSquadArchived && (
        <div className="flex shrink-0 items-center gap-2 border-b bg-muted/50 px-6 py-2 text-xs text-muted-foreground">
          <AlertCircle className="h-3.5 w-3.5 shrink-0" />
          <span className="flex-1">
            {t(($) => $.inspector.archived_banner)}
          </span>
          {canManageSquad && (
            <Button
              variant="outline"
              size="sm"
              className="h-6 text-xs"
              disabled={restoreMut.isPending}
              onClick={() => restoreMut.mutate()}
            >
              {t(($) => $.inspector.restore_button)}
            </Button>
          )}
        </div>
      )}

      {/* Two-column grid mirrors agent-detail-page: left inspector (identity +
          properties + leader), right pane with tabs (Members | Instructions).
          Mobile collapses to stacked single column. */}
      <div className="flex flex-1 min-h-0 flex-col gap-3 overflow-y-auto p-3 md:grid md:grid-cols-[280px_minmax(0,1fr)] md:gap-4 md:overflow-hidden md:p-6 lg:grid-cols-[320px_minmax(0,1fr)]">
        <SquadDetailInspector
          squad={squad}
          memberCount={members.length}
          leaderName={getEntityName("agent", squad.leader_id)}
          creatorName={getEntityName("member", squad.creator_id)}
          canManage={canEditSquad}
          uploadingAvatar={updateSquadMut.isPending}
          onUploadAvatar={(url) => updateSquadMut.mutateAsync({ avatar_url: url })}
          onRename={async (next) => { await updateSquadMut.mutateAsync({ name: next.trim() }); }}
          onUpdateDescription={async (next) => { await updateSquadMut.mutateAsync({ description: next }); }}
        />

        <SquadOverviewPane
          squad={squad}
          members={members}
          memberStatusById={memberStatusById}
          isLeader={isLeader}
          isArchived={isArchived}
          canManage={canEditSquad}
          getEntityName={getEntityName}
          onAddMemberClick={() => setShowAddMember(true)}
          onCreateAgentClick={canEditSquad ? () => setShowCreateAgent(true) : undefined}
          onSetLeader={(id) => setLeaderMut.mutate(id)}
          onRemoveMember={(m) => removeMemberMut.mutate(m)}
          onUpdateRole={async (m, role) => { await updateRoleMut.mutateAsync({ member: m, role }); }}
          onSaveInstructions={async (next) => { await updateSquadMut.mutateAsync({ instructions: next }); toast.success("小队指令已保存"); }}
          onApplyUserCenterSOP={async () => { await updateSquadMut.mutateAsync({ sop_profile: USER_CENTER_SOP_PROFILE }); toast.success("用户中心 SOP 已应用"); }}
          onApplyMulticaCodingSOP={async () => { await updateSquadMut.mutateAsync({ sop_profile: MULTICA_CODING_SOP_PROFILE }); toast.success("Multica 编码小队模板已应用"); }}
          setLeaderPending={setLeaderMut.isPending}
        />
      </div>

      {showAddMember && canEditSquad && (
        <AddMemberDialog
          availableMembers={availableMembers}
          availableAgents={availableAgents}
          onClose={() => setShowAddMember(false)}
          onSubmit={async (input) => { await addMemberMut.mutateAsync(input); }}
        />
      )}

      {/* Squad-scoped create flow: same dialog as the Agents page but
          with squadId set, so the dialog runs api.addSquadMember after
          api.createAgent and skips the agent-detail navigation. Only
          mounted for workspace owner/admin since AddSquadMember is
          owner/admin-gated server-side; for everyone else the trigger
          never renders. */}
      {showCreateAgent && canEditSquad && (
        <CreateAgentDialog
          runtimes={runtimes}
          runtimesLoading={runtimesLoading}
          members={wsMembers}
          currentUserId={currentUser?.id ?? null}
          squadId={squadId}
          onClose={() => setShowCreateAgent(false)}
          onCreate={handleCreateAgent}
        />
      )}

      {confirmArchive && (
        <AlertDialog
          open
          onOpenChange={(v) => { if (!v && !deleteMut.isPending) setConfirmArchive(false); }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t(($) => $.archive_dialog.title)}</AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.archive_dialog.description, { name: squad.name })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={deleteMut.isPending}>
                {t(($) => $.archive_dialog.cancel)}
              </AlertDialogCancel>
              <AlertDialogAction
                onClick={() => deleteMut.mutate()}
                disabled={deleteMut.isPending}
                className="bg-destructive text-white hover:bg-destructive/90"
              >
                {deleteMut.isPending
                  ? t(($) => $.archive_dialog.archiving)
                  : t(($) => $.archive_dialog.confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

// Initial-load skeleton — mirrors the two-column layout of the loaded page
// (left inspector + right tabs panel) so the swap to real content doesn't
// shift layout. Column widths match the md:/lg: breakpoints used below.
function SquadDetailSkeleton() {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="px-5">
        <Skeleton className="h-5 w-48" />
      </PageHeader>
      <div className="flex flex-1 min-h-0 flex-col gap-3 overflow-y-auto p-3 md:grid md:grid-cols-[280px_minmax(0,1fr)] md:gap-4 md:overflow-hidden md:p-6 lg:grid-cols-[320px_minmax(0,1fr)]">
        <div className="flex flex-col gap-4 rounded-lg border p-5">
          <Skeleton className="h-16 w-16 rounded-lg" />
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-3 w-full" />
          <div className="space-y-2">
            <Skeleton className="h-3 w-3/4" />
            <Skeleton className="h-3 w-2/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
        <div className="flex flex-col gap-4 rounded-lg border p-6">
          <div className="flex items-center gap-4">
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-4 w-24" />
          </div>
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-5/6" />
          <Skeleton className="h-4 w-4/6" />
        </div>
      </div>
    </div>
  );
}

// Compact 16px avatar shown next to the name in the page header. Falls back
// to the Users icon when no custom avatar is set so the squad still has a
// recognisable glyph in the breadcrumb strip.
function SquadHeaderAvatar({ squad, initials }: { squad: Squad; initials: string }) {
  if (!squad.avatar_url) {
    return <Users className="h-4 w-4 text-muted-foreground" />;
  }
  return (
    <ActorAvatarBase
      name={squad.name}
      initials={initials}
      avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
      size={16}
      className="rounded"
    />
  );
}

// Large click-to-upload avatar editor. Mirrors AvatarEditor in
// agent-detail-inspector.tsx — square (rounded-md) treatment is reserved
// for non-human actors (agent, squad), circles for humans.
function SquadAvatarEditor({
  squad,
  initials,
  uploading,
  onUpload,
}: {
  squad: Squad;
  initials: string;
  uploading: boolean;
  onUpload: (url: string) => Promise<unknown>;
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const { upload, uploading: fileUploading } = useFileUpload(api);
  const busy = uploading || fileUploading;

  const handleFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    e.target.value = "";
    try {
      const result = await upload(file);
      if (!result) return;
      await onUpload(result.link);
      toast.success("Avatar updated");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to upload avatar");
    }
  };

  return (
    <>
      <button
        type="button"
        className="group relative h-16 w-16 shrink-0 overflow-hidden rounded-lg bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        onClick={() => fileInputRef.current?.click()}
        disabled={busy}
        aria-label="Change squad avatar"
      >
        {squad.avatar_url ? (
          <ActorAvatarBase
            name={squad.name}
            initials={initials}
            avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
            size={64}
            className="rounded-none"
          />
        ) : (
          <div className="flex h-full w-full items-center justify-center text-muted-foreground">
            <Users className="h-7 w-7" />
          </div>
        )}
        <div className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition-opacity group-hover:opacity-100">
          {busy ? (
            <Loader2 className="h-4 w-4 animate-spin text-white" />
          ) : (
            <Camera className="h-4 w-4 text-white" />
          )}
        </div>
      </button>
      <input
        ref={fileInputRef}
        type="file"
        accept="image/*"
        className="hidden"
        onChange={handleFile}
      />
    </>
  );
}

// Inline name editor — reveals a Pencil affordance on hover, opens a small
// popover with a single-line input. Mirrors the NameAndDescription editor
// in the agent inspector.
function SquadNameEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
}) {
  return (
    <InlineEditPopover
      value={value}
      onSave={onSave}
      title="重命名小队"
      placeholder="小队名称"
      validate={(v) => (v.trim().length > 0 ? null : "请输入名称")}
    >
      {(triggerProps) => (
        <button
          type="button"
          {...triggerProps}
          className="group -mx-1 inline-flex items-center gap-1.5 self-start rounded px-1 text-left text-lg font-semibold leading-tight transition-colors hover:bg-accent/50"
        >
          <span>{value}</span>
          <Pencil className="h-3.5 w-3.5 shrink-0 text-muted-foreground/0 transition-colors group-hover:text-muted-foreground" />
        </button>
      )}
    </InlineEditPopover>
  );
}

function InlineEditPopover({
  value,
  onSave,
  title,
  placeholder,
  validate,
  children,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
  title: string;
  placeholder?: string;
  validate?: (v: string) => string | null;
  children: (triggerProps: { onClick: (e: React.MouseEvent) => void }) => ReactNode;
}) {
  const { t } = useT("squads");
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setDraft(value);
      setError(null);
    }
  }, [open, value]);

  const commit = async () => {
    const err = validate?.(draft) ?? null;
    if (err) {
      setError(err);
      return;
    }
    if (draft === value) {
      setOpen(false);
      return;
    }
    setSaving(true);
    try {
      await onSave(draft);
      setOpen(false);
      toast.success("Saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={children({ onClick: () => setOpen(true) }) as React.ReactElement}
      />
      <PopoverContent align="start" className="w-72 p-3">
        <div className="space-y-2">
          <p className="text-xs font-medium">{title}</p>
          <Input
            autoFocus
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              if (error) setError(null);
            }}
            placeholder={placeholder}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setOpen(false);
                return;
              }
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                void commit();
              }
            }}
            className="h-8"
          />
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={saving}>
              {t(($) => $.name_editor.cancel)}
            </Button>
            <Button size="sm" onClick={() => void commit()} disabled={saving || draft === value}>
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : "Save"}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// Two-step add-member dialog (mirrors CreateAgentDialog's compact layout):
// 1) pick a target — Members + Agents in one searchable popover, each row
//    with an avatar so visual recognition matches the issue assignee picker;
// 2) optionally describe the role they'll play in this squad. Description
//    lives here (not on the picker) because role is per-squad context that
//    only makes sense at the moment of joining.
function AddMemberDialog({
  availableMembers,
  availableAgents,
  onClose,
  onSubmit,
}: {
  availableMembers: MemberWithUser[];
  availableAgents: Agent[];
  onClose: () => void;
  onSubmit: (input: { type: "agent" | "member"; id: string; role?: string }) => Promise<void>;
}) {
  const { t } = useT("squads");
  const [target, setTarget] = useState<{ type: "agent" | "member"; id: string; name: string } | null>(null);
  const [role, setRole] = useState("");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerFilter, setPickerFilter] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const query = pickerFilter.trim().toLowerCase();
  const filteredMembers = availableMembers.filter((m) => m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query));
  const filteredAgents = availableAgents.filter((a) => a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query));

  const canSubmit = !!target && !submitting;

  const handleSubmit = async () => {
    if (!target) return;
    setSubmitting(true);
    try {
      await onSubmit({ type: target.type, id: target.id, role });
      onClose();
    } catch {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.add_member_dialog.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.add_member_dialog.description)}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 min-w-0">
          <div>
            <Label className="text-xs text-muted-foreground">{t(($) => $.add_member_dialog.label_member)}</Label>
            <Popover open={pickerOpen} onOpenChange={(v) => { setPickerOpen(v); if (!v) setPickerFilter(""); }}>
              <PopoverTrigger className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1 text-left text-sm transition-colors hover:bg-muted">
                {target ? (
                  <ActorAvatar actorType={target.type} actorId={target.id} size={20} />
                ) : (
                  <UserPlus className="h-4 w-4 shrink-0 text-muted-foreground" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">
                    {target?.name ?? "Select a member or agent"}
                  </div>
                  {target && (
                    <div className="truncate text-xs text-muted-foreground capitalize">{target.type}</div>
                  )}
                </div>
                <ChevronDown className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${pickerOpen ? "rotate-180" : ""}`} />
              </PopoverTrigger>
              <PopoverContent align="start" className="w-[var(--anchor-width)] p-0">
                <div className="px-2 py-1.5 border-b">
                  <input
                    autoFocus
                    type="text"
                    value={pickerFilter}
                    onChange={(e) => setPickerFilter(e.target.value)}
                    placeholder="搜索成员或智能体..."
                    className="w-full bg-transparent text-sm placeholder:text-muted-foreground outline-none"
                  />
                </div>
                <div className="p-1 max-h-72 overflow-y-auto">
                  {filteredMembers.length > 0 && (
                    <PickerSection label="成员">
                      {filteredMembers.map((m) => (
                        <PickerItem
                          key={m.user_id}
                          selected={target?.type === "member" && target.id === m.user_id}
                          onClick={() => {
                            setTarget({ type: "member", id: m.user_id, name: m.name });
                            setPickerOpen(false);
                            setPickerFilter("");
                          }}
                        >
                          <ActorAvatar actorType="member" actorId={m.user_id} size={18} />
                          <span>{m.name}</span>
                        </PickerItem>
                      ))}
                    </PickerSection>
                  )}
                  {filteredAgents.length > 0 && (
                    <PickerSection label="智能体">
                      {filteredAgents.map((a) => (
                        <PickerItem
                          key={a.id}
                          selected={target?.type === "agent" && target.id === a.id}
                          onClick={() => {
                            setTarget({ type: "agent", id: a.id, name: a.name });
                            setPickerOpen(false);
                            setPickerFilter("");
                          }}
                        >
                          <ActorAvatar actorType="agent" actorId={a.id} size={18} showStatusDot />
                          <span>{a.name}</span>
                        </PickerItem>
                      ))}
                    </PickerSection>
                  )}
                  {filteredMembers.length === 0 && filteredAgents.length === 0 && <PickerEmpty />}
                </div>
              </PopoverContent>
            </Popover>
          </div>

          <div>
            <Label className="text-xs text-muted-foreground">
              {t(($) => $.add_member_dialog.label_role)}{" "}
              <span className="text-muted-foreground/60">{t(($) => $.add_member_dialog.label_optional)}</span>
            </Label>
            <Input
              type="text"
              value={role}
              onChange={(e) => setRole(e.target.value)}
              placeholder="e.g. Reviewer, Frontend Lead"
              className="mt-1"
              onKeyDown={(e) => {
                if (isImeComposing(e)) return;
                if (e.key === "Enter" && canSubmit) void handleSubmit();
              }}
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>{t(($) => $.add_member_dialog.cancel)}</Button>
          <Button onClick={() => void handleSubmit()} disabled={!canSubmit}>
            {submitting ? <Loader2 className="size-3.5 animate-spin" /> : "Add"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Inline click-to-edit role line. Renders the current role as muted text;
// click (or click the placeholder when empty) to swap in an input that
// commits on blur / Enter and cancels on Escape. Avoids opening a modal
// for what is usually a one-word change.
function RoleEditor({ value, onSave }: { value: string; onSave: (next: string) => Promise<void> }) {
  const { t } = useT("squads");
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);

  useEffect(() => { if (!editing) setDraft(value); }, [value, editing]);

  const commit = async () => {
    const next = draft.trim();
    if (next === value.trim()) { setEditing(false); return; }
    setSaving(true);
    try {
      await onSave(next);
      setEditing(false);
    } catch {
      // toast handled by mutation
    } finally {
      setSaving(false);
    }
  };

  if (editing) {
    return (
      <Input
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => void commit()}
        onKeyDown={(e) => {
          if (isImeComposing(e)) return;
          if (e.key === "Enter") void commit();
          else if (e.key === "Escape") { setDraft(value); setEditing(false); }
        }}
        disabled={saving}
        placeholder="Role (e.g. Reviewer)"
        className="h-6 mt-0.5 text-xs px-1.5"
      />
    );
  }

  return (
    <button
      type="button"
      onClick={() => setEditing(true)}
      className="text-xs text-muted-foreground mt-0.5 text-left hover:text-foreground transition-colors"
    >
      {value || <span className="italic opacity-60">{t(($) => $.add_member_dialog.placeholder_role_inline)}</span>}
    </button>
  );
}

// ---------------------------------------------------------------------------
// SquadDetailInspector — left 320px column, mirrors AgentDetailInspector.
// Holds identity (avatar / name / description) + leader / member count /
// timestamps. All inline-editable.
// ---------------------------------------------------------------------------
function SquadDetailInspector({
  squad,
  memberCount,
  leaderName,
  creatorName,
  canManage,
  uploadingAvatar,
  onUploadAvatar,
  onRename,
  onUpdateDescription,
}: {
  squad: Squad;
  memberCount: number;
  leaderName: string;
  creatorName: string;
  canManage: boolean;
  uploadingAvatar: boolean;
  onUploadAvatar: (url: string) => Promise<unknown>;
  onRename: (next: string) => Promise<void>;
  onUpdateDescription: (next: string) => Promise<void>;
}) {
  const { t } = useT("squads");
  const timeAgo = useTimeAgo();
  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  return (
    <aside className="flex w-full flex-col rounded-lg border bg-background md:h-full md:min-h-0 md:overflow-y-auto">
      {/* Identity */}
      <div className="flex flex-col gap-3 border-b px-5 pb-5 pt-5">
        <SquadAvatarEditor
          squad={squad}
          initials={initials}
          uploading={uploadingAvatar}
          onUpload={onUploadAvatar}
        />
        <div className="flex flex-col gap-1">
          {canManage ? (
            <>
              <SquadNameEditor value={squad.name} onSave={onRename} />
              <SquadDescriptionEditor
                value={squad.description ?? ""}
                onSave={onUpdateDescription}
              />
            </>
          ) : (
            <>
              <div className="text-sm font-medium">{squad.name}</div>
              {squad.description ? (
                <div className="text-xs text-muted-foreground">{squad.description}</div>
              ) : null}
            </>
          )}
        </div>
      </div>

      {/* Details — read-only */}
      <div className="border-b px-5 py-4">
        <div className="mb-1 -mx-2 px-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
          {t(($) => $.inspector.details_section)}
        </div>
        <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5">
          <InspectorRow label="队长">
            <span className="flex min-w-0 items-center gap-1.5">
              <ActorAvatar actorType="agent" actorId={squad.leader_id} size={14} />
              <span className="truncate">{leaderName}</span>
            </span>
          </InspectorRow>
          <InspectorRow label="成员">
            <span className="text-muted-foreground tabular-nums">{memberCount}</span>
          </InspectorRow>
          <InspectorRow label="可见性">
            <span className="text-muted-foreground">
              {squad.visibility === "personal"
                ? t(($) => $.page.visibility_personal)
                : t(($) => $.page.visibility_workspace)}
            </span>
          </InspectorRow>
          <InspectorRow label="创建者">
            <span className="flex min-w-0 items-center gap-1.5">
              <ActorAvatar actorType="member" actorId={squad.creator_id} size={14} />
              <span className="truncate">{creatorName}</span>
            </span>
          </InspectorRow>
          <InspectorRow label="创建时间">
            <span className="text-muted-foreground">{timeAgo(squad.created_at)}</span>
          </InspectorRow>
          <InspectorRow label="更新时间">
            <span className="text-muted-foreground">{timeAgo(squad.updated_at)}</span>
          </InspectorRow>
        </div>
      </div>
  </aside>
  );
}

function InspectorRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <div className="px-2 py-1 text-xs text-muted-foreground">{label}</div>
      <div className="min-w-0 px-2 py-1 text-xs">{children}</div>
    </>
  );
}

// Click-to-edit description editor for the inspector. Mirrors
// agent-detail-inspector's DescriptionEditor: opens a modal with a textarea
// (enough room for multi-paragraph descriptions); the inline trigger shows
// the current value (or a placeholder) with a hover-revealed Pencil.
function SquadDescriptionEditor({
  value,
  onSave,
}: {
  value: string;
  onSave: (next: string) => Promise<void>;
}) {
  const { t } = useT("squads");
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="group -mx-1 inline-flex items-start gap-1.5 self-start rounded px-1 text-left text-xs leading-relaxed transition-colors hover:bg-accent/50"
      >
        {value ? (
          <span className="text-muted-foreground">{value}</span>
        ) : (
          <span className="italic text-muted-foreground/50">{t(($) => $.description_dialog.placeholder_empty)}</span>
        )}
        <Pencil className="mt-0.5 h-3 w-3 shrink-0 text-muted-foreground/0 transition-colors group-hover:text-muted-foreground" />
      </button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-lg">
          {open && (
            <SquadDescriptionEditorBody
              initialValue={value}
              onSave={onSave}
              onClose={() => setOpen(false)}
            />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

function SquadDescriptionEditorBody({
  initialValue,
  onSave,
  onClose,
}: {
  initialValue: string;
  onSave: (next: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useT("squads");
  const [draft, setDraft] = useState(initialValue);
  const [saving, setSaving] = useState(false);
  const dirty = draft !== initialValue;

  const commit = async () => {
    if (!dirty) { onClose(); return; }
    setSaving(true);
    try {
      await onSave(draft);
      onClose();
    } catch {
      // toast handled by parent's mutation
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t(($) => $.description_dialog.title)}</DialogTitle>
      </DialogHeader>
      <textarea
        autoFocus
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder="What is this squad responsible for?"
        rows={6}
        onKeyDown={(e) => {
          if (e.key === "Escape") { onClose(); return; }
          if (isImeComposing(e)) return;
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            void commit();
          }
        }}
        className="w-full resize-none rounded-md border bg-transparent px-3 py-2 text-sm outline-none focus-visible:border-input"
      />
      <DialogFooter>
        <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>{t(($) => $.description_dialog.cancel)}</Button>
        <Button size="sm" onClick={() => void commit()} disabled={saving || !dirty}>
          {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : "Save"}
        </Button>
      </DialogFooter>
    </>
  );
}

// ---------------------------------------------------------------------------
// SquadOverviewPane — right column with two tabs (Members | Instructions).
// Mirrors AgentOverviewPane: dirty-guard via AlertDialog when switching tabs
// with unsaved Instructions.
// ---------------------------------------------------------------------------
type SquadDetailTab = "members" | "instructions";

const squadDetailTabs: { id: SquadDetailTab; label: string; icon: typeof FileText }[] = [
  { id: "members", label: "成员", icon: Users },
  { id: "instructions", label: "指令", icon: FileText },
];

function SquadOverviewPane({
  squad,
  members,
  memberStatusById,
  isLeader,
  isArchived,
  canManage,
  getEntityName,
  onAddMemberClick,
  onCreateAgentClick,
  onSetLeader,
  onRemoveMember,
  onUpdateRole,
  onSaveInstructions,
  onApplyUserCenterSOP,
  onApplyMulticaCodingSOP,
  setLeaderPending,
}: {
  squad: Squad;
  members: SquadMember[];
  memberStatusById: Map<string, SquadMemberStatus>;
  isLeader: (m: SquadMember) => boolean;
  isArchived: (m: SquadMember) => boolean;
  canManage: boolean;
  getEntityName: (type: string, id: string) => string;
  onAddMemberClick: () => void;
  // Optional — only passed when the current user can manage the squad
  // (workspace owner/admin). Hidden otherwise so plain members don't
  // see a button they can't action.
  onCreateAgentClick?: () => void;
  onSetLeader: (agentId: string) => void;
  onRemoveMember: (m: SquadMember) => void;
  onUpdateRole: (m: SquadMember, role: string) => Promise<void>;
  onSaveInstructions: (next: string) => Promise<void>;
  onApplyUserCenterSOP: () => Promise<void>;
  onApplyMulticaCodingSOP: () => Promise<void>;
  setLeaderPending: boolean;
}) {
  const { t } = useT("squads");
  const [activeTab, setActiveTab] = useState<SquadDetailTab>("members");
  const [activeDirty, setActiveDirty] = useState(false);
  const [pendingTab, setPendingTab] = useState<SquadDetailTab | null>(null);

  const requestTabChange = (next: SquadDetailTab) => {
    if (next === activeTab) return;
    if (activeDirty) { setPendingTab(next); return; }
    setActiveTab(next);
  };

  const commitTabChange = () => {
    if (pendingTab) {
      setActiveTab(pendingTab);
      setActiveDirty(false);
      setPendingTab(null);
    }
  };

  return (
    <div className="flex min-h-[60vh] flex-col overflow-hidden rounded-lg border bg-background md:h-full md:min-h-0">
      <div className="flex shrink-0 items-center gap-0 overflow-x-auto border-b px-2 md:px-4">
        {squadDetailTabs.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => requestTabChange(tab.id)}
            className={`flex shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2.5 text-xs font-medium transition-colors ${
              activeTab === tab.id
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            <tab.icon className="h-3.5 w-3.5" />
            {tab.label}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto">
        {activeTab === "members" && (
          <div className="flex h-full flex-col p-4 md:p-6">
            <SquadMembersTab
              members={members}
              memberStatusById={memberStatusById}
              isLeader={isLeader}
              isArchived={isArchived}
              canManage={canManage}
              getEntityName={getEntityName}
              onAddMemberClick={onAddMemberClick}
              onCreateAgentClick={onCreateAgentClick}
              onSetLeader={onSetLeader}
              onRemoveMember={onRemoveMember}
              onUpdateRole={onUpdateRole}
              setLeaderPending={setLeaderPending}
            />
          </div>
        )}
        {activeTab === "instructions" && (
          <div className="flex h-full flex-col p-4 md:p-6">
            <SquadInstructionsTab
              squad={squad}
              onSave={canManage ? onSaveInstructions : undefined}
              onApplyUserCenterSOP={canManage ? onApplyUserCenterSOP : undefined}
              onApplyMulticaCodingSOP={canManage ? onApplyMulticaCodingSOP : undefined}
              onDirtyChange={setActiveDirty}
            />
          </div>
        )}
      </div>

      {pendingTab !== null && (
        <AlertDialog open onOpenChange={(v) => { if (!v) setPendingTab(null); }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t(($) => $.discard_changes_dialog.title)}</AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.discard_changes_dialog.description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t(($) => $.discard_changes_dialog.keep_editing)}</AlertDialogCancel>
              <AlertDialogAction variant="destructive" onClick={commitTabChange}>
                {t(($) => $.discard_changes_dialog.discard_button)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

// Visual config for the five squad member status buckets. Mirrors
// availabilityConfig + workloadConfig in packages/views/agents/presence.ts —
// same semantic tokens so a status dot here matches the agent page's dot.
// Unknown / null statuses (human members, server-side enum drift) render as
// a neutral muted pill; this is the "downgrade, don't crash" defense from
// CLAUDE.md > API Response Compatibility.
const SQUAD_STATUS_DOT_CLASS: Record<SquadMemberStatusValue, string> = {
  working: "bg-success",
  idle: "bg-muted-foreground/40",
  offline: "bg-muted-foreground/40",
  unstable: "bg-warning",
  archived: "bg-muted-foreground/40",
};

// Members tab body — re-uses the existing list/role editing patterns.
function SquadMembersTab({
  members,
  memberStatusById,
  isLeader,
  isArchived,
  canManage,
  getEntityName,
  onAddMemberClick,
  onCreateAgentClick,
  onSetLeader,
  onRemoveMember,
  onUpdateRole,
  setLeaderPending,
}: {
  members: SquadMember[];
  memberStatusById: Map<string, SquadMemberStatus>;
  isLeader: (m: SquadMember) => boolean;
  isArchived: (m: SquadMember) => boolean;
  canManage: boolean;
  getEntityName: (type: string, id: string) => string;
  onAddMemberClick: () => void;
  // Hidden for non-admins — see SquadOverviewPane.
  onCreateAgentClick?: () => void;
  onSetLeader: (agentId: string) => void;
  onRemoveMember: (m: SquadMember) => void;
  onUpdateRole: (m: SquadMember, role: string) => Promise<void>;
  setLeaderPending: boolean;
}) {
  const { t } = useT("squads");
  const timeAgo = useTimeAgo();
  const p = useWorkspacePaths();
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">{t(($) => $.members_tab.section_title)}</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            {t(($) => $.members_tab.section_count, { count: members.length })}
          </p>
        </div>
        {canManage && (
          <div className="flex items-center gap-2">
            {onCreateAgentClick && (
              <Button size="sm" variant="outline" onClick={onCreateAgentClick}>
                <Plus className="size-3.5 mr-1.5" />
                {t(($) => $.members_tab.create_agent_button)}
              </Button>
            )}
            <Button size="sm" variant="outline" onClick={onAddMemberClick}>
              <Plus className="size-3.5 mr-1.5" />
              {t(($) => $.members_tab.add_member_button)}
            </Button>
          </div>
        )}
      </div>

      <div className="space-y-2">
        {members.map((m) => {
          const status = memberStatusById.get(m.member_id);
          const statusValue = status?.status ?? null;
          const dotClass =
            statusValue && statusValue in SQUAD_STATUS_DOT_CLASS
              ? SQUAD_STATUS_DOT_CLASS[statusValue as keyof typeof SQUAD_STATUS_DOT_CLASS]
              : null;
          const statusLabel =
            statusValue === "working" ? t(($) => $.members_tab.status_working)
              : statusValue === "idle" ? t(($) => $.members_tab.status_idle)
              : statusValue === "offline" ? t(($) => $.members_tab.status_offline)
              : statusValue === "unstable" ? t(($) => $.members_tab.status_unstable)
              : statusValue === "archived" ? t(($) => $.members_tab.status_archived)
              : null;
          const activeIssues = status?.active_issues ?? [];
          const primaryIssue = activeIssues[0];
          const extraIssueCount = Math.max(0, activeIssues.length - 1);
          // Show last_active only when the agent isn't currently working —
          // a "working" pill already implies the agent is live, and a
          // "last active 2s ago" line next to it is just noise.
          const showLastActive =
            m.member_type === "agent" && statusValue && statusValue !== "working" && status?.last_active_at;
          return (
            <div key={m.id} className="group flex items-start gap-3 rounded-lg border p-3">
              <ActorAvatar
                actorType={m.member_type}
                actorId={m.member_id}
                size={32}
                showStatusDot
                enableHoverCard={m.member_type === "agent"}
                hoverCardVariant="live"
              />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium">{getEntityName(m.member_type, m.member_id)}</span>
                  <span className="text-xs text-muted-foreground capitalize">{m.member_type}</span>
                  {isLeader(m) && (
                    <span className="inline-flex items-center gap-0.5 text-xs bg-amber-100 dark:bg-amber-900/30 text-amber-700 dark:text-amber-400 px-1.5 py-0.5 rounded">
                      <Crown className="size-3" />
                      {t(($) => $.members_tab.leader_chip)}
                    </span>
                  )}
                  {m.member_type === "agent" && statusLabel && (
                    <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                      <span className={`h-1.5 w-1.5 rounded-full ${dotClass ?? "bg-muted-foreground/40"}`} />
                      {statusLabel}
                    </span>
                  )}
                </div>
                {canManage ? (
                  <RoleEditor
                    value={m.role ?? ""}
                    onSave={async (next) => { await onUpdateRole(m, next); }}
                  />
                ) : m.role ? (
                  <div className="mt-0.5 text-xs text-muted-foreground">{m.role}</div>
                ) : null}
                {primaryIssue && (
                  <div className="mt-1 flex items-center gap-1 text-xs text-muted-foreground min-w-0">
                    <AppLink
                      href={p.issueDetail(primaryIssue.issue_id)}
                      className="inline-flex items-center gap-1 min-w-0 hover:text-foreground transition-colors"
                    >
                      <span className="font-mono text-[10px] uppercase shrink-0">{primaryIssue.identifier}</span>
                      <span className="truncate">{primaryIssue.title}</span>
                      {primaryIssue.issue_status === "blocked" && (
                        <span className="shrink-0 inline-flex items-center text-[10px] uppercase tracking-wide text-warning">
                          {t(($) => $.members_tab.issue_status_blocked)}
                        </span>
                      )}
                    </AppLink>
                    {extraIssueCount > 0 && (
                      <span className="shrink-0">
                        · {t(($) => $.members_tab.active_issue_more, { count: extraIssueCount })}
                      </span>
                    )}
                  </div>
                )}
                {showLastActive && (
                  <div className="mt-0.5 text-xs text-muted-foreground">
                    {t(($) => $.members_tab.last_active_label, {
                      time: timeAgo(status!.last_active_at!),
                    })}
                  </div>
                )}
              </div>
              <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity">
                {m.member_type === "agent" && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <AppLink
                          href={p.agentDetail(m.member_id)}
                          className="inline-flex items-center justify-center h-8 w-8 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
                          aria-label={t(($) => $.members_tab.view_agent_tooltip)}
                        >
                          <ArrowUpRight className="size-3.5" />
                        </AppLink>
                      }
                    />
                    <TooltipContent>
                      {t(($) => $.members_tab.view_agent_tooltip)}
                    </TooltipContent>
                  </Tooltip>
                )}
                {canManage && m.member_type === "agent" && !isLeader(m) && !isArchived(m) && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-muted-foreground hover:text-amber-600 h-8 w-8 p-0"
                          onClick={() => onSetLeader(m.member_id)}
                          disabled={setLeaderPending}
                          aria-label={t(($) => $.members_tab.make_leader_tooltip)}
                        >
                          <Crown className="size-3.5" />
                        </Button>
                      }
                    />
                    <TooltipContent>
                      {t(($) => $.members_tab.make_leader_tooltip)}
                    </TooltipContent>
                  </Tooltip>
                )}
                {canManage && !isLeader(m) && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-muted-foreground hover:text-destructive h-8 w-8 p-0"
                          onClick={() => onRemoveMember(m)}
                          aria-label={t(($) => $.members_tab.remove_member_tooltip)}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      }
                    />
                    <TooltipContent>
                      {t(($) => $.members_tab.remove_member_tooltip)}
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// Instructions tab body — mirrors agent's InstructionsTab. ContentEditor +
// Save button. The squad leader's prompt picks these up at task claim time
// (server/internal/handler/daemon.go).
function SquadInstructionsTab({
  squad,
  onSave,
  onApplyUserCenterSOP,
  onApplyMulticaCodingSOP,
  onDirtyChange,
}: {
  squad: Squad;
  onSave?: (instructions: string) => Promise<void>;
  onApplyUserCenterSOP?: () => Promise<void>;
  onApplyMulticaCodingSOP?: () => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("squads");
  const [value, setValue] = useState(squad.instructions ?? "");
  const [saving, setSaving] = useState(false);
  const [applyingSOP, setApplyingSOP] = useState(false);
  const [applyingMulticaSOP, setApplyingMulticaSOP] = useState(false);
  const isDirty = value !== (squad.instructions ?? "");
  const sopProfile = normalizeSOPProfile(squad.sop_profile);
  const hasUserCenterProfile = sopProfile?.profile_key === USER_CENTER_SOP_PROFILE.profile_key || sopProfile?.project === USER_CENTER_SOP_PROFILE.project;
  const hasMulticaCodingProfile = sopProfile?.profile_key === MULTICA_CODING_SOP_PROFILE.profile_key;
  const canEdit = !!onSave;

  useEffect(() => {
    setValue(squad.instructions ?? "");
  }, [squad.id, squad.instructions]);

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSave = async () => {
    if (!onSave) return;
    setSaving(true);
    try {
      await onSave(value);
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  const handleApplyUserCenterSOP = async () => {
    if (!onApplyUserCenterSOP) return;
    setApplyingSOP(true);
    try {
      await onApplyUserCenterSOP();
    } catch {
      // toast handled by parent
    } finally {
      setApplyingSOP(false);
    }
  };

  const handleApplyMulticaCodingSOP = async () => {
    if (!onApplyMulticaCodingSOP) return;
    setApplyingMulticaSOP(true);
    try {
      await onApplyMulticaCodingSOP();
    } catch {
      // toast handled by parent
    } finally {
      setApplyingMulticaSOP(false);
    }
  };

  return (
    <div className="flex h-full flex-col gap-4">
      <p className="text-xs text-muted-foreground">
        {t(($) => $.instructions_tab.description)}
      </p>

      <div className="rounded-md border bg-muted/20 px-4 py-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <div className="text-sm font-medium text-foreground">项目 SOP 配置</div>
            <div className="mt-1 text-xs text-muted-foreground">
              issue 指派给小队后，队长会先按阶段链推进，再把实现工作委派给对应 skill 或成员。
            </div>
          </div>
          {canEdit && (
            <div className="flex shrink-0 flex-wrap gap-2">
              <Button
                type="button"
                size="sm"
                variant={hasUserCenterProfile ? "secondary" : "default"}
                onClick={handleApplyUserCenterSOP}
                disabled={applyingSOP}
              >
                {applyingSOP ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <FileText className="h-3.5 w-3.5" />
                )}
                {hasUserCenterProfile ? "重新应用用户中心 SOP" : "应用用户中心 SOP"}
              </Button>
              <Button
                type="button"
                size="sm"
                variant={hasMulticaCodingProfile ? "secondary" : "outline"}
                onClick={handleApplyMulticaCodingSOP}
                disabled={applyingMulticaSOP}
              >
                {applyingMulticaSOP ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Users className="h-3.5 w-3.5" />
                )}
                {hasMulticaCodingProfile ? "重新应用 Multica 编码小队" : "应用 Multica 编码小队"}
              </Button>
            </div>
          )}
        </div>

        {sopProfile ? (
          <div className="mt-3 space-y-3 text-xs text-muted-foreground">
            <div className="grid gap-2 md:grid-cols-2">
              <SOPProfileRow label="模板" value={sopProfile.profile_key ?? ""} />
              <SOPProfileRow label="项目" value={sopProfile.project} />
              <SOPProfileRow label="仓库" value={sopProfile.repo} />
              <SOPProfileRow label="执行方式" value={formatSOPMode(sopProfile.mode)} />
              <SOPProfileRow label="阶段链" value={formatSOPSteps(sopProfile)} wide />
              <SOPProfileRow label="操作技能" value={sopProfile.operation_skills.join("、")} />
              <SOPProfileRow label="验收要求" value={sopProfile.acceptance.join("、")} />
              <SOPProfileRow label="禁止事项" value={(sopProfile.forbidden_actions ?? []).join("、")} />
            </div>
            {sopProfile.model_policy && (
              <div>
                <div className="mb-1 text-[11px] font-medium text-muted-foreground">模型策略</div>
                <div className="grid gap-1 md:grid-cols-2">
                  {Object.entries(sopProfile.model_policy).map(([key, value]) => (
                    <SOPProfileRow key={key} label={key} value={String(value ?? "")} />
                  ))}
                </div>
              </div>
            )}
            {sopProfile.roles && sopProfile.roles.length > 0 && (
              <div>
                <div className="mb-1 text-[11px] font-medium text-muted-foreground">角色矩阵</div>
                <div className="grid gap-2 md:grid-cols-2">
                  {sopProfile.roles.map((role, index) => (
                    <div key={String(role.key ?? role.name ?? index)} className="rounded-md border border-border/70 bg-background px-3 py-2">
                      <div className="font-medium text-foreground">{String(role.name ?? role.key ?? "未命名角色")}</div>
                      <div className="mt-1 line-clamp-2">{String(role.responsibility ?? role.boundary ?? "")}</div>
                      {role.input ? (
                        <div className="mt-1 line-clamp-2">输入：{String(role.input)}</div>
                      ) : null}
                      {role.output ? (
                        <div className="mt-1 line-clamp-2">交付物：{String(role.output)}</div>
                      ) : null}
                      {role.boundary ? (
                        <div className="mt-1 line-clamp-2">边界：{String(role.boundary)}</div>
                      ) : null}
                      {role.forbidden ? (
                        <div className="mt-1 text-destructive/80">禁止：{String(role.forbidden)}</div>
                      ) : null}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        ) : (
          <div className="mt-3 text-xs text-muted-foreground">
            尚未配置项目 SOP。可先应用用户中心 SOP，再按项目语义调整。
          </div>
        )}
      </div>

      <ObservabilitySummaryCard
        title="小队观测摘要"
        scopeLabel="按当前小队聚合 SOP 执行、阶段事件、耗时和证据"
        squadId={squad.id}
      />

      <div className="flex-1 min-h-0 overflow-y-auto rounded-md border bg-background px-4 py-3 transition-colors focus-within:border-input">
        {canEdit ? (
          <ContentEditor
            key={squad.id}
            defaultValue={value}
            onUpdate={setValue}
            placeholder="例如：先澄清需求和验收口径，再拆分任务；实现后必须补齐测试证据和交接记录。"
            debounceMs={150}
            disableMentions
            className="min-h-full"
          />
        ) : (
          <div className="whitespace-pre-wrap text-sm text-muted-foreground">
            {value || "暂无小队指令。"}
          </div>
        )}
      </div>

      {canEdit && (
        <div className="flex items-center justify-end gap-3">
          {isDirty && (
            <span className="text-xs text-muted-foreground">{t(($) => $.instructions_tab.unsaved_changes)}</span>
          )}
          <Button size="sm" onClick={handleSave} disabled={!isDirty || saving}>
            {saving ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Save className="h-3.5 w-3.5" />
            )}
            {t(($) => $.instructions_tab.save_button)}
          </Button>
        </div>
      )}
    </div>
  );
}

function SOPProfileRow({
  label,
  value,
  wide,
}: {
  label: string;
  value: string;
  wide?: boolean;
}) {
  return (
    <div className={wide ? "md:col-span-2" : undefined}>
      <span className="text-muted-foreground/80">{label}：</span>
      <span className="break-words text-foreground">{value || "未配置"}</span>
    </div>
  );
}

function formatSOPMode(mode: string): string {
  if (mode === "stage_chain") return "阶段链";
  if (mode === "coding_squad") return "编码小队";
  return mode;
}

function normalizeSOPProfile(raw: Record<string, unknown> | null | undefined): SquadSOPProfile | null {
  if (!raw || typeof raw !== "object") return null;

  const project = typeof raw.project === "string" ? raw.project.trim() : "";
  const repo = typeof raw.repo === "string" ? raw.repo.trim() : "";
  const mode = typeof raw.mode === "string" ? raw.mode.trim() : "";
  const profileKey = typeof raw.profile_key === "string" ? raw.profile_key.trim() : "";
  const roles = toRecordList(raw.roles);
  const steps = toRecordList(raw.steps);
  const modelPolicy = raw.model_policy && typeof raw.model_policy === "object" && !Array.isArray(raw.model_policy)
    ? raw.model_policy as Record<string, unknown>
    : undefined;
  const stageSkills = toStringList(raw.stage_skills);
  const operationSkills = toStringList(raw.operation_skills);
  const acceptance = toStringList(raw.acceptance);
  const archivePolicy = typeof raw.archive_policy === "string" ? raw.archive_policy.trim() : "";
  const forbiddenActions = toStringList(raw.forbidden_actions);

  if (!profileKey && !project && !repo && !mode && roles.length === 0 && steps.length === 0 && stageSkills.length === 0 && operationSkills.length === 0 && acceptance.length === 0) {
    return null;
  }

  return {
    profile_key: profileKey,
    project,
    repo,
    mode,
    roles,
    steps,
    model_policy: modelPolicy,
    stage_skills: stageSkills,
    operation_skills: operationSkills,
    acceptance,
    archive_policy: archivePolicy || undefined,
    forbidden_actions: forbiddenActions,
  };
}

function toStringList(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string" && item.trim().length > 0);
}

function toRecordList(value: unknown): Array<Record<string, unknown>> {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is Record<string, unknown> => !!item && typeof item === "object" && !Array.isArray(item));
}

function formatSOPSteps(profile: SquadSOPProfile): string {
  if (profile.steps && profile.steps.length > 0) {
    return profile.steps
      .map((step) => String(step.name ?? step.key ?? step.step_key ?? "未命名阶段"))
      .join(" → ");
  }
  return profile.stage_skills.join(" → ");
}
