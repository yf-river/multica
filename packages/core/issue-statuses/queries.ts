// 本文件提供工作区的「issue 状态目录」查询与解析逻辑（MUL-6243）。
//
// 工作区永远有 8 个内置状态，还可能有自定义状态。每个状态——无论内置还是
// 自定义——都属于 8 个类别中的恰好一个，而类别决定了平台行为、看板列与展示
// 方式。所以这份目录对 UI 只回答一个问题：拿到 issue 上存的状态 key，它的
// 标签、颜色和类别是什么？

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { STATUS_CONFIG, STATUS_ORDER } from "../issues/config";
import type { IssueStatusCategory, IssueStatusEntry } from "../types";

/** issue 状态相关查询的 query key 命名空间。 */
export const issueStatusKeys = {
  all: (wsId: string) => ["issue-statuses", wsId] as const,
  list: (wsId: string) => [...issueStatusKeys.all(wsId), "list"] as const,
};

/** 查询工作区的完整状态目录（含归档状态）。 */
export function issueStatusListOptions(wsId: string) {
  return queryOptions({
    queryKey: issueStatusKeys.list(wsId),
    // 刻意包含已归档条目。归档把某个状态从「将来可指派」里退休，但已有 issue
    // 仍然停在这个状态上，那些 issue 必须继续显示真实的名称、颜色和类别。若
    // 在这里丢掉归档行，它们会退化成带猜测类别的裸 key。选择器请用
    // `activeStatuses`（已排除归档）。 (MUL-6243)
    queryFn: () => api.listIssueStatuses(true),
    select: (data) => data.statuses,
    // 目录只在管理员编辑时变化，很少发生，所以慷慨的 staleTime 让每次需要
    // 状态标签的渲染都不必走到这条查询的关键路径上。
    staleTime: 5 * 60_000,
  });
}

/**
 * 对状态目录的一种「已解析」视图。任何查询都有可渲染的兜底，因为 issue 上
 * 完全可能带着一份本客户端还没见过的状态：另一个会话几秒钟前刚创建的，或
 * 目录数据还没拉回来的。
 */
export interface IssueStatusCatalog {
  /**
   * 按展示顺序排列的全部状态，包含已归档——这样停在归档状态上的 issue 依然
   * 能解析出真实的名称、颜色和类别。任何「提供状态让用户挑」的界面请改用
   * `activeStatuses`。
   */
  statuses: IssueStatusEntry[];
  /** 可指派状态——`statuses` 去掉归档项。 */
  activeStatuses: IssueStatusEntry[];
  /** 某个状态 key 的类别；内置 key 兜底为 key 本身，未知 key 兜底为 "todo"。 */
  categoryOf: (statusKey: string) => IssueStatusCategory;
  /** 某个状态 key 的人类可读标签；先兜底为类别标签，再兜底为原始 key。 */
  labelOf: (statusKey: string) => string;
  /** 某个状态 key 的目录条目；目录不认识时返回 undefined。 */
  entryOf: (statusKey: string) => IssueStatusEntry | undefined;
  /**
   * 界面给某个状态上色要用的 `#rrggbb`；应该用类别语义 token 时返回 null。
   *
   * 内置状态恒为 null。8 个内置状态在目录里带着一个种子 hex，但它们不可
   * 重新着色（服务端拒绝任何对系统行的修改），且所有界面都用 token 渲染它们
   * ——`text-success` 之类，正是这让他们跟着主题进入深色模式。调用方若改用
   * `entryOf(key)?.color`，画出来的会是原始种子色，与同状态的 token 渲染相差
   * 几个像素的色差。(MUL-6440)
   */
  colorOf: (statusKey: string) => string | null;
  /** 某类别下的 ACTIVE（未归档）状态，按展示顺序。 */
  inCategory: (category: IssueStatusCategory) => IssueStatusEntry[];
  /** 目录一旦加载完成即为 true；仍在途时为 false。 */
  isLoaded: boolean;
  /**
   * 目录仍在途、什么都没有解析出来。
   *
   * 按自定义状态 key 路由的界面，在它为 true 时必须稳住 loading 状态。
   * 仅 `isLoaded === false` 不足以下手：「还没到」和「请求失败」的区别，决定
   * 用户看到的是 spinner 还是可重试错误；第一版实现两者都不给——只有一块
   * 静悄悄空着的看板。(MUL-6243)
   */
  isPending: boolean;
  /**
   * 目录请求失败，且没有可用的旧快照可兜底。
   *
   * 刻意比「查询出错」更窄：后台刷新可能在上一次成功目录还在缓存里时失败，
   * 此时阻塞界面等于扔掉完全可用的数据。只有「失败且背后一无所有」才值得
   * 停下来。(MUL-6243)
   */
  isError: boolean;
  /** 重新发起目录请求。接到界面的重试按钮上。 */
  retry: () => void;
  /**
   * 目录已 LOADED 且至少含一个自定义状态时为 true。
   *
   * 它是「按类别分组」界面契约的开关。必须同时满足两个条件，理由有二：
   *
   * - 滚动部署。`group.kind=status_category` 是本功能引入的服务端契约，客户端
   *   无脑发它会对任何还没升级的 pod 报 400。而工作区只有在「创建状态」开关
   *   打开后才会真的出现自定义状态，这意味着集群当时已经在提供这个版本。
   * - 冷加载。目录落地之前，`categoryOf` 无法区分自定义 key 与未知 key，只会
   *   兜底成 `todo`。按这个猜测路由，会把保存的 `qa` 过滤器送进 todo 列，
   *   渲染出一块空看板。
   *
   * 因此没有自定义状态的工作区，发出的请求与本功能之前完全一致——这也让它
   * 的看板不必做类别契约需要的那些额外目录读取。(MUL-6243)
   */
  hasCustomStatuses: boolean;
}

const BUILT_IN = new Set<string>(STATUS_ORDER);

const CATEGORY_RANK = new Map<string, number>(STATUS_ORDER.map((c, i) => [c, i]));

/**
 * 镜像服务端的目录排序，用于客户端重排。
 *
 * 先按类别排名，再按类别内位置，最后用 key 做稳定的打破平手项——见
 * `issue_status.sql` 里的 `ListIssueStatusEntries`。乐观重排必须用这个排序，
 * 否则新位置已经写进缓存、列表却仍按旧顺序渲染。
 *
 * @param a - 条目 A
 * @param b - 条目 B
 * @returns 负数/0/正数，对应 a 在 b 前/并列/后
 */
export function compareIssueStatusEntries(a: IssueStatusEntry, b: IssueStatusEntry): number {
  const rank =
    (CATEGORY_RANK.get(a.category) ?? STATUS_ORDER.length) -
    (CATEGORY_RANK.get(b.category) ?? STATUS_ORDER.length);
  if (rank !== 0) return rank;
  if (a.position !== b.position) return a.position - b.position;
  return a.key.localeCompare(b.key);
}

/** 判断一个字符串是否为 8 个内置状态类别之一（类型收窄到 IssueStatusCategory）。 */
export function isIssueStatusCategory(value: string): value is IssueStatusCategory {
  return BUILT_IN.has(value);
}

/**
 * 一条目录条目该用什么颜色画：自定义状态用它的 hex；内置状态或本客户端还没
 * 解析出来的条目返回 null。
 *
 * 这是 {@link IssueStatusCatalog.colorOf} 的纯函数形态，给那些已经拿着条目
 * （例如正在 map 的列表）而不只是 key 的调用方用。
 *
 * @param entry - 目录条目（可为空）
 * @returns 自定义颜色的 hex；内置/未知条目返回 null
 */
export function issueStatusColor(entry: IssueStatusEntry | undefined): string | null {
  if (!entry || entry.is_system === true) return null;
  return entry.color;
}

/**
 * 从原始条目列表构建已解析的目录。纯函数，store 与其他非 React 调用方可以
 * 拿一份自己已有的列表来用。
 *
 * @param entries - 原始状态条目（未加载时为 undefined）
 * @param status - 可选的请求状态（用于 isPending / isError / retry）
 * @returns 解析好的状态目录
 */
export function buildIssueStatusCatalog(
  entries: IssueStatusEntry[] | undefined,
  status: { isPending?: boolean; isError?: boolean; retry?: () => void } = {},
): IssueStatusCatalog {
  const list = entries ?? [];
  const byKey = new Map(list.map((e) => [e.key, e]));

  const categoryOf = (statusKey: string): IssueStatusCategory => {
    const category = byKey.get(statusKey)?.category;
    if (category && isIssueStatusCategory(category)) return category;
    // 内置 key 本身就是它的类别，所以目录没加载时也能正确解析全部 8 个——
    // 这正是默认工作区在拉取落地前渲染完全一致的原因。
    if (isIssueStatusCategory(statusKey)) return statusKey;
    // 未知的自定义 key：放到一个合理的地方渲染，而不是丢掉它。
    return "todo";
  };

  return {
    statuses: list,
    activeStatuses: list.filter((e) => !e.archived_at),
    categoryOf,
    entryOf: (statusKey) => byKey.get(statusKey),
    colorOf: (statusKey) => issueStatusColor(byKey.get(statusKey)),
    labelOf: (statusKey) => {
      const entry = byKey.get(statusKey);
      if (entry) return entry.name;
      if (isIssueStatusCategory(statusKey)) return STATUS_CONFIG[statusKey]?.label ?? statusKey;
      return statusKey;
    },
    inCategory: (category) => list.filter((e) => e.category === category && !e.archived_at),
    isLoaded: entries !== undefined,
    // 默认值描述的是「拿着已有列表」的非 React 调用方：有条目即已解析，
    // 没条目即仍在途。
    isPending: status.isPending ?? entries === undefined,
    // 失败但背后还有条目，属于「旧数据」而非「阻塞性失败」。
    isError: (status.isError ?? false) && entries === undefined,
    retry: status.retry ?? (() => {}),
    hasCustomStatuses:
      entries !== undefined && list.some((e) => e.is_system !== true),
  };
}
