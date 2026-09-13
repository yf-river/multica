// 从原始服务器字段（status + last_seen_at）纯函数推导 runtime 的用户可见"健康"状态。
// 把离线状态按时间分成不同口味，让 UI 无需改 schema 就能区分"刚断——多半是暂时的"
// 与"很久没见了——需要关注"。

import type { AgentRuntime } from "../types";
import type { RuntimeHealth } from "./types";

const FIVE_MINUTES_MS = 5 * 60 * 1000;
// 长期离线是按"观测到的可达性"定的展示状态，不承诺服务端清理何时/是否会发生。
const LONG_OFFLINE_THRESHOLD_MS = 6 * 24 * 3600 * 1000; // 6 天

/** 由 runtime 状态 + 最近心跳时间推导四态健康值（online/recently_lost/offline/long_offline）。 */
export function deriveRuntimeHealth(runtime: AgentRuntime, now: number): RuntimeHealth {
  if (runtime.status === "online") return "online";

  // 从未记录过 last_seen 时间戳——按长期离线处理。这是少见的边角情况
  // （后端在注册时总会设 last_seen_at），但防御它能让 UI 不因遗留行而崩溃。
  const lastSeen = runtime.last_seen_at ? new Date(runtime.last_seen_at).getTime() : 0;
  const offlineFor = now - lastSeen;

  if (offlineFor < FIVE_MINUTES_MS) return "recently_lost";
  if (offlineFor > LONG_OFFLINE_THRESHOLD_MS) return "long_offline";
  return "offline";
}
