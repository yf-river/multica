"use client";

import { useMemo } from "react";
import type { AgentTask } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useActorName } from "@multica/core/workspace/hooks";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import {
  cacheHitRatePercent,
  collectUnmappedModels,
  formatTokens,
  formatUsd,
  hasKnownCost,
  hasKnownCacheSavings,
  summarizeTaskUsage,
  summarizeTaskUsageAcross,
  type TaskUsageSummary,
} from "../../runtimes/utils";
import { KpiCard } from "../../runtimes/components/shared";
import { useStatusLabel, useTriggerText } from "./task-run-labels";
import { TaskStatusIcon } from "./task-status-icon";

// 本文件：IssueUsageDialog —— 单条问题"按次运行花费"的明细弹窗，
// 由执行日志头部用量合计打开。
//
// 执行日志逐行回答"这次运行花了多少"；这里回答"哪次运行最贵、为什么"。
// 该对比需要输入/输出/缓存命中拆分并排展示，288px 侧栏放不下，
// 所以它住在这里而不是去扩展那些行。
//
// 每个数字都来自侧栏同款 `summarizeTaskUsage` 辅助函数，
// 因此这里的合计与打开它的合计永远不会不一致。

/**
 * 单问题按次花费弹窗：KPI 卡 + 按 agent 条形 + 明细表（职责见文件头）。
 */
export function IssueUsageDialog({
  open,
  onOpenChange,
  identifier,
  tasks,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  identifier: string;
  tasks: AgentTask[];
}) {
  const { t } = useT("issues");
  // `estimateCost` 是从 Zustand store 命令式读自定义费率的，
  // 所以用户保存新费率不会让本弹窗自己重渲染。订阅快照并把它带进每一个
  // 给用量定价的 memo——否则合计、按次花费与"未映射模型"提示都会一直
  // 显示旧价格，直到任务列表碰巧重新拉取。runtime 用量页在 usage-section.tsx
  // 订阅也是同一个原因。
  const pricings = useCustomPricingStore((s) => s.pricings);

  // 只有真实记录到用量的运行才配占一行：没有数字的运行无从比较，
  // 只会贡献一行全是破折号。它们的存在仍由下面脚注交代。
  const priced = useMemo(
    () => tasks.filter((task) => (task.usage?.length ?? 0) > 0),
    [tasks],
  );
  const unpricedCount = tasks.length - priced.length;

  const total = useMemo(
    () => summarizeTaskUsageAcross(priced.map((task) => task.usage)),
    [priced, pricings],
  );
  // 与统计页、执行日志同一个口径：整批没有可定价来源时显示未知，而不是 $0.00。
  const costKnown = useMemo(
    () => hasKnownCost(priced.flatMap((task) => task.usage ?? [])),
    [priced, pricings],
  );

  const cacheSavingsKnown = useMemo(
    () => hasKnownCacheSavings(priced.flatMap((task) => task.usage ?? [])),
    [priced, pricings],
  );

  const agentIds = useMemo(
    () => Array.from(new Set(priced.map((task) => task.agent_id).filter(Boolean))),
    [priced],
  );

  // 既无费率表条目、也无服务端报价的模型：token 计入、花费不计，
  // 因此下面的合计会低于实际。说破这一点，是"估算"与"错误数字"的区别。
  const unmapped = useMemo(
    () => collectUnmappedModels(priced.flatMap((task) => task.usage ?? [])),
    [priced, pricings],
  );

  // 向下取整而非四舍五入：缓存密集型问题上 99.55% 会四舍五入成
  // "命中率 100%"——那等于宣称每一个 token 都来自缓存。向下取整只在
  // 真是 100% 时才说 100%，一个百分点的悲观比一个看着就不可能
  // 的数字便宜。
  const cacheHitRate =
    total == null
      ? null
      : cacheHitRatePercent(total.input, total.cacheRead, total.cacheWrite);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* DialogContent 的基础是 `sm:max-w-sm`，同特异度的 `max-w-4xl`
          压不过它——于是表格溢出盒子而不是盒子长大。`!` 赢下这场较量，
          与 transcript 弹窗一致。用 5xl 而非 4xl，因为九列加 token 条
          需要约 920px：4xl 时 Cost 列——正是人们为它打开本弹窗的那列——
          会落在滚动视口之外。 */}
      <DialogContent className="!max-w-5xl !w-[calc(100vw-4rem)]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.usage_detail.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.usage_detail.subtitle, {
              identifier,
              count: priced.length,
            })}
          </DialogDescription>
        </DialogHeader>

        {total == null ? (
          <p className="py-8 text-center text-body text-muted-foreground">
            {t(($) => $.usage_detail.empty)}
          </p>
        ) : (
          /* `min-w-0`：DialogContent 是 grid，grid 项默认
             `min-width: auto`——它按内容最小值而非轨道尺寸定宽。没有它，
             超宽运行表会把本列顶过弹窗自身的 max-width，所有兄弟元素
             （KPI 卡、按 agent 的条、脚注）跟着一起拉伸、画到盒子外面。 */
          <div className="flex min-w-0 flex-col gap-5">
            <div className="grid grid-cols-3 divide-x rounded-lg border bg-card">
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cost)}
                value={costKnown ? formatUsd(total.cost) : "—"}
                hint={<CostConcentrationHint tasks={priced} total={total} />}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cache)}
                value={cacheSavingsKnown ? formatUsd(total.cacheSavings) : "—"}
                accent={
                  cacheSavingsKnown && total.cacheSavings > 0 ? "success" : "default"
                }
                hint={
                  cacheHitRate == null
                    ? "—"
                    : t(($) => $.usage_detail.kpi_cache_hint, {
                        pct: cacheHitRate,
                        reads: formatTokens(total.cacheRead),
                      })
                }
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_tokens)}
                value={formatTokens(total.tokens)}
                hint={t(($) => $.usage_detail.kpi_tokens_hint, {
                  input: formatTokens(total.input),
                  output: formatTokens(total.output),
                })}
              />
            </div>

            {agentIds.length > 1 && (
              <CostByAgent
                tasks={priced}
                agentIds={agentIds}
                total={total}
                costKnown={costKnown}
              />
            )}

            <RunTable tasks={priced} total={total} costKnown={costKnown} />

            <div className="space-y-1 text-micro text-muted-foreground">
              {unpricedCount > 0 && (
                <p>{t(($) => $.usage_detail.note_unpriced, { count: unpricedCount })}</p>
              )}
              {unmapped.length > 0 && (
                <p>
                  {t(($) => $.usage_detail.note_unmapped, { models: unmapped.join(", ") })}
                </p>
              )}
              <p>{t(($) => $.usage_detail.note_estimate)}</p>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// "N 次失败运行占了 X%"——合计能对自己说的最有用的一句话，也是本弹窗
// 存在的原因。只在确实存在"有花费的失败运行"时渲染，
// 健康的问题不会看到唠叨提示。
function CostConcentrationHint({
  tasks,
  total,
}: {
  tasks: AgentTask[];
  total: TaskUsageSummary;
}) {
  const { t } = useT("issues");
  const failed = tasks.filter((task) => task.status === "failed");
  if (failed.length === 0 || total.cost <= 0) return null;

  const failedCost = failed.reduce(
    (sum, task) => sum + (summarizeTaskUsage(task.usage)?.cost ?? 0),
    0,
  );
  if (failedCost <= 0) return null;

  return (
    <span className="text-warning">
      {t(($) => $.usage_detail.kpi_cost_hint, {
        count: failed.length,
        pct: Math.round((failedCost / total.cost) * 100),
      })}
    </span>
  );
}

// 与 runtime 用量页"按 agent 成本"块同一套条形语言：每个 agent 一行，
// 条长按最大花费者缩放。单 agent 的问题整体隐藏——一条独条柱状图
// 说不了任何合计没说过的信息。
function CostByAgent({
  tasks,
  agentIds,
  total,
  costKnown,
}: {
  tasks: AgentTask[];
  agentIds: string[];
  total: TaskUsageSummary;
  costKnown: boolean;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const pricings = useCustomPricingStore((s) => s.pricings);

  const rows = useMemo(() => {
    return agentIds
      .map((agentId) => {
        const own = tasks.filter((task) => task.agent_id === agentId);
        const summary = summarizeTaskUsageAcross(own.map((task) => task.usage));
        // 每行只用自己的用量判断：整窗里有一个可定价模型，不代表这个 agent 的
        // 花费是已知的，直接沿用整窗标志会把"不知道"写成 $0.00。
        const ownCostKnown = hasKnownCost(own.flatMap((task) => task.usage ?? []));
        return {
          agentId,
          cost: summary?.cost ?? 0,
          tokens: summary?.tokens ?? 0,
          costKnown: ownCostKnown,
        };
      })
      .toSorted((a, b) => b.cost - a.cost);
  }, [agentIds, tasks, pricings]);

  const maxCost = rows.reduce((m, r) => Math.max(m, r.cost), 0);

  return (
    <div>
      <div className="mb-2 text-caption font-medium">
        {t(($) => $.usage_detail.by_agent)}
      </div>
      <div className="space-y-2">
        {rows.map((row) => (
          <div
            key={row.agentId}
            className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)_5rem_4rem] items-center gap-3"
          >
            <div className="flex min-w-0 items-center gap-2">
              <ActorAvatar actorType="agent" actorId={row.agentId} size="sm" enableHoverCard />
              <span className="truncate text-caption">
                {getActorName("agent", row.agentId)}
              </span>
            </div>
            <div className="relative h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-chart-1"
                style={{ width: `${maxCost > 0 ? (row.cost / maxCost) * 100 : 0}%` }}
              />
            </div>
            <div className="text-right text-caption tabular-nums text-muted-foreground">
              {formatTokens(row.tokens)}
            </div>
            <div className="text-right text-caption font-medium tabular-nums">
              {row.costKnown ? formatUsd(row.cost) : "—"}
            </div>
          </div>
        ))}
      </div>
      <div className="sr-only">
        {t(($) => $.usage_detail.by_agent_total, {
          cost: costKnown ? formatUsd(total.cost) : "—",
        })}
      </div>
    </div>
  );
}

/**
 * 按次运行明细表。
 */
function RunTable({
  tasks,
  total,
  costKnown,
}: {
  tasks: AgentTask[];
  total: TaskUsageSummary;
  costKnown: boolean;
}) {
  const { t } = useT("issues");
  const maxTokens = tasks.reduce(
    (m, task) => Math.max(m, summarizeTaskUsage(task.usage)?.tokens ?? 0),
    0,
  );

  return (
    // 九列数字有最小宽度；窗口窄时横向滚动，
    // 而不是把触发文本挤成渣或把合计推出弹窗。
    //
    // `min-w-0` 才是让 `overflow-auto` 真正生效裁剪的东西：作为 flex 项，
    // 本盒子默认 `min-width: auto`，会长到表格 min-content 宽度、
    // 永不滚动。去掉它表格就又被顶出弹窗。
    <div className="max-h-[45vh] min-w-0 overflow-auto">
      <table className="w-full min-w-[46rem]">
        <thead className="sticky top-0 bg-popover">
          <tr className="text-micro text-muted-foreground [&>th]:whitespace-nowrap [&>th]:px-2 [&>th]:pb-1.5 [&>th]:text-right [&>th]:font-normal">
            <th className="!pl-0 !text-left">{t(($) => $.usage_detail.col_run)}</th>
            <th>{t(($) => $.usage_detail.col_model)}</th>
            <th>{t(($) => $.usage_detail.col_duration)}</th>
            <th>{t(($) => $.usage_detail.col_input)}</th>
            <th>{t(($) => $.usage_detail.col_output)}</th>
            <th>{t(($) => $.usage_detail.col_cache_read)}</th>
            <th>{t(($) => $.usage_detail.col_cache_write)}</th>
            <th className="!pr-16">{t(($) => $.usage_detail.col_tokens)}</th>
            <th className="!pr-0">{t(($) => $.usage_detail.col_cost)}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <RunRow key={task.id} task={task} maxTokens={maxTokens} />
          ))}
        </tbody>
        <tfoot>
          <tr className="text-caption font-medium tabular-nums [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:border-input [&>td]:px-2 [&>td]:py-2 [&>td]:text-right">
            <td className="!pl-0 !text-left">{t(($) => $.usage_detail.total)}</td>
            <td colSpan={2} />
            <td>{formatTokens(total.input)}</td>
            <td>{formatTokens(total.output)}</td>
            <td>{formatTokens(total.cacheRead)}</td>
            <td>{formatTokens(total.cacheWrite)}</td>
            <td className="!pr-16">{formatTokens(total.tokens)}</td>
            <td className="!pr-0">{costKnown ? formatUsd(total.cost) : "—"}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  );
}

/**
 * 明细表的一行（一条运行）。
 */
function RunRow({ task, maxTokens }: { task: AgentTask; maxTokens: number }) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  const statusLabel = useStatusLabel(task.status, task.failure_reason);
  const summary = summarizeTaskUsage(task.usage);
  if (!summary) return null;

  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : "";

  return (
    <tr className="text-caption tabular-nums transition-colors hover:bg-accent/40 [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:px-2 [&>td]:py-1.5 [&>td]:text-right">
      <td className="!pl-0 !text-left">
        <div className="flex items-center gap-2">
          <ActorAvatar actorType="agent" actorId={task.agent_id} size="sm" enableHoverCard />
          <span className="max-w-[13rem] truncate">{trigger}</span>
          {task.status === "running" ? (
            <span className="inline-flex shrink-0 items-center gap-1 text-micro text-info">
              <span className="h-1.5 w-1.5 rounded-full bg-info" />
              {t(($) => $.execution_log.status_running)}
            </span>
          ) : (
            <>
              {/* TaskStatusIcon 是 aria-hidden 的，单靠那个图形读屏器无法
                  区分失败运行与完成运行。与执行日志行同款 sr-only 配对。 */}
              <TaskStatusIcon status={task.status} />
              <span className="sr-only">{statusLabel}</span>
            </>
          )}
        </div>
      </td>
      {/* 一次跨模型的运行会把它们全列出来，而真实模型 id 很长
          （`claude-haiku-4-5-20251001, claude-opus-5[1m]`）。不加上限时，
          这一个单元格就定了整张表的宽度、逼着每条其它问题的弹窗横向滚动，
          所以封顶并把完整清单放进 title。`whitespace-nowrap` 来自行级样式，
          因此本单元格需要自己的宽度边界，`truncate` 才有东西可截。
          长模型名也放进 title。 */}
      <td className="max-w-[11rem] text-micro text-muted-foreground">
        <span className="block truncate" title={summary.models.join(", ")}>
          {summary.models.join(", ") || "—"}
        </span>
      </td>
      <td className="text-muted-foreground">{duration || "—"}</td>
      <td>{formatTokens(summary.input)}</td>
      <td>{formatTokens(summary.output)}</td>
      <td>{formatTokens(summary.cacheRead)}</td>
      <td>{formatTokens(summary.cacheWrite)}</td>
      <td className="!pr-2">
        <div className="flex items-center justify-end gap-2">
          <span>{formatTokens(summary.tokens)}</span>
          <span className="relative h-1 w-14 overflow-hidden rounded-full bg-muted">
            <span
              className="absolute inset-y-0 left-0 rounded-full bg-chart-1"
              style={{ width: `${maxTokens > 0 ? (summary.tokens / maxTokens) * 100 : 0}%` }}
            />
          </span>
        </div>
      </td>
      <td className="!pr-0 font-medium">
        {hasKnownCost(task.usage ?? []) ? formatUsd(summary.cost) : "—"}
      </td>
    </tr>
  );
}
