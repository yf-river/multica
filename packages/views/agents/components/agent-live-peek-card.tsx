"use client";

// 本文件是 Agent 头像的实时“窥视”卡片，展示 squad 成员页关心的三个实时信号。

import { useQuery } from "@tanstack/react-query";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  agentTaskSnapshotOptions,
  useAgentPresenceDetail,
} from "@multica/core/agents";
import { issueDetailOptions } from "@multica/core/issues";
import type { AgentTask } from "@multica/core/types";
import { AlertTriangle } from "lucide-react";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { availabilityConfig, workloadConfig } from "../presence";

interface AgentLivePeekCardProps {
  agentId: string;
}

// Agent 头像的实时“窥视”卡片——展示 squad 成员页关心的三个实时信号
// （工作负载、当前 issue、最近活动）。与展示静态身份（描述、运行时、技能、拥有者）
// 的 AgentProfileCard 是配套关系。把它们分开，是为了避免把“仅实时”的关注点
// 污染到 23+ 处已存在的 AgentProfileCard 调用点上。
export function AgentLivePeekCard({ agentId }: AgentLivePeekCardProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { data: agents = [], isLoading: agentsLoading } = useQuery(
    agentListOptions(wsId),
  );
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const presence = useAgentPresenceDetail(wsId, agentId);

  const agent = agents.find((a) => a.id === agentId);

  if (agentsLoading && !agent) {
    return (
      <div className="flex items-center gap-3">
        <Skeleton className="h-10 w-10 rounded-full" />
        <div className="flex-1 space-y-1.5">
          <Skeleton className="h-4 w-28" />
          <Skeleton className="h-3 w-20" />
        </div>
      </div>
    );
  }

  if (!agent) {
    return (
      <div className="text-caption text-muted-foreground">
        {t(($) => $.profile_card.unavailable)}
      </div>
    );
  }

  const agentTasks = snapshot.filter((t) => t.agent_id === agentId);
  const runningTask = agentTasks.find(
    (t) => t.status === "running" && !!t.issue_id,
  );
  const currentIssueId = runningTask?.issue_id ?? null;
  const lastTerminal = pickLatestTerminal(agentTasks);

  const initials = agent.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  // 归档优先于工作负载——退役的 Agent 读作 "Archived"，绝不会是 "Idle"/"Working"。
  // availability 是统一信号（见 deriveAgentPresenceDetail）；对于归档，它在任何
  // 任务扫描之前就已确定。
  const isArchived =
    presence !== "loading" && presence.availability === "archived";
  const workload = presence === "loading" ? null : presence.workload;
  const workloadVisual = workload ? workloadConfig[workload] : null;
  const archivedVisual = availabilityConfig.archived;

  return (
    <div className="flex flex-col gap-3 text-left">
      {/* 头部——头像 + 名字。 */}
      <div className="flex items-start gap-3">
        <ActorAvatarBase
          name={agent.name}
          initials={initials}
          avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
          isAgent
          size="xl"
        />
        <div className="min-w-0 flex-1">
          <p className="truncate text-body font-semibold">{agent.name}</p>
          <div className="mt-0.5 inline-flex items-center gap-1.5">
            {isArchived ? (
              <>
                <archivedVisual.icon
                  className={`h-3 w-3 shrink-0 ${archivedVisual.textClass}`}
                />
                <span className={`text-caption ${archivedVisual.textClass}`}>
                  {t(($) => $.availability.archived)}
                </span>
              </>
            ) : workloadVisual ? (
              <>
                <workloadVisual.icon
                  className={`h-3 w-3 shrink-0 ${workloadVisual.textClass}`}
                />
                <span className={`text-caption ${workloadVisual.textClass}`}>
                  {t(($) => $.workload[workload!])}
                </span>
              </>
            ) : (
              <Skeleton className="h-3 w-12" />
            )}
          </div>
        </div>
      </div>

      {/* 元信息行。 */}
      <div className="flex flex-col gap-1.5 text-caption">
        <CurrentIssueRow
          wsId={wsId}
          issueId={currentIssueId}
          label={t(($) => $.live_peek.current_issue_label)}
          emptyLabel={t(($) => $.live_peek.no_current_issue)}
          issueHref={(id) => p.issueDetail(id)}
        />
        <LastActivityRow
          task={lastTerminal}
          label={t(($) => $.live_peek.last_activity_label)}
          emptyLabel={t(($) => $.live_peek.no_recent_activity)}
          failedLabel={t(($) => $.live_peek.failed_indicator)}
        />
      </div>
    </div>
  );
}

// 为“最近活动”挑选最近的一个终态任务。快照已经把每个 Agent 限制成一行终态
// （见 queries.ts 头注释），但这里按 completed_at 取最大做防御，万一那个形状
// 变了也能保持卡片正确。只有 completed / failed 计入，与快照的结果筛选完全一致：
// cancelled 永远不会到这张卡片，若在这里把它当成终态，一旦端点开始返回它，
// 一次被中止的尝试就会掩盖 Agent 最后一次真实结果。
function pickLatestTerminal(tasks: readonly AgentTask[]): AgentTask | null {
  let best: AgentTask | null = null;
  for (const t of tasks) {
    if (t.status !== "completed" && t.status !== "failed") {
      continue;
    }
    if (!t.completed_at) continue;
    if (!best || (best.completed_at && t.completed_at > best.completed_at)) {
      best = t;
    }
  }
  return best;
}

function CurrentIssueRow({
  wsId,
  issueId,
  label,
  emptyLabel,
  issueHref,
}: {
  wsId: string;
  issueId: string | null;
  label: string;
  emptyLabel: string;
  issueHref: (id: string) => string;
}) {
  // 懒加载 issue 详情——只在卡片挂载且确有运行中 issue id 时才启用。快照已经
  // 给了我们 id；这个 hook 只是解析出人类可读的标识符（MUL-123）+ 标题。
  const { data: issue } = useQuery({
    ...issueDetailOptions(wsId, issueId ?? ""),
    enabled: !!issueId,
  });

  return (
    <div className="flex items-center gap-1.5">
      <span className="w-16 shrink-0 text-muted-foreground">{label}</span>
      {issueId ? (
        issue ? (
          <AppLink
            href={issueHref(issueId)}
            className="min-w-0 truncate text-brand hover:underline"
            title={`${issue.identifier} ${issue.title}`}
          >
            <span className="mr-1 font-mono text-micro">{issue.identifier}</span>
            <span>{issue.title}</span>
          </AppLink>
        ) : (
          <Skeleton className="h-3 w-24" />
        )
      ) : (
        <span className="text-muted-foreground">{emptyLabel}</span>
      )}
    </div>
  );
}

function LastActivityRow({
  task,
  label,
  emptyLabel,
  failedLabel,
}: {
  task: AgentTask | null;
  label: string;
  emptyLabel: string;
  failedLabel: string;
}) {
  const timeAgo = useTimeAgo();
  return (
    <div className="flex items-center gap-1.5">
      <span className="w-16 shrink-0 text-muted-foreground">{label}</span>
      {task && task.completed_at ? (
        <span className="inline-flex min-w-0 items-center gap-1 truncate">
          <span className="truncate">{timeAgo(task.completed_at)}</span>
          {task.status === "failed" && (
            // 只有失败终态显示在这里——上面的工作负载仍保持干净的“现在手头在做什么”
            // 读法（working/queued/idle），与项目刻意的“当前 vs 历史”状态切分一致。
            <span
              className="inline-flex items-center gap-0.5 rounded bg-warning/10 px-1 py-0.5 text-micro font-medium text-warning"
              title={failedLabel}
            >
              <AlertTriangle className="h-2.5 w-2.5" />
              {failedLabel}
            </span>
          )}
        </span>
      ) : (
        <span className="text-muted-foreground">{emptyLabel}</span>
      )}
    </div>
  );
}
