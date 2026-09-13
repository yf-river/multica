// 记录当前用户最近一次 @ 某对象（成员 / agent / issue / "all"）的时间，
// 按工作区分别存进浏览器存储。用来给 mention 建议下拉排序，让最近提到过
// 的对象排前面。
//
// 数据刻意按设备存放——目标是「让下一次 mention 更快」，不是跨设备档案。
// localStorage 不可用（SSR、沙箱环境）时，所有访问器都降级为 no-op，
// 调用方可以无条件使用。

import type { MentionItem } from "./mention-suggestion";

type RecencyMap = Record<string, number>;

const STORAGE_PREFIX = "multica:mention-recency:";
const MAX_ENTRIES = 200;

/** 拼接某个工作区在 localStorage 里的存储 key。 */
function storageKey(workspaceId: string): string {
  return `${STORAGE_PREFIX}${workspaceId}`;
}

/** 安全获取 localStorage；不可用时（无 window / 抛错）返回 null。 */
function getStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** 读取某个工作区的最近提及映射；没有、损坏或存储不可用时返回空对象。 */
function readRecencyMap(workspaceId: string): RecencyMap {
  const storage = getStorage();
  if (!storage) return {};
  const raw = storage.getItem(storageKey(workspaceId));
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object") return parsed as RecencyMap;
  } catch {
    // 数据损坏——不抛错，等下次写入时自然覆盖。
  }
  return {};
}

/** 写回某个工作区的最近提及映射；存储不可用时静默跳过。 */
function writeRecencyMap(workspaceId: string, map: RecencyMap): void {
  const storage = getStorage();
  if (!storage) return;
  try {
    storage.setItem(storageKey(workspaceId), JSON.stringify(map));
  } catch {
    // 配额超限或存储被禁用——静默跳过。
  }
}

/** 同一类型同一 id 的对象对应同一个键。 */
function recencyKey(item: Pick<MentionItem, "type" | "id">): string {
  return `${item.type}:${item.id}`;
}

/**
 * 记录一次 @ 对象的使用，把该对象的最近时间戳刷新为当前时间。
 */
export function recordMentionUsage(
  workspaceId: string,
  item: Pick<MentionItem, "type" | "id">,
): void {
  if (!workspaceId) return;
  const map = readRecencyMap(workspaceId);
  map[recencyKey(item)] = Date.now();

  // 惰性裁剪：让映射保持有界，不随成员/agent 来来去去无限膨胀。
  const entries = Object.entries(map);
  if (entries.length > MAX_ENTRIES) {
    entries.sort(([, ta], [, tb]) => tb - ta);
    const trimmed: RecencyMap = {};
    for (const [key, ts] of entries.slice(0, MAX_ENTRIES)) {
      trimmed[key] = ts;
    }
    writeRecencyMap(workspaceId, trimmed);
    return;
  }

  writeRecencyMap(workspaceId, map);
}

/**
 * 读取某工作区全量的最近提及时间戳映射。
 */
export function getRecencyMap(workspaceId: string): RecencyMap {
  if (!workspaceId) return {};
  return readRecencyMap(workspaceId);
}

// 把用户类（member/agent）mention 项按最近使用时间倒序排列，用户从未
// 提过的项退回按名字字母序兜底。用于把原先分开的成员、agent 两个桶
// 合并成单一列表。
export function sortUserItemsByRecency(
  items: MentionItem[],
  recency: RecencyMap,
): MentionItem[] {
  return items.toSorted((a, b) => {
    const ra = recency[recencyKey(a)] ?? 0;
    const rb = recency[recencyKey(b)] ?? 0;
    if (ra !== rb) return rb - ra;
    return a.label.localeCompare(b.label);
  });
}
