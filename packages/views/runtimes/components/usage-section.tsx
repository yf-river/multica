// ============================================================================
// 用量区（UsageSection）——「这台 runtime 这段时间花了多少钱」的核心区块
//
// 角色定位：单台 runtime 详情里的用量总览。自上而下由四层组成：
//  1. 顶部周期选择器（7d/30d/90d/180d × 按天/按周）；
//  2. 三张 KPI 卡（总费用 + 环比、缓存节省 + 命中率、总 token + 输入/输出拆分）；
//  3. WHEN 图（每日/每周的费用或 token 堆叠图，可切换成 26 周活动热力图）；
//  4. 成本归因（按 agent / 按模型）与可折叠的明细表。
//
// 数据流：后端按「查看者时区」的 UTC 小时滚桶 → useQuery 拉一次 180 天缓存 →
// 用 utils.sliceWindow 客户端切成「所选窗口 / 上一等长窗口」→ 汇总与图表。所有
// 聚合/估算函数来自 ../utils，把金额「厂商已收 + 费率表补差」两半相加。
//
// 周期选择器的唯一事实来源是 TIME_RANGES：KPI、WHEN 图、Cost-by 页签与
// CSV 导出都读同一个 `days` 值，保证标签（如 "· 30D"）与数据切片永不一致。
//
// 界面结构：loading → UsageSkeleton；无用量 → UsageEmpty；否则四层内容。
// ============================================================================
"use client";

import { useMemo, useState } from "react";
import { BarChart3, ChevronRight, AlertCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import {
  CompactNumberFlow,
  CurrencyNumberFlow,
} from "@multica/ui/components/ui/number-flow";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { RuntimeUsage, AgentRuntime } from "@multica/core/types";
import {
  runtimeUsageOptions,
  runtimeUsageByAgentOptions,
} from "@multica/core/runtimes/queries";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import {
  cacheHitRatePercent,
  formatTokens,
  formatUsd,
  estimateCost,
  hasKnownCost,
  hasKnownCacheSavings,
  estimateCacheSavings,
  aggregateByDate,
  aggregateByWeek,
  aggregateCostByAgent,
  aggregateCostByModel,
  collectUnmappedModels,
  pctChange,
  sliceWindow,
  type CostByKey,
} from "../utils";
import { KpiCard } from "./shared";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  DailyCostChart,
  DailyTokensChart,
  WeeklyCostChart,
  WeeklyTokensChart,
  ActivityHeatmap,
} from "./charts";
import { CustomPricingDialog } from "./custom-pricing-dialog";
import { useT } from "../../i18n";

// 周期选择器的唯一事实来源。KPI、WHEN 图、Cost-by 页签与 CSV 导出都读同一个
// `days` 值，保证标签（如 "· 30D"）与数据切片永远不会互相矛盾。
//
// `dims` 声明每个周期允许在哪些维度（按天/按周）下使用。7 天按周粒度聚合后
// 只有一根柱，所以 7d 仅限按天；180 天画 180 根日柱视觉上不可读，因此 180d
// 仅限按周。
const TIME_RANGES = [
  { label: "7d", days: 7, dims: ["daily"] as const },
  { label: "30d", days: 30, dims: ["daily", "weekly"] as const },
  { label: "90d", days: 90, dims: ["daily", "weekly"] as const },
  { label: "180d", days: 180, dims: ["weekly"] as const },
] as const;

type TimeRange = (typeof TIME_RANGES)[number]["days"];
type WhenTab = "daily" | "weekly" | "heatmap";

// 各维度对应的默认周期。切换维度时把周期重置为该维度的默认值，而不是保留
// 一个已经非法的旧值。
const DEFAULT_DAYS_BY_DIM: Record<Exclude<WhenTab, "heatmap">, TimeRange> = {
  daily: 30,
  weekly: 90,
};

// 返回某维度允许使用的周期选项。
function rangesForDim(dim: Exclude<WhenTab, "heatmap">) {
  return TIME_RANGES.filter((r) =>
    (r.dims as readonly string[]).includes(dim),
  );
}

// ---------------------------------------------------------------------------
// 本地分段控件。shadcn 的 Tabs 为整页页签设计，带有键盘导航与 ARIA 语义——
// 这些对一块紧凑的工具条药丸来说都是多余的。视觉样式：浅灰轨道 + 白色
// 「凸起」的活动药丸。
// ---------------------------------------------------------------------------

function Segmented<T extends string | number>({
  value,
  onChange,
  options,
  disabled,
}: {
  value: T;
  onChange: (v: T) => void;
  options: readonly { label: string; value: T }[];
  disabled?: boolean;
}) {
  return (
    <div
      className={`inline-flex items-center gap-0.5 rounded-md bg-muted p-0.5 ${
        disabled ? "opacity-50" : ""
      }`}
    >
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          disabled={disabled}
          onClick={() => onChange(o.value)}
          className={`rounded-sm px-2.5 py-1 text-caption font-medium transition-colors disabled:cursor-not-allowed ${
            o.value === value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 顶层编排组件。持有时间窗口，一次性拉取 180 天用量缓存，切成「当前 / 上一」
// 两个等长窗口做环比，再把数据分发给下面四个视觉区块。
//
// 180 天（相比旧版 90 天）是为热力图页签扩的——它展示 26 周（约 6 个月），
// 长周期视图才真的「长」。7d/30d/90d 的周期选择在客户端切片；费用 KPI 的
// 「上一周期」环比也受益于多出来的历史数据。
// ---------------------------------------------------------------------------

export function UsageSection({ runtime }: { runtime: AgentRuntime }) {
  const { t, i18n } = useT("runtimes");
  const runtimeId = runtime.id;
  // 报表按查看者时区渲染——后端用与我们这里相同的 `tz` 切 UTC 小时滚桶，
  // 因此前端每个窗口计算与服务端共享同一条时间轴。
  const tz = useViewingTimezone();
  const { data: usage = [], isLoading: loading } = useQuery(
    runtimeUsageOptions(runtimeId, 180, tz),
  );
  const [dim, setDim] = useState<Exclude<WhenTab, "heatmap">>("daily");
  const [days, setDays] = useState<TimeRange>(30);
  // 订阅自定义定价存储：KPI 卡在渲染时直接调 estimateCost（不经 memo），
  // 用户保存自定义费率后必须重算。聚合子组件（WhenChart、CostByBlock、
  // ActivityHeatmap）各自单独订阅，并把 pricings 作为各自 memo 的依赖。
  useCustomPricingStore((s) => s.pricings);

  if (loading) return <UsageSkeleton />;
  if (usage.length === 0) return <UsageEmpty />;

  // 把 180 天缓存切成用户选中的子窗口与紧邻其前的等长窗口。KPI 的环比
  // （"+18% vs prev"）由此比较「等长的两段时间」，而不是「本期 vs 全部历史」。
  // 感知时区：截断点落在后端分桶用的同一条自然日边界上。
  const { filtered, prevFiltered } = sliceWindow(usage, days, tz);

  const allowedRanges = rangesForDim(dim);
  const handleDimChange = (next: Exclude<WhenTab, "heatmap">) => {
    setDim(next);
    const stillAllowed = (rangesForDim(next) as readonly { days: number }[]).some(
      (r) => r.days === days,
    );
    if (!stillAllowed) {
      setDays(DEFAULT_DAYS_BY_DIM[next]);
    }
  };
  const totals = computeTotals(filtered);
  const prevTotals = computeTotals(prevFiltered);

  const tokensTotal =
    totals.input + totals.output + totals.cacheRead + totals.cacheWrite;
  const cacheHitRate = cacheHitRatePercent(
    totals.input,
    totals.cacheRead,
    totals.cacheWrite,
  );

  const costDelta = pctChange(totals.cost, prevTotals.cost);
  // 有 token 消耗、但没有任何一行能定价时，费用是"未知"而不是 0。
  const costKnown = tokensTotal === 0 || hasKnownCost(filtered);
  const cacheSavingsKnown = tokensTotal === 0 || hasKnownCacheSavings(filtered);
  const locales = i18n.resolvedLanguage ?? i18n.language;

  return (
    <div className="space-y-5">
      {/* 页面级周期选择器。放在顶部因为它控制着下面几乎一切：KPI 数字与
          标签、日/周图窗口、cost-by 聚合。热力图页签是唯一忽略它的子视图
          （永远显示 26 周），它的页签会禁用本控件来传达这一点。 */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span className="text-caption uppercase tracking-wider text-muted-foreground">
            {t(($) => $.usage.dimension_label)}
          </span>
          <Segmented
            value={dim}
            onChange={handleDimChange}
            options={
              [
                { label: t(($) => $.usage.when_tab_daily), value: "daily" },
                { label: t(($) => $.usage.when_tab_weekly), value: "weekly" },
              ] as const
            }
          />
        </div>
        <div className="flex items-center gap-3">
          <span className="text-caption uppercase tracking-wider text-muted-foreground">
            {t(($) => $.usage.period_label)}
          </span>
          <Segmented
            value={days}
            onChange={setDays}
            options={allowedRanges.map((r) => ({
              label: r.label,
              value: r.days,
            }))}
          />
        </div>
      </div>

      {/* 定价栏。放在 KPI 网格上方：当窗口内「部分模型可定价、部分不可」时
          （图表仍正常渲染），这里仍有一个进入手动定价对话框的可见入口——否则
          未定价的 token 会静默以 $0 计入总额。一旦所有模型都已定价且用户保存过
          覆盖价，这一栏仍保留入口，让那些费率随时可改、可删。 */}
      <CustomPricingBar usage={filtered} />

      <div className="grid grid-cols-3 divide-x rounded-lg border bg-card">
        <KpiCard
          label={t(($) => $.usage.kpi_cost_label, { days })}
          value={
            costKnown ? (
              <CurrencyNumberFlow
                value={totals.cost}
                locales={locales}
                aria-label={formatUsd(totals.cost)}
              />
            ) : (
              // 没有任何一行能定价时，$0.00 会被读成"这些运行免费"。实际是价格
              // 未知（厂商没报价、费率表也不认这个模型），必须显示破折号。
              <span aria-label={t(($) => $.usage.kpi_cost_unknown)}>—</span>
            )
          }
          hint={
            !costKnown ? (
              <span>{t(($) => $.usage.kpi_cost_unknown)}</span>
            ) : costDelta == null ? undefined : (
              <span
                className={
                  costDelta > 0
                    ? "text-warning"
                    : costDelta < 0
                      ? "text-success"
                      : ""
                }
              >
                {t(($) => $.usage.kpi_cost_delta, {
                  sign: costDelta > 0 ? "+" : "",
                  pct: costDelta,
                })}
              </span>
            )
          }
        />
        <KpiCard
          label={t(($) => $.usage.kpi_cache_label, { days })}
          value={
            cacheSavingsKnown ? (
              <CurrencyNumberFlow
                value={totals.cacheSavings}
                locales={locales}
                aria-label={formatUsd(totals.cacheSavings)}
              />
            ) : (
              // 同理：没有价格时"缓存省了多少钱"也是未知，不是 0。
              <span aria-label={t(($) => $.usage.kpi_cost_unknown)}>—</span>
            )
          }
          accent={
            cacheSavingsKnown && totals.cacheSavings > 0 ? "success" : "default"
          }
          hint={
            <span>
              {cacheHitRate == null
                ? "—"
                : t(($) => $.usage.kpi_cache_hint, {
                    pct: cacheHitRate,
                    reads: formatTokens(totals.cacheRead),
                  })}
            </span>
          }
        />
        <KpiCard
          label={t(($) => $.usage.kpi_tokens_label, { days })}
          value={
            <CompactNumberFlow
              value={tokensTotal}
              locales={locales}
              aria-label={formatTokens(tokensTotal)}
            />
          }
          hint={
            <span>
              {t(($) => $.usage.kpi_tokens_hint, {
                input: formatTokens(totals.input),
                output: formatTokens(totals.output),
              })}
            </span>
          }
        />
      </div>

      {/* 第 2 层——WHEN 图。维度（日/周）由父组件持有，顶部的周期选择器才能
          响应它。热力图是这张卡片内部的独立开关——按设计忽略周期选择器
          （固定 26 周的长周期视图）。 */}
      <WhenChart
        usage={usage}
        filtered={filtered}
        days={days}
        dim={dim}
        tz={tz}
      />

      {/* 第 3 层——是谁（哪个 agent / 哪个模型）烧掉了这笔钱。 */}
      <CostByBlock runtimeId={runtimeId} days={days} usage={filtered} tz={tz} />

      {/* 第 4 层——可折叠的原始视图。热力图原本也在这里，后来被提升到 WHEN
          图的切换里，这里只剩明细表。 */}
      <FoldedRow usage={filtered} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// WhenChart——回答「这台 runtime 是在什么时候花钱的？」
// 维度（日/周）由父组件持有，页面级周期选择器才能响应它。热力图是这张卡片
// 内切换的独立视图；它同时忽略维度与周期选择器，永远展示 26 周长周期。
// ---------------------------------------------------------------------------

type Dim = Exclude<WhenTab, "heatmap">;
type DailyMetric = "cost" | "tokens";

function WhenChart({
  usage,
  filtered,
  days,
  dim,
  tz,
}: {
  usage: RuntimeUsage[];
  filtered: RuntimeUsage[];
  days: TimeRange;
  dim: Dim;
  tz: string;
}) {
  const { t } = useT("runtimes");
  // 热力图是「独立」的兄弟视图——在这里切换，不属于页面级维度分段控件。
  const [showHeatmap, setShowHeatmap] = useState(false);
  // 日/周视图共用同一个「费用 vs token」指标切换。
  const [chartMetric, setChartMetric] = useState<DailyMetric>("cost");
  // Memo 依赖——下面的聚合会执行 estimateCost，它现在会查用户覆盖存储。
  // 不把 pricings 列进依赖，查询数据没变时 memo 会缓存「覆盖前的旧总额」。
  const pricings = useCustomPricingStore((s) => s.pricings);

  const { dailyCostStack, dailyTokens } = useMemo(
    () => aggregateByDate(filtered),
    [filtered, pricings],
  );
  // 周聚合恰好构建「锚定今天（运行时 tz）的 N 个尾部自然周」。空桶在
  // aggregateByWeek 内部已预置零值，没有用量的周会渲染成空柱；窗口外的行被
  // 丢弃。这避免了旧 bug：在稀疏的 180 天聚合上做切片，会冒出「早期有数据的
  // 老周」而不是「窗口内的空周」。
  const weekCount = Math.max(1, Math.ceil(days / 7));
  const { weeklyTokens, weeklyCostStack } = useMemo(
    () => aggregateByWeek(usage, tz, weekCount),
    [usage, tz, weekCount, pricings],
  );

  const metricToggleVisible = !showHeatmap;
  // 两种指标现在都带缓存读取段：token 堆叠一直都有；费用堆叠在不再把缓存
  // 读取的开销从总额中剔除后也补上了（MUL-6334）。
  const legendIncludesCacheRead = !showHeatmap;

  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <h4 className="text-body font-semibold">{t(($) => $.usage.when_title)}</h4>
          {/* 费用 / token 指标切换——只有当图表真的有两种序列类型可切时才有
              意义。 */}
          {metricToggleVisible && (
            <Segmented
              value={chartMetric}
              onChange={setChartMetric}
              options={
                [
                  { label: t(($) => $.usage.daily_metric_cost), value: "cost" },
                  { label: t(($) => $.usage.daily_metric_tokens), value: "tokens" },
                ] as const
              }
            />
          )}
          {/* 热力图开关——独立于页面级的维度分段控件。打开后本卡片进入固定
              26 周的长周期视图，忽略周期选择器。 */}
          <button
            type="button"
            onClick={() => setShowHeatmap((v) => !v)}
            className={`rounded-md border px-2.5 py-1 text-caption font-medium transition-colors ${
              showHeatmap
                ? "border-foreground bg-foreground text-background"
                : "border-border text-muted-foreground hover:text-foreground"
            }`}
            aria-pressed={showHeatmap}
          >
            {t(($) => $.usage.when_tab_heatmap)}
          </button>
        </div>
        {!showHeatmap && (
          <ChartLegend includeCacheRead={legendIncludesCacheRead} />
        )}
      </div>

      {showHeatmap && (
        <p className="mb-2 text-center text-caption text-muted-foreground">
          {t(($) => $.usage.heatmap_caption)}
        </p>
      )}

      <div className="min-h-[260px]">
        {showHeatmap ? (
          <ActivityHeatmap usage={usage} tz={tz} />
        ) : dim === "daily" ? (
          <DailyTab
            metric={chartMetric}
            costData={dailyCostStack}
            tokensData={dailyTokens}
            usage={filtered}
          />
        ) : (
          <WeeklyTab
            metric={chartMetric}
            costData={weeklyCostStack}
            tokensData={weeklyTokens}
            usage={filtered}
          />
        )}
      </div>
    </div>
  );
}

function DailyTab({
  metric,
  costData,
  tokensData,
  usage,
}: {
  metric: DailyMetric;
  costData: Parameters<typeof DailyCostChart>[0]["data"];
  tokensData: Parameters<typeof DailyTokensChart>[0]["data"];
  usage: RuntimeUsage[];
}) {
  if (metric === "tokens") {
    // token 图自带空状态：若没有任何 token 被记录，图表确实为空（与定价无关
    // ——未映射定价的模型仍然贡献原始 token 计数）。
    const totalTokens = tokensData.reduce(
      (s, d) => s + d.input + d.output + d.cacheRead + d.cacheWrite,
      0,
    );
    if (totalTokens === 0) return <EmptyChartState usage={usage} />;
    return <DailyTokensChart data={tokensData} />;
  }
  const totalCost = costData.reduce((s, d) => s + d.total, 0);
  if (totalCost === 0) return <EmptyChartState usage={usage} />;
  return <DailyCostChart data={costData} />;
}

function WeeklyTab({
  metric,
  costData,
  tokensData,
  usage,
}: {
  metric: DailyMetric;
  costData: Parameters<typeof WeeklyCostChart>[0]["data"];
  tokensData: Parameters<typeof WeeklyTokensChart>[0]["data"];
  usage: RuntimeUsage[];
}) {
  if (metric === "tokens") {
    const totalTokens = tokensData.reduce(
      (s, d) => s + d.input + d.output + d.cacheRead + d.cacheWrite,
      0,
    );
    if (totalTokens === 0) return <EmptyChartState usage={usage} />;
    return <WeeklyTokensChart data={tokensData} />;
  }
  const totalCost = costData.reduce((s, d) => s + d.total, 0);
  if (totalCost === 0) return <EmptyChartState usage={usage} />;
  return <WeeklyCostChart data={costData} />;
}

// ---------------------------------------------------------------------------
// EmptyChartState——「图表本会渲染成空白」时的替换占位。区分两种情况：
//   1. 完全没有 token → 「没有用量」（确实什么都没发生）。
//   2. 有 token 但费用是 $0 → 几乎总意味着 daemon 上报的模型名不在我们的
//      定价表里。把「罪魁祸首」列出来，方便开发者一次性更新 MODEL_PRICING。
// ---------------------------------------------------------------------------

function EmptyChartState({ usage }: { usage: RuntimeUsage[] }) {
  const { t } = useT("runtimes");
  const hasTokens = usage.some(
    (u) =>
      u.input_tokens + u.output_tokens + u.cache_read_tokens + u.cache_write_tokens >
      0,
  );
  const unmapped = collectUnmappedModels(usage);

  return (
    <div className="flex aspect-[3/1] flex-col items-center justify-center gap-2 rounded-md border border-dashed bg-muted/20 p-6 text-center">
      <BarChart3 className="h-5 w-5 text-faint-foreground" />
      {!hasTokens ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.usage.empty_no_usage)}
        </p>
      ) : unmapped.length > 0 ? (
        // 操作入口在上方页面级的 UnmappedPricingNotice 里。这里只写说明性
        // 文案，避免两个 UI 互相抢话。
        <>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.usage.empty_pricing_missing)}
          </p>
          <p className="font-mono text-micro text-foreground">
            {unmapped.join(", ")}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.usage.empty_pricing_hint)}
          </p>
        </>
      ) : (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.usage.empty_zero_cost)}
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// CustomPricingBar——自定义定价对话框的唯一入口，渲染在 KPI 网格上方。两种状态：
//
//   1. 窗口内含无法定价的模型 → 告警横幅。覆盖「部分不可定价」的情形：图表
//      仍会渲染（EmptyChartState 不会触发），但一些 token 在静默地以 $0 计入。
//   2. 全部可定价、但用户保存过覆盖价 → 一行低调的提示，仍提供进入对话框的
//      入口。以前把整条栏的显示条件绑在「有未定价模型」上，导致最后一个覆盖
//      价保存后横幅就消失了：覆盖价让模型变得可解析、横幅消失，已保存的费率
//      就再也没法从 UI 里更正或删除了。
//
// 当既无未定价模型、又无覆盖价时整条隐藏。
// ---------------------------------------------------------------------------

function CustomPricingBar({ usage }: { usage: RuntimeUsage[] }) {
  const { t } = useT("runtimes");
  const [dialogOpen, setDialogOpen] = useState(false);
  // 用布尔值而非对象本身作为选择器返回值，保证引用稳定、避免多余渲染。
  const hasOverrides = useCustomPricingStore(
    (s) => Object.keys(s.pricings).length > 0,
  );
  const unmapped = collectUnmappedModels(usage);
  if (unmapped.length === 0 && !hasOverrides) return null;

  const hasGap = unmapped.length > 0;
  return (
    <div
      role={hasGap ? "alert" : undefined}
      className={cn(
        "flex flex-wrap items-center gap-3 rounded-lg border px-3 py-2 text-caption",
        hasGap ? "border-warning/30 bg-warning/10" : "bg-muted/20",
      )}
    >
      {hasGap ? (
        <>
          <AlertCircle className="h-4 w-4 shrink-0 text-warning" />
          <div className="min-w-0 flex-1 space-y-0.5">
            <p className="text-foreground">
              {t(($) => $.usage.unmapped_notice, { count: unmapped.length })}
            </p>
            <p className="truncate font-mono text-micro text-muted-foreground">
              {unmapped.join(", ")}
            </p>
          </div>
        </>
      ) : (
        <p className="min-w-0 flex-1 text-muted-foreground">
          {t(($) => $.usage.custom_pricing.active_notice)}
        </p>
      )}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setDialogOpen(true)}
      >
        {hasGap
          ? t(($) => $.usage.custom_pricing.open_button)
          : t(($) => $.usage.custom_pricing.edit_button)}
      </Button>
      <CustomPricingDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        unmappedModels={unmapped}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// 图表图例——堆叠的每一段一个色点 + 标签，渲染在 WhenChart 头部，让图表主体
// 保留全部纵向空间。
// ---------------------------------------------------------------------------

function ChartLegend({ includeCacheRead = false }: { includeCacheRead?: boolean }) {
  const { t } = useT("runtimes");
  // 缓存读取的色点放在输出与缓存写入之间，与 token、费用两个堆叠的段序一致。
  // 只有完全没堆叠的热力图不显示它。
  const items = [
    { label: t(($) => $.usage.legend_input), color: "var(--color-chart-1)" },
    { label: t(($) => $.usage.legend_output), color: "var(--color-chart-2)" },
    ...(includeCacheRead
      ? [{ label: t(($) => $.usage.legend_cache_read), color: "var(--color-chart-4)" }]
      : []),
    { label: t(($) => $.usage.legend_cache_write), color: "var(--color-chart-3)" },
  ];
  return (
    <div className="flex items-center gap-3 text-caption text-muted-foreground">
      {items.map((it) => (
        <span key={it.label} className="inline-flex items-center gap-1.5">
          <span
            className="h-2 w-2 rounded-sm"
            style={{ background: it.color }}
          />
          {it.label}
        </span>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Cost-by 区块：双页签归因视图（按 agent / 按模型）。
// ---------------------------------------------------------------------------

function CostByBlock({
  runtimeId,
  days,
  usage,
  tz,
}: {
  runtimeId: string;
  days: number;
  usage: RuntimeUsage[];
  tz: string;
}) {
  const { t } = useT("runtimes");
  const [tab, setTab] = useState<"agent" | "model">("agent");
  // Memo 依赖——理由同 WhenChart：aggregateCostBy{Agent,Model} 会调
  // estimateCost，后者现在会读覆盖存储。
  const pricings = useCustomPricingStore((s) => s.pricings);

  // 按 agent 是服务端聚合（切到该页签时才懒加载）；按模型由父组件已有的
  // 日粒度缓存推导——零额外请求。
  const { data: byAgentRows = [] } = useQuery({
    ...runtimeUsageByAgentOptions(runtimeId, days, tz),
    enabled: tab === "agent",
  });

  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));

  const byAgent = useMemo(
    () => aggregateCostByAgent(byAgentRows),
    [byAgentRows, pricings],
  );
  const byModel = useMemo(
    () => aggregateCostByModel(usage),
    [usage, pricings],
  );

  const caption =
    tab === "agent"
      ? t(($) => $.usage.cost_by_caption_agent, { count: byAgent.length })
      : t(($) => $.usage.cost_by_caption_model, { count: byModel.length });

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b pb-3">
        <div className="flex items-center gap-3">
          <h4 className="text-body font-semibold">
            {tab === "agent"
              ? t(($) => $.usage.cost_by_title_agent)
              : t(($) => $.usage.cost_by_title_model)}
          </h4>
          <Segmented
            value={tab}
            onChange={setTab}
            options={
              [
                { label: t(($) => $.usage.cost_by_tab_agent), value: "agent" },
                { label: t(($) => $.usage.cost_by_tab_model), value: "model" },
              ] as const
            }
          />
        </div>
        <span className="text-caption text-muted-foreground">{caption}</span>
      </div>
      <div className="pt-4">
        {tab === "agent" && (
          <CostByList
            rows={byAgent}
            renderKey={(key) => {
              const agent = agents.find((a) => a.id === key);
              return (
                <div className="flex min-w-0 items-center gap-2">
                  <ActorAvatar actorType="agent" actorId={key} size="md" enableHoverCard />
                  <span className="cursor-pointer truncate text-body font-medium">
                    {agent?.name ?? key}
                  </span>
                </div>
              );
            }}
          />
        )}
        {tab === "model" && (
          <CostByList
            rows={byModel}
            renderKey={(key) => (
              <span className="truncate font-mono text-caption text-foreground">
                {key}
              </span>
            )}
          />
        )}
      </div>
    </div>
  );
}

// 两个 Cost-by 页签共用的通用横向条形列表。每行的条相对该组最重的一行
// 缩放，视觉排名恒为 0..max，最大花销者视觉上占满整列。
function CostByList({
  rows,
  renderKey,
  emptyHint,
}: {
  rows: CostByKey[];
  renderKey: (key: string) => React.ReactNode;
  emptyHint?: string;
}) {
  const { t } = useT("runtimes");
  if (rows.length === 0) {
    return (
      <p className="py-4 text-center text-caption text-muted-foreground">
        {emptyHint ?? t(($) => $.usage.empty_no_usage)}
      </p>
    );
  }
  const maxCost = rows.reduce((m, r) => Math.max(m, r.cost), 0);
  return (
    <div className="space-y-2">
      {rows.map((row) => {
        const pct = maxCost > 0 ? (row.cost / maxCost) * 100 : 0;
        return (
          <div
            key={row.key}
            className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)_5rem_5rem] items-center gap-3 py-1"
          >
            <div className="min-w-0">{renderKey(row.key)}</div>
            <div className="relative h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-chart-1"
                style={{ width: `${pct}%` }}
              />
            </div>
            <div className="text-right text-caption tabular-nums text-muted-foreground">
              {formatTokens(row.tokens)}
            </div>
            <div className="text-right text-body font-medium tabular-nums">
              ${row.cost.toFixed(2)}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 可折叠行——一个 chevron 开关链接，展开后露出原始明细表。活动热力图原本
// 也在这里，后被提升为 WhenChart 的切换项，这里只剩明细表。
// ---------------------------------------------------------------------------

function FoldedRow({ usage }: { usage: RuntimeUsage[] }) {
  const { t } = useT("runtimes");
  const [open, setOpen] = useState(false);
  return (
    <div className="border-t pt-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1 text-caption text-muted-foreground transition-colors hover:text-foreground"
      >
        <ChevronRight
          className={`h-3 w-3 transition-transform ${open ? "rotate-90" : ""}`}
        />
        {t(($) => $.usage.daily_breakdown_toggle)}
      </button>
      {open && (
        <div className="mt-3 rounded-md border p-4">
          <DailyBreakdownTable usage={usage} />
        </div>
      )}
    </div>
  );
}

function DailyBreakdownTable({ usage }: { usage: RuntimeUsage[] }) {
  const { t } = useT("runtimes");
  const byDate = new Map<string, RuntimeUsage[]>();
  for (const u of usage) {
    const existing = byDate.get(u.date) ?? [];
    existing.push(u);
    byDate.set(u.date, existing);
  }
  return (
    <div className="rounded-lg border">
      <div className="grid grid-cols-[100px_1fr_80px_80px_80px_80px] gap-2 border-b px-3 py-2 text-caption font-medium text-muted-foreground">
        <div>{t(($) => $.usage.table_date)}</div>
        <div>{t(($) => $.usage.table_model)}</div>
        <div className="text-right">{t(($) => $.usage.table_input)}</div>
        <div className="text-right">{t(($) => $.usage.table_output)}</div>
        <div className="text-right">{t(($) => $.usage.table_cache_r)}</div>
        <div className="text-right">{t(($) => $.usage.table_cache_w)}</div>
      </div>
      <div className="max-h-64 overflow-y-auto divide-y">
        {[...byDate.entries()].map(([date, rows]) =>
          rows.map((row, i) => (
            <div
              key={`${date}-${row.model}-${i}`}
              className="grid grid-cols-[100px_1fr_80px_80px_80px_80px] gap-2 px-3 py-1.5 text-caption"
            >
              <div className="text-muted-foreground">{date}</div>
              <div className="truncate font-mono">{row.model}</div>
              <div className="text-right tabular-nums">
                {formatTokens(row.input_tokens)}
              </div>
              <div className="text-right tabular-nums">
                {formatTokens(row.output_tokens)}
              </div>
              <div className="text-right tabular-nums">
                {formatTokens(row.cache_read_tokens)}
              </div>
              <div className="text-right tabular-nums">
                {formatTokens(row.cache_write_tokens)}
              </div>
            </div>
          )),
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 加载中与空状态
// ---------------------------------------------------------------------------

function UsageSkeleton() {
  return (
    <div className="space-y-5">
      <Skeleton className="h-28 rounded-lg" />
      <Skeleton className="h-56 rounded-lg" />
      <Skeleton className="h-32" />
    </div>
  );
}

function UsageEmpty() {
  const { t } = useT("runtimes");
  return (
    <div className="flex flex-col items-center rounded-lg border border-dashed py-8">
      <BarChart3 className="h-5 w-5 text-faint-foreground" />
      <p className="mt-2 text-caption text-muted-foreground">
        {t(($) => $.usage.no_data)}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// 把一行行用量聚成总额（四种 token、总费用、缓存节省），KPI 卡渲染用。
interface UsageTotals {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  cost: number;
  cacheSavings: number;
}

function computeTotals(rows: RuntimeUsage[]): UsageTotals {
  return rows.reduce<UsageTotals>(
    (acc, u) => ({
      input: acc.input + u.input_tokens,
      output: acc.output + u.output_tokens,
      cacheRead: acc.cacheRead + u.cache_read_tokens,
      cacheWrite: acc.cacheWrite + u.cache_write_tokens,
      cost: acc.cost + estimateCost(u),
      cacheSavings: acc.cacheSavings + estimateCacheSavings(u),
    }),
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0, cacheSavings: 0 },
  );
}
