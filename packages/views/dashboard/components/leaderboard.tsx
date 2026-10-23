"use client";

// 排行榜卡片：按 tokens/cost/time/tasks 给 Agent 排名，支持折叠尾部。
import { useMemo, useState, type CSSProperties } from "react";
import { EyeOff, Trash2 } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { formatTokens } from "../../runtimes/utils";
import { useT } from "../../i18n";
import {
  DELETED_AGENTS_ROW_ID,
  formatDuration,
  isSyntheticAgentRow,
  RESTRICTED_AGENTS_ROW_ID,
  type AgentDashboardRow,
} from "../utils";
import { Segmented } from "./dashboard-shared";
import "./leaderboard.css";

// 排行榜按哪个指标排名。它决定行顺序、进度条宽度、以及哪个列头被高亮——
// 三者保持同步，用户才能始终看清排名到底在衡量什么。
type LeaderboardSort = "tokens" | "cost" | "time" | "tasks";

const SORT_METRIC: Record<LeaderboardSort, (r: AgentDashboardRow) => number> = {
  tokens: (r) => r.tokens,
  cost: (r) => r.cost,
  time: (r) => r.seconds,
  tasks: (r) => r.taskCount,
};

// 排行榜最多直接展示多少个 Agent，超出部分折叠到一个开关后面，和失败者列表的
// 上限保持一致。几十个 Agent 的工作区以前会把每一个都渲染出来，把下面的内容
// 往下挤了整整一屏甚至更多（MUL-5388）。十个足够回答「谁花得最多」，尾部通过
// 开关仍可展开。
const LEADERBOARD_LIMIT = 10;

// 弹性列自己声明可读性下限，而不是从一个脱离内容的总宽度反推。Agent 列保留 24px 头像、
// 8px 间距和 128px 可读名字；Progress 列把 8px 高的条保持在 12:1 的对比长度。
// `fit-content` 让这些下限加上固定轨道、间距、行内边距共同构成内容的自然滚动宽度，
// 同时 fr 轨道在更宽的卡片上仍能继续伸展。
const LEADERBOARD_GRID_STYLE = {
  minWidth: "fit-content",
  gridTemplateColumns:
    "minmax(10rem, 1.6fr) minmax(6rem, 1fr) 5rem 5rem 5rem 4rem",
} satisfies CSSProperties;

const LEADERBOARD_GRID = "grid items-center gap-3";

/**
 * 排行榜卡片：列出各 Agent 的开销排行。
 *
 * 顶部是排序切换器（tokens/cost/time/tasks），中间是排行榜主体，超过上限的
 * 尾部行折叠到「显示全部」开关后面。
 *
 * @param rows 合并后的每-Agent 一行数据（含可能的「已删除 Agent」/「受限 Agent」桶）
 * @param agents 可解析出名字的 Agent 列表
 * @param deletedAgentCount 被折叠进「已删除」桶的独立 Agent 数量，用于文案后缀
 * @param lessThanMinuteLabel 不足一分钟时显示的文案
 */
export function Leaderboard({
  rows,
  agents,
  deletedAgentCount,
  lessThanMinuteLabel,
  costKnown,
}: {
  rows: AgentDashboardRow[];
  agents: { id: string; name: string }[];
  deletedAgentCount: number;
  lessThanMinuteLabel: string;
  // 整段窗口没有任何一行能定价时，费用列显示未知而不是 $0.00——这一列的和
  // 与上面的费用 KPI 同源，两处必须给出同一个答案。
  costKnown: boolean;
}) {
  const { t } = useT("usage");
  const [sortBy, setSortBy] = useState<LeaderboardSort>("tokens");
  const [showAll, setShowAll] = useState(false);

  const sortOptions = useMemo(
    () => [
      { value: "tokens" as const, label: t(($) => $.leaderboard.header_tokens) },
      { value: "cost" as const, label: t(($) => $.leaderboard.header_cost) },
      { value: "time" as const, label: t(($) => $.leaderboard.header_time) },
      { value: "tasks" as const, label: t(($) => $.leaderboard.header_tasks) },
    ],
    [t],
  );

  // 指标变化时重新排序；不改动合并后的输入，这样上游 `mergeAgentDashboardRows`
  // 的平局规则（按运行时长降序）在数值相同的一组里仍然生效。
  const sortedRows = useMemo(() => {
    const metric = SORT_METRIC[sortBy];
    return rows.toSorted((a, b) => metric(b) - metric(a));
  }, [rows, sortBy]);

  // 最大值按所有行计算，而不是只看可见行，这样条宽在折叠和展开时含义一致——
  // 领先者始终铺满轨道，尾部展开进来时也不会重新缩放。
  const maxValue = useMemo(() => {
    const metric = SORT_METRIC[sortBy];
    return sortedRows.reduce((m, r) => Math.max(m, metric(r)), 0);
  }, [sortedRows, sortBy]);

  const visibleRows = showAll
    ? sortedRows
    : sortedRows.slice(0, LEADERBOARD_LIMIT);

  // "N agents" 统计的是真正有名字的 Agent 行。最多有两行是合成桶（已删除、受限），
  // 以前固定减 1 会在两个桶同时存在时多报一个 Agent。
  const namedAgentCount = useMemo(
    () => rows.filter((r) => !isSyntheticAgentRow(r.agentId)).length,
    [rows],
  );

  // 当前激活的列用前景色，其余保持弱化。帮用户一眼看出「条现在衡量的是什么」。
  const colClass = (key: LeaderboardSort) =>
    `text-right ${sortBy === key ? "text-foreground" : "text-muted-foreground"}`;

  return (
    <div className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 pt-4 pb-3">
        <h4 className="text-body font-semibold">{t(($) => $.leaderboard.title)}</h4>
        <div className="flex flex-wrap items-center justify-end gap-3">
          <Segmented
            label={t(($) => $.leaderboard.sort_label)}
            value={sortBy}
            onChange={setSortBy}
            options={sortOptions}
          />
          <span className="text-caption text-muted-foreground">
            {deletedAgentCount > 0
              ? t(($) => $.leaderboard.caption_with_deleted, {
                  count: namedAgentCount,
                  deleted: deletedAgentCount,
                })
              : t(($) => $.leaderboard.caption, { count: namedAgentCount })}
          </span>
          {/* 紧挨着的说明文字已经写明了窗口覆盖多少个 Agent，所以这个开关只在
              折叠时带上数字——一旦「已删除 Agent」桶把说明文字拆成两部分，把总数
              写两遍会读起来像两个不同的数字。 */}
          {sortedRows.length > LEADERBOARD_LIMIT ? (
            <button
              type="button"
              onClick={() => setShowAll((v) => !v)}
              className="text-caption text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            >
              {showAll
                ? t(($) => $.leaderboard.show_less, { count: LEADERBOARD_LIMIT })
                : t(($) => $.leaderboard.show_all)}
            </button>
          ) : null}
        </div>
      </div>
      {sortedRows.length === 0 ? (
        <p className="px-4 py-8 text-center text-caption text-muted-foreground">
          {t(($) => $.leaderboard.no_data)}
        </p>
      ) : (
        <div
          role="region"
          aria-label={t(($) => $.leaderboard.title)}
          tabIndex={0}
          className="leaderboard-scroll-region overflow-x-auto overscroll-x-contain [-webkit-overflow-scrolling:touch]"
        >
          <div>
            <div
              className={`${LEADERBOARD_GRID} border-b px-4 py-2 text-caption font-medium text-muted-foreground`}
              style={LEADERBOARD_GRID_STYLE}
            >
              <span>{t(($) => $.leaderboard.header_agent)}</span>
              <span />
              <span className={colClass("tokens")}>
                {t(($) => $.leaderboard.header_tokens)}
              </span>
              <span className={colClass("cost")}>
                {t(($) => $.leaderboard.header_cost)}
              </span>
              <span className={colClass("time")}>
                {t(($) => $.leaderboard.header_time)}
              </span>
              <span className={colClass("tasks")}>
                {t(($) => $.leaderboard.header_tasks)}
              </span>
            </div>
            {/* 用真正的列表，和 Errors 标签页的失败者列表一样：行是一份被截断的
                排名，屏幕阅读器需要总数和条目边界，而不是一堆光秃秃的 div。 */}
            <ul aria-label={t(($) => $.leaderboard.title)} className="divide-y">
              {visibleRows.map((row) => {
                // 两种合成行，都不是真正的 Agent：两者都渲染一个中性的占位符
                // （不拉头像 / 不显示悬停卡 / 不显示 UUID），而不是去 Agent 列表里查这个 id。
                //
                // 只有「已删除」桶把 Time/Tasks 打成破折号——它本来就不携带这两项
                // （见 bucketUnknownAgentRows）。服务端的受限桶是有的：那些 Agent 活着
                // 且跑过，只是服务端把它们合并了（MUL-5409），所以把它们的列清零会
                // 少报工作区的运行时长。
                //
                // 它的文案用中性的「其他 Agent」而不提任何权限，因为它覆盖两类人：
                // 这个查看者可能看不到的 Agent，以及 agent-builder 会话背后隐藏的系统
                // 载体——连拥有它们的管理员也叫不出名字。
                const isDeletedBucket = row.agentId === DELETED_AGENTS_ROW_ID;
                const isRestrictedBucket =
                  row.agentId === RESTRICTED_AGENTS_ROW_ID;
                const isBucket = isDeletedBucket || isRestrictedBucket;
                const agent = agents.find((a) => a.id === row.agentId);
                const value = SORT_METRIC[sortBy](row);
                const pct = maxValue > 0 ? (value / maxValue) * 100 : 0;
                return (
                  <li
                    key={row.agentId}
                    className={`${LEADERBOARD_GRID} px-4 py-2`}
                    style={LEADERBOARD_GRID_STYLE}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      {isBucket ? (
                        <>
                          <span className="flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                            {isDeletedBucket ? (
                              <Trash2 className="h-3 w-3" />
                            ) : (
                              <EyeOff className="h-3 w-3" />
                            )}
                          </span>
                          <span className="truncate text-body font-medium italic text-muted-foreground">
                            {isDeletedBucket
                              ? t(($) => $.leaderboard.deleted_agents)
                              : t(($) => $.leaderboard.other_agents)}
                          </span>
                        </>
                      ) : (
                        <>
                          <ActorAvatar
                            actorType="agent"
                            actorId={row.agentId}
                            size="md"
                            enableHoverCard
                          />
                          <span className="cursor-pointer truncate text-body font-medium">
                            {agent?.name ?? row.agentId}
                          </span>
                        </>
                      )}
                    </div>
                    <div className="relative h-2 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-chart-1 transition-[width] duration-300 ease-out"
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                    <div
                      className={`text-right text-caption tabular-nums ${sortBy === "tokens" ? "font-medium text-foreground" : "text-muted-foreground"}`}
                    >
                      {formatTokens(row.tokens)}
                    </div>
                    <div
                      className={`text-right tabular-nums ${sortBy === "cost" ? "text-body font-medium" : "text-caption text-muted-foreground"}`}
                    >
                      {costKnown ? `$${row.cost.toFixed(2)}` : "—"}
                    </div>
                    <div
                      className={`text-right text-caption tabular-nums ${sortBy === "time" ? "font-medium text-foreground" : "text-muted-foreground"}`}
                    >
                      {isDeletedBucket
                        ? "—"
                        : formatDuration(row.seconds, lessThanMinuteLabel)}
                    </div>
                    <div
                      className={`text-right text-caption tabular-nums ${sortBy === "tasks" ? "font-medium text-foreground" : "text-muted-foreground"}`}
                    >
                      {isDeletedBucket ? "—" : row.taskCount}
                    </div>
                  </li>
                );
              })}
            </ul>
          </div>
        </div>
      )}
    </div>
  );
}
