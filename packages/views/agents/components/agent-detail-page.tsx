"use client";

// 本文件是 Agent 详情页：头部信息 + 归档/恢复等操作、顶部横幅（无权限/已归档/未绑定
// 运行环境），主体交给 AgentOverviewPane 按概览/工作/能力/设置分区展示。数据先读
// 列表缓存（零请求快速路径），找不到再走详情查询以支持直达链接并区分 403/404。

import { useState } from "react";
import {
  AlertCircle,
  ArrowLeft,
  Bot,
  Clock3,
  Lock,
  MessageSquare,
  MoreHorizontal,
  Plus,
  Server,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  UpdateAgentRequest,
} from "@multica/core/types";
import {
  type AgentPresenceDetail,
  isAgentRuntimeBound,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { api, ApiError } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentDetailOptions,
  agentListOptions,
  cacheAgentResponse,
  memberListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { runtimeDisplayLabel, runtimeListOptions } from "@multica/core/runtimes";
import { useAgentPermissions } from "@multica/core/permissions";
import { Button } from "@multica/ui/components/ui/button";
import { CapabilityBanner } from "@multica/ui/components/common/capability-banner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink, useNavigation } from "../../navigation";
import { PageHeader } from "../../layout/page-header";
import { ActorAvatar } from "../../common/actor-avatar";
import { AgentPresenceIndicator } from "./agent-presence-indicator";
import { VisibilityBadge } from "./visibility-badge";
import { AgentOverviewPane, type DetailTab } from "./agent-overview-pane";
import { ExpandableDescription } from "../../common/expandable-description";
import { useT, useTimeAgo } from "../../i18n";

interface AgentDetailPageProps {
  /** 要展示详情的 Agent id（来自路由）。 */
  agentId: string;
}

/** Agent 详情页。数据主要吃列表缓存；无权限/不存在/加载失败各有独立占位页。 */
export function AgentDetailPage({ agentId }: AgentDetailPageProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const qc = useQueryClient();
  const currentUser = useAuthStore((s) => s.user);

  const {
    data: agents = [],
    isLoading: agentsLoading,
    error: agentsError,
    refetch: refetchAgents,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  // 工作区级做一遍在场状态查询，本页只读自己的槽位。hook 持有 30s 心跳，
  // 失败窗口的自动清除也在这里生效。
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);

  const listAgent = agents.find((a) => a.id === agentId) ?? null;

  // 列表缓存是“零请求”的常规路径。当列表已 settle 却没有目标 Agent 时，
  // 再用规范详情查询解析直达链接，并把 403/404 和瞬时失败区分开。新建 Agent
  // 会水合（hydrate）同一 key，所以刚创建的 Agent 能立即渲染出来。
  const detailQuery = useQuery({
    ...agentDetailOptions(wsId, agentId),
    enabled: !agentsLoading && !listAgent && !!agentId,
  });
  const detailError = detailQuery.error;
  const isForbidden =
    detailError instanceof ApiError && detailError.status === 403;
  const isNotFound =
    detailError instanceof ApiError && detailError.status === 404;
  // TanStack 在刷新失败时有意保留成功数据。不要让这份过期快照掩盖掉访问被撤销
  // 或 Agent 被删除后的 403/404——但瞬时网络故障期间又应保留它。两种情况下，
  // 仍可见的列表响应都是权威。
  const agent =
    listAgent ?? (isForbidden || isNotFound ? null : detailQuery.data) ?? null;
  const presence: AgentPresenceDetail | null =
    agent ? presenceMap.get(agent.id) ?? null : null;

  // 权限 hook 必须无条件调用——它的 `agent | null` 签名内部就处理了“找不到/加载中”
  // 的情况，使下方这些提前 return 不违反 hooks 规则。后端对归档/恢复的权限门槛
  // 与编辑一致，所以一个 `canEdit` 就能覆盖全部动作。
  const {
    canAssign,
    canEdit,
    isLoading: permissionsLoading,
  } = useAgentPermissions(agent, wsId);

  const [confirmArchive, setConfirmArchive] = useState(false);

  // 一次性频道：检查器里紧凑的 Lark 状态行请概览 pane 聚焦某个 Tab。
  // pane 消费完这个意图后清除它。
  const [tabNavIntent, setTabNavIntent] = useState<DetailTab | null>(null);

  const handleUpdate = async (id: string, data: Record<string, unknown>) => {
    // 乐观更新：在网络请求出去之前先就地补丁缓存列表里对应的 Agent，
    // 让检查器的选择器 chip 一点击就翻成新值。否则每个选择器
    //（推理/可见性/并发/模型/运行环境）都要等 0.5-2s 的“接口返回 + 失效 + 重拉”，
    // 界面上会明显卡顿。
    //
    // 出错时只回滚本次调用写入的字段，不动其他并发修改的字段，然后 invalidate
    // 让缓存与服务端收敛。若做整列表快照回滚，当失败调用最后才 resolve 时
    //（例如同时翻可见性与运行环境、只有可见性 PATCH 失败），会冲掉另一个
    // 并发成功的变更。
    const optimisticData =
      typeof data.runtime_id === "string"
        ? { ...data, runtime_bound: data.runtime_id.trim().length > 0 }
        : data;
    const queryKey = workspaceKeys.agents(wsId);
    const detailQueryKey = workspaceKeys.agent(wsId, id);
    const prevAgents = qc.getQueryData<Agent[]>(queryKey);
    const prevListAgent = prevAgents?.find((a) => a.id === id);
    const prevDetailAgent = qc.getQueryData<Agent>(detailQueryKey);
    const previousFields = (previousAgent: Agent | undefined) => {
      const fields: Record<string, unknown> = {};
      if (!previousAgent) return fields;
      for (const key of Object.keys(optimisticData)) {
        fields[key] = (
          previousAgent as unknown as Record<string, unknown>
        )[key];
      }
      return fields;
    };
    const prevListFields = previousFields(prevListAgent);
    const prevDetailFields = previousFields(prevDetailAgent);
    qc.setQueryData<Agent[]>(queryKey, (old) =>
      old?.map((a) =>
        a.id === id ? ({ ...a, ...optimisticData } as Agent) : a,
      ),
    );
    qc.setQueryData<Agent>(detailQueryKey, (old) =>
      old ? ({ ...old, ...optimisticData } as Agent) : old,
    );
    try {
      const updatedAgent = await api.updateAgent(
        id,
        data as UpdateAgentRequest,
      );
      cacheAgentResponse(qc, wsId, updatedAgent, { insertIntoList: false });
      void qc.invalidateQueries({ queryKey });
      toast.success(t(($) => $.detail.agent_updated_toast));
    } catch (e) {
      if (prevListAgent) {
        qc.setQueryData<Agent[]>(queryKey, (old) =>
          old?.map((a) =>
            a.id === id ? ({ ...a, ...prevListFields } as Agent) : a,
          ),
        );
      }
      if (prevDetailAgent) {
        qc.setQueryData<Agent>(detailQueryKey, (old) =>
          old ? ({ ...old, ...prevDetailFields } as Agent) : old,
        );
      }
      void qc.invalidateQueries({ queryKey });
      toast.error(e instanceof Error ? e.message : t(($) => $.detail.update_failed_toast));
      throw e;
    }
  };

  const handleArchive = async (id: string) => {
    try {
      await api.archiveAgent(id);
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(t(($) => $.detail.agent_archived_toast));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.detail.archive_failed_toast));
    }
  };

  const handleRestore = async (id: string) => {
    try {
      await api.restoreAgent(id);
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(t(($) => $.detail.agent_restored_toast));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.detail.restore_failed_toast));
    }
  };

  // --- Loading ---
  if (!agent && (agentsLoading || detailQuery.isPending)) {
    return <DetailLoadingSkeleton />;
  }

  // --- No permission (private agent the caller is not in allowed_principals for) ---
  if (!agent && isForbidden) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <BackHeader paths={paths.agents()} title={t(($) => $.detail.back_to_agents)} />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
          <Lock className="h-8 w-8 text-muted-foreground" />
          <div>
            <p className="text-body font-medium">{t(($) => $.detail.no_access_title)}</p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.detail.no_access_hint)}
            </p>
          </div>
          <Button
            size="sm"
            render={<AppLink href={paths.agents()} />}
            nativeButton={false}
          >
            {t(($) => $.detail.back_to_agents_full)}
          </Button>
        </div>
      </div>
    );
  }

  // --- Not found / error ---
  if (!agent) {
    const loadError = detailError ?? agentsError;
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <BackHeader paths={paths.agents()} title={t(($) => $.detail.back_to_agents)} />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
          <AlertCircle className="h-8 w-8 text-destructive" />
          <div>
            <p className="text-body font-medium">
              {isNotFound
                ? t(($) => $.detail.not_found_title)
                : t(($) => $.detail.load_failed_title)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {isNotFound
                ? t(($) => $.detail.not_found_default)
                : loadError instanceof Error
                  ? loadError.message
                  : t(($) => $.detail.load_failed_default)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void Promise.all([refetchAgents(), detailQuery.refetch()]);
              }}
            >
              {t(($) => $.detail.try_again)}
            </Button>
            <Button
              size="sm"
              render={<AppLink href={paths.agents()} />}
              nativeButton={false}
            >
              {t(($) => $.detail.back_to_agents_full)}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const isArchived = !!agent.archived_at;
  const runtimeBound = isAgentRuntimeBound(agent);
  const runtime = runtimeBound
    ? runtimes.find((r) => r.id === agent.runtime_id) ?? null
    : null;
  const owner = agent.owner_id
    ? members.find((m) => m.user_id === agent.owner_id) ?? null
    : null;

  // 发消息与派单共用同一“调用门槛”（MUL-3963）：发起会话就会触发 Agent 运行。
  // 按钮无论有无权限都保持可见——被拒的点击会自己解释原因，而不是让控件无声消失。
  // 成员关系还在解析时结论未定，此时按钮置灰，避免对真成员误报“无权限”。
  //
  // 控件是真实链接，所以门槛被拦时得取消 AppLink 本来要做的跳转——preventDefault
  // 就是这个“取消”。
  const handleDm = (e: React.MouseEvent<HTMLAnchorElement>) => {
    if (permissionsLoading) {
      e.preventDefault();
      return;
    }
    if (!canAssign.allowed) {
      e.preventDefault();
      toast.error(t(($) => $.detail.dm_no_permission_toast));
      return;
    }
    if (!runtimeBound) {
      e.preventDefault();
      toast.error(t(($) => $.detail.runtime_required_toast));
    }
  };
  const handleAssign = () => {
    if (!runtimeBound) {
      toast.error(t(($) => $.detail.runtime_required_toast));
      return;
    }
    useModalStore
      .getState()
      .open("quick-create-issue", { agent_id: agent.id });
  };

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <DetailHeader
        agent={agent}
        runtime={runtime}
        presence={presence}
        backHref={paths.agents()}
        canAssign={canAssign.allowed}
        canArchive={canEdit.allowed}
        dmPending={permissionsLoading}
        dmHref={`${paths.chat()}?agent=${agent.id}`}
        onDm={handleDm}
        onAssign={handleAssign}
        onArchive={
          agent.system_key ? undefined : () => setConfirmArchive(true)
        }
      />

      {!canEdit.allowed && (
        <div className="px-6 pt-3">
          <CapabilityBanner
            reason={canEdit.reason}
            resource="agent"
            ownerName={owner?.name}
          />
        </div>
      )}

      {isArchived && (
        <div className="flex shrink-0 items-center gap-2 border-b bg-muted/50 px-6 py-2 text-caption text-muted-foreground">
          <AlertCircle className="h-3.5 w-3.5 shrink-0" />
          <span className="flex-1">
            {t(($) => $.detail.archived_banner)}
          </span>
          {canEdit.allowed && (
            <Button
              variant="outline"
              size="sm"
              className="h-6 text-caption"
              onClick={() => handleRestore(agent.id)}
            >
              {t(($) => $.detail.restore)}
            </Button>
          )}
        </div>
      )}

      {!isArchived && !runtimeBound && (
        <div className="flex shrink-0 items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-6 py-2 text-caption text-amber-900 dark:text-amber-100">
          <Server className="h-3.5 w-3.5 shrink-0" />
          <span className="flex-1">
            {t(($) => $.detail.runtime_required_banner)}
          </span>
          {canEdit.allowed && (
            <Button
              variant="outline"
              size="sm"
              className="h-6 border-amber-500/40 bg-background/70 text-caption"
              onClick={() => setTabNavIntent("general")}
            >
              {t(($) => $.detail.bind_runtime)}
            </Button>
          )}
        </div>
      )}

      <div className="flex flex-1 min-h-0 flex-col overflow-hidden">
        <AgentOverviewPane
          agent={agent}
          runtime={runtime}
          owner={owner}
          runtimes={runtimes}
          members={members}
          onUpdate={handleUpdate}
          currentUserId={currentUser?.id ?? null}
          canEdit={canEdit.allowed}
          navIntent={tabNavIntent}
          onNavIntentHandled={() => setTabNavIntent(null)}
        />
      </div>

      {confirmArchive && (
        <Dialog
          open
          onOpenChange={(v) => {
            if (!v) setConfirmArchive(false);
          }}
        >
          <DialogContent className="max-w-sm" showCloseButton={false}>
            <div className="flex items-center gap-3">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-destructive/10">
                <AlertCircle className="h-5 w-5 text-destructive" />
              </div>
              <DialogHeader className="flex-1 gap-1">
                <DialogTitle className="text-body font-semibold">
                  {t(($) => $.detail.archive_dialog_title)}
                </DialogTitle>
                <DialogDescription className="text-caption">
                  {t(($) => $.detail.archive_dialog_description, { name: agent.name })}
                </DialogDescription>
              </DialogHeader>
            </div>
            <DialogFooter>
              <Button
                variant="ghost"
                onClick={() => setConfirmArchive(false)}
              >
                {t(($) => $.detail.archive_dialog_cancel)}
              </Button>
              <Button
                variant="destructive"
                onClick={() => {
                  setConfirmArchive(false);
                  handleArchive(agent.id);
                  navigation.push(paths.agents());
                }}
              >
                {t(($) => $.detail.archive_dialog_confirm)}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}

/** 顶部“面包屑 + 名字/描述/元信息 + 操作按钮”的头部栏。 */
function DetailHeader({
  agent,
  runtime,
  presence,
  backHref,
  canAssign,
  canArchive,
  dmPending,
  dmHref,
  onDm,
  onAssign,
  onArchive,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  backHref: string;
  canAssign: boolean;
  canArchive: boolean;
  dmPending: boolean;
  dmHref: string;
  /** onDm：先于链接跳转执行；门槛拒绝发消息时调 preventDefault，
   *  这正是拦下 AppLink 不再 push 的关键。 */
  onDm: (e: React.MouseEvent<HTMLAnchorElement>) => void;
  onAssign: () => void;
  /** Multica 内置 Agent 没有此回调——服务端拒绝归档它们，菜单直接隐藏该动作，
   *  而不是提供一个注定失败的入口。 */
  onArchive?: () => void;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const isArchived = !!agent.archived_at;
  const hasMoreActions = !!onArchive;

  return (
    <header className="shrink-0 border-b bg-background px-4 pb-5 pt-3 sm:px-6">
      <div className="mx-auto max-w-[1440px]">
        <div className="flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
          <AppLink
            href={backHref}
            className="rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {t(($) => $.page.title)}
          </AppLink>
          <span aria-hidden="true">/</span>
          <span className="truncate text-foreground">{agent.name}</span>
        </div>

        <div className="mt-4 flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="flex min-w-0 items-start gap-4">
            <ActorAvatar
              actorType="agent"
              actorId={agent.id}
              size="2xl"
              profileLink={false}
              className="ring-1 ring-border"
            />
            <div className="min-w-0 pt-0.5">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <h1 className="min-w-0 text-balance text-title-lg font-semibold tracking-tight sm:text-display-sm">
                  {agent.name}
                </h1>
                <AgentPresenceIndicator detail={presence} />
              </div>
              <ExpandableDescription>
                {agent.description ||
                  t(($) => $.inspector.no_description_placeholder)}
              </ExpandableDescription>
              <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-caption text-muted-foreground">
                <span className="inline-flex min-w-0 items-center gap-1.5">
                  <Bot className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  <span className="truncate">{agent.model || t(($) => $.pickers.model_default)}</span>
                </span>
                <span className="inline-flex min-w-0 items-center gap-1.5">
                  <Server className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  <span className="truncate">
                    {runtime
                      ? runtimeDisplayLabel(runtime)
                      : t(($) => $.pickers.runtime_none)}
                  </span>
                </span>
                <VisibilityBadge value={agent.visibility} />
                <span className="inline-flex items-center gap-1.5">
                  <Clock3 className="h-3.5 w-3.5" aria-hidden="true" />
                  {t(($) => $.detail.updated, { when: timeAgo(agent.updated_at) })}
                </span>
              </div>
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-2 self-end lg:self-start">
            {!isArchived && (
              <Button
                variant="outline"
                size="sm"
                disabled={dmPending}
                // 锚点永远不会命中 `:disabled`，所以基础变体的 `disabled:` 规则
                // 在这里都不生效——承担“变灰、不可点”外观的是 Base UI 的
                // data-disabled。
                className="data-disabled:pointer-events-none data-disabled:opacity-50"
                render={<AppLink href={dmHref} onClick={onDm} />}
                nativeButton={false}
              >
                <MessageSquare className="h-4 w-4" aria-hidden="true" />
                {t(($) => $.detail.dm)}
              </Button>
            )}
            {!isArchived && canAssign && (
              <Button type="button" size="sm" onClick={onAssign}>
                <Plus className="h-4 w-4" aria-hidden="true" />
                {t(($) => $.detail.assign_work)}
              </Button>
            )}
            {!isArchived && canArchive && hasMoreActions ? (
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={<Button variant="ghost" size="icon-sm" />}
                  aria-label={t(($) => $.detail.more_actions_aria)}
                >
                  <MoreHorizontal
                    className="h-4 w-4 text-muted-foreground"
                    aria-hidden="true"
                  />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-auto">
                  {onArchive && (
                    <DropdownMenuItem variant="destructive" onClick={onArchive}>
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                      {t(($) => $.detail.more_archive)}
                    </DropdownMenuItem>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            ) : null}
          </div>
        </div>
      </div>
    </header>
  );
}

/** 错误/无权限页顶部的“返回 Agents”小面包屑。 */
function BackHeader({ paths, title }: { paths: string; title: string }) {
  return (
    <PageHeader>
      <AppLink
        href={paths}
        className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-caption text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <ArrowLeft className="h-3.5 w-3.5" />
        {title}
      </AppLink>
    </PageHeader>
  );
}

/** 详情页加载中的骨架屏：头部 + 主体两栏都先占位。 */
function DetailLoadingSkeleton() {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <div className="shrink-0 border-b px-6 pb-5 pt-3">
        <Skeleton className="h-4 w-48" />
        <div className="mt-4 flex items-start gap-4">
          <Skeleton className="h-14 w-14 rounded-full" />
          <div className="flex-1 space-y-3">
            <Skeleton className="h-7 w-64" />
            <Skeleton className="h-4 w-full max-w-xl" />
            <Skeleton className="h-4 w-full max-w-lg" />
          </div>
        </div>
      </div>
      <div className="flex flex-1 flex-col p-6">
        <Skeleton className="h-9 w-96" />
        <div className="mt-6 grid flex-1 gap-6 xl:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-5">
            <Skeleton className="h-48 w-full" />
            <Skeleton className="h-64 w-full" />
          </div>
          <Skeleton className="h-96 w-full" />
        </div>
      </div>
    </div>
  );
}
