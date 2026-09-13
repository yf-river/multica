// 把路由里的议题标识（UUID 或 `MUL-123` 这种人类可读 id）解析成议题和它的规范 UUID。
import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Issue } from "../types";
import { issueDetailOptions, issueKeys } from "./queries";

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** 判断是否是规范议题 UUID；是 `MUL-123` 这种标识符则返回 false。 */
export function isIssueUuid(value: string): boolean {
  return UUID_RE.test(value);
}

export interface CanonicalIssue {
  /**
   * 议题的 UUID；当标识符仍在解析中、或解析结果为空时为 `null`。
   */
  canonicalId: string | null;
  /** 议题本身，从以 UUID 为 key 的缓存条目里读取。 */
  issue: Issue | undefined;
  isResolving: boolean;
  /**
   * 终态：这个标识符在当前工作区里不指向任何议题。
   *
   * 调用方必须据此分支并渲染“未找到”状态，而不是把未解析的片段交给一个
   * 会再次查询它的视图。因为第二个观察者挂载到失败的查询上时会重取
   * （`retryOnMount`），把 hook 又翻回解析中，卸载那个视图，重取失败后再重新挂载——
   * 形成永不停歇、也永远不会停在“未找到”的请求循环。
   */
  notFound: boolean;
}

/**
 * 把原始的 `/{ws}/issues/{segment}` 路由参数——一个 UUID 或人类可读标识符
 * （`MUL-123`）——解析成议题及其 UUID。
 *
 * 为什么调用方必须以 UUID 而不是原始片段为 key：realtime 更新器用 WS 载荷携带的
 * UUID 去修补 `issueKeys.detail(wsId, issue.id)`（见 `ws-updaters.ts`）。
 * 以标识符为 key 的视图会停在一个任何 realtime 事件都够不到的缓存条目上，
 * 于是详情页能正常渲染，随后却悄悄停止更新。标识符始终只是展示层的东西：
 * 它待在 URL 里，绝不出现在缓存 key 里。
 *
 * 解析复用 `GET /api/issues/{id}`（两种形式都接受），所以它本来就是详情视图
 * 本来就要发的那一次请求。把响应作为 `initialData` 交给规范查询，正是把它
 * 控制在“一次请求”的关键：`initialData` 在观察者创建时就被应用，所以规范查询
 * 永远不会观察到空缓存、也不会自己再发一次请求。若改成在 effect 里播种缓存，
 * 就会跑到这个决策之后，让“单次请求”这个性质依赖 hook 的执行顺序。
 */
export function useCanonicalIssue(wsId: string, routeId: string): CanonicalIssue {
  const qc = useQueryClient();
  const isUuid = isIssueUuid(routeId);
  const resolveEnabled = !isUuid && !!wsId && !!routeId;

  const resolve = useQuery({
    ...issueDetailOptions(wsId, routeId),
    enabled: resolveEnabled,
  });

  const canonicalId = isUuid ? routeId : (resolve.data?.id ?? null);

  const detail = useQuery({
    ...issueDetailOptions(wsId, canonicalId ?? ""),
    enabled: !!canonicalId && !!wsId,
    // 一旦条目已有数据就被忽略，所以已经被 realtime 事件修补过的行，
    // 绝不会被这份更旧的解析响应覆盖。
    initialData: isUuid ? undefined : resolve.data,
  });

  // 把已加载的行镜像到以标识符为 key 的条目里——即上面 `initialData` 一跳的逆操作。
  //
  // 应用内链接仍带 UUID，所以打开一个链接会先落在 UUID URL 上，路由随后把地址栏
  // 重写成标识符。那次重写是一次导航：路由带着标识符作为片段重新渲染，若没有这里
  // 的播种，上面的解析查询会错过一个没有任何东西填充过的缓存条目，从而重新拉取
  // 一个本已到手的议题——为打开一个议题多发一次请求。`??` 让 realtime 修补过的条目
  // 保持原样，而且只会读回 `.id`（它永远不变）。
  const loadedIdentifier = detail.data?.identifier;
  const loadedIssue = detail.data;
  useEffect(() => {
    if (!loadedIssue || !loadedIdentifier || loadedIdentifier === routeId) return;
    qc.setQueryData<Issue>(
      issueKeys.detail(wsId, loadedIdentifier),
      (old) => old ?? loadedIssue,
    );
  }, [qc, wsId, loadedIdentifier, loadedIssue, routeId]);

  // 读解析查询自己已“结算”的状态，而不是“pending 且无数据”：失败的解析必须
  // 表现为“终态未找到”，绝不能是“仍在解析中”，否则调用方会翻回加载态，
  // 触发 `notFound` 里描述的那种重挂载循环。
  const failed = resolveEnabled && resolve.isError;

  return {
    canonicalId,
    issue: detail.data,
    isResolving: resolveEnabled && !failed && resolve.data === undefined,
    notFound: failed,
  };
}
