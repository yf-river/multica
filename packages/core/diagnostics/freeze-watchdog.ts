// 本文件负责 Web 端「页面卡死」监控（freeze watchdog）。
//
// 它在主线程里安装一个长任务（long task）观察器。「长任务」指主线程长时间
// 没有回到事件循环的时间段；浏览器本身就会跟踪它们，并在线程恢复后将每条记录
// 逐个回调，所以哪怕卡了好几秒，事后也能报出真实时长。这里只对达到
// FREEZE_THRESHOLD_MS 的阻塞才上报，避免把常规 50–600ms 的渲染开销误报成
// 「几乎卡死」。
//
// 这是「线程内、可恢复」的那一层：它只能抓到线程挺过来的卡顿。真正不可恢复
// 的死循环（线程永远不回来）无法在同一个浏览器线程里被观察到。
//
// 上报的 `client_unresponsive` 事件会自动带上 `client_type`。

import { captureEvent } from "../analytics";
import { bucketDiagnosticPath } from "./diagnostic-context";

// 2 秒远高于常规切换/渲染开销（实测 50–600ms），又低于浏览器渲染进程卡死的
// 级别——达到这个值上报意味着「用户真的感觉到了停顿」，又不会在重型渲染时刷屏。
const FREEZE_THRESHOLD_MS = 2000;

// 一次持续的卡死会被浏览器拆成多条长任务记录逐条回调，若每条都上报，
// client_unresponsive 的量会随卡死时长无界增长（MUL-3331）。用全局冷却把
// 上报压到每个时间窗最多一条。状态放在模块级（整个页面生命周期）最合适——
// 和下面的 `installed` 单例一致，整页刷新（本身就少见、本身就是一个独立信号）
// 时会重置。不做路由分桶：全局窗口是控制上报量最直接的封顶办法。
const COOLDOWN_MS = 60_000;
let lastEmitMs = 0;

let installed = false;

/**
 * 安装长任务观察器。可重复调用（幂等），在服务端调用也安全（没有 `window` /
 * `PerformanceObserver` 时直接空跑）。请在一个仅客户端执行的效果里调用一次。
 */
export function installFreezeWatchdog(): void {
  if (installed) return;
  if (typeof window === "undefined") return;
  if (typeof PerformanceObserver === "undefined") return;
  installed = true;

  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.duration < FREEZE_THRESHOLD_MS) continue;
        // 冷却只对「够格上报」的卡死起作用，低于阈值的任务既不上报也不重置窗口。
        const now = Date.now();
        if (now - lastEmitMs < COOLDOWN_MS) continue;
        lastEmitMs = now;
        captureEvent("client_unresponsive", {
          source: "longtask",
          duration_ms: Math.round(entry.duration),
          path: resolveDiagnosticPath(),
        });
      }
    });
    // 不开 `buffered: true`——只要安装之后的卡死。回放安装前缓冲的任务会把
    // 「启动慢」误标成「运行时卡死」。
    observer.observe({ type: "longtask" });
  } catch {
    // 当前引擎不支持 longtask 入口类型——没有别的可做。
  }
}

/**
 * 解析卡死发生时所在的页面路径（不带任何用户 ID），用于上报定位。
 *
 * @returns 去标识化后的路径模板；拿不到 location 时返回 undefined
 */
function resolveDiagnosticPath(): string | undefined {
  return typeof location !== "undefined"
    ? bucketDiagnosticPath(location.pathname)
    : undefined;
}
