"use client";

// 「什么坏了」标签页：失败 KPI、失败趋势、失败类型构成、失败者排行四块内容。
import { useMemo, useState } from "react";
import { BarChart3 } from "lucide-react";
import { NumberFlow } from "@multica/ui/components/ui/number-flow";
import { FAILURE_CLASSES, type FailureClass } from "@multica/core/dashboard";
import { useWorkspacePaths } from "@multica/core/paths";
import { KpiCard } from "../../runtimes/components/shared";
import {
  DailyErrorsChart,
  WeeklyErrorsChart,
  FAILURE_CLASS_COLOR,
  formatRate,
} from "../../runtimes/components/charts";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import {
  aggregateDailyErrors,
  aggregateWeeklyErrors,
  hasRateSample,
  MIN_RATE_SAMPLE,
  OFFENDER_METRIC,
  sortAgentFailures,
  type AgentFailureRow,
  type FailureClassRow,
  type FailureReasonRow,
  type FailureTotals,
  type OffenderSort,
} from "../utils";
import { type Dim } from "./dashboard-shared";
import { Segmented } from "./dashboard-shared";
import { DimSegmented } from "./dim-segmented";

// 失败者列表在折叠尾部前最多展示多少个。列表按绝对失败次数排名，所以被折起来的
// 尾部是只失败了一两次的 Agent——真实存在，但没人打开这个视图是为了看它们。
const TOP_OFFENDER_LIMIT = 8;

// 由失败者列头和每一行共用，保证两者不会出现错位。
const OFFENDER_GRID =
  "grid grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_4rem_4rem_4rem] items-center gap-3";

// 失败类型的翻译文案。用 switch 而不是查表对象，是为了让类型检查器能揪出
// 新加到 FAILURE_CLASSES 里却没有配文案的类型。
function useFailureClassLabel(): (c: FailureClass) => string {
  const { t } = useT("usage");
  return (c) => {
    switch (c) {
      case "auth":
        return t(($) => $.errors.class.auth);
      case "rate_limit":
        return t(($) => $.errors.class.rate_limit);
      case "timeout":
        return t(($) => $.errors.class.timeout);
      case "provider":
        return t(($) => $.errors.class.provider);
      case "runtime":
        return t(($) => $.errors.class.runtime);
      case "agent":
        return t(($) => $.errors.class.agent);
      case "other":
        return t(($) => $.errors.class.other);
    }
  };
}

/**
 * 「什么坏了」，作为一个完整视图，而不是塞在开销页底部的一张压缩卡片。
 *
 * 自上而下按人们真正提问的顺序来读：有多糟（KPI）→ 什么时候发生（趋势）→
 * 坏了哪类东西（构成）→ 坏了谁的（失败者排行）。这几块以前要么是单张卡片里的
 * 一个小节，要么是一个切换器的第五个选项——其余四个还都是开销指标。
 *
 * @param days 当前选中的时间范围天数
 * @param allowedDims 当前时间段允许的图表维度
 * @param totals 失败总量（失败数/总数/失败率）
 * @param classRows 按失败类型聚合的数据
 * @param reasonRows 按原始失败原因聚合的数据
 * @param agentRows 按 Agent 聚合的失败数据
 * @param dailyErrors / weeklyErrors 日/周粒度的失败序列
 * @param agents 可解析出名字的 Agent 列表
 * @param locales 数字格式化 locale
 */
export function ErrorsTab({
  days,
  allowedDims,
  totals,
  classRows,
  reasonRows,
  agentRows,
  dailyErrors,
  weeklyErrors,
  agents,
  locales,
}: {
  days: number;
  allowedDims: readonly Dim[];
  totals: FailureTotals;
  classRows: FailureClassRow[];
  reasonRows: FailureReasonRow[];
  agentRows: AgentFailureRow[];
  dailyErrors: ReturnType<typeof aggregateDailyErrors>;
  weeklyErrors: ReturnType<typeof aggregateWeeklyErrors>;
  agents: { id: string; name: string }[];
  locales?: Intl.LocalesArgument;
}) {
  const { t } = useT("usage");
  const classLabel = useFailureClassLabel();

  // 「受影响的 Agent」直接统计下面失败者列表的行数，这样 KPI 和列表永远对得上。
  // 这个查看者无法解析的 Agent 已在上游被折叠成一行匿名行，所以当有多个隐藏 Agent
  // 失败时，这个数字是个下界——另一种做法（逐个计数）会泄露到底有多少私有 Agent 存在。
  const affectedAgents = agentRows.length;
  const worst = agentRows[0];
  const worstName = worst
    ? (agents.find((a) => a.id === worst.agentId)?.name ??
      t(($) => $.errors.other_agents))
    : null;

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-1 divide-y rounded-lg border bg-card sm:grid-cols-3 sm:divide-x sm:divide-y-0">
        <KpiCard
          label={t(($) => $.errors.kpi_failed_label, { days })}
          value={
            <NumberFlow
              value={totals.failed}
              locales={locales}
              format={{ maximumFractionDigits: 0 }}
              aria-label={String(totals.failed)}
            />
          }
          hint={t(($) => $.errors.kpi_failed_hint, { total: totals.total })}
        />
        {/* 失败率把分母直接写在它下面。Usage 标签页的 Tasks 卡片引用的是一个不同的、
            更小的失败数——它只统计真正启动过的任务——两个来自不同分母的光秃百分比
            读起来像互相矛盾，除非各自说明自己统计的是什么。 */}
        <KpiCard
          label={t(($) => $.errors.kpi_rate_label, { days })}
          value={formatRate(totals.failed, totals.total)}
          hint={t(($) => $.errors.summary, {
            failed: totals.failed,
            total: totals.total,
            rate: formatRate(totals.failed, totals.total),
          })}
        />
        <KpiCard
          label={t(($) => $.errors.kpi_agents_label, { days })}
          value={
            <NumberFlow
              value={affectedAgents}
              locales={locales}
              format={{ maximumFractionDigits: 0 }}
              aria-label={String(affectedAgents)}
            />
          }
          hint={
            worst && worstName
              ? t(($) => $.errors.kpi_agents_hint, {
                  name: worstName,
                  count: worst.failed,
                })
              : undefined
          }
        />
      </div>

      {totals.failed === 0 ? (
        <div className="flex flex-col items-center rounded-lg border border-dashed py-12 text-center">
          <BarChart3 className="h-6 w-6 text-faint-foreground" />
          <p className="mt-3 text-caption text-muted-foreground">
            {t(($) => $.errors.no_data)}
          </p>
        </div>
      ) : (
        <>
          <ErrorTrendCard
            allowedDims={allowedDims}
            dailyErrors={dailyErrors}
            weeklyErrors={weeklyErrors}
          />
          <FailureMixCard
            totals={totals}
            classRows={classRows}
            reasonRows={reasonRows}
            classLabel={classLabel}
          />
          <OffendersCard
            agentRows={agentRows}
            agents={agents}
            classLabel={classLabel}
          />
        </>
      )}
    </div>
  );
}

/** 失败随时间的变化趋势，和开销图表共用同一根 x 轴。它从开销切换器的第五个选项
 *  晋升而来——在那里，想看失败就意味着必须藏起开销。 */
function ErrorTrendCard({
  allowedDims,
  dailyErrors,
  weeklyErrors,
}: {
  allowedDims: readonly Dim[];
  dailyErrors: ReturnType<typeof aggregateDailyErrors>;
  weeklyErrors: ReturnType<typeof aggregateWeeklyErrors>;
}) {
  const { t } = useT("usage");
  const [dim, setDim] = useState<Dim>("daily");
  const effectiveDim: Dim = allowedDims.includes(dim) ? dim : allowedDims[0]!;
  const weekly = effectiveDim === "weekly";

  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-body font-semibold">
          {weekly ? t(($) => $.weekly.title_errors) : t(($) => $.daily.title_errors)}
        </h4>
        <DimSegmented allowedDims={allowedDims} value={effectiveDim} onChange={setDim} />
      </div>
      <div className="min-h-[240px]">
        {weekly ? (
          <WeeklyErrorsChart data={weeklyErrors} />
        ) : (
          <DailyErrorsChart data={dailyErrors} />
        )}
      </div>
    </div>
  );
}

/** 坏了哪类东西：失败类型构成，原始的线上枚举值放在一键之遥的位置。 */
function FailureMixCard({
  totals,
  classRows,
  reasonRows,
  classLabel,
}: {
  totals: FailureTotals;
  classRows: FailureClassRow[];
  reasonRows: FailureReasonRow[];
  classLabel: (c: FailureClass) => string;
}) {
  const { t } = useT("usage");
  const [showReasons, setShowReasons] = useState(false);

  return (
    <div className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 pt-4 pb-3">
        {/* 自己把分母写清楚：上面的失败率卡片是「所有运行」里的比率，而这一节
            只对「失败」本身做拆分。 */}
        <h4 className="text-body font-semibold">
          {t(($) => $.errors.mix_title, { failed: totals.failed })}
        </h4>
        <button
          type="button"
          onClick={() => setShowReasons((v) => !v)}
          className="text-caption text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          {showReasons
            ? t(($) => $.errors.hide_reasons)
            : t(($) => $.errors.show_reasons)}
        </button>
      </div>
      <div className="p-4">
        {showReasons ? (
          <ReasonList rows={reasonRows} />
        ) : (
          <ClassComposition rows={classRows} classLabel={classLabel} />
        )}
      </div>
    </div>
  );
}

/** 坏了谁的（失败者排行）。 */
function OffendersCard({
  agentRows,
  agents,
  classLabel,
}: {
  agentRows: AgentFailureRow[];
  agents: { id: string; name: string }[];
  classLabel: (c: FailureClass) => string;
}) {
  const { t } = useT("usage");
  const [showAllAgents, setShowAllAgents] = useState(false);
  const [sortBy, setSortBy] = useState<OffenderSort>("failed");

  const sortOptions = useMemo(
    () => [
      { value: "failed" as const, label: t(($) => $.errors.sort_failed) },
      { value: "rate" as const, label: t(($) => $.errors.sort_rate) },
    ],
    [t],
  );

  const sortedAgents = useMemo(
    () => sortAgentFailures(agentRows, sortBy),
    [agentRows, sortBy],
  );

  // 领先者铺满轨道，但最大值按所有行（而非仅可见行）计算，这样条宽在折叠和
  // 展开时含义一致。用领先者（而不是对原始行取 max）也能让 Rate 排序的刻度可用：
  // 一个被降级的小样本行可能失败率高于领先者，若按它缩放会把每根有意义的条压成一条缝。
  const leader = sortedAgents[0];
  const maxValue = leader ? OFFENDER_METRIC[sortBy](leader) : 0;

  const visibleAgents = showAllAgents
    ? sortedAgents
    : sortedAgents.slice(0, TOP_OFFENDER_LIMIT);

  return (
    <div className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 pt-4 pb-3">
        <h4 className="text-body font-semibold">{t(($) => $.errors.by_agent)}</h4>
        <div className="flex flex-wrap items-center justify-end gap-3">
          <Segmented
            label={t(($) => $.errors.sort_label)}
            value={sortBy}
            onChange={setSortBy}
            options={sortOptions}
          />
          {sortedAgents.length > TOP_OFFENDER_LIMIT ? (
            <button
              type="button"
              onClick={() => setShowAllAgents((v) => !v)}
              className="text-caption text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            >
              {showAllAgents
                ? t(($) => $.errors.show_less, { count: TOP_OFFENDER_LIMIT })
                : t(($) => $.errors.show_all, { count: sortedAgents.length })}
            </button>
          ) : null}
        </div>
      </div>
      <div className="p-4 pt-0">
        {/* 列头，和排行榜一样：`4 / 10 · 40%` 以前是一坨没有标签的数字，
            读者得猜哪个数字是哪个。当前指标的列会被强调，这样排名和条在衡量什么
            一目了然。 */}
        {sortedAgents.length > 0 ? (
          <div
            className={`${OFFENDER_GRID} border-b py-2 text-caption font-medium text-muted-foreground`}
          >
            <span>{t(($) => $.errors.header_agent)}</span>
            <span />
            <span
              className={`text-right ${sortBy === "failed" ? "text-foreground" : ""}`}
            >
              {t(($) => $.errors.header_failed)}
            </span>
            <span className="text-right">{t(($) => $.errors.header_runs)}</span>
            <span
              className={`text-right ${sortBy === "rate" ? "text-foreground" : ""}`}
            >
              {t(($) => $.errors.header_rate)}
            </span>
          </div>
        ) : null}
        <ul aria-label={t(($) => $.errors.by_agent)} className="divide-y">
          {visibleAgents.map((row) => (
            <AgentFailureItem
              key={row.agentId}
              row={row}
              name={agents.find((a) => a.id === row.agentId)?.name ?? null}
              maxValue={maxValue}
              sortBy={sortBy}
              classLabel={classLabel}
            />
          ))}
        </ul>
      </div>
    </div>
  );
}

/**
 * 失败类型构成：一根 100% 堆叠条 + 图例。
 *
 * 它回答的问题是「构成比例是多少」，一根条能直接看出各自占总量多少——
 * 如果画成多根分离的条，读者就得去比长度、再在脑子里自己加总。
 */
function ClassComposition({
  rows,
  classLabel,
}: {
  rows: FailureClassRow[];
  classLabel: (c: FailureClass) => string;
}) {
  const { t } = useT("usage");
  const total = rows.reduce((sum, r) => sum + r.count, 0);
  if (total === 0) return null;

  return (
    <div className="space-y-2.5">
      {/* 分段按数量降序排列（聚合器已经排好序），所以条从左到右是「占比最大在前」。 */}
      <div className="flex h-2 w-full overflow-hidden rounded-full bg-muted">
        {rows.map((row) => (
          <div
            key={row.failureClass}
            className="h-full transition-[width] duration-300 ease-out"
            style={{
              width: `${(row.count / total) * 100}%`,
              backgroundColor: FAILURE_CLASS_COLOR[row.failureClass],
            }}
          />
        ))}
      </div>
      <ul
        aria-label={t(($) => $.errors.mix_label)}
        className="flex flex-wrap items-center gap-x-4 gap-y-1.5"
      >
        {rows.map((row) => (
          <li key={row.failureClass} className="flex items-center gap-1.5">
            <span
              aria-hidden
              className="h-2 w-2 shrink-0 rounded-[2px]"
              style={{ backgroundColor: FAILURE_CLASS_COLOR[row.failureClass] }}
            />
            <span className="text-caption">{classLabel(row.failureClass)}</span>
            <span className="text-caption tabular-nums text-muted-foreground">
              {row.count}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * 类型汇总背后的原始 `failure_reason` 值。刻意不翻译：它们是后端的线上枚举，
 * 运维把某个值粘到日志检索或 issue 里时，需要的是精确的原始字符串。
 */
function ReasonList({ rows }: { rows: FailureReasonRow[] }) {
  const { t } = useT("usage");
  return (
    <ul
      aria-label={t(($) => $.errors.codes_label)}
      className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2"
    >
      {rows.map((row) => (
        <li key={row.reason} className="flex items-center justify-between gap-2">
          <span className="flex min-w-0 items-center gap-2">
            <span
              aria-hidden
              className="h-2 w-2 shrink-0 rounded-[2px]"
              style={{ backgroundColor: FAILURE_CLASS_COLOR[row.failureClass] }}
            />
            <code className="truncate text-caption text-muted-foreground">
              {row.reason}
            </code>
          </span>
          <span className="shrink-0 text-caption tabular-nums">{row.count}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * 一行失败者，形状和 Usage 标签页的排行榜行一致：身份 → 比例条 → 每个数字一列。
 *
 * 条的长度衡量列表当前排序所用的指标，对应的列也会被强调——和排行榜保持同样的同步。
 *
 * 条按失败类型堆叠着色，颜色也只表达这一层含义。只以一种方式失败的是一整块实色；
 * 以五种方式失败的会明显看到条纹——这种区分是过去「单一主导类型徽章」抹掉的。
 */
function AgentFailureItem({
  row,
  name,
  maxValue,
  sortBy,
  classLabel,
}: {
  row: AgentFailureRow;
  name: string | null;
  maxValue: number;
  sortBy: OffenderSort;
  classLabel: (c: FailureClass) => string;
}) {
  const { t } = useT("usage");
  const wsPaths = useWorkspacePaths();

  const segments = FAILURE_CLASSES.filter((c) => row.classes[c] > 0);
  // 堆叠条的文本等价物。条是唯一渲染失败类型拆分的地方，所以它既要给屏幕阅读器
  // 提供名称，也要给其他人提供悬停提示。
  const composition = segments
    .map((c) => `${classLabel(c)} ${row.classes[c]}`)
    .join(" · ");

  // 要「夹紧」而不是只「缩放」：在 Rate 排序下，小样本行会被降级到领先者之下，
  // 却仍可能带着更高的失败率，不夹紧的宽度会溢出轨道。
  const value = OFFENDER_METRIC[sortBy](row);
  const pct = maxValue > 0 ? Math.min(100, (value / maxValue) * 100) : 0;

  // 低于 MIN_RATE_SAMPLE 时，失败率只是算术结果、不是有效信号（1/1 就是 100%）。
  // 这些行在 Rate 排序下排最后，也拿不到标记当前列的那种强调，但它们仍会渲染，
  // 并在悬停时说明原因。
  const weakSample = !hasRateSample(row);

  // 这一行链接到 Agent 的 Overview 页，那里的 ActivityTab 会列出近期运行及每次
  // 失败的原因——即从「这个 Agent 是问题」下钻到真正失败的那些运行。不是 Work 标签：
  // 那个列的是分配给该 Agent 的 issue，完全是另一个问题。
  //
  // 无法解析出名字的 Agent 要么被硬删了，要么对别人私有；无论哪种，都没有可打开的
  // 页面、也没有可显示的名字，所以这一行退化成中性占位符。这里若渲染 `row.agentId`
  // 会泄露一个光秃秃的 UUID——而对私有 Agent 来说，还会把它的存在和失败画像泄露给
  // 一个根本看不到它的成员。
  const label = (
    <span
      className={`block truncate text-caption${name ? "" : " italic text-muted-foreground"}`}
    >
      {name ?? t(($) => $.errors.other_agents)}
    </span>
  );

  return (
    <li className={`${OFFENDER_GRID} py-2`}>
      {name ? (
        <AppLink
          href={`${wsPaths.agentDetail(row.agentId)}?view=overview`}
          className="min-w-0 hover:underline"
        >
          {label}
        </AppLink>
      ) : (
        label
      )}
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div
          role="img"
          aria-label={composition}
          title={composition}
          className="flex h-full overflow-hidden rounded-full transition-[width] duration-300 ease-out"
          style={{ width: `${pct}%` }}
        >
          {segments.map((c) => (
            <div
              key={c}
              className="h-full"
              style={{
                width: `${(row.classes[c] / row.failed) * 100}%`,
                backgroundColor: FAILURE_CLASS_COLOR[c],
              }}
            />
          ))}
        </div>
      </div>
      <span
        className={`text-right text-caption tabular-nums ${sortBy === "failed" ? "font-medium text-foreground" : "text-muted-foreground"}`}
      >
        {row.failed}
      </span>
      <span className="text-right text-caption tabular-nums text-muted-foreground">
        {row.total}
      </span>
      <span
        title={
          weakSample
            ? t(($) => $.errors.low_sample, { count: MIN_RATE_SAMPLE })
            : undefined
        }
        className={`text-right text-caption tabular-nums ${
          sortBy === "rate" && !weakSample
            ? "font-medium text-foreground"
            : "text-muted-foreground"
        }`}
      >
        {formatRate(row.failed, row.total)}
      </span>
    </li>
  );
}
