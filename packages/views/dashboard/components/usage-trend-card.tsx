"use client";

// 用量趋势卡片：同一根时间轴上用切换器展示 tokens/cost/time/tasks 四个指标。
import { useState } from "react";
import { BarChart3 } from "lucide-react";
import {
  DailyCostChart,
  DailyTokensChart,
  DailyTimeChart,
  DailyTasksChart,
  WeeklyCostChart,
  WeeklyTokensChart,
  WeeklyTimeChart,
  WeeklyTasksChart,
} from "../../runtimes/components/charts";
import { aggregateByWeek } from "../../runtimes/utils";
import { useT } from "../../i18n";
import {
  aggregateDailyCost,
  aggregateDailyTasks,
  aggregateDailyTime,
  aggregateDailyTokens,
  aggregateWeeklyTasks,
  aggregateWeeklyTime,
  formatDuration,
} from "../utils";
import { Segmented, type Dim } from "./dashboard-shared";
import { DimSegmented } from "./dim-segmented";

/** 用量趋势卡片可切换的四个指标。 */
type UsageMetric = "tokens" | "cost" | "time" | "tasks";

/**
 * 随时间的开销趋势：一根 x 轴，四个指标藏在切换器后面，让读者翻来翻去就能
 * 在脑子里把它们叠加起来对比。
 *
 * Errors 曾经是这里的第五个指标。它其实是另一个问题——「什么坏了」而不是「花了多少」，
 * 跟开销指标共用一个切换器意味着想看失败就必须先藏起开销。现在它已经在 Errors 标签页
 * 有了自己的图表。
 *
 * @param allowedDims 当前时间段允许的维度列表
 * @param dailyCost / dailyTokens / dailyTime / dailyTasks 日粒度的四个指标数据
 * @param weeklyCost / weeklyTokens / weeklyTime / weeklyTasks 周粒度的四个指标数据
 * @param lessThanMinuteLabel 不足一分钟时显示的文案
 */
export function UsageTrendCard({
  allowedDims,
  dailyCost,
  dailyTokens,
  dailyTime,
  dailyTasks,
  weeklyCost,
  weeklyTokens,
  weeklyTime,
  weeklyTasks,
  lessThanMinuteLabel,
}: {
  allowedDims: readonly Dim[];
  dailyCost: ReturnType<typeof aggregateDailyCost>;
  dailyTokens: ReturnType<typeof aggregateDailyTokens>;
  dailyTime: ReturnType<typeof aggregateDailyTime>;
  dailyTasks: ReturnType<typeof aggregateDailyTasks>;
  weeklyCost: ReturnType<typeof aggregateByWeek>["weeklyCostStack"];
  weeklyTokens: ReturnType<typeof aggregateByWeek>["weeklyTokens"];
  weeklyTime: ReturnType<typeof aggregateWeeklyTime>;
  weeklyTasks: ReturnType<typeof aggregateWeeklyTasks>;
  lessThanMinuteLabel: string;
}) {
  const { t } = useT("usage");
  const [metric, setMetric] = useState<UsageMetric>("tokens");
  const [dim, setDim] = useState<Dim>("daily");
  // 派生值，绝不回写 state：时间段收窄到 1d 时卡片只画该范围允许的那一个维度，
  // 之后再放宽又能恢复读者的选择；如果把纠正结果写回 state，卡片就会「忘记」用户
  // 原来的选择。
  const effectiveDim: Dim = allowedDims.includes(dim) ? dim : allowedDims[0]!;
  const weekly = effectiveDim === "weekly";

  // 空状态按指标分别判断，让每个切换选项独立决定自己有没有数据——例如已经有 token
  // 记录、但还没有任何已结束的运行，就应该正常显示 Tokens，而 Time / Tasks 落空显示。
  const costData = weekly ? weeklyCost : dailyCost;
  const tokensData = weekly ? weeklyTokens : dailyTokens;
  const timeData = weekly ? weeklyTime : dailyTime;
  const tasksData = weekly ? weeklyTasks : dailyTasks;

  const totalCost = costData.reduce((sum, d) => sum + d.total, 0);
  const totalTokens = tokensData.reduce(
    (sum, d) => sum + d.input + d.output + d.cacheRead + d.cacheWrite,
    0,
  );
  const totalSeconds = timeData.reduce((sum, d) => sum + d.totalSeconds, 0);
  const totalTasks = tasksData.reduce((sum, d) => sum + d.completed + d.failed, 0);
  const isEmpty =
    metric === "cost"
      ? totalCost === 0
      : metric === "tokens"
        ? totalTokens === 0
        : metric === "time"
          ? totalSeconds === 0
          : totalTasks === 0;

  const title = weekly
    ? metric === "cost"
      ? t(($) => $.weekly.title_cost)
      : metric === "tokens"
        ? t(($) => $.weekly.title_tokens)
        : metric === "time"
          ? t(($) => $.weekly.title_time)
          : t(($) => $.weekly.title_tasks)
    : metric === "cost"
      ? t(($) => $.daily.title_cost)
      : metric === "tokens"
        ? t(($) => $.daily.title_tokens)
        : metric === "time"
          ? t(($) => $.daily.title_time)
          : t(($) => $.daily.title_tasks);

  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-body font-semibold">{title}</h4>
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            label={t(($) => $.daily.metric_label)}
            value={metric}
            onChange={setMetric}
            options={[
              { label: t(($) => $.daily.metric_tokens), value: "tokens" as const },
              { label: t(($) => $.daily.metric_cost), value: "cost" as const },
              { label: t(($) => $.daily.metric_time), value: "time" as const },
              { label: t(($) => $.daily.metric_tasks), value: "tasks" as const },
            ]}
          />
          <DimSegmented allowedDims={allowedDims} value={effectiveDim} onChange={setDim} />
        </div>
      </div>
      <div className="min-h-[240px]">
        {isEmpty ? (
          <div className="flex aspect-[3/1] flex-col items-center justify-center gap-2 rounded-md border border-dashed bg-muted/20 p-6 text-center">
            <BarChart3 className="h-5 w-5 text-faint-foreground" />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.daily.no_data)}
            </p>
          </div>
        ) : weekly ? (
          metric === "cost" ? (
            <WeeklyCostChart data={weeklyCost} />
          ) : metric === "tokens" ? (
            <WeeklyTokensChart data={weeklyTokens} />
          ) : metric === "time" ? (
            <WeeklyTimeChart
              data={weeklyTime}
              formatY={(s) => formatDuration(s, lessThanMinuteLabel)}
              formatTooltip={(s) => formatDuration(s, lessThanMinuteLabel)}
            />
          ) : (
            <WeeklyTasksChart data={weeklyTasks} />
          )
        ) : metric === "cost" ? (
          <DailyCostChart data={dailyCost} />
        ) : metric === "tokens" ? (
          <DailyTokensChart data={dailyTokens} />
        ) : metric === "time" ? (
          <DailyTimeChart
            data={dailyTime}
            formatY={(s) => formatDuration(s, lessThanMinuteLabel)}
            formatTooltip={(s) => formatDuration(s, lessThanMinuteLabel)}
          />
        ) : (
          <DailyTasksChart data={dailyTasks} />
        )}
      </div>
    </div>
  );
}
