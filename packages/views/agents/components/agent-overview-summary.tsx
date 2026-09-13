"use client";

// 本文件提供 Agent 工作台“概览”页的只读信息侧栏：集中展示身份信息（拥有者、可见性、
// 运行环境、模型、并发上限）、技能清单和近期性能概览。纯浏览、不可编辑——编辑都放“设置”页。

import { Bot, Server } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { VisibilityBadge } from "./visibility-badge";
import { AgentPerformanceSummary } from "./tabs/activity-tab";

interface AgentOverviewSummaryProps {
  /** 要展示信息的 Agent。 */
  agent: Agent;
  /** Agent 绑定的运行环境；为 null 表示还没绑定（如刚创建完）。 */
  runtime: AgentRuntime | null;
  /** Agent 的拥有者成员；为 null 表示暂无归属信息。 */
  owner: MemberWithUser | null;
}

/**
 * 概览页的只读信息侧栏。编辑一律放在“设置”下，这里刻意做成不可交互，
 * 让用户能快速扫一眼“身份 + 运行 + 能力”的上下文，而不会把每个值误当成可点的控件。
 */
export function AgentOverviewSummary({
  agent,
  runtime,
  owner,
}: AgentOverviewSummaryProps) {
  const { t } = useT("agents");
  const runtimeOnline = runtime?.status === "online";

  return (
    <aside className="self-start rounded-xl border border-surface-border bg-surface p-5 shadow-[var(--surface-shadow)] xl:sticky xl:top-6">
      <section>
        <h2 className="text-body font-medium">
          {t(($) => $.overview.agent_context)}
        </h2>
        <dl className="mt-4 space-y-3 text-caption">
          {owner && (
            <SummaryRow label={t(($) => $.inspector.prop_owner)}>
              <span className="flex min-w-0 items-center gap-1.5">
                <ActorAvatar
                  actorType="member"
                  actorId={owner.user_id}
                  size="xs"
                />
                <span className="truncate text-foreground">{owner.name}</span>
              </span>
            </SummaryRow>
          )}
          <SummaryRow label={t(($) => $.overview.access)}>
            <VisibilityBadge value={agent.visibility} />
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_runtime)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  runtimeOnline ? "bg-success" : "bg-muted-foreground/40"
                }`}
                aria-hidden="true"
              />
              <Server className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="truncate">
                {runtime
                  ? runtimeDisplayLabel(runtime)
                  : t(($) => $.pickers.runtime_none)}
              </span>
            </span>
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_model)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <Bot className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="truncate">
                {agent.model || t(($) => $.pickers.model_default)}
              </span>
            </span>
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_concurrency)}>
            <span className="font-mono tabular-nums text-foreground">
              {agent.max_concurrent_tasks}
            </span>
          </SummaryRow>
        </dl>
      </section>

      <section className="mt-5 border-t pt-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-body font-medium">
            {t(($) => $.inspector.section_skills)}
          </h2>
          <span className="font-mono text-caption tabular-nums text-muted-foreground">
            {agent.skills.length}
          </span>
        </div>
        {agent.skills.length > 0 ? (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {agent.skills.map((skill) => (
              <span
                key={skill.id}
                className="max-w-full truncate rounded-md border border-surface-border bg-surface-hover px-2 py-1 text-caption text-muted-foreground"
              >
                {skill.name}
              </span>
            ))}
          </div>
        ) : (
          <p className="mt-3 text-caption text-muted-foreground">
            {t(($) => $.tab_body.skills.empty_title)}
          </p>
        )}
      </section>

      <AgentPerformanceSummary agent={agent} />
    </aside>
  );
}

/** 信息栏里的单行条目：左侧固定宽度放标签（dt），右侧内容区自适应宽度放值（dd）。 */
function SummaryRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[88px_minmax(0,1fr)] items-center gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
