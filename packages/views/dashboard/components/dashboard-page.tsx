"use client";

// 工作区用量仪表盘页面：Usage（花了多少）和 Errors（坏了什么）两个标签页。
import { useMemo, useState } from "react";
import { BarChart3, RefreshCw } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import {
  CompactNumberFlow,
  CurrencyNumberFlow,
  NumberFlow,
} from "@multica/ui/components/ui/number-flow";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Agent } from "@multica/core/types";
import { agentListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  dashboardKeys,
  dashboardUsageDailyOptions,
  dashboardUsageByAgentOptions,
  dashboardAgentRunTimeOptions,
  dashboardRunTimeDailyOptions,
  dashboardFailuresDailyOptions,
  dashboardFailuresByAgentOptions,
} from "@multica/core/dashboard";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { PAGE_GUTTER } from "../../layout/page-header";
import { CollectionPageHeader } from "../../layout/collection-page";
import { KpiCard } from "../../runtimes/components/shared";
import { useNavigation } from "../../navigation";
import {
  addDaysIso,
  aggregateByWeek,
  formatTokens,
  hasKnownCost,
  hasUnknownCostRemainder,
  todayIso,
} from "../../runtimes/utils";
import { useT } from "../../i18n";
import {
  aggregateAgentFailures,
  aggregateAgentTokens,
  aggregateDailyCost,
  aggregateDailyErrors,
  aggregateDailyTasks,
  aggregateDailyTime,
  aggregateDailyTokens,
  aggregateFailureClasses,
  aggregateFailureReasons,
  aggregateWeeklyErrors,
  aggregateWeeklyTasks,
  aggregateWeeklyTime,
  bucketUnknownAgentRows,
  anonymizeUnresolvedAgentRows,
  costKnownByAgent as buildCostKnownByAgent,
  computeDailyTotals,
  computeFailureTotals,
  isSyntheticAgentRow,
  mergeAgentDashboardRows,
} from "../utils";
import {
  ALL_PROJECTS,
  DurationNumberFlow,
  dimsForDays,
  type TimeRange,
} from "./dashboard-shared";
import { ProjectFilter, TimeRangeFilter } from "./dashboard-filters";
import { UsageTrendCard } from "./usage-trend-card";
import { Leaderboard } from "./leaderboard";
import { ErrorsTab } from "./errors-tab";
import { cn } from "@multica/ui/lib/utils";

// 稳定的空数组引用——`data ?? []` 会在查询加载期间的每次渲染都新建一个空数组，
// 破坏 useMemo 的引用相等性依赖检查，还会触发 exhaustive-deps 的 lint 规则。
const EMPTY_DAILY: import("@multica/core/types").DashboardUsageDaily[] = [];
const EMPTY_BY_AGENT: import("@multica/core/types").DashboardUsageByAgent[] = [];
const EMPTY_RUNTIME: import("@multica/core/types").DashboardAgentRunTime[] = [];
const EMPTY_RUNTIME_DAILY: import("@multica/core/types").DashboardRunTimeDaily[] = [];
const EMPTY_FAILURE_DAILY: import("@multica/core/types").DashboardFailureDaily[] = [];
const EMPTY_FAILURE_BY_AGENT: import("@multica/core/types").DashboardFailureByAgent[] =
  [];
const EMPTY_AGENTS: Agent[] = [];

type DashboardTab = "usage" | "errors";
const TAB_QUERY_KEY = "tab";
const DEFAULT_TAB: DashboardTab = "usage";

/** 最近一次成功抓取数据的时间，按查看者的时区换算成本地时间。
 *  这个页面上的每个数字都按该时区分桶，所以头部要标明是哪个时区——同一批数据
 *  换一个时区就是另一个答案，而页面以前从不承认这一点。 */
function useDataFreshness(
  updatedAts: (number | undefined)[],
  viewTZ: string,
  locales: Intl.LocalesArgument,
): { tzLabel: string | null; updatedLabel: string | null } {
  return useMemo(() => {
    const stamps = updatedAts.filter(
      (n): n is number => typeof n === "number" && n > 0,
    );
    const latest = stamps.length > 0 ? Math.max(...stamps) : null;
    // 存储的时区是用户输入，未经校验就传到了这里；Intl 遇到不认识的字符串会抛异常，
    // 而一个头部标签不值得为此把整页搞崩。
    try {
      const tzLabel =
        new Intl.DateTimeFormat(locales, {
          timeZone: viewTZ,
          timeZoneName: "shortOffset",
        })
          .formatToParts(new Date())
          .find((part) => part.type === "timeZoneName")?.value ?? null;
      const updatedLabel =
        latest === null
          ? null
          : new Intl.DateTimeFormat(locales, {
              timeZone: viewTZ,
              hour: "2-digit",
              minute: "2-digit",
            }).format(new Date(latest));
      return { tzLabel, updatedLabel };
    } catch {
      return { tzLabel: null, updatedLabel: null };
    }
    // `updatedAts` 每次渲染都是新数组；把它展开进依赖列表，让 memo 以「值」而非
    // 「引用」作为缓存键。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...updatedAts, viewTZ, locales]);
}

/**
 * 工作区 + 项目用量仪表盘。
 *
 * 路由在 `/{slug}/usage`。两个标签页，按「读者带着什么问题进来」而非「由哪个
 * 汇总接口喂数据」来切分：Usage 回答「花了多少」，Errors 回答「坏了什么」。它们
 * 以前挤在同一张滚动页里，失败明细压在排行榜下面（排行榜本身就可能拉到三十行），
 * 想看失败图表就只能先藏起开销。
 *
 * 作用范围由控件所在的位置来表达：头部下方的工具栏承载标签页和两个页面级筛选
 * （时间范围、项目），每张卡片则自带各自的视图切换。六个汇总接口在两个标签页
 * 都会抓取——数据量小，预取正是切换标签页能秒开的原因——但加载态和空态是
 * 按标签页区分的，所以 Usage 不必等失败的查询。
 *
 * 成本计算在客户端用 runtimes utils 完成——让仪表盘和运行时页面共用同一张价格表。
 */
export function DashboardPage() {
  const { t, i18n } = useT("usage");
  const wsId = useWorkspaceId();
  const viewTZ = useViewingTimezone();
  const navigation = useNavigation();
  const locales = i18n.resolvedLanguage ?? i18n.language;
  const [days, setDays] = useState<TimeRange>(30);
  const [projectValue, setProjectValue] = useState<string>(ALL_PROJECTS);

  // 标签页放在 URL 里，因为 Errors 视图正是这页里人们互相粘贴链接的那一半
  // （"这个 agent 失败了 54 次，你自己看"）；组件状态是没法被链接到的。用 `replace`
  // 而不是 `push`，这样切标签页不会堆出一堆历史记录。未知的 ?tab= 值回退到 Usage，
  // 而不是渲染一片空白。
  const tabFromUrl = navigation.searchParams.get(TAB_QUERY_KEY);
  const tab: DashboardTab = tabFromUrl === "errors" ? "errors" : DEFAULT_TAB;
  const handleTabChange = (next: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    if (next === DEFAULT_TAB) params.delete(TAB_QUERY_KEY);
    else params.set(TAB_QUERY_KEY, next);
    const query = params.toString();
    navigation.replace(query ? `${navigation.pathname}?${query}` : navigation.pathname);
  };

  // 用户可以在 runtimes 页保存模型价格；订阅这个 store，让他们保存时触发重渲染，
  // 使仪表盘反映新的费率。
  const pricings = useCustomPricingStore((s) => s.pricings);

  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const agentsQuery = useQuery(agentListOptions(wsId));
  const agents = agentsQuery.data ?? EMPTY_AGENTS;

  // 把选中的项目放到当前工作区的列表里校验。一个过期的 UUID——来自被删除的项目，
  // 或切换工作区后的残留——会悄悄把每个查询都过滤成空行，而头部却仍显示「所有项目」。
  // 推导出真正生效的筛选，让 API 调用与用户可见的选择保持一致。
  const projectId = useMemo(() => {
    if (projectValue === ALL_PROJECTS) return null;
    return projects.some((p) => p.id === projectValue) ? projectValue : null;
  }, [projectValue, projects]);

  // 周图表要画 `ceil(days / 7)` 个以「今天所在 UTC」为锚的末尾日历周。最坏
  // 情况（今天是周日）下最左侧的周一在 `weekCount * 7 - 1` 天前，直接按
  // `days=30` 去请求会静默截掉最左的桶。这里把按日期查询多取一些天，覆盖
  // 完整的第一周。
  //
  // 无条件多取（不只是图表处于周粒度时）：维度现在由卡片自己控制，页面无法
  // 知道当前屏上是哪种粒度——两个粒度取更宽的那个，卡片在 Daily/Weekly 之间
  // 切换就永远不会重新拉取。每日聚合在客户端用下面的 `dailyCutoffIso` 精确裁回
  // `days`，多取的行不影响它们展示的内容。按 Agent 的汇总保持 `days`，让
  // KPI/排行榜标签（如 "Tasks · 30D"）仍对应它们宣称的窗口。
  const weekCount = Math.max(1, Math.ceil(days / 7));
  const chartFetchDays = weekCount * 7;

  const dailyQuery = useQuery(
    dashboardUsageDailyOptions(wsId, chartFetchDays, projectId, viewTZ),
  );
  // 三个按 Agent 的汇总不携带日期，下面的 `dailyCutoffIso` 裁不到它们——它们的
  // 窗口在服务端按精确的 `days` 个日历桶闭合（parseExactSinceParamInTZ）。凡从
  // 这三个派生出来的数据已经与裁剪后的每日序列同跨距；不要再把按 Agent 的汇总
  // 放到 N+1 的 cutoff 上，否则排行榜和 Run time/Tasks KPI 会悄悄宽出一天，而
  // 旁边的图表和 Cost/Tokens KPI 却没有（MUL-5551）。
  const byAgentQuery = useQuery(
    dashboardUsageByAgentOptions(wsId, days, projectId, viewTZ),
  );
  const runTimeQuery = useQuery(
    dashboardAgentRunTimeOptions(wsId, days, projectId, viewTZ),
  );
  const runTimeDailyQuery = useQuery(
    dashboardRunTimeDailyOptions(wsId, chartFetchDays, projectId, viewTZ),
  );
  const failuresDailyQuery = useQuery(
    dashboardFailuresDailyOptions(wsId, chartFetchDays, projectId, viewTZ),
  );
  const failuresByAgentQuery = useQuery(
    dashboardFailuresByAgentOptions(wsId, days, projectId, viewTZ),
  );

  const dailyUsage = dailyQuery.data ?? EMPTY_DAILY;
  const byAgentUsage = byAgentQuery.data ?? EMPTY_BY_AGENT;
  const runTimeRows = runTimeQuery.data ?? EMPTY_RUNTIME;
  const runTimeDailyRows = runTimeDailyQuery.data ?? EMPTY_RUNTIME_DAILY;
  const failureDailyRows = failuresDailyQuery.data ?? EMPTY_FAILURE_DAILY;
  const failureByAgentRows = failuresByAgentQuery.data ?? EMPTY_FAILURE_BY_AGENT;

  const queryClient = useQueryClient();
  // "Refreshing" 覆盖六个汇总任一在途的情况，无论刷新由什么触发（按钮、定时器、
  // 挂载）——头部旋转图标与时间戳描述的是同一组查询。
  const isRefreshing =
    dailyQuery.isFetching ||
    byAgentQuery.isFetching ||
    runTimeQuery.isFetching ||
    runTimeDailyQuery.isFetching ||
    failuresDailyQuery.isFetching ||
    failuresByAgentQuery.isFetching;
  const handleRefresh = () => {
    void queryClient.invalidateQueries({ queryKey: dashboardKeys.all(wsId) });
  };

  const { tzLabel, updatedLabel } = useDataFreshness(
    [
      dailyQuery.dataUpdatedAt,
      byAgentQuery.dataUpdatedAt,
      runTimeQuery.dataUpdatedAt,
      runTimeDailyQuery.dataUpdatedAt,
      failuresDailyQuery.dataUpdatedAt,
      failuresByAgentQuery.dataUpdatedAt,
    ],
    viewTZ,
    locales,
  );

  // 每日聚合的展示面重新限定到用户选的 `days`（尽管按日期的查询为周图表多取了）。
  // 截止锚在查看者的时区上——与后端切 `bucket_hour` 的轴相同——所以落在同一个
  // 日历边界。两个维度都应用，这样 1d 严格表示「今天」，即使撞到午夜边界（用墙钟
  // 时间裁会把昨天也含进来）。
  const dailyCutoffIso = useMemo(
    () => addDaysIso(todayIso(viewTZ), -(days - 1)),
    [days, viewTZ],
  );
  const dailyUsageInWindow = useMemo(
    () => dailyUsage.filter((u) => u.date >= dailyCutoffIso),
    [dailyUsage, dailyCutoffIso],
  );
  const runTimeDailyInWindow = useMemo(
    () => runTimeDailyRows.filter((r) => r.date >= dailyCutoffIso),
    [runTimeDailyRows, dailyCutoffIso],
  );
  const failureDailyInWindow = useMemo(
    () => failureDailyRows.filter((r) => r.date >= dailyCutoffIso),
    [failureDailyRows, dailyCutoffIso],
  );

  // 加载与空态按标签页分开：Usage 标签页没理由等两个失败汇总；一个「有花费但没有
  // 失败」的工作区也不是空仪表盘。
  const usageLoading =
    dailyQuery.isLoading ||
    byAgentQuery.isLoading ||
    runTimeQuery.isLoading ||
    runTimeDailyQuery.isLoading;
  const errorsLoading =
    failuresDailyQuery.isLoading || failuresByAgentQuery.isLoading;

  const usageHasNoData =
    !usageLoading &&
    dailyUsage.length === 0 &&
    byAgentUsage.length === 0 &&
    runTimeRows.length === 0 &&
    runTimeDailyRows.length === 0;

  // 成本 / token 计算——usage、days 或自定义价格变化时重新派生。
  const totals = useMemo(
    () => computeDailyTotals(dailyUsageInWindow),
    [dailyUsageInWindow, pricings],
  );
  // 整段窗口内没有任何一行能定价时，费用是"未知"而不是 0——直接渲染 $0.00
  // 等于断言"这次一分钱没花"，而我们并没有这个依据。
  const costKnown = useMemo(
    () => hasKnownCost(dailyUsageInWindow),
    [dailyUsageInWindow, pricings],
  );
  const dailyCost = useMemo(
    () => aggregateDailyCost(dailyUsageInWindow),
    [dailyUsageInWindow, pricings],
  );
  const dailyTokens = useMemo(
    () => aggregateDailyTokens(dailyUsageInWindow),
    [dailyUsageInWindow],
  );
  const dailyTime = useMemo(
    () => aggregateDailyTime(runTimeDailyInWindow),
    [runTimeDailyInWindow],
  );
  const dailyTasks = useMemo(
    () => aggregateDailyTasks(runTimeDailyInWindow),
    [runTimeDailyInWindow],
  );
  const dailyErrors = useMemo(
    () => aggregateDailyErrors(failureDailyInWindow),
    [failureDailyInWindow],
  );

  // 失败摘要。
  //
  // 合计 / 类别 / 原因都从「按日期分桶」的汇总派生（先经图表同款的 `dailyCutoffIso`
  // 裁剪），而不是从按 Agent 的那份。`parseSinceParamInTZ` 故意给 N+1 个日历日
  // 的余量（见 server/internal/handler/runtime.go 里的 sinceFromDays），只有带日期
  // 的序列才能在客户端裁掉那多出的一天。从按 Agent 的汇总读这些，会把摘要比旁边
  // 的图表宽出一个日历日——在 1D 时图表可能显示「无失败」而磁贴却数进昨天的。
  const failureTotals = useMemo(
    () => computeFailureTotals(failureDailyInWindow),
    [failureDailyInWindow],
  );
  const failureClassRows = useMemo(
    () => aggregateFailureClasses(failureDailyInWindow),
    [failureDailyInWindow],
  );
  const failureReasonRows = useMemo(
    () => aggregateFailureReasons(failureDailyInWindow),
    [failureDailyInWindow],
  );
  // 这个查看者能真正解析成名字的 Agent id 集合。在这里声明（而不是排行榜旁边）
  // 是因为下面的 Errors 聚合也需要它——见 anonymizeUnresolvedAgentRows。
  const knownAgentIds = useMemo(
    () => (agentsQuery.isSuccess ? new Set(agents.map((a) => a.id)) : null),
    [agentsQuery.isSuccess, agents],
  );

  // 按 Agent 的拆分没有日期可裁，所以它的窗口改在服务端闭合——
  // GetDashboardFailuresByAgent 用的是精确的 N 天 cutoff 而不是 N+1。
  //
  // 在聚合之前匿名化：这样哨兵 id 就和任何其它 agent id 一样，桶的失败类别由
  // 真实的按 (agent, reason) 行求和得到，而不是从「已经塌缩成单一主导类别」的
  // 行里重构。
  const agentFailureRows = useMemo(
    () =>
      aggregateAgentFailures(
        anonymizeUnresolvedAgentRows(failureByAgentRows, knownAgentIds),
      ),
    [failureByAgentRows, knownAgentIds],
  );

  // 周聚合——用「多取了天数的按日期查询」构建，这样即使用户选的 `days`（如 30D）
  // 短于图表的 `weekCount * 7` 跨距，最左的末尾周也总有数据。桶在辅助函数里
  // 预置了零，稀疏周渲染成空柱而不是被丢弃（MUL-2382 周窗口限定）。周边界跟随
  // 查看者的时区。
  const weekly = useMemo(
    () => aggregateByWeek(dailyUsage, viewTZ, weekCount),
    [dailyUsage, viewTZ, weekCount],
  );
  const weeklyCost = weekly.weeklyCostStack;
  const weeklyTokens = weekly.weeklyTokens;
  const weeklyTime = useMemo(
    () => aggregateWeeklyTime(runTimeDailyRows, viewTZ, weekCount),
    [runTimeDailyRows, viewTZ, weekCount],
  );
  const weeklyTasks = useMemo(
    () => aggregateWeeklyTasks(runTimeDailyRows, viewTZ, weekCount),
    [runTimeDailyRows, viewTZ, weekCount],
  );
  const weeklyErrors = useMemo(
    () => aggregateWeeklyErrors(failureDailyRows, viewTZ, weekCount),
    [failureDailyRows, viewTZ, weekCount],
  );
  const agentTokenRows = useMemo(
    () => aggregateAgentTokens(byAgentUsage),
    [byAgentUsage, pricings],
  );

  // 运行时长合计——taskCount + failedCount 求和给 KPI 行。
  const runTimeTotals = useMemo(() => {
    let totalSeconds = 0;
    let taskCount = 0;
    let failedCount = 0;
    for (const r of runTimeRows) {
      totalSeconds += r.total_seconds;
      taskCount += r.task_count;
      failedCount += r.failed_count;
    }
    return { totalSeconds, taskCount, failedCount };
  }, [runTimeRows]);

  const agentRows = useMemo(
    () => mergeAgentDashboardRows(agentTokenRows, runTimeRows),
    [agentTokenRows, runTimeRows],
  );
  // 每个 agent 单独判断"这批用量有没有价格来源"。窗口级的标志只适合 KPI 合计，
  // 拿它给每一行用，会让恰好只有不可定价模型的 agent 也显示 $0.00。
  const costKnownByAgent_ = useMemo(
    () => buildCostKnownByAgent(byAgentUsage, knownAgentIds),
    [byAgentUsage, knownAgentIds, pricings],
  );
  // 有可定价行、也有完全不可定价的行时，合计只是"已定价部分"的和。把这件事说出来，
  // 读者才不会把部分已知当成完整费用。
  const costPartial = useMemo(
    () =>
      costKnown &&
      dailyUsageInWindow.some((row) => hasUnknownCostRemainder(row)),
    [costKnown, dailyUsageInWindow, pricings],
  );

  // 把「已硬删除的 Agent」的汇总行折进一个聚合的 "Deleted agents" 行，而不是
  // 显示成一串裸 UUID（MUL-3771）或直接丢弃——丢弃会让分 Agent 明细与仍计入
  // 该花费的顶部 Cost/Tokens KPI 对不上账（MUL-3776, #4640）。归档的 Agent
  // 保持各自身份（Agent 列表拉取时含归档）；只有真正被移除的 Agent 才塌进桶里。
  // Agent 列表加载完成前跳过分桶，慢拉取不会瞬时把所有行合并到一起。
  const visibleAgentRows = useMemo(
    () => bucketUnknownAgentRows(agentRows, knownAgentIds),
    [agentRows, knownAgentIds],
  );
  // 折进桶里的不同硬删除 Agent 数量——驱动 caption 的 "· N deleted" 后缀
  // （桶本身只是一行）。服务端的 restricted 桶同样不在 `knownAgentIds` 里，但它
  // 不是删除，所以不能撑大这个计数——那种贴错标签正是 MUL-5409 带来的 bug。
  const deletedAgentCount = useMemo(
    () =>
      knownAgentIds
        ? agentRows.filter(
            (r) => !knownAgentIds.has(r.agentId) && !isSyntheticAgentRow(r.agentId),
          ).length
        : 0,
    [agentRows, knownAgentIds],
  );

  const allowedDims = dimsForDays(days);
  const lessThanMinuteLabel = t(($) => $.duration.less_than_minute);

  return (
    <Tabs
      value={tab}
      onValueChange={handleTabChange}
      className="flex h-full min-h-0 flex-col gap-0"
    >
      <CollectionPageHeader
        icon={BarChart3}
        title={t(($) => $.title)}
        actions={
          /* 数据新鲜度簇：时间戳与推进它的操作呆在一起。刷新重拉的是同一作用域，
             所以它放在这里、和页面元信息一起，而不是混进作用域控件里。 */
          <div className="flex items-center gap-1">
            {tzLabel ? (
              <span className="hidden text-caption text-muted-foreground lg:inline">
                {updatedLabel
                  ? t(($) => $.header.timezone_and_updated, {
                      tz: tzLabel,
                      time: updatedLabel,
                    })
                  : tzLabel}
              </span>
            ) : null}
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.header.refresh)}
              onClick={handleRefresh}
              disabled={isRefreshing}
            >
              <RefreshCw className={isRefreshing ? "animate-spin" : undefined} />
            </Button>
          </div>
        }
      />

      {/* 视图工具栏，与 issues 面的头部同一套语法：左切换视图、右放页面级筛选。
          两个标签页共享范围与项目筛选，这正是筛选放在这里而不是某个标签页里的
          原因。 */}
      <div className={cn("h-12 shrink-0 overflow-x-auto border-b [-webkit-overflow-scrolling:touch]", PAGE_GUTTER)}>
        <div className="flex h-full w-max min-w-full items-center justify-between gap-2">
          <TabsList variant="line" className="gap-0 p-0 group-data-horizontal/tabs:h-full">
            <TabsTrigger
              value="usage"
              className="h-full rounded-none px-2.5 text-label group-data-horizontal/tabs:after:bottom-0"
            >
              {t(($) => $.tab_usage)}
            </TabsTrigger>
            <TabsTrigger
              value="errors"
              className="h-full rounded-none px-2.5 text-label group-data-horizontal/tabs:after:bottom-0"
            >
              {t(($) => $.errors.title)}
            </TabsTrigger>
          </TabsList>
          <div className="flex shrink-0 items-center gap-2">
            <TimeRangeFilter days={days} onChange={setDays} />
            <ProjectFilter
              projects={projects}
              projectValue={projectValue}
              onProjectChange={setProjectValue}
            />
          </div>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-6xl p-6">
          <TabsContent value="usage" className="space-y-5">
            {usageLoading ? (
              <DashboardSkeleton />
            ) : usageHasNoData ? (
              <DashboardEmpty />
            ) : (
              <>
                {/* KPI 行——与运行时用量区同款的三分栏卡片网格，扩到四个磁贴。 */}
                <div className="grid grid-cols-1 divide-y rounded-lg border bg-card sm:grid-cols-2 sm:divide-x sm:divide-y-0 lg:grid-cols-4">
                  <KpiCard
                    label={t(($) => $.kpi.cost_label, { days })}
                    value={
                      costKnown ? (
                        <CurrencyNumberFlow value={totals.cost} locales={locales} />
                      ) : (
                        <span>—</span>
                      )
                    }
                    hint={
                      costPartial
                        ? t(($) => $.kpi.cost_partial_hint)
                        : undefined
                    }
                  />
                  <KpiCard
                    label={t(($) => $.kpi.tokens_label, { days })}
                    value={
                      <CompactNumberFlow
                        value={
                          totals.input +
                          totals.output +
                          totals.cacheRead +
                          totals.cacheWrite
                        }
                        locales={locales}
                      />
                    }
                    hint={t(($) => $.kpi.tokens_hint, {
                      input: formatTokens(totals.input),
                      output: formatTokens(totals.output),
                    })}
                  />
                  <KpiCard
                    label={t(($) => $.kpi.run_time_label, { days })}
                    value={
                      <DurationNumberFlow
                        seconds={runTimeTotals.totalSeconds}
                        lessThanMinuteLabel={lessThanMinuteLabel}
                        locales={locales}
                      />
                    }
                    hint={t(($) => $.kpi.run_time_hint, {
                      tasks: runTimeTotals.taskCount,
                    })}
                  />
                  <KpiCard
                    label={t(($) => $.kpi.tasks_label, { days })}
                    value={
                      <NumberFlow
                        value={runTimeTotals.taskCount}
                        locales={locales}
                        format={{ maximumFractionDigits: 0 }}
                        aria-label={String(runTimeTotals.taskCount)}
                      />
                    }
                    // 有意取自 `runTimeTotals` 而非失败汇总：磁贴自己的值只统计
                    // 已启动的任务，在这里引用失败汇总那个更大的失败数，会让一个
                    // 磁贴里出现两个不同的分母。Errors 标签页则明确标出分母来陈述
                    // 它的失败率。
                    hint={t(($) => $.kpi.tasks_hint, {
                      failed: runTimeTotals.failedCount,
                    })}
                  />
                </div>

                <UsageTrendCard
                  allowedDims={allowedDims}
                  dailyCost={dailyCost}
                  dailyTokens={dailyTokens}
                  dailyTime={dailyTime}
                  dailyTasks={dailyTasks}
                  weeklyCost={weeklyCost}
                  weeklyTokens={weeklyTokens}
                  weeklyTime={weeklyTime}
                  weeklyTasks={weeklyTasks}
                  lessThanMinuteLabel={lessThanMinuteLabel}
                />

                <Leaderboard
                  rows={visibleAgentRows}
                  agents={agents}
                  deletedAgentCount={deletedAgentCount}
                  lessThanMinuteLabel={lessThanMinuteLabel}
                  costKnown={costKnown}
                  costKnownByAgent={costKnownByAgent_}
                />
              </>
            )}
          </TabsContent>

          <TabsContent value="errors">
            {errorsLoading ? (
              <DashboardSkeleton />
            ) : (
              <ErrorsTab
                days={days}
                allowedDims={allowedDims}
                totals={failureTotals}
                classRows={failureClassRows}
                reasonRows={failureReasonRows}
                agentRows={agentFailureRows}
                dailyErrors={dailyErrors}
                weeklyErrors={weeklyErrors}
                agents={agents}
                locales={locales}
              />
            )}
          </TabsContent>
        </div>
      </div>
    </Tabs>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-5">
      <Skeleton className="h-28 rounded-lg" />
      <Skeleton className="h-56 rounded-lg" />
      <Skeleton className="h-48 rounded-lg" />
    </div>
  );
}

function DashboardEmpty() {
  const { t } = useT("usage");
  return (
    <div className="flex flex-col items-center rounded-lg border border-dashed py-12 text-center">
      <BarChart3 className="h-6 w-6 text-faint-foreground" />
      <p className="mt-3 text-body font-medium">{t(($) => $.empty.title)}</p>
      <p className="mt-1 max-w-md text-caption text-muted-foreground">
        {t(($) => $.empty.body)}
      </p>
    </div>
  );
}
