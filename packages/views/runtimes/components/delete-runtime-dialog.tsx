// ============================================================================
// 删除 Runtime 确认对话框（DeleteRuntimeDialog）
// 角色定位详见下方导出组件注释——本文件是删除操作唯一的确认界面，支持
// 「无 agent 轻量确认 / 有 agent 级联解绑删除」两种模式，并处理自愈 runtime
// 与「计划已变」等服务端冲突。
// ============================================================================
"use client";

import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, Globe, Info, Lock } from "lucide-react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import {
  useDeleteRuntime,
  useUnbindAgentsAndDeleteRuntime,
} from "@multica/core/runtimes/mutations";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import {
  type AgentPresenceDetail,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { useAuthStore } from "@multica/core/auth";
import {
  AlertDialog,
  AlertDialogContent,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { ActorAvatar } from "../../common/actor-avatar";
import { availabilityConfig, workloadConfig } from "../../agents/presence";
import { useT } from "../../i18n";
import { isSelfHealingRuntime } from "../utils";

// ============================================================================
// 删除 Runtime 的确认对话框（DeleteRuntimeDialog）
//
// 角色定位：runtime 删除的唯一确认界面，列表页 kebab 菜单与详情页诊断卡共用。
// 两种模式共享同一外壳——没有 agent 绑定时走「轻量确认」（与旧的 "are you
// sure" 提示一致）；有活动的 agent 会被解绑时走「级联删除」。
//
// 关键措辞是「解绑」而非删除（MUL-5559）：agent 连同它的指令、聊天与任务
// 历史都会被保留，只是失去这台 runtime——在绑定到另一台之前无法运行。这个
// 对话框以前会说这些 agent 将被「归档」，而服务端其实是硬删它们。
//
// 模式动态判定：
//   1. 初始：瞄一眼缓存的 agent 列表，根据是否有活动 agent 的
//      runtime_id === runtime.id 决定轻量还是级联。这让对话框无需额外请求
//      就能以正确状态打开。
//   2. 严格 DELETE 之后：若服务端以 `runtime_has_active_agents` 拒绝（本地
//      数据已过期），改用服务端权威 agent 列表切到级联模式。
//   3. 级联端点之后：若以 `runtime_delete_plan_changed` 拒绝，用服务端快照
//      刷新展示的列表，并强制用户重新勾选确认框。
//
// 自愈本机 runtime（在线本机 daemon，删除几秒后自行重新注册，见
// isSelfHealingRuntime）不会在这一层被拦截（MUL-3352）。触发它的按钮让
// 归属者能点到底，本对话框则弹出自愈告警横幅——用户需要知道除非先停掉
// daemon 进程，否则它会重新注册出一行新的 runtime。确认仍可继续。
export interface DeleteRuntimeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  runtime: AgentRuntime;
  wsId: string;
  // 删除成功后的回调。列表页关闭对话框并弹 toast；详情页还会导航回
  // /runtimes。
  onDeleted: () => void;
}

export function DeleteRuntimeDialog({
  open,
  onOpenChange,
  runtime,
  wsId,
  onDeleted,
}: DeleteRuntimeDialogProps) {
  const { t } = useT("runtimes");
  // 复用工作区级缓存数据——每个消费页面都已挂载这些查询，因此打开本对话框
  // 零新增请求。
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);
  const user = useAuthStore((s) => s.user);

  // 展示中的「删除计划」从缓存 agent 列表出发；当后端返回结构化 409（初次
  // DELETE 或「计划已变」的级联拒绝）时，被服务端快照替换。把它放在 state
  // 里，即使缓存失效闪烁也能保证渲染确定。
  const cachedActiveAgents = useMemo(
    () => agents.filter((a) => a.runtime_id === runtime.id && !a.archived_at),
    [agents, runtime.id],
  );
  const [planAgents, setPlanAgents] = useState<Agent[]>(cachedActiveAgents);
  // `cascade` 当可见计划里至少有一个 agent 时为 true。模式是推导值而非用户
  // 可控——用户永远确认「当前计划」；切换是服务端响应的结果，不是开关。
  const cascade = planAgents.length > 0;

  const [confirmed, setConfirmed] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  // 「计划已变」时显示在 agent 表上方的服务端消息——告诉用户「列表刷新了，
  // 因为有东西在变动」，不在标题里重复。存的是已翻译的字符串，消费方直接渲染。
  const [planChangedNotice, setPlanChangedNotice] = useState<string | null>(null);

  // 每次打开对话框时重置临时状态。否则上次的计划变更提示或过期的勾选会跨
  // 开关周期存活，迷惑下一次操作。
  useEffect(() => {
    if (open) {
      setPlanAgents(cachedActiveAgents);
      setConfirmed(false);
      setSubmitting(false);
      setPlanChangedNotice(null);
    }
  }, [open, cachedActiveAgents]);

  const lightMutation = useDeleteRuntime(wsId);
  const cascadeMutation = useUnbindAgentsAndDeleteRuntime(wsId);

  const handleConfirm = async () => {
    setSubmitting(true);
    setPlanChangedNotice(null);

    try {
      if (cascade) {
        await cascadeMutation.mutateAsync({
          runtimeId: runtime.id,
          expectedActiveAgentIds: planAgents.map((a) => a.id),
        });
        onDeleted();
      } else {
        try {
          await lightMutation.mutateAsync(runtime.id);
          onDeleted();
        } catch (err) {
          // 严格 DELETE 在「打开对话框到确认之间」出现了新建的活动 agent 时
          // 会返回结构化 409。用服务端权威列表切入级联模式、取消勾选，并弹
          // 出一条提示让用户明白为什么对话框突然多了一张 agent 表。
          const conflict = parseActiveAgentsConflict(err);
          if (conflict?.code === "runtime_has_active_agents") {
            setPlanAgents(conflict.activeAgents);
            setConfirmed(false);
            setPlanChangedNotice(
              t(
                ($) =>
                  $.detail.delete_dialog.cascade.notice_runtime_has_active_agents,
              ),
            );
            return;
          }
          throw err;
        }
      }
    } catch (err) {
      // 级联侧的「计划已变」：用户确认了计划 A，但线上集合现在是计划 B。
      // 用服务端快照刷新展示的列表，强制用户重新勾选确认框。
      const conflict = parseActiveAgentsConflict(err);
      if (conflict?.code === "runtime_delete_plan_changed") {
        setPlanAgents(conflict.activeAgents);
        setConfirmed(false);
        setPlanChangedNotice(
          t(
            ($) =>
              $.detail.delete_dialog.cascade.notice_runtime_delete_plan_changed,
          ),
        );
        return;
      }
      const message =
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.detail.delete_dialog.cascade.delete_failed_toast);
      toast.error(message);
    } finally {
      setSubmitting(false);
    }
  };

  // 操作许可——提交中为 `false`：阻止关闭、防止误点底层页面导致写操作中途
  // 取消。
  const handleOpenChange = (next: boolean) => {
    if (submitting) return;
    onOpenChange(next);
  };

  // 轻量模式沿用旧的简短文案。级联模式复刻产品负责人（赵刚）拟定的计划：
  // 带数量的破坏性标题、一条破坏性告警横幅、agent 表，然后是勾选确认——
  // 其标签用与告警相同的措辞重申后果。
  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent
        className={
          cascade
            ? "w-[calc(100vw-2rem)] !max-w-[640px] gap-0 overflow-hidden rounded-lg p-0"
            : "w-[calc(100vw-2rem)] !max-w-[440px] gap-0 overflow-hidden rounded-lg p-0"
        }
        onClick={(e) => e.stopPropagation()}
      >
        {cascade ? (
          <CascadeBody
            runtime={runtime}
            agents={planAgents}
            members={members}
            presenceMap={presenceMap}
            currentUserId={user?.id ?? null}
            confirmed={confirmed}
            onConfirmedChange={setConfirmed}
            planChangedNotice={planChangedNotice}
            submitting={submitting}
            onCancel={() => handleOpenChange(false)}
            onConfirm={handleConfirm}
          />
        ) : (
          <LightBody
            runtime={runtime}
            submitting={submitting}
            onCancel={() => handleOpenChange(false)}
            onConfirm={handleConfirm}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  );
}

// ---------------------------------------------------------------------------
// 持久性提示——删除 runtime 行可能无法永久移除可运行后端时的信息性横幅。
// 带自定义 profile 的 runtime 优先于通用本机 daemon 告警：当工作区级的
// profile 仍然存在时，只停掉 daemon 还不够。
// ---------------------------------------------------------------------------

function DeletePersistenceNotice({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  if (runtime.profile_id) {
    return (
      <div
        role="status"
        className="mt-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-caption"
      >
        <Info className="mt-0.5 size-3.5 shrink-0 text-warning" />
        <span>{t(($) => $.detail.delete_dialog.profile_backed_notice)}</span>
      </div>
    );
  }
  if (!isSelfHealingRuntime(runtime)) return null;
  return (
    <div
      role="status"
      className="mt-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-caption"
    >
      <Info className="mt-0.5 size-3.5 shrink-0 text-warning" />
      <span>{t(($) => $.detail.delete_dialog.self_heal_notice)}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 轻量模式——没有活动 agent，经典的 "are you sure" 提示。标题与描述沿用
// 旧版 AlertDialog，既有截图/肌肉记忆仍然适用。
// ---------------------------------------------------------------------------

function LightBody({
  runtime,
  submitting,
  onCancel,
  onConfirm,
}: {
  runtime: AgentRuntime;
  submitting: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useT("runtimes");
  return (
    <>
      <div className="px-5 pb-4 pt-5">
        <h2 className="text-title-sm font-semibold">
          {t(($) => $.detail.delete_dialog.light.title)}
        </h2>
        <p className="mt-1 text-body leading-5 text-muted-foreground">
          {t(($) => $.detail.delete_dialog.light.description, {
            name: runtimeDisplayLabel(runtime),
          })}
        </p>
        <DeletePersistenceNotice runtime={runtime} />
      </div>
      <div className="border-t bg-muted/25 px-5 py-3">
        <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button
            type="button"
            variant="outline"
            className="w-full sm:w-auto"
            onClick={onCancel}
            disabled={submitting}
          >
            {t(($) => $.detail.delete_dialog.light.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            className="w-full sm:w-auto"
            onClick={onConfirm}
            disabled={submitting}
          >
            {submitting
              ? t(($) => $.detail.delete_dialog.light.submitting)
              : t(($) => $.detail.delete_dialog.light.confirm)}
          </Button>
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// 级联模式——破坏性告警、agent 表、需勾选确认的破坏性按钮。文案按小队负责
// 人的指示，原样沿用产品负责人（赵刚）的英文文本。
// ---------------------------------------------------------------------------

function CascadeBody({
  runtime,
  agents,
  members,
  presenceMap,
  currentUserId,
  confirmed,
  onConfirmedChange,
  planChangedNotice,
  submitting,
  onCancel,
  onConfirm,
}: {
  runtime: AgentRuntime;
  agents: Agent[];
  members: MemberWithUser[];
  presenceMap: Map<string, AgentPresenceDetail>;
  currentUserId: string | null;
  confirmed: boolean;
  onConfirmedChange: (next: boolean) => void;
  planChangedNotice: string | null;
  submitting: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useT("runtimes");
  const count = agents.length;

  return (
    <>
      <div className="px-5 pb-4 pt-5">
        <h2 className="text-title-sm font-semibold">
          {t(($) => $.detail.delete_dialog.cascade.title, { count })}
        </h2>
        <p className="mt-1 text-body leading-5 text-muted-foreground">
          {t(($) => $.detail.delete_dialog.cascade.description, {
            name: runtimeDisplayLabel(runtime),
          })}
        </p>

        <DeletePersistenceNotice runtime={runtime} />

        {/* 破坏性横幅——让用户的目光先落在这不可逆的一半上，再看 agent 表。 */}
        <div
          role="alert"
          className="mt-3 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-caption text-destructive"
        >
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          <span>{t(($) => $.detail.delete_dialog.cascade.warning)}</span>
        </div>

        {planChangedNotice && (
          <div
            role="status"
            className="mt-2 rounded-md border bg-muted/40 px-3 py-2 text-caption text-foreground"
          >
            {planChangedNotice}
          </div>
        )}

        <AgentPlanTable
          agents={agents}
          members={members}
          presenceMap={presenceMap}
          currentUserId={currentUserId}
        />
      </div>

      <div className="border-t bg-muted/25 px-5 py-4">
        <label className="flex cursor-pointer items-start gap-2 text-body text-foreground">
          <Checkbox
            className="mt-0.5"
            checked={confirmed}
            onCheckedChange={(next) => onConfirmedChange(next === true)}
            disabled={submitting}
          />
          <span className="leading-5">
            {t(($) => $.detail.delete_dialog.cascade.checkbox, { count })}
          </span>
        </label>
        <div className="mt-3 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button
            type="button"
            variant="outline"
            className="w-full sm:w-auto"
            onClick={onCancel}
            disabled={submitting}
          >
            {t(($) => $.detail.delete_dialog.cascade.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            className="w-full sm:w-auto"
            onClick={onConfirm}
            disabled={!confirmed || submitting}
          >
            {submitting
              ? t(($) => $.detail.delete_dialog.cascade.submitting)
              : t(($) => $.detail.delete_dialog.cascade.confirm, { count })}
          </Button>
        </div>
      </div>
    </>
  );
}

// AgentPlanTable 渲染级联计划：每个 agent 一行，展示产品负责人（赵刚）指定
// 的字段。表头 + 滚动主体放在固定高度的容器里，agent 很多的 runtime 也不会
// 把破坏性确认按钮挤出视野——主体自己滚动。
function AgentPlanTable({
  agents,
  members,
  presenceMap,
  currentUserId,
}: {
  agents: Agent[];
  members: MemberWithUser[];
  presenceMap: Map<string, AgentPresenceDetail>;
  currentUserId: string | null;
}) {
  const { t } = useT("runtimes");
  const memberById = useMemo(() => {
    const map = new Map<string, MemberWithUser>();
    for (const m of members) map.set(m.user_id, m);
    return map;
  }, [members]);

  return (
    <div className="mt-3 overflow-hidden rounded-md border">
      <div className="grid grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)_minmax(0,1.2fr)_minmax(0,0.8fr)_minmax(0,1fr)] gap-3 border-b bg-muted/40 px-3 py-2 text-micro uppercase tracking-wide text-muted-foreground">
        <span>{t(($) => $.detail.delete_dialog.cascade.table.header_agent)}</span>
        <span>{t(($) => $.detail.delete_dialog.cascade.table.header_owner)}</span>
        <span>{t(($) => $.detail.delete_dialog.cascade.table.header_status)}</span>
        <span>
          {t(($) => $.detail.delete_dialog.cascade.table.header_visibility)}
        </span>
        <span>{t(($) => $.detail.delete_dialog.cascade.table.header_model)}</span>
      </div>
      <div className="max-h-[240px] overflow-y-auto divide-y">
        {agents.map((agent) => {
          const ownerMember = agent.owner_id
            ? memberById.get(agent.owner_id) ?? null
            : null;
          const ownerLabel = ownerMember
            ? ownerMember.user_id === currentUserId
              ? t(($) => $.detail.delete_dialog.cascade.table.owner_self)
              : ownerMember.name
            : t(($) => $.detail.delete_dialog.cascade.table.owner_unassigned);
          const presence = presenceMap.get(agent.id);
          return (
            <div
              key={agent.id}
              className="grid grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)_minmax(0,1.2fr)_minmax(0,0.8fr)_minmax(0,1fr)] items-center gap-3 px-3 py-2 text-caption"
            >
              <span className="inline-flex min-w-0 items-center gap-2">
                <ActorAvatar
                  actorType="agent"
                  actorId={agent.id}
                  size="sm"
                  enableHoverCard
                />
                <span className="truncate font-medium text-foreground">
                  {agent.name}
                </span>
              </span>
              <span className="inline-flex min-w-0 items-center gap-1.5">
                {ownerMember ? (
                  <ActorAvatar
                    actorType="member"
                    actorId={ownerMember.user_id}
                    size="sm"
                  />
                ) : null}
                <span className="truncate text-muted-foreground">
                  {ownerLabel}
                </span>
              </span>
              <PresenceCell presence={presence} />
              <VisibilityCell visibility={agent.visibility} />
              <span className="truncate text-muted-foreground">
                {agent.model ||
                  t(($) => $.detail.delete_dialog.cascade.table.model_unset)}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function PresenceCell({ presence }: { presence: AgentPresenceDetail | undefined }) {
  const { t } = useT("runtimes");
  if (!presence) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.detail.delete_dialog.cascade.table.presence_unknown)}
      </span>
    );
  }
  const av = availabilityConfig[presence.availability];
  const wl = workloadConfig[presence.workload];
  const counts =
    presence.workload === "working"
      ? presence.queuedCount > 0
        ? `${presence.runningCount} +${presence.queuedCount}q`
        : `${presence.runningCount}`
      : presence.workload === "queued"
        ? `${presence.queuedCount}`
        : null;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span className={`size-1.5 shrink-0 rounded-full ${av.dotClass}`} />
      <span className={av.textClass}>
        {presence.workload === "idle"
          ? t(($) => $.detail.delete_dialog.cascade.table.workload_idle)
          : null}
        {presence.workload === "working" && (
          <wl.icon className={`mr-1 inline size-3 align-[-2px] animate-spin ${wl.textClass}`} />
        )}
        {presence.workload === "queued" && (
          <wl.icon className={`mr-1 inline size-3 align-[-2px] ${wl.textClass}`} />
        )}
        {presence.workload === "working" &&
          t(($) => $.detail.delete_dialog.cascade.table.workload_working)}
        {presence.workload === "queued" &&
          t(($) => $.detail.delete_dialog.cascade.table.workload_queued)}
      </span>
      {counts && (
        <span className="font-mono tabular-nums text-muted-foreground">
          {counts}
        </span>
      )}
    </span>
  );
}

function VisibilityCell({ visibility }: { visibility: string }) {
  const { t } = useT("runtimes");
  if (visibility === "public" || visibility === "workspace") {
    return (
      <span className="inline-flex items-center gap-1 text-muted-foreground">
        <Globe className="size-3" />
        <span>
          {t(($) => $.detail.delete_dialog.cascade.table.visibility_workspace)}
        </span>
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 text-muted-foreground">
      <Lock className="size-3" />
      <span>
        {t(($) => $.detail.delete_dialog.cascade.table.visibility_private)}
      </span>
    </span>
  );
}

// ---------------------------------------------------------------------------
// 服务端响应解析
// ---------------------------------------------------------------------------

// 结构化 409 冲突信息的形状：冲突码 + 服务端权威的活动 agent 列表。
interface ActiveAgentsConflict {
  code: "runtime_has_active_agents" | "runtime_delete_plan_changed";
  activeAgents: Agent[];
}

// parseActiveAgentsConflict 从 ApiError 上取出结构化 409 的字段。非 409、
// 非活动-agent 冲突码以及缺 body 的情况一律收敛为 `null`，调用方据此回退到
// 通用错误 toast。
function parseActiveAgentsConflict(err: unknown): ActiveAgentsConflict | null {
  if (!(err instanceof ApiError)) return null;
  if (err.status !== 409) return null;
  const body = err.body;
  if (!body || typeof body !== "object") return null;
  const code = (body as Record<string, unknown>).code;
  if (
    code !== "runtime_has_active_agents" &&
    code !== "runtime_delete_plan_changed"
  ) {
    return null;
  }
  const rawAgents = (body as Record<string, unknown>).active_agents;
  if (!Array.isArray(rawAgents)) {
    return { code, activeAgents: [] };
  }
  // 这里信任服务端契约——响应与 agent 列表端点返回的是同一种 AgentResponse
  // 形状。对 id/name 的轻量运行时检查足以抓住真正畸形的内容，无需逐字段
  // 复打类型。
  const activeAgents = rawAgents.filter(
    (a): a is Agent =>
      typeof a === "object" &&
      a !== null &&
      typeof (a as Record<string, unknown>).id === "string" &&
      typeof (a as Record<string, unknown>).name === "string",
  );
  return { code, activeAgents };
}
